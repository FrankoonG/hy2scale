import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import {
  Modal, Button, Input, PasswordInput, FormGroup, Badge, DetailGrid, Progress, useConfirm,
} from '@hy2scale/ui';
import { api as request, getBasePath } from '@/api/client';
import * as api from '@/api';
import { sha256, getSessionHash, getSavedCredentials } from '@/hooks/useAuth';
import { versionGreater } from '@/utils/version';
import { fmtBytes } from '@/hooks/useFormat';

/*
 * Remote node dialog: sign in to a node reached through the mesh, then
 * manage it from here: open its UI, upgrade it from GitHub, or install this
 * node's binary on it.
 *
 * Sign-in (top) is tried automatically: the relay trust of the remote
 * (relay_admin_passthrough) first, then this tab's own credentials; else
 * the remote's credentials are asked for. The sign-in lives in this tab's
 * sessionStorage for REMOTE_TTL and is never refreshed; sign-out ends the
 * remote session too. Operations (bottom) stay greyed out until signed in.
 */

const REMOTE_TTL = 30 * 60 * 1000;
const RESTART_WAIT = 180_000;

type Via = 'trust' | 'password';
interface Auth { token: string; via: Via; exp: number }
interface RemoteInfo { version: string; os: string; arch: string; inDocker: boolean }
type Op =
  | { kind: 'idle' }
  | { kind: 'checking' }
  | { kind: 'checked'; latest: string; available: boolean; size: number }
  | { kind: 'downloading'; progress: number }
  | { kind: 'pushing' }
  | { kind: 'restarting'; from: string }
  | { kind: 'done'; from: string; to: string }
  | { kind: 'error'; message: string };

interface Props {
  open: boolean;
  onClose: () => void;
  /** Relay chain to the remote node, e.g. ["jp"] or ["us","us-east"]. */
  chain: string[];
  targetLabel: string;
  animateFrom?: { x: number; y: number };
}

