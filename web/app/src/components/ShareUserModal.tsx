import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import clsx from 'clsx';
import { Modal, Button, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import type { UserConfig, TopologyNode } from '@/api';
import { useNodeStore } from '@/store/node';
import { useDialogInit } from '@/hooks/useFormInit';
import { remoteChains, useRemoteAccess, sessionFor } from '@/utils/remoteAuth';

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

interface Candidate { name: string; chain: string[]; version: string }

/**
 * Nodes the user can be shared to: manageable from here with a live
 * session, accepting shared users (1.4.4+), not holding it already.
 */
export function useShareCandidates(user: UserConfig | null, enabled: boolean): Candidate[] {
  const topology = useNodeStore((s) => s.topology);
  const chains = useMemo(() => remoteChains(topology), [topology]);
  const live = useRemoteAccess(chains, enabled && !(window as any).__PROXY__);
  return useMemo(() => {
    const held = new Set<string>();
    for (const m of user?.shared?.members || []) {
      held.add(m.name);
      if (m.node_id) held.add(m.node_id);
    }
    const out: Candidate[] = [];
    for (const [name, chain] of chains) {
      if (!live.has(name) || held.has(name)) continue;
      const n = nodeAt(topology, chain);
      // only 1.4.4+ announces the attribute: it implies support
      if (!n || n.attrs?.shared_users !== true) continue;
      out.push({ name, chain, version: n.version || '' });
    }
    return out.sort((a, b) => a.name.localeCompare(b.name));
  }, [chains, live, topology, user]);
}

interface Props {
  open: boolean;
  onClose: () => void;
  user: UserConfig | null;
  animateFrom?: { x: number; y: number };
}

/** Share a user to one more node. */
export default function ShareUserModal({ open, onClose, user, animateFrom }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();
  const candidates = useShareCandidates(user, open);
  const [pick, setPick] = useState('');
  const [loading, setLoading] = useState(false);
  useDialogInit(open, user?.id ?? null, () => { setPick(''); setLoading(false); });
  const chosen = candidates.find((c) => c.name === pick);

  const submit = async () => {
    if (!user || !chosen) return;
    setLoading(true);
    try {
      const token = sessionFor(chosen.name, chosen.chain)?.auth.token;
      await api.shareUser(user.id, chosen.chain, token);
      toast.success(t('users.sharedTo', { name: chosen.name }));
      queryClient.invalidateQueries({ queryKey: ['users'] });
      onClose();
    } catch (e: any) {
      toast.error(String(e?.message || e));
    } finally {
      setLoading(false);
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={user ? t('users.shareTitle', { name: user.username }) : t('users.share')}
      animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          <Button variant="primary" disabled={!chosen} loading={loading} onClick={submit} data-testid="share-confirm">
            {t('users.shareConfirm')}
          </Button>
        </>
      }
    >
      <div className="iface-parent-list" data-testid="share-candidates">
        {candidates.length === 0 && <span className="text-muted">{t('users.shareNoCandidates')}</span>}
        {candidates.map((c) => (
          <label key={c.name} className={clsx('iface-parent-item', pick === c.name && 'on')} data-testid={`share-candidate-${c.name}`}>
            <input type="radio" name="share-target" checked={pick === c.name} onChange={() => setPick(c.name)} />
            <span className="iface-parent-name">{c.name}</span>
            <span className="iface-parent-addr">{c.version}</span>
          </label>
        ))}
      </div>
    </Modal>
  );
}
