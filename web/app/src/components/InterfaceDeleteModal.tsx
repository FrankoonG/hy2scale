import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Modal, Button, Select, FormGroup, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import type { IfaceEntry, IfaceReference } from '@/api/types';
import { useDialogInit } from '@/hooks/useFormInit';

interface Props {
  open: boolean;
  onClose: () => void;
  /** Child interfaces to delete (one or several). */
  targets: IfaceEntry[];
  /** Other existing interfaces the references can move to. */
  candidates: string[];
  animateFrom?: { x: number; y: number };
  onDeleted?: () => void;
}

export default function InterfaceDeleteModal({ open, onClose, targets, candidates, animateFrom, onDeleted }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [replacement, setReplacement] = useState('');
  const [deleting, setDeleting] = useState(false);
  const names = targets.map((x) => x.name);
  const key = names.join(',');

  useDialogInit(open, key, () => setReplacement(''));

  const refsQ = useQuery({
    queryKey: ['ifaceRefs', key],
    queryFn: async () => {
      const all = await Promise.all(names.map(async (n) => {
        const r = await api.getIfaceReferences(n);
        return (Array.isArray(r) ? r : []).map((ref) => ({ ...ref, iface: n }));
      }));
      return all.flat();
    },
    enabled: open && names.length > 0,
    staleTime: 0,
  });
  const refs: (IfaceReference & { iface: string })[] = Array.isArray(refsQ.data) ? refsQ.data : [];

  const refText = (r: IfaceReference & { iface: string }) => {
    let text: string = r.type;
    if (r.type === 'egress') {
      const fam = (r as any).family === 'ipv6' ? 'IPv6' : 'IPv4';
      text = t('iface.refEgress', { fam });
    } else if (r.type === 'punch') {
      text = t('iface.refPunch');
    } else if (r.type === 'node_address') {
      text = t('iface.refNode', { node: (r as any).node, address: (r as any).address });
    }
    return names.length > 1 ? `${r.iface}: ${text}` : text;
  };

  const handleDelete = async () => {
    if (!names.length || !replacement) return;
    setDeleting(true);
    try {
      for (const n of names) await api.deleteChildIface(n, replacement);
      toast.success(names.length > 1
        ? t('iface.deletedMany', { count: names.length })
        : t('iface.deleted', { name: names[0] }));
      queryClient.invalidateQueries({ queryKey: ['ifaces'] });
      queryClient.invalidateQueries({ queryKey: ['interfaces'] });
      queryClient.invalidateQueries({ queryKey: ['topology'] });
      onDeleted?.();
      onClose();
    } catch (e: any) {
      toast.error(String(e.message || e));
      queryClient.invalidateQueries({ queryKey: ['ifaces'] });
    } finally {
      setDeleting(false);
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={names.length > 1 ? t('iface.deleteManyTitle', { count: names.length }) : t('iface.deleteTitle', { name: names[0] || '' })}
      animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          <Button variant="danger" onClick={handleDelete} loading={deleting} disabled={!replacement}
            data-testid="iface-delete-confirm">{t('app.delete')}</Button>
        </>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }} data-testid="iface-delete-modal">
        <FormGroup label={t('iface.references')}>
          <div className="iface-ref-list" data-testid="iface-refs">
            {refsQ.isLoading ? (
              <span className="iface-muted">{t('app.loading')}</span>
            ) : refsQ.isError ? (
              <span className="iface-bad">{String((refsQ.error as any)?.message || refsQ.error)}</span>
            ) : refs.length === 0 ? (
              <span className="iface-muted">{t('iface.noRefs')}</span>
            ) : (
              refs.map((r, i) => <div key={i}>{refText(r)}</div>)
            )}
          </div>
        </FormGroup>
        <FormGroup label={t('iface.replacement')} required>
          <div data-testid="iface-replacement">
            <Select
              value={replacement}
              onChange={(e) => setReplacement(e.target.value)}
              placeholder={t('iface.selectReplacement')}
              options={candidates.filter((c) => !names.includes(c)).map((c) => ({ value: c, label: c }))}
            />
          </div>
        </FormGroup>
      </div>
    </Modal>
  );
}
