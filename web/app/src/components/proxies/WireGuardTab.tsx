import { useState, useEffect, useCallback, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Card, Button, Input, Toggle, FormGroup, FormGrid, Badge, Modal,
  CopyButton, useToast, useConfirm,
} from '@hy2scale/ui';
import { ExitPathList, exitPathToApi, apiToExitPath, type ExitPathValue } from '@/components/ExitPathList';
import { ExitViaCell } from '@/components/ExitViaCell';
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
  const [pubKey, setPubKey] = useState('');
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
  const [detailQR, setDetailQR] = useState('');

  const [clickPos, setClickPos] = useState<{ x: number; y: number } | undefined>();

  useEffect(() => {
    if (wg) {
      setEnabled(wg.enabled);
      setListenPort(String(wg.listen_port || ''));
      setAddress(wg.address || '');
      setPrivKey(wg.private_key || '');
      setPubKey(wg.public_key || '');
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

  const generateServerKey = async () => {
    try {
      const { private_key, public_key } = await api.generateWGKey();
      setPrivKey(private_key);
      setPubKey(public_key);
    } catch (e: any) { toast.error(String(e.message || e)); }
  };

  const openAddPeer = async (e: MouseEvent) => {
    setClickPos({ x: e.clientX, y: e.clientY });
    setEditPeerName(null);
    setPeerName(''); setPeerPrivKey(''); setPeerPubKey('');
    setPeerKeepalive('25');
    setPeerExitPath({ paths: [''], mode: '' });
    // Auto-suggest next IP
    const addr = address;
    if (addr) {
      const base = addr.split('/')[0].split('.');
      const lastOctet = parseInt(base[3]) + (wg?.peers?.length || 0) + 1;
      if (lastOctet < 255) base[3] = String(lastOctet);
      setPeerAllowedIPs(base.join('.') + '/32');
    } else {
      setPeerAllowedIPs('');
    }
    // Auto-generate keys
    try {
      const { private_key, public_key } = await api.generateWGKey();
      setPeerPrivKey(private_key);
      setPeerPubKey(public_key);
    } catch { /* ignore */ }
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

  const showPeerDetail = useCallback(async (name: string, e?: MouseEvent) => {
    if (e) setClickPos({ x: e.clientX, y: e.clientY });
    try {
      const res = await api.getWGPeerConfig(name);
      const text = await (res as any).text();
      setDetailName(name);
      setDetailConfig(text);
      // Fetch QR as blob with auth header
      try {
        const qrRes = await api.getWGQR(text);
        const blob = await (qrRes as any).blob();
        setDetailQR(URL.createObjectURL(blob));
      } catch { setDetailQR(''); }
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

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
      {/* Server Settings */}
      <Card title={<>
        {t('wg.title')}
        {wg?.running && <> <span style={{ color: 'var(--green)', fontSize: 12 }}>●</span> <Badge variant="green">{wg.connected ? t('wg.connectedStatus', { count: wg.connected }) : t('wg.runningStatus')}</Badge></>}
      </>}>
        <div style={{ maxWidth: 500, display: 'flex', flexDirection: 'column', gap: 14 }}>
          <FormGrid>
            <FormGroup label={t('wg.port')}>
              <Input type="number" value={listenPort} onChange={(e) => setListenPort(e.target.value)} placeholder="51820" />
            </FormGroup>
            <FormGroup label={t('app.enabled')}>
              <div style={{ paddingTop: 6 }}>
                <Toggle checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
              </div>
            </FormGroup>
          </FormGrid>
          <FormGroup label={t('wg.address')}>
            <Input value={address} onChange={(e) => setAddress(e.target.value)} placeholder="10.0.0.1/24" />
          </FormGroup>
          <FormGroup label={t('wg.privKey')}>
            <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
              <Input value={privKey} onChange={(e) => setPrivKey(e.target.value)} style={{ flex: 1, fontFamily: 'var(--mono)', fontSize: 11 }} />
              <button className="hy-circle-btn" title={t('wg.generateKey')} onClick={generateServerKey}>
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4"/>
                </svg>
              </button>
            </div>
          </FormGroup>
          <FormGroup label={t('wg.pubKey')}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <Input value={pubKey} readOnly style={{ fontFamily: 'var(--mono)', fontSize: 11, color: 'var(--text-muted)' }} />
              {pubKey && <CopyButton text={pubKey} />}
            </div>
          </FormGroup>
          <FormGroup label={t('wg.mtu')}>
            <Input type="number" value={wgMtu} onChange={(e) => setWgMtu(e.target.value)} />
          </FormGroup>
          <Button variant="primary" onClick={handleSaveServer} loading={saving} style={{ alignSelf: 'flex-start' }}>{t('app.save')}</Button>
        </div>
      </Card>

      {/* Peers Table */}
      <Card
        title={t('wg.peers')}
        count={peers.length}
        actions={<Button size="sm" variant="primary" onClick={openAddPeer}>{t('wg.addPeer')}</Button>}
        noPadding
      >
        {peers.length === 0 ? (
          <div className="hy-empty" dangerouslySetInnerHTML={{ __html: t('wg.noPeers') }} />
        ) : (
          <div className="hy-table-wrap">
            <table className="hy-table">
              <thead>
                <tr>
                  <th style={{ width: 120 }}>{t('wg.peerName')}</th>
                  <th style={{ minWidth: 180 }}>{t('wg.peerExitVia')}</th>
                  <th style={{ width: 130 }}>{t('wg.peerAllowedIPs')}</th>
                  <th style={{ width: 50 }}>{t('wg.ka')}</th>
                  <th style={{ width: 150 }}></th>
                </tr>
              </thead>
              <tbody>
                {peers.map((p) => (
                  <tr key={p.name}>
                    <td>
                      <a href="#" onClick={(e) => { e.preventDefault(); showPeerDetail(p.name, e); }} style={{ fontWeight: 600, color: 'var(--primary)', textDecoration: 'none' }}>
                        {p.name}
                      </a>
                    </td>
                    <td><ExitViaCell exitVia={p.exit_via || ''} exitPaths={p.exit_paths} exitMode={p.exit_mode} /></td>
                    <td><span className="mono" style={{ fontSize: 12 }}>{p.allowed_ips}</span></td>
                    <td>{p.keepalive || '—'}</td>
                    <td className="col-actions">
                      <div className="act-group">
                        <button className="act-btn edit" onClick={(e) => openEditPeer(p, e)}>
                          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7"/><path d="M18.5 2.5a2.12 2.12 0 013 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
                        </button>
                        <button className="act-btn danger" onClick={() => handleDeletePeer(p.name)}>
                          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M3 6h18M19 6v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6m3 0V4a2 2 0 012-2h4a2 2 0 012 2v2"/></svg>
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {/* Add/Edit Peer Modal */}
      <Modal
        open={peerOpen} onClose={() => setPeerOpen(false)}
        title={editPeerName ? t('wg.editPeerPrefix', { name: editPeerName }) : t('wg.addPeerTitle')}
        animateFrom={clickPos}
        footer={
          <>
            <Button onClick={() => setPeerOpen(false)}>{t('app.cancel')}</Button>
            <Button variant="primary" onClick={handleSavePeer} loading={peerSaving}>
              {editPeerName ? t('app.save') : t('app.add')}
            </Button>
          </>
        }
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <FormGroup label={t('wg.peerName')} required>
            <Input value={peerName} onChange={(e) => setPeerName(e.target.value)} placeholder="phone" />
          </FormGroup>
          <FormGroup label={t('wg.peerPubKey')} required>
            <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
              <Input value={peerPubKey} onChange={(e) => setPeerPubKey(e.target.value)} style={{ flex: 1, fontFamily: 'var(--mono)', fontSize: 11 }} />
              <button className="hy-circle-btn" title={t('wg.generateKey')} onClick={generatePeerKey}>
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4"/>
                </svg>
              </button>
            </div>
          </FormGroup>
          <FormGroup label={t('wg.peerPrivKey')}>
            <Input value={peerPrivKey} onChange={(e) => setPeerPrivKey(e.target.value)} style={{ fontFamily: 'var(--mono)', fontSize: 11 }} placeholder={t('wg.privKeyHint')} />
          </FormGroup>
          <FormGroup label={t('wg.peerAllowedIPs')} required>
            <Input value={peerAllowedIPs} onChange={(e) => setPeerAllowedIPs(e.target.value)} placeholder="10.0.0.2/32" />
          </FormGroup>
          <ExitPathList value={peerExitPath} onChange={setPeerExitPath} label={t('wg.peerExitVia')} />
          <FormGroup label={t('wg.peerKeepalive')}>
            <Input type="number" value={peerKeepalive} onChange={(e) => setPeerKeepalive(e.target.value)} placeholder="25" />
          </FormGroup>
        </div>
      </Modal>

      {/* Peer Config Detail Modal */}
      <Modal
        open={detailOpen} onClose={() => setDetailOpen(false)}
        title={detailName}
        animateFrom={clickPos}
        footer={
          <>
            <Button onClick={() => setDetailOpen(false)}>{t('app.close')}</Button>
            <Button variant="primary" onClick={downloadConf}>{t('wg.downloadConf')}</Button>
          </>
        }
      >
        {detailConfig && (
          <>
            {detailQR && (
              <div style={{ textAlign: 'center', marginBottom: 16 }}>
                <img
                  src={detailQR}
                  alt="QR"
                  style={{ width: 256, height: 256, border: '1px solid var(--border)', borderRadius: 8 }}
                />
              </div>
            )}
            <pre className="hy-code-block">
              {detailConfig}
            </pre>
          </>
        )}
      </Modal>
    </div>
  );
}