const storeKey = (chain: string[]) => `hy-remote:${chain.join('/')}`;
const readAuth = (chain: string[]): Auth | null => {
  try {
    const a = JSON.parse(sessionStorage.getItem(storeKey(chain)) || 'null') as Auth | null;
    return a && a.token && a.exp > Date.now() ? a : null;
  } catch { return null; }
};
const writeAuth = (chain: string[], a: Auth | null) => {
  try {
    if (a) sessionStorage.setItem(storeKey(chain), JSON.stringify(a));
    else sessionStorage.removeItem(storeKey(chain));
  } catch { /* storage unavailable */ }
};
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const mmss = (ms: number) => {
  const s = Math.max(0, Math.round(ms / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
};

export default function RemoteModal({ open, onClose, chain, targetLabel, animateFrom }: Props) {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const chainKey = chain.join('/');
  const proxyBase = `${getBasePath()}/remote/${chain.map(encodeURIComponent).join('/')}`;

  const [auth, setAuthState] = useState<Auth | null>(null);
  const [phase, setPhase] = useState<'checking' | 'out' | 'signing' | 'in'>('checking');
  const [username, setUsername] = useState('admin');
  const [password, setPassword] = useState('');
  const [authError, setAuthError] = useState<string | null>(null);
  const [now, setNow] = useState(Date.now());
  const [info, setInfo] = useState<RemoteInfo | null>(null);
  const [local, setLocal] = useState<{ version: string; os: string; arch: string; size: number } | null>(null);
  const [op, setOp] = useState<Op>({ kind: 'idle' });
  // The password sign-in, kept in memory only, to sign in again after the
  // remote restarts (its sessions do not survive a restart).
  const credRef = useRef<{ u: string; h: string } | null>(null);
  const alive = useRef(true);
  useEffect(() => () => { alive.current = false; }, []);

  const setAuth = useCallback((a: Auth | null) => {
    setAuthState(a);
    writeAuth(chain, a);
    setPhase(a ? 'in' : 'out');
  }, [chainKey]); // eslint-disable-line react-hooks/exhaustive-deps

  /** A request to the remote node through this node's proxy. */
  const rfetch = useCallback((path: string, init: RequestInit = {}, token?: string) => {
    const headers: Record<string, string> = { ...(init.headers as Record<string, string> || {}) };
    if (token) headers.Authorization = `Bearer ${token}`;
    if (typeof init.body === 'string') headers['Content-Type'] = 'application/json';
    return fetch(`${proxyBase}/api${path}`, { ...init, headers, credentials: 'same-origin', cache: 'no-store' });
  }, [proxyBase]);

  const login = useCallback(async (u: string, h: string) => {
    const r = await rfetch('/login', { method: 'POST', body: JSON.stringify({ username: u, password: h }) });
    if (r.status === 401) return { ok: false as const, status: 401 };
    if (!r.ok) return { ok: false as const, status: r.status, msg: (await r.text()).trim() || `HTTP ${r.status}` };
    const d = await r.json();
    return d?.token ? { ok: true as const, token: d.token as string } : { ok: false as const, status: r.status, msg: 'no token' };
  }, [rfetch]);

  /** Relay trust, then this tab's credentials; null when neither works. */
  const autoSignIn = useCallback(async (): Promise<Auth | null> => {
    try {
      const r = await rfetch('/relay-passthrough-token', { method: 'POST' });
      if (r.ok) {
        const d = await r.json();
        if (d?.token) return { token: d.token, via: 'trust', exp: Date.now() + REMOTE_TTL };
      }
    } catch { /* fall through */ }
    const cands: { u: string; h: string }[] = [];
    if (credRef.current) cands.push(credRef.current);
    const sess = getSessionHash();
    if (sess) cands.push(sess);
    const saved = getSavedCredentials();
    if (saved) cands.push(saved);
    for (const c of cands) {
      try {
        const res = await login(c.u, c.h);
        if (res.ok) {
          credRef.current = c;
          return { token: res.token, via: 'password', exp: Date.now() + REMOTE_TTL };
        }
      } catch { /* next */ }
    }
    return null;
  }, [rfetch, login]);

  // On open: a still valid stored sign-in, else the automatic sign-in.
  useEffect(() => {
    if (!open || !chain.length) return;
    let gone = false;
    setOp({ kind: 'idle' });
    setInfo(null);
    setAuthError(null);
    setPassword('');
    setPhase('checking');
    (async () => {
      const stored = readAuth(chain);
      if (stored) {
        try {
          const r = await rfetch('/node', {}, stored.token);
          if (r.ok) { if (!gone) setAuth(stored); return; }
        } catch { /* sign in again */ }
      }
      const a = await autoSignIn();
      if (!gone) setAuth(a);
    })();
    api.getSystemArch().then((a: any) => setLocal({ version: a.version, os: a.os, arch: a.arch, size: a.binary_size || 0 })).catch(() => {});
    return () => { gone = true; };
  }, [open, chainKey]); // eslint-disable-line react-hooks/exhaustive-deps

  // Countdown; the sign-in ends at its expiry.
  useEffect(() => {
    if (!open || phase !== 'in') return;
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, [open, phase]);
  useEffect(() => {
    if (auth && phase === 'in' && now >= auth.exp) signOut();
  }, [now]); // eslint-disable-line react-hooks/exhaustive-deps

  // The remote's version and platform once signed in.
  const loadInfo = useCallback(async (token: string) => {
    try {
      const r = await rfetch('/system/arch', {}, token);
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      const d = await r.json();
      if (alive.current) setInfo({ version: d.version, os: d.os, arch: d.arch, inDocker: !!d.in_docker });
    } catch {
      if (alive.current) setInfo(null);
    }
  }, [rfetch]);
  useEffect(() => {
    if (open && phase === 'in' && auth) loadInfo(auth.token);
  }, [open, phase, auth?.token]); // eslint-disable-line react-hooks/exhaustive-deps

  const submit = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (phase === 'signing') return;
    setPhase('signing');
    setAuthError(null);
    try {
      const h = await sha256(password);
      const res = await login(username, h);
      if (res.ok) {
        credRef.current = { u: username, h };
        setPassword('');
        setAuth({ token: res.token, via: 'password', exp: Date.now() + REMOTE_TTL });
        return;
      }
      setAuthError(res.status === 401 ? t('remote.invalidCredentials') : res.msg || 'network error');
    } catch (err: any) {
      setAuthError(String(err?.message || err));
    }
    setPhase('out');
  };

  const retryAuto = async () => {
    setPhase('checking');
    setAuthError(null);
    const a = await autoSignIn();
    setAuth(a);
    if (!a) setAuthError(t('remote.autoFailed'));
  };

  function signOut() {
    const a = auth;
    credRef.current = null;
    setAuth(null);
    setInfo(null);
    setOp({ kind: 'idle' });
    if (a) rfetch('/logout', { method: 'POST' }, a.token).catch(() => {});
  }

  const openUi = () => {
    if (!auth) return;
    window.open(`${proxyBase}/scale/#tok=${encodeURIComponent(auth.token)}`, '_blank');
  };

  /**
   * After an upgrade: the remote goes away and comes back. Done when it
   * answers again after being unreachable, or when the old session is gone
   * (sessions do not survive a restart). Then signs in again the same way.
   */
  const awaitRestart = async (from: string) => {
    if (!auth) return;
    const via = auth.via;
    const t0 = Date.now();
    let down = false;
    let back = false;
    while (Date.now() - t0 < RESTART_WAIT && alive.current) {
      await sleep(2000);
      try {
        const r = await rfetch('/node', {}, auth.token);
        if (r.status === 401 || (down && r.ok)) { back = true; break; }
        if (!r.ok) down = true;
      } catch { down = true; }
    }
    if (!alive.current) return;
    if (!back) { setOp({ kind: 'error', message: t('remote.restartTimeout') }); return; }
    let a: Auth | null = null;
    for (let i = 0; i < 5 && !a; i++) {
      a = await autoSignIn();
      if (!a) await sleep(2000);
    }
    if (!a || (via === 'trust' && a.via !== 'trust')) {
      setAuth(a);
      setOp({ kind: 'error', message: t('remote.signInAgain') });
      return;
    }
    setAuth(a);
    try {
      const r = await rfetch('/system/arch', {}, a.token);
      const d = await r.json();
      setInfo({ version: d.version, os: d.os, arch: d.arch, inDocker: !!d.in_docker });
      setOp({ kind: 'done', from, to: d.version });
    } catch {
      setOp({ kind: 'done', from, to: '' });
    }
  };

  const checkGithub = async () => {
    if (!auth) return;
    setOp({ kind: 'checking' });
    try {
      const r = await rfetch('/upgrade/check', {}, auth.token);
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      const d = await r.json();
      setOp({ kind: 'checked', latest: d.latest || '', available: !!d.update_available, size: d.size || 0 });
    } catch (err: any) {
      setOp({ kind: 'error', message: t('remote.checkFailed', { msg: String(err?.message || err) }) });
    }
  };

  const upgradeGithub = async (latest: string) => {
    if (!auth || !info) return;
    if (!await confirm({
      title: t('remote.upgradeTitle', { name: targetLabel }),
      message: t('remote.upgradeGithubConfirm', { from: info.version, to: latest }),
      confirmText: t('remote.upgrade'),
    })) return;
    const from = info.version;
    setOp({ kind: 'downloading', progress: 0 });
    try {
      let r = await rfetch('/upgrade/download', { method: 'POST' }, auth.token);
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      for (;;) {
        await sleep(1000);
        if (!alive.current) return;
        r = await rfetch('/upgrade/status', {}, auth.token);
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        const s = await r.json();
        if (s.state === 'error') throw new Error(s.error || 'download failed');
        if (s.state === 'ready') break;
        setOp({ kind: 'downloading', progress: s.progress || 0 });
      }
      r = await rfetch('/upgrade/apply', { method: 'POST' }, auth.token);
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      setOp({ kind: 'restarting', from });
      await awaitRestart(from);
    } catch (err: any) {
      setOp({ kind: 'error', message: String(err?.message || err) });
    }
  };

  const pushLocal = async () => {
    if (!auth || !info || !local) return;
    if (!await confirm({
      title: t('remote.upgradeTitle', { name: targetLabel }),
      message: t('remote.pushConfirm', { from: info.version, to: local.version }),
      confirmText: t('remote.upgrade'),
    })) return;
    const from = info.version;
    setOp({ kind: 'pushing' });
    try {
      const res = await request<{ ok: boolean; status: number; message: string }>('/remote/push-binary', {
        method: 'POST', body: JSON.stringify({ chain, token: auth.token }),
      });
      if (!res.ok) throw new Error(res.message || `HTTP ${res.status}`);
      setOp({ kind: 'restarting', from });
      await awaitRestart(from);
    } catch (err: any) {
      setOp({ kind: 'error', message: String(err?.message || err) });
    }
  };

  // ---------------------------------------------------------------- render
  const signedIn = phase === 'in' && !!auth;
  const busy = op.kind === 'checking' || op.kind === 'downloading' || op.kind === 'pushing' || op.kind === 'restarting';
  const platform = info ? `${info.os}/${info.arch}` : '';
  const localPlatform = local ? `${local.os}/${local.arch}` : '';
  const upgradeBlock = info && !info.inDocker ? t('remote.notContainer') : '';
  const pushBlock = upgradeBlock || (!info || !local ? '…'
    : localPlatform !== platform ? t('remote.platformDiffers', { platform })
      : !versionGreater(local.version, info.version) ? t('remote.notNewer') : '');

  const authBody = (() => {
    if (phase === 'checking') return <div className="remote-muted">{t('remote.connecting')}</div>;
    if (signedIn) {
      return (
        <div className="remote-auth-in" data-testid="remote-auth-in">
          <Badge variant="green">{t('remote.signedIn')}</Badge>
          <span className="remote-muted">{auth!.via === 'trust' ? t('remote.viaTrust') : t('remote.viaPassword')}</span>
          <span className="remote-muted" data-testid="remote-expiry">{t('remote.expiresIn', { time: mmss(auth!.exp - now) })}</span>
          <span className="spacer" />
          <Button size="sm" onClick={signOut} disabled={busy} data-testid="remote-signout">{t('remote.signOut')}</Button>
        </div>
      );
    }
    return (
      <form className="remote-auth-form" onSubmit={submit} data-testid="remote-auth-form">
        <FormGroup label={t('app.username')}>
          <Input value={username} onChange={(e) => setUsername(e.target.value)} />
        </FormGroup>
        <FormGroup label={t('app.password')}>
          <PasswordInput value={password} onChange={(e) => setPassword(e.target.value)} autoFocus data-testid="remote-password" />
        </FormGroup>
        <div className="remote-auth-actions">
          <Button type="submit" variant="primary" loading={phase === 'signing'} disabled={!password} data-testid="remote-signin">{t('remote.signIn')}</Button>
          <Button type="button" onClick={retryAuto}>{t('remote.retryAuto')}</Button>
        </div>
        {authError && <div className="remote-error">{authError}</div>}
      </form>
    );
  })();

  const opStatus = (() => {
    switch (op.kind) {
      case 'checking': return <div className="remote-muted">{t('remote.checking')}</div>;
      case 'downloading': return (
        <div className="remote-progress">
          <span className="remote-muted">{t('remote.downloading', { pct: Math.round(op.progress) })}</span>
          <Progress value={op.progress} />
        </div>
      );
      case 'pushing': return <div className="remote-muted">{t('remote.pushing', { size: local ? fmtBytes(local.size) : '' })}</div>;
      case 'restarting': return <div className="remote-muted">{t('remote.restarting')}</div>;
      case 'done': return (
        <div className={op.to && op.to !== op.from ? 'remote-ok' : 'remote-warn'} data-testid="remote-done">
          {op.to && op.to !== op.from ? t('remote.upgraded', { from: op.from, to: op.to }) : t('remote.unchanged', { version: op.to || op.from })}
        </div>
      );
      case 'error': return <div className="remote-error" data-testid="remote-op-error">{op.message}</div>;
      default: return null;
    }
  })();

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('remote.title', { name: targetLabel })}
      animateFrom={animateFrom}
      wide
      footer={<><span className="spacer" /><Button onClick={onClose}>{t('app.close')}</Button></>}
    >
      <div className="remote" data-testid="remote-modal">
        <section className="remote-sec">
          <div className="remote-sec-title">{t('remote.auth')}</div>
          {authBody}
        </section>

        <section className={clsx('remote-sec', 'remote-ops', !signedIn && 'locked')} aria-disabled={!signedIn} data-testid="remote-ops">
          <div className="remote-sec-title">{t('remote.manage')}</div>
          <DetailGrid
            variant="plain"
            columns={3}
            items={[
              [t('remote.version'), <span className="mono" data-testid="remote-version">{info?.version || '—'}</span>],
              [t('remote.platform'), <span className="mono">{platform || '—'}</span>],
              [t('remote.deployment'), info ? (info.inDocker ? t('remote.container') : t('remote.native')) : '—'],
            ]}
          />
          <div className="remote-row">
            <Button variant="primary" onClick={openUi} disabled={!signedIn} data-testid="remote-open-ui">{t('remote.openUi')}</Button>
          </div>

          <div className="remote-sub">{t('remote.upgradeSection')}</div>
          {upgradeBlock && <div className="remote-muted">{upgradeBlock}</div>}
          <div className="remote-upg">
            <div className="remote-upg-item">
              <div className="remote-upg-name">{t('remote.fromGithub')}</div>
              <div className="remote-muted">
                {op.kind === 'checked'
                  ? (op.available ? t('remote.latestAvailable', { version: op.latest }) : t('remote.upToDate', { version: op.latest || info?.version || '' }))
                  : t('remote.fromGithubHint')}
              </div>
              <div className="remote-upg-actions">
                {op.kind === 'checked' && op.available ? (
                  <Button size="sm" variant="primary" disabled={!signedIn || busy || !!upgradeBlock}
                    onClick={() => upgradeGithub(op.latest)} data-testid="remote-github-upgrade">
                    {t('remote.upgradeTo', { version: op.latest })}
                  </Button>
                ) : (
                  <Button size="sm" disabled={!signedIn || busy || !!upgradeBlock} loading={op.kind === 'checking'}
                    onClick={checkGithub} data-testid="remote-github-check">
                    {t('remote.checkUpdate')}
                  </Button>
                )}
              </div>
            </div>
            <div className="remote-upg-item">
              <div className="remote-upg-name">{t('remote.pushLocal')}</div>
              <div className="remote-muted">
                {local ? t('remote.localBinary', { version: local.version, platform: localPlatform, size: fmtBytes(local.size) }) : '…'}
              </div>
              <div className="remote-upg-actions">
                <span title={signedIn && pushBlock !== '…' ? pushBlock || undefined : undefined}>
                  <Button size="sm" disabled={!signedIn || busy || !!pushBlock} onClick={pushLocal} data-testid="remote-push">
                    {t('remote.install')}
                  </Button>
                </span>
                {signedIn && pushBlock && pushBlock !== '…' && !upgradeBlock && <span className="remote-muted" data-testid="remote-push-reason">{pushBlock}</span>}
              </div>
            </div>
          </div>
          {opStatus}
        </section>
      </div>
    </Modal>
  );
}
