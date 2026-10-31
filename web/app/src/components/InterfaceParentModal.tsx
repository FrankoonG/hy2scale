import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import { Modal, Button } from '@hy2scale/ui';
import type { IfaceEntry } from '@/api/types';
import { useDialogInit } from '@/hooks/useFormInit';

interface Props {
  open: boolean;
  onClose: () => void;
  /** Physical NICs that take child interfaces. */
  candidates: IfaceEntry[];
  onNext: (parent: IfaceEntry) => void;
  animateFrom?: { x: number; y: number };
}

/** Step 1 of adding a child interface: pick its physical NIC. */
export default function InterfaceParentModal({ open, onClose, candidates, onNext, animateFrom }: Props) {
  const { t } = useTranslation();
  const [pick, setPick] = useState('');
  useDialogInit(open, 'parent', () => setPick(candidates.length === 1 ? candidates[0].name : ''));
  const chosen = candidates.find((c) => c.name === pick);

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('iface.pickParent')}
      animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          <Button variant="primary" disabled={!chosen} onClick={() => chosen && onNext(chosen)} data-testid="iface-parent-next">
            {t('iface.next')}
          </Button>
        </>
      }
    >
      <div className="iface-parent-list" data-testid="iface-parent-list">
        {candidates.length === 0 && <span className="iface-muted">{t('iface.noParent')}</span>}
        {candidates.map((c) => {
          const v4 = c.ipv4?.addrs?.[0]?.prefix;
          const v6 = c.ipv6?.addrs?.find((a) => !a.prefix.toLowerCase().startsWith('fe80'))?.prefix;
          return (
            <label key={c.name} className={clsx('iface-parent-item', pick === c.name && 'on')}>
              <input type="radio" name="iface-parent" checked={pick === c.name} onChange={() => setPick(c.name)} />
              <span className="iface-parent-name">{c.name}</span>
              <span className="iface-parent-addr">{[v4, v6].filter(Boolean).join(' · ') || '—'}</span>
              {c.wireless && <span className="iface-tag faint">{t('iface.wireless')}</span>}
            </label>
          );
        })}
      </div>
    </Modal>
  );
}
