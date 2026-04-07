import { useState, useEffect, useCallback, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Card, Button, Input, Toggle, FormGroup, FormGrid, Table, Badge, Modal,
  IconButton, CopyButton, useToast, useConfirm, type Column,
} from '@hy2scale/ui';
import { ExitPathList, exitPathToApi, apiToExitPath, type ExitPathValue } from '@/components/ExitPathList';
import * as api from '@/api';
import type { WireGuardPeer } from '@/api';

export default function WireGuardTab({ limited }: { limited?: boolean }) {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const queryClient = useQueryClient();

  const { data: wg } = useQuery({ queryKey: ['wireguard'], queryFn: api.getWireGuard });

  // Server config
  const [enabled, setEnabled] = useState(false);
  const [listenPort, setListenPort] = useState('');
  const [address, setAddress] = useState('');
  const [privKey, setPrivKey] = useState('');
  const [wgMtu, setWgMtu] = useState('1420');
  const [saving, setSaving] = useState(false);

  // Peer modal
  const [peerOpen, setPeerOpen] = useState(false);
  const [editPeerName, setEditPeerName] = useState<string | null>(null);
  const [peerName, setPeerName] = useState('');
  const [peerPubKey, setPeerPubKey] = useState('');
  const [peerPrivKey, setPeerPrivKey] = useState('');
  const [peerAllowedIPs, setPeerAllowedIPs] = useState('');
  const [peerKeepalive, setPeerKeepalive] = useState('');
  const [peerExitPath, setPeerExitPath] = useState<ExitPathValue>({ paths: [''], mode: '' });
  const [peerSaving, setPeerSaving] = useState(false);

  // Peer detail modal
  const [detailOpen, setDetailOpen] = useState(false);
  const [detailConfig, setDetailConfig] = useState('');
  const [detailName, setDetailName] = useState('');

  const [clickPos, setClickPos] = useState<{ x: number; y: number } | undefined>();

  useEffect(() => {
    if (wg) {
      setEnabled(wg.enabled);
      setListenPort(String(wg.listen_port || ''));
      setAddress(wg.address || '');
      setPrivKey(wg.private_key || '');
      setWgMtu(String(wg.mtu || 1420));
    }
  }, [wg]);

  const handleSaveServer = async () => {
    setSaving(true);
    try {
      await api.updateWireGuard({
        enabled, listen_port: parseInt(listenPort) || 0,
        address, private_key: privKey, mtu: parseInt(wgMtu) || 1420,
      } as any);
      toast.success(t('wg.saved'));
      queryClient.invalidateQueries({ queryKey: ['wireguard'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setSaving(false); }
  };

  const generateKey = async () => {
    try {
      const { private_key, public_key } = await api.generateWGKey();
      setPrivKey(private_key);
    } catch (e: any) { toast.error(String(e.message || e)); }
  };

  const openAddPeer = (e: MouseEvent) => {
    setClickPos({ x: e.clientX, y: e.clientY });
    setEditPeerName(null);
    setPeerName(''); setPeerPubKey(''); setPeerPrivKey(''); setPeerAllowedIPs('0.0.0.0/0');
    setPeerKeepalive('25'); setPeerExitPath({ paths: [''], mode: '' });
    setPeerOpen(true);
  };

  const openEditPeer = (peer: WireGuardPeer, e: MouseEvent) => {
    setClickPos({ x: e.clientX, y: e.clientY });
    setEditPeerName(peer.name);
    setPeerName(peer.name); setPeerPubKey(peer.public_key); setPeerPrivKey(peer.private_key);
    setPeerAllowedIPs(peer.allowed_ips); setPeerKeepalive(String(peer.keepalive || ''));
    setPeerExitPath(apiToExitPath(peer.exit_via, peer.exit_paths, peer.exit_mode));
    setPeerOpen(true);
  };

  const handleSavePeer = async () => {
    if (!peerName) { toast.error(t('wg.nameRequired')); return; }
    if (!peerPubKey && !peerPrivKey) { toast.error(t('wg.pubKeyRequired')); return; }
    if (!peerAllowedIPs) { toast.error(t('wg.allowedIpsRequired')); return; }

    setPeerSaving(true);
    const exitData = exitPathToApi(peerExitPath);
    const data: WireGuardPeer = {
      name: peerName, public_key: peerPubKey, private_key: peerPrivKey,
      allowed_ips: peerAllowedIPs, keepalive: parseInt(peerKeepalive) || 0, ...exitData,
    } as any;

    try {
      if (editPeerName) {
        await api.updateWGPeer(editPeerName, data);
        toast.success(t('wg.peerUpdated'));
      } else {
        await api.createWGPeer(data);
        toast.success(t('wg.peerAdded'));
      }
      queryClient.invalidateQueries({ queryKey: ['wireguard'] });
      setPeerOpen(false);
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setPeerSaving(false); }
  };

  const generatePeerKey = async () => {
    try {
      const { private_key, public_key } = await api.generateWGKey();
      setPeerPrivKey(private_key);
      setPeerPubKey(public_key);
    } catch (e: any) { toast.error(String(e.message || e)); }
  };

  const handleDeletePeer = useCallback(async (name: string) => {
    const ok = await confirm({
      title: t('app.delete'), message: t('wg.deleteConfirm', { name }),
      danger: true, confirmText: t('app.delete'), cancelText: t('app.cancel'),
    });
    if (!ok) return;
    try {
      await api.deleteWGPeer(name);
      toast.success(t('wg.peerRemoved'));
      queryClient.invalidateQueries({ queryKey: ['wireguard'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [confirm, t, queryClient, toast]);

  const showPeerConfig = useCallback(async (name: string) => {
    try {
      const res = await api.getWGPeerConfig(name);
      const text = await (res as any).text();
      setDetailName(name);
      setDetailConfig(text);
      setDetailOpen(true);
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [toast]);

  const downloadConf = () => {
    const blob = new Blob([detailConfig], { type: 'text/plain' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url; a.download = `${detailName}.conf`; a.click();
    URL.revokeObjectURL(url);
  };

  if (limited || (wg && wg.enabled === undefined && !wg.listen_port)) {
    return (
      <Card title={t('wg.title')}>
        <div className="hy-limited-overlay">
          <div className="hy-limited-msg">{t('l2tp.warnText')}</div>
        </div>
      </Card>
    );
  }

  const peers = wg?.peers || [];

  const peerColumns: Column<WireGuardPeer>[] = [
    { key: 'name', title: t('wg.peerName'), render: (p) => <strong>{p.name}</strong> },
    { key: 'pubkey', title: t('wg.peerPubKey'), render: (p) => <span className="mono" style={{ fontSize: 11 }}>{p.public_key?.slice(0, 20)}...</span> },
    { key: 'ips', title: t('wg.peerAllowedIPs'), render: (p) => <span className="mono">{p.allowed_ips}</span> },
    { key: 'ka', title: t('wg.ka'), render: (p) => p.keepalive || '—' },
    {
      key: 'actions', title: '', width: '120px', render: (p) => (
        <div className="actions">
          <IconButton icon="📋" tooltip={t('wg.downloadConf')} onClick={() => showPeerConfig(p.name)} />
          <IconButton icon="✎" tooltip={t('app.edit')} onClick={(e) => openEditPeer(p, e as any)} />
          <IconButton icon="✕" variant="danger" tooltip={t('app.delete')} onClick={() => handleDeletePeer(p.name)} />
        </div>
      ),
    },
  ];

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
      <Card title={t('wg.title')}>
        <div style={{ maxWidth: 500, display: 'flex', flexDirection: 'column', gap: 14 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <FormGroup label={t('app.enabled')}>
              <Toggle checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
            </FormGroup>
            {wg?.running && <Badge variant="green">{t('wg.runningStatus')}</Badge>}
            {wg?.connected !== undefined && wg.connected > 0 && (
              <Badge variant="blue">{t('wg.connectedStatus', { count: wg.connected })}</Badge>
            )}
          </div>
          <FormGrid>
            <FormGroup label={t('wg.port')}>
              <Input type="number" value={listenPort} onChange={(e) => setListenPort(e.target.value)} placeholder="51820" />
            </FormGroup>
            <FormGroup label={t('wg.address')}>
              <Input value={address} onChange={(e) => setAddress(e.target.value)} placeholder="10.0.0.1/24" />
            </FormGroup>
          </FormGrid>
          <FormGroup label={t('wg.privKey')}>
            <div style={{ display: 'flex', gap: 8 }}>
              <Input value={privKey} onChange={(e) => setPrivKey(e.target.value)} style={{ flex: 1, fontFamily: 'var(--mono)', fontSize: 11 }} />
              <Button size="sm" onClick={generateKey}>{t('wg.generateKey')}</Button>
            </div>
          </FormGroup>
          {wg?.public_key && (
            <FormGroup label={t('wg.pubKey')}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <span className="mono" style={{ fontSize: 11 }}>{wg.public_key}</span>
                <CopyButton text={wg.public_key} />
              </div>
            </FormGroup>
          )}
          <FormGroup label={t('wg.mtu')}>
            <Input type="number" value={wgMtu} onChange={(e) => setWgMtu(e.target.value)} />
          </FormGroup>
          <Button variant="primary" onClick={handleSaveServer} loading={saving} style={{ alignSelf: 'flex-start' }}>{t('app.save')}</Button>
        </div>
      </Card>

      <Card
        title={t('wg.peers')}
        count={peers.length}
        actions={<Button size="sm" variant="primary" onClick={openAddPeer}>{t('wg.addPeer')}</Button>}
        noPadding
      >
        <Table columns={peerColumns} data={peers} rowKey={(p) => p.name} emptyText={t('wg.noPeers')} />
      </Card>

      {/* Add/Edit Peer Modal */}
      <Modal
        open={peerOpen} onClose={() => setPeerOpen(false)}
        title={editPeerName ? t('wg.editPeerPrefix', { name: editPeerName }) : t('wg.addPeerTitle')}
        animateFrom={clickPos}
        footer={
          <>
            <Button onClick={() => setPeerOpen(false)}>{t('app.cancel')}</Button>
            <Button variant="primary" onClick={handleSavePeer} loading={peerSaving}>{t('app.save')}</Button>
          </>
        }
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <FormGroup label={t('wg.peerName')} required>
            <Input value={peerName} onChange={(e) => setPeerName(e.target.value)} disabled={!!editPeerName} />
          </FormGroup>
          <FormGrid>
            <FormGroup label={t('wg.peerPubKey')}>
              <Input value={peerPubKey} onChange={(e) => setPeerPubKey(e.target.value)} style={{ fontFamily: 'var(--mono)', fontSize: 11 }} />
            </FormGroup>
            <FormGroup label={t('wg.peerPrivKey')}>
              <Input value={peerPrivKey} onChange={(e) => setPeerPrivKey(e.target.value)} style={{ fontFamily: 'var(--mono)', fontSize: 11 }} />
            </FormGroup>
          </FormGrid>
          <Button size="sm" variant="ghost" onClick={generatePeerKey}>{t('wg.generateKey')}</Button>
          <FormGroup label={t('wg.peerAllowedIPs')} required>
            <Input value={peerAllowedIPs} onChange={(e) => setPeerAllowedIPs(e.target.value)} placeholder="0.0.0.0/0" />
          </FormGroup>
          <FormGroup label={t('wg.peerKeepalive')}>
            <Input type="number" value={peerKeepalive} onChange={(e) => setPeerKeepalive(e.target.value)} placeholder="25" />
          </FormGroup>
          <ExitPathList value={peerExitPath} onChange={setPeerExitPath} label={t('wg.peerExitVia')} />
        </div>
      </Modal>

      {/* Peer Config Detail Modal */}
      <Modal
        open={detailOpen} onClose={() => setDetailOpen(false)}
        title={detailName}
        footer={
          <>
            <Button onClick={downloadConf}>{t('wg.downloadConf')}</Button>
            <Button onClick={() => setDetailOpen(false)}>{t('app.close')}</Button>
          </>
        }
      >
        <pre style={{ fontSize: 11, fontFamily: 'var(--mono)', background: 'var(--bg)', padding: 12, borderRadius: 'var(--radius-sm)', overflow: 'auto', maxHeight: 300 }}>
          {detailConfig}
        </pre>
        {detailConfig && (
          <div style={{ textAlign: 'center', marginTop: 16 }}>
            <img src={api.getWGQRUrl(detailConfig)} alt="QR" style={{ maxWidth: 256 }} />
          </div>
        )}
      </Modal>
    </div>
  );
}
