import { useCallback, useRef, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, type UseQueryResult } from '@tanstack/react-query';
import { Card, ExpandButton, Table, useToast, useSelection, useModalState, type Column } from '@hy2scale/ui';
import { ExitViaCell, exitViaSortValue } from '@/components/ExitViaCell';
import ResponsiveActions from '@/components/ResponsiveActions';
import { useImportExport } from '@/components/ImportExport';
import { BulkButtons, EditSelectedButton, pickSelected } from '@/components/SelectionActions';
import { useBulkAction } from '@/hooks/useBulkAction';
import { ListScope } from '@/hooks/useDeselectOnBlankClick';
import * as api from '@/api';
import type { WireGuardPeer } from '@/api';
import WgServerCard from './WgServerCard';
import WgPeerModal from './WgPeerModal';
import WgPeerConfigModal, { type WgPeerConfig } from './WgPeerConfigModal';
import { nextPeerIP } from './wgAddr';
import { boldMarkup } from '@/utils/richText';

/** Endpoints → WireGuard: server settings and its peers. */
export default function WireGuardTab() {
  const wgQ = useQuery({ queryKey: ['wireguard'], queryFn: api.getWireGuard });
  const wg = wgQ.data;
  const { t } = useTranslation();

  // Limited runtime: no WireGuard support on this node.
  if (wg && wg.enabled === undefined && !wg.listen_port) {
    return (
      <Card title={t('wg.title')}>
        <div className="hy-limited-overlay">
          <div className="hy-limited-msg">{t('l2tp.warnText')}</div>
        </div>
      </Card>
    );
  }
  return <WireGuardPanels wgQ={wgQ} />;
}

function WireGuardPanels({ wgQ }: { wgQ: UseQueryResult<api.WireGuardConfig> }) {
  const { t } = useTranslation();
  const ie = useImportExport('wg-peers');
  const toast = useToast();
  const peers = wgQ.data?.peers || [];

  // Server addresses as typed (new peers get the next free host of each).
  const serverAddr = useRef({ v4: '', v6: '' });
  const onAddress = useCallback((v4: string, v6: string) => { serverAddr.current = { v4, v6 }; }, []);

  const edit = useModalState<WireGuardPeer | null>();
  const config = useModalState<WgPeerConfig>();

  const selection = useSelection(peers.map((p) => p.name));
  const selected = pickSelected(peers, selection.selected, (p) => p.name);
  const bulk = useBulkAction(['wireguard']);
  const bulkDelete = () => bulk(selection.selected, (name) => api.deleteWGPeer(name), {
    label: t('app.bulkDelete'),
    confirm: { title: t('app.bulkDelete'), message: t('app.bulkDeleteConfirm', { count: selection.count }), confirmText: t('app.delete'), danger: true },
  });
  // Where the last click in the list landed (row-click dialog origin).
  const lastClick = useRef<{ x: number; y: number } | undefined>(undefined);

  const showConfig = useCallback(async (name: string, from?: MouseEvent | { x: number; y: number }) => {
    try {
      // [Peer] Endpoint defaults to the host this admin UI is reached
      // through — the same node serves WireGuard.
      const res = await api.getWGPeerConfig(name, window.location.hostname);
      const text = await (res as any).text();
      let qr = '';
      try {
        const qrRes = await api.getWGQR(text);
        qr = URL.createObjectURL(await (qrRes as any).blob());
      } catch { /* no QR */ }
      config.show({ name, text, qr }, from);
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [toast, config.show]);

  const suggestIPs = () => [nextPeerIP(serverAddr.current.v4, peers), nextPeerIP(serverAddr.current.v6, peers)].filter(Boolean).join(', ');

  const peerColumns: Column<WireGuardPeer>[] = [
    {
      key: 'name', title: t('wg.peerName'), width: '120px', sortValue: (p) => p.name, render: (p) => (
        <a href="#" className="peer-link peer-name-cell" onClick={(e) => { e.preventDefault(); showConfig(p.name, e); }}>{p.name}</a>
      ),
    },
    { key: 'exit', title: t('wg.peerExitVia'), sortValue: (p) => exitViaSortValue(p.exit_via, p.exit_paths), render: (p) => <ExitViaCell exitVia={p.exit_via || ''} exitPaths={p.exit_paths} exitMode={p.exit_mode} /> },
    { key: 'ips', title: t('wg.peerAllowedIPs'), width: '130px', sortValue: (p) => (p.allowed_ips || '').split(',')[0].trim() || null, render: (p) => <span className="mono">{p.allowed_ips}</span> },
    { key: 'ka', title: t('wg.ka'), width: '50px', sortValue: (p) => p.keepalive || null, render: (p) => <>{p.keepalive || '—'}</> },
  ];

  return (
    <div className="wg-panels">
      <WgServerCard wgQ={wgQ} onAddress={onAddress} />

      <Card
        fill={1}
        title={t('wg.peers')}
        count={peers.length}
        actions={
          <ResponsiveActions selectedCount={selection.count} onClearSelection={selection.clear}>
            <BulkButtons selected={selected} onDelete={bulkDelete} />
            <EditSelectedButton selected={selected} onEdit={(p, e) => edit.show(p, e)} />
            <ExpandButton variant="primary" label={t('wg.addPeer')} moreLabel={t('app.moreActions')}
              onClick={(e) => edit.show(null, e)} actions={ie.actions} />
          </ResponsiveActions>
        }
        noPadding
      >
        <ListScope selection={selection} lastClick={lastClick}>
          <Table
            id="wg.peers"
            columns={peerColumns}
            data={peers}
            rowKey={(p) => p.name}
            emptyText={boldMarkup(t('wg.noPeers'))}
            selection={selection}
            onRowClick={(p) => showConfig(p.name, lastClick.current)}
          />
        </ListScope>
      </Card>

      <WgPeerModal {...edit.modalProps} editing={edit.data ?? null} suggestIPs={suggestIPs} />
      <WgPeerConfigModal {...config.modalProps} config={config.data} />
      {ie.modal}
    </div>
  );
}
