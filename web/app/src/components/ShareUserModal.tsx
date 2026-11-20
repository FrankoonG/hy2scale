import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import clsx from 'clsx';
import { Modal, Button, Input, FormGroup, FormStack, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import type { UserConfig, TopologyNode } from '@/api';
import { useNodeStore } from '@/store/node';
import { useDialogInit } from '@/hooks/useFormInit';
import { sha256, getSessionHash, getSavedCredentials } from '@/hooks/useAuth';
import { remoteChains, useRemoteAccess, sessionFor } from '@/utils/remoteAuth';
import { versionAtLeast } from '@/utils/version';
import RemoteModal from '@/components/RemoteModal';

/** The first release that holds shared users. */
const MIN_VERSION = '1.4.4';

/** The topology node at the end of `chain` (hops from this node). */
function nodeAt(topology: TopologyNode[], chain: string[]): TopologyNode | undefined {
  const self = topology.find((n) => n.is_self);
  let level = [...(self?.children || []), ...topology.filter((n) => !n.is_self)];
  let node: TopologyNode | undefined;
  for (const hop of chain) {
    node = level.find((n) => n.name === hop);
    if (!node) return undefined;
    level = node.children || [];
  }
  return node;
}

/** ok: can be picked; off: does not accept shared users; old: before 1.4.4. */
type CandState = 'ok' | 'off' | 'old';
interface Candidate { name: string; chain: string[]; version: string; state: CandState }

/**
 * Every node this node can manage (reachable, not native, not through node
 * access by user) that does not hold the user yet, with whether it can take
 * it. A sign-in is not needed to be listed: sharing asks for one only when
 * the node wants it.
 */
export function useShareCandidates(user: UserConfig | null, enabled: boolean): Candidate[] {
  const topology = useNodeStore((s) => s.topology);
  const chains = useMemo(() => remoteChains(topology), [topology]);
  // sessions this tab holds (or gets by relay trust) are used when sharing
  useRemoteAccess(chains, enabled && !(window as any).__PROXY__);
  return useMemo(() => {
    const held = new Set<string>();
    for (const m of user?.shared?.members || []) {
      held.add(m.name);
      if (m.node_id) held.add(m.node_id);
    }
    const rank: Record<CandState, number> = { ok: 0, off: 1, old: 2 };
    const out: Candidate[] = [];
    for (const [name, chain] of chains) {
      if (held.has(name)) continue;
      const n = nodeAt(topology, chain);
      if (!n) continue;
      const version = n.version || '';
      const state: CandState = n.attrs?.shared_users === true ? 'ok'
        : version && !versionAtLeast(version, MIN_VERSION) ? 'old' : 'off';
      out.push({ name, chain, version, state });
    }
    return out.sort((a, b) => rank[a.state] - rank[b.state] || a.name.localeCompare(b.name));
  }, [chains, topology, user]);
}

interface Props {
  open: boolean;
  onClose: () => void;
  user: UserConfig | null;
  animateFrom?: { x: number; y: number };
}

/** Share a user to one more node; signs in there when that node asks. */
export default function ShareUserModal({ open, onClose, user, animateFrom }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();
  const candidates = useShareCandidates(user, open);
  const [pick, setPick] = useState('');
  const [loading, setLoading] = useState(false);
  const [login, setLogin] = useState(false);
  const [username, setUsername] = useState('admin');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [remote, setRemote] = useState<Candidate | null>(null);
  useDialogInit(open, user?.id ?? null, () => {
    setPick(''); setLoading(false); setLogin(false); setPassword(''); setError('');
  });
  const chosen = candidates.find((c) => c.name === pick);

  const done = (name: string) => {
    toast.success(t('users.sharedTo', { name }));
    queryClient.invalidateQueries({ queryKey: ['users'] });
    onClose();
  };
  const reason = (e: any) => {
    switch (e?.code) {
      case 'not_accepting': return t('users.shareNotAccepting');
      case 'unsupported': return t('users.shareUnsupported');
      case 'login_failed': return t('remote.invalidCredentials');
      default: return String(e?.message || e);
    }
  };

  // Relay trust or a session this tab holds first, then this tab's own
  // credentials; the sign-in form only when those do not do.
  const share = async () => {
    if (!user || !chosen || chosen.state !== 'ok') return;
    setLoading(true);
    setError('');
    try {
      const token = (window as any).__PROXY__ ? undefined : sessionFor(chosen.name, chosen.chain)?.auth.token;
      try {
        await api.shareUser(user.id, chosen.chain, { token });
        return done(chosen.name);
      } catch (e: any) {
        if (e?.code !== 'needs_login' && e?.code !== 'login_failed') throw e;
      }
      for (const c of [getSessionHash(), getSavedCredentials()]) {
        if (!c) continue;
        try {
          await api.shareUser(user.id, chosen.chain, { username: c.u, password: c.h });
          return done(chosen.name);
        } catch (e: any) {
          if (e?.code !== 'login_failed') throw e;
        }
      }
      setLogin(true);
    } catch (e: any) {
      setError(reason(e));
    } finally {
      setLoading(false);
    }
  };

  const signIn = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!user || !chosen || loading) return;
    setLoading(true);
    setError('');
    try {
      await api.shareUser(user.id, chosen.chain, { username, password: await sha256(password) });
      done(chosen.name);
    } catch (err: any) {
      setError(reason(err));
    } finally {
      setLoading(false);
    }
  };

  const stateText = (c: Candidate) => c.state === 'off' ? t('users.shareStateOff')
    : c.state === 'old' ? t('users.shareStateOld', { version: MIN_VERSION }) : c.version;

  return (
    <>
      <Modal
        open={open}
        onClose={onClose}
        title={login && chosen ? t('users.shareSignIn', { name: chosen.name }) : user ? t('users.shareTitle', { name: user.username }) : t('users.share')}
        animateFrom={animateFrom}
        footer={login ? (
          <>
            <Button onClick={() => { setLogin(false); setError(''); }}>{t('app.cancel')}</Button>
            <Button variant="primary" loading={loading} disabled={!password} onClick={() => signIn()} data-testid="share-signin">
              {t('users.shareConfirm')}
            </Button>
          </>
        ) : (
          <>
            <Button onClick={onClose}>{t('app.cancel')}</Button>
            <Button variant="primary" disabled={!chosen || chosen.state !== 'ok'} loading={loading} onClick={share} data-testid="share-confirm">
              {t('users.shareConfirm')}
            </Button>
          </>
        )}
      >
        {login && chosen ? (
          <form onSubmit={signIn} data-testid="share-login">
            <FormStack>
              <div className="text-muted">{t('users.shareSignInHint')}</div>
              <FormGroup label={t('app.username')}>
                <Input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" data-testid="share-username" />
              </FormGroup>
              <FormGroup label={t('app.password')}>
                <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoFocus autoComplete="off" data-testid="share-password" />
              </FormGroup>
              {error && <div className="text-bad" data-testid="share-error">{error}</div>}
              <button type="submit" hidden />
            </FormStack>
          </form>
        ) : (
          <FormStack>
            <div className="iface-parent-list" data-testid="share-candidates">
              {candidates.length === 0 && <span className="text-muted">{t('users.shareNoCandidates')}</span>}
              {candidates.map((c) => (
                <label key={c.name} className={clsx('iface-parent-item', pick === c.name && 'on')} data-testid={`share-candidate-${c.name}`} data-state={c.state}>
                  <input type="radio" name="share-target" checked={pick === c.name} onChange={() => { setPick(c.name); setError(''); }} />
                  <span className="iface-parent-name">{c.name}</span>
                  <span className={clsx('iface-parent-addr', c.state !== 'ok' && 'text-muted')}>{stateText(c)}</span>
                </label>
              ))}
            </div>
            {chosen && chosen.state === 'off' && (
              <div className="text-muted" data-testid="share-off">
                {t('users.shareNotAccepting')}{' '}
                <Button size="sm" onClick={() => setRemote(chosen)} data-testid="share-open-remote">{t('remote.manage')}</Button>
              </div>
            )}
            {chosen && chosen.state === 'old' && <div className="text-muted">{t('users.shareUnsupported')}</div>}
            {error && <div className="text-bad" data-testid="share-error">{error}</div>}
          </FormStack>
        )}
      </Modal>
      <RemoteModal
        open={!!remote}
        onClose={() => setRemote(null)}
        chain={remote?.chain ?? []}
        targetLabel={remote?.name ?? ''}
      />
    </>
  );
}
