import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Modal, Button, Input, PasswordInput, FormGroup, useToast } from '@hy2scale/ui';
import { sha256, getSessionHash, getSavedCredentials } from '@/hooks/useAuth';

interface Props {
  open: boolean;
  onClose: () => void;
  // Relay chain to the remote node, e.g. ["jp"] or ["us","us-east"]. Used
  // to construct the proxy base path /scale/remote/<chain>/.
  chain: string[];
  // Display name of the target — shown in the modal title.
  targetLabel: string;
  // Where to anchor the modal's open animation (the click point on the
  // graph), so it slides out of the dot the user clicked.
  animateFrom?: { x: number; y: number };
}

// RemoteConnectModal handles the entire login handshake for a remote node
// IN-PLACE on the local page, then opens the remote tab with a token
// pinned into the URL hash. The new tab's bootstrap/handoff module
// extracts that hash on the very first synchronous tick, drops it into
// sessionStorage under the proxy basePath key, and strips the fragment —
// so by the time the auth store's create() reads getToken(), the new
// tab is already "logged in" and the remote SPA never renders its own
// LoginPage. SessionStorage is per-tab; we cannot pre-populate the new
// tab's sessionStorage from this tab, which is why the URL hash is the
// transport — hashes are never sent over HTTP, so the token doesn't
// land in any proxy or server access log.
// Flow on confirm:
//   1. Try saved-credentials-on-this-tab (sessionHash) and remember-me
//      credentials in turn — if either matches the remote's web password,
//      the auto-login succeeds silently.
//   2. Otherwise expose a username/password form right here.
//   3. On success, window.open(proxyBase + '/scale/#tok=<token>').
export default function RemoteConnectModal({ open, onClose, chain, targetLabel, animateFrom }: Props) {
  const { t } = useTranslation();
  const toast = useToast();

  const proxyBase = `/scale/remote/${chain.join('/')}`;

  // 'idle'  — modal just opened, haven't tried anything yet.
  // 'auto'  — silently trying saved credentials.
  // 'creds' — auto-login failed (or no creds stored), prompt the user.
  // 'busy'  — submitting the manual creds.
  // 'done'  — token stored, opening the tab.
  const [phase, setPhase] = useState<'idle' | 'auto' | 'creds' | 'busy' | 'done'>('idle');
  const [username, setUsername] = useState('admin');
  const [password, setPassword] = useState('');
  const [errorMsg, setErrorMsg] = useState<string | null>(null);
  // Block multiple confirm clicks while the auto path is racing.
  const triedAutoRef = useRef(false);

  // Reset on every (re-)open so the previous attempt's state doesn't bleed
  // through if the user closes mid-flow and reopens for a different peer.
  useEffect(() => {
    if (!open) return;
    setPhase('idle');
    setUsername('admin');
    setPassword('');
    setErrorMsg(null);
    triedAutoRef.current = false;
  }, [open, chain.join('/')]);

  // POSTs /api/login through the proxy with a pre-hashed password and, on
  // success, returns the token to the caller for handoff to the new tab.
  const tryLogin = async (u: string, passHash: string): Promise<{ ok: boolean; status: number; token?: string; msg?: string }> => {
    try {
      const r = await fetch(proxyBase + '/api/login', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ username: u, password: passHash }),
      });
      if (r.status === 401) return { ok: false, status: 401 };
      if (!r.ok) return { ok: false, status: r.status, msg: `HTTP ${r.status}` };
      const data = await r.json();
      if (!data.token) return { ok: false, status: r.status, msg: 'no token' };
      return { ok: true, status: 200, token: data.token };
    } catch (e: any) {
      return { ok: false, status: 0, msg: String(e?.message || e) };
    }
  };

  const launchRemote = (token: string) => {
    setPhase('done');
    // Token rides via URL hash so the new tab's handoff module can copy
    // it into ITS OWN sessionStorage on the first tick. Parent and child
    // tabs do not share sessionStorage, so we cannot pre-populate the
    // child's storage directly from here.
    const url = proxyBase + '/scale/#tok=' + encodeURIComponent(token);
    window.open(url, '_blank');
    // Close the modal a hair later so the new-tab focus animation has time
    // to start — closing instantly looks like nothing happened.
    setTimeout(onClose, 200);
  };

  // Confirm handler — clicked once when the modal first appears (idle).
  const handleConfirm = async () => {
    if (triedAutoRef.current) return;
    triedAutoRef.current = true;
    setPhase('auto');
    setErrorMsg(null);

    // Step 1: ask the remote to mint a token via the relay-passthrough
    // endpoint. The remote-proxy on this hub is already authenticated to
    // the upstream peer with our system password, so if the remote has
    // RelayAdminPassthrough enabled it returns a token here without ever
    // looking at a web password — and the modal can skip the credentials
    // form entirely. 401/403 just means passthrough isn't on for this
    // node; fall through to the credentials path.
    try {
      const r = await fetch(proxyBase + '/api/relay-passthrough-token', { method: 'POST' });
      if (r.ok) {
        const data = await r.json();
        if (data?.token) {
          launchRemote(data.token);
          return;
        }
      }
    } catch {
      // network error — let the credential path produce the user-facing
      // error message instead of failing silently here.
    }

    // Step 2: try this-tab session hash, then saved (remember-me) creds.
    const candidates: { u: string; h: string }[] = [];
    const sess = getSessionHash();
    if (sess) candidates.push(sess);
    const saved = getSavedCredentials();
    if (saved && (!sess || saved.u !== sess.u || saved.h !== sess.h)) candidates.push(saved);

    for (const c of candidates) {
      const res = await tryLogin(c.u, c.h);
      if (res.ok && res.token) {
        launchRemote(res.token);
        return;
      }
      if (res.status === 0) {
        setErrorMsg(res.msg || 'network error');
        setPhase('creds');
        return;
      }
      // 401 / other auth failure — keep trying remaining candidates.
    }

    // No auto-login succeeded — prompt for credentials.
    setPhase('creds');
  };

  // Manual-creds submit.
  const handleManualSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (phase === 'busy') return;
    setPhase('busy');
    setErrorMsg(null);
    const passHash = await sha256(password);
    const res = await tryLogin(username, passHash);
    if (res.ok && res.token) {
      launchRemote(res.token);
      return;
    }
    setPhase('creds');
    if (res.status === 401) {
      setErrorMsg(t('remote.invalidCredentials'));
    } else {
      const m = res.msg || 'network error';
      setErrorMsg(m);
      toast.error(m);
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('remote.connectTitle', { name: targetLabel })}
      animateFrom={animateFrom}
      footer={
        phase === 'creds' ? (
          <>
            <Button onClick={onClose}>{t('app.cancel')}</Button>
            <Button variant="primary" onClick={handleManualSubmit as any} loading={false}>
              {t('remote.connectBtn')}
            </Button>
          </>
        ) : (
          <>
            <Button onClick={onClose} disabled={phase === 'auto' || phase === 'done'}>{t('app.cancel')}</Button>
            <Button
              variant="primary"
              onClick={handleConfirm}
              loading={phase === 'auto' || phase === 'done'}
              disabled={phase === 'auto' || phase === 'done'}
            >
              {t('remote.connectBtn')}
            </Button>
          </>
        )
      }
    >
      {phase === 'idle' && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          <div style={{ fontSize: 13 }}>{t('remote.connectPrompt', { name: targetLabel })}</div>
          <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('remote.connectHint')}</div>
        </div>
      )}

      {phase === 'auto' && (
        <div style={{ fontSize: 13, color: 'var(--text-muted)' }}>{t('remote.connecting')}</div>
      )}

      {(phase === 'creds' || phase === 'busy') && (
        <form onSubmit={handleManualSubmit} style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('remote.credentialsHint')}</div>
          <FormGroup label={t('app.username')}>
            <Input value={username} onChange={(e) => setUsername(e.target.value)} autoFocus />
          </FormGroup>
          <FormGroup label={t('app.password')}>
            <PasswordInput value={password} onChange={(e) => setPassword(e.target.value)} />
          </FormGroup>
          {errorMsg && (
            <div style={{ color: 'var(--red, #ef4444)', fontSize: 13 }}>{errorMsg}</div>
          )}
          {/* Hidden submit button so Enter in either field submits the form */}
          <button type="submit" style={{ display: 'none' }} />
        </form>
      )}

      {phase === 'done' && (
        <div style={{ fontSize: 13, color: 'var(--text-muted)' }}>{t('remote.opening')}</div>
      )}
    </Modal>
  );
}
