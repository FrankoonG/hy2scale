import { type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Modal, Button, DetailGrid } from '@hy2scale/ui';
import type { IfaceEntry, IfaceFamilyKey } from '@/api/types';
import { fmtBytes, fmtDuration } from '@/hooks/useFormat';

const FAMS: IfaceFamilyKey[] = ['ipv4', 'ipv6'];
const FAM_LABEL: Record<IfaceFamilyKey, string> = { ipv4: 'IPv4', ipv6: 'IPv6' };

interface Props {
  open: boolean;
  onClose: () => void;
  iface: IfaceEntry | null;
  parent?: IfaceEntry;
  animateFrom?: { x: number; y: number };
  /** Child interfaces: edit / enable·disable / renew / delete. */
  onEdit?: () => void;
  onToggle?: () => void;
  onRenew?: () => void;
  onDelete?: () => void;
  /** Physical NICs that take child interfaces. */
  onAddChild?: () => void;
  addChildDisabledReason?: string;
}

export default function InterfaceDetailModal({
  open, onClose, iface: x, parent, animateFrom,
  onEdit, onToggle, onRenew, onDelete, onAddChild, addChildDisabledReason,
}: Props) {
  const { t } = useTranslation();
  const items: [string, ReactNode][] = [];
  if (x) {
    const srcLabel = (s: string) => t(`iface.src.${s}`, { defaultValue: s });
    // Only families that have addresses; one "—" row when neither does.
    let shown = 0;
    for (const fam of FAMS) {
      const f = x[fam];
      const addrs = f?.addrs || [];
      const on = !!f && f.mode !== 'off';
      if (addrs.length) {
        items.push([FAM_LABEL[fam], <span className="detail-lines">{addrs.map((a) => <span key={a.prefix}>{a.prefix} <span className="iface-muted">{srcLabel(a.source)}</span></span>)}</span>]);
        shown++;
      } else if (on && f.state === 'acquiring') {
        items.push([FAM_LABEL[fam], <span className="iface-pending">{t('iface.acquiring')}</span>]);
        shown++;
      } else if (on && f.state === 'failed') {
        items.push([FAM_LABEL[fam], <span className="iface-bad" title={f.error || undefined}>{t('iface.failed')}</span>]);
        shown++;
      }
    }
    if (!shown) items.push([t('iface.address'), '—']);
    for (const fam of FAMS) {
      const p = x.public?.[fam];
      if (p) items.push([t('iface.publicFam', { fam: FAM_LABEL[fam] }), p.ok ? p.ip : <span className="iface-bad">{t('iface.unavailable')}</span>]);
    }
    items.push([t('iface.mac'), x.mac || '—']);
    items.push([t('iface.mtu'), x.kind === 'child' && !x.mtu
      ? `${parent?.mtu || ''}${parent?.mtu ? ' ' : ''}(${t('iface.mtuFollow')})`
      : (x.mtu || '—')]);
    if (x.kind === 'child') {
      items.push([t('iface.parent'), x.parent || '—']);
      items.push([t('iface.mode'), t(x.mode === 'system' ? 'iface.modeSystem' : 'iface.modeBuiltin')]);
      items.push([t('iface.vlan'), x.vlan?.enabled ? String(x.vlan.id) : t('iface.untagged')]);
    }
    for (const fam of FAMS) items.push([t('iface.gatewayFam', { fam: FAM_LABEL[fam] }), x[fam]?.gateway || '—']);
    const dns = [...(x.ipv4?.dns || []), ...(x.ipv6?.dns || [])];
    items.push([t('iface.dns'), dns.length ? dns.join(', ') : '—']);
    if (x.ipv4?.mode === 'dhcp') {
      const lease = x.ipv4.addrs?.find((a) => a.expires > 0);
      items.push([t('iface.lease'), lease ? fmtDuration(Math.max(0, Math.round(lease.expires - Date.now() / 1000))) || '0s' : '—']);
      if (x.ipv4.hostname) items.push([t('iface.hostname'), x.ipv4.hostname]);
      if (x.ipv4.client_id) items.push([t('iface.clientId'), x.ipv4.client_id]);
    }
    items.push([t('iface.traffic'), (
      <span>
        <span className="stat-up">↑{fmtBytes(x.tx_bytes || 0)}</span>
        <span className="sep">/</span>
        <span className="stat-down">↓{fmtBytes(x.rx_bytes || 0)}</span>
      </span>
    )]);
    const errs = [x.error, ...FAMS.map((f) => (x[f]?.state === 'failed' ? x[f]?.error : ''))].filter(Boolean);
    if (errs.length) items.push([t('iface.error'), <span className="iface-bad">{errs.join('; ')}</span>]);
  }
  const child = x?.kind === 'child';
  const off = x?.enabled === false;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={x?.name}
      animateFrom={animateFrom}
      wide
      footer={
        <>
          {child && onDelete && <Button variant="danger" onClick={onDelete} data-testid="iface-detail-delete">{t('app.delete')}</Button>}
          <span className="spacer" />
          {child && onRenew && x?.ipv4?.mode === 'dhcp' && (
            <Button onClick={onRenew} disabled={off} data-testid="iface-detail-renew">{t('iface.renew')}</Button>
          )}
          {child && onToggle && (
            <Button onClick={onToggle} data-testid="iface-detail-toggle">{off ? t('app.enable') : t('app.disable')}</Button>
          )}
          {child && onEdit && <Button variant="primary" onClick={onEdit} data-testid="iface-detail-edit">{t('app.edit')}</Button>}
          {!child && onAddChild && (
            <span title={addChildDisabledReason || undefined} className="inline-flex">
              <Button variant="primary" onClick={onAddChild} disabled={!!addChildDisabledReason} data-testid="iface-detail-add">{t('iface.addChild')}</Button>
            </span>
          )}
        </>
      }
    >
      <DetailGrid items={items} testId={x ? `detail-${x.name}` : undefined} />
    </Modal>
  );
}
