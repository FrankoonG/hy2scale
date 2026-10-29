import { useState, useCallback, useRef, type MouseEvent } from 'react';
import { useDeselectOnBlankClick } from '@/hooks/useDeselectOnBlankClick';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Card, Button, Input, Toggle, Table, FormGroup, FormGrid, Badge, Modal,
  useToast, useConfirm, useSelection, type Column,
} from '@hy2scale/ui';
import { ExitPathList, exitPathToApi, apiToExitPath, type ExitPathValue } from '@/components/ExitPathList';
import { ExitViaCell, exitViaSortValue } from '@/components/ExitViaCell';
import ResponsiveActions from '@/components/ResponsiveActions';
import ImportExportButton from '@/components/ImportExportButton';
import * as api from '@/api';
import { useServerFormInit } from '@/hooks/useFormInit';
import type { WireGuardPeer } from '@/api';

// ---- peer address suggestions (IPv4 and IPv6) ----

function parseIP(s: string): { n: bigint; bits: 32 | 128 } | null {
  s = s.trim();
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(s)) {
    let n = 0n;
    for (const o of s.split('.')) {
      const v = Number(o);
      if (v > 255) return null;
      n = (n << 8n) + BigInt(v);
    }
    return { n, bits: 32 };
  }
  const halves = s.split('::');
  if (halves.length > 2 || s.includes('.')) return null;
  const head = halves[0] ? halves[0].split(':') : [];
  const tail = halves.length === 2 && halves[1] ? halves[1].split(':') : [];
  const fill = 8 - head.length - tail.length;
  if (halves.length === 1 ? head.length !== 8 : fill < 0) return null;
  let n = 0n;
  for (const g of [...head, ...Array(halves.length === 2 ? fill : 0).fill('0'), ...tail]) {
    if (!/^[0-9a-fA-F]{1,4}$/.test(g)) return null;
    n = (n << 16n) + BigInt(parseInt(g, 16));
  }
  return { n, bits: 128 };
}

function formatIP(n: bigint, bits: 32 | 128): string {
  if (bits === 32) return [24n, 16n, 8n, 0n].map((sh) => String((n >> sh) & 255n)).join('.');
  const g: string[] = [];
  for (let i = 7; i >= 0; i--) g.push(((n >> BigInt(i * 16)) & 0xffffn).toString(16));
  let best = -1, bestLen = 1;
  for (let i = 0; i < 8;) {
    if (g[i] !== '0') { i++; continue; }
    let j = i;
    while (j < 8 && g[j] === '0') j++;
    if (j - i > bestLen) { best = i; bestLen = j - i; }
    i = j;
  }
  return best < 0 ? g.join(':') : `${g.slice(0, best).join(':')}::${g.slice(best + bestLen).join(':')}`;
}

// nextPeerIP is the first host after the server's address in its subnet
// that no peer holds yet, as a host route ("/32" or "/128"); '' if none.
function nextPeerIP(serverCIDR: string, peers: WireGuardPeer[]): string {
  const [addr, len] = serverCIDR.split('/');
  const a = parseIP(addr || '');
  if (!a) return '';
  const plen = len === undefined ? a.bits : Number(len);
  if (!Number.isInteger(plen) || plen < 0 || plen > a.bits) return '';
  const hostBits = BigInt(a.bits - plen);
  const last = ((a.n >> hostBits) << hostBits) + (1n << hostBits) - (a.bits === 32 ? 2n : 1n);
  const used = new Set<string>([a.n.toString()]);
  for (const p of peers) {
    for (const e of (p.allowed_ips || '').split(',')) {
      const ip = parseIP(e.split('/')[0]);
      if (ip && ip.bits === a.bits) used.add(ip.n.toString());
    }
  }
  for (let c = a.n + 1n, i = 0; c <= last && i < 65536; c++, i++) {
    if (!used.has(c.toString())) return `${formatIP(c, a.bits)}/${a.bits}`;
  }
  return '';
}

export default function WireGuardTab() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const queryClient = useQueryClient();

  const wgQ = useQuery({ queryKey: ['wireguard'], queryFn: api.getWireGuard });
  const wg = wgQ.data;

  // Server config
  const [enabled, setEnabled] = useState(false);
  const [listenPort, setListenPort] = useState('');
  const [address, setAddress] = useState('');
  const [address6, setAddress6] = useState('');
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

  // Server fields seed once and after their own save; peer add/edit/delete
  // also refetches ['wireguard'] and must not revert unsaved server edits.
  const resyncServer = useServerFormInit(wgQ, (wg) => {
    setEnabled(wg.enabled);
    setListenPort(String(wg.listen_port || ''));
    setAddress(wg.address || '');
    setAddress6(wg.address6 || '');
    setPrivKey(wg.private_key || '');
    setPubKey(wg.public_key || '');
    setWgMtu(String(wg.mtu || 1420));
  });

  const handleSaveServer = async () => {
    setSaving(true);
    try {
      await api.updateWireGuard({
        enabled, listen_port: parseInt(listenPort) || 0,
        address, address6: address6.trim(), private_key: privKey, mtu: parseInt(wgMtu) || 1420,
      } as any);
      toast.success(t('wg.saved'));
      resyncServer();
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
    // Suggest the next free address of each family
    const peers = wg?.peers || [];
    setPeerAllowedIPs([nextPeerIP(address, peers), nextPeerIP(address6, peers)].filter(Boolean).join(', '));
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
      // Default the [Peer] Endpoint to the host the user is currently
      // accessing this admin UI through — that's the same node serving
      // WireGuard, so it's a safe baseline. The user can edit the .conf
      // afterwards if their actual public hostname differs.
      const res = await api.getWGPeerConfig(name, window.location.hostname);
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
    // Revoking at once can cancel the download in some browsers.
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  const peers = wg?.peers || [];
  const peerSelection = useSelection(peers.map((p) => p.name));
  const peerListScopeRef = useRef<HTMLDivElement | null>(null);
  useDeselectOnBlankClick(peerSelection, peerListScopeRef);
  // Where the last click in the list landed (modal animation origin).
  const peerClickPos = useRef<{ x: number; y: number } | undefined>(undefined);

  const bulkDeletePeers = useCallback(async () => {
    const ok = await confirm({
      title: t('app.bulkDelete'), message: t('wg.deleteConfirm', { name: `${peerSelection.count} peers` }),
      danger: true, confirmText: t('app.delete'), cancelText: t('app.cancel'),
    });
    if (!ok) return;
    try {
      await Promise.all([...peerSelection.selected].map((name) => api.deleteWGPeer(name)));
      toast.success(`${t('app.bulkDelete')}: ${peerSelection.count}`);
      queryClient.invalidateQueries({ queryKey: ['wireguard'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [peerSelection, confirm, queryClient, toast, t]);

  const peerColumns: Column<WireGuardPeer>[] = [
    {
      key: 'name', title: t('wg.peerName'), width: '120px', sortValue: (p) => p.name, render: (p) => (
        <a href="#" onClick={(e) => { e.preventDefault(); showPeerDetail(p.name, e); }} style={{ fontWeight: 600, color: 'var(--primary)', textDecoration: 'none' }}>
          {p.name}
        </a>
      ),
    },
    { key: 'exit', title: t('wg.peerExitVia'), sortValue: (p) => exitViaSortValue(p.exit_via, p.exit_paths), render: (p) => <ExitViaCell exitVia={p.exit_via || ''} exitPaths={p.exit_paths} exitMode={p.exit_mode} /> },
    { key: 'ips', title: t('wg.peerAllowedIPs'), width: '130px', sortValue: (p) => (p.allowed_ips || '').split(',')[0].trim() || null, render: (p) => <span className="mono" style={{ fontSize: 12 }}>{p.allowed_ips}</span> },
    { key: 'ka', title: t('wg.ka'), width: '50px', sortValue: (p) => p.keepalive || null, render: (p) => <>{p.keepalive || '—'}</> },
    // Per-row edit column retired — select a row and use the top-right Edit button.
  ];

  // Limited runtime: placed after every hook so the hook order never changes.
  if (wg && wg.enabled === undefined && !wg.listen_port) {
    return (
      <Card title={t('wg.title')}>
        <div className="hy-limited-overlay">
          <div className="hy-limited-msg">{t('l2tp.warnText')}</div>
        </div>
      </Card>
    );
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20, flex: 1, minHeight: 0 }}>
      {/* Server Settings */}
      <Card fill={2} title={<>
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
          <FormGrid>
            <FormGroup label={t('wg.address')}>
              <Input value={address} onChange={(e) => setAddress(e.target.value)} placeholder="10.0.0.1/24" />
            </FormGroup>
            <FormGroup label={t('wg.address6')}>
              <Input value={address6} onChange={(e) => setAddress6(e.target.value)} placeholder="fd00:9::1/64" />
            </FormGroup>
          </FormGrid>
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
            <Input value={pubKey} readOnly style={{ fontFamily: 'var(--mono)', fontSize: 11, color: 'var(--text-muted)' }} />
          </FormGroup>
          <FormGroup label={t('wg.mtu')}>
            <Input type="number" value={wgMtu} onChange={(e) => setWgMtu(e.target.value)} />
          </FormGroup>
          <Button variant="primary" onClick={handleSaveServer} loading={saving} style={{ alignSelf: 'flex-start' }}>{t('app.save')}</Button>
        </div>
      </Card>

      {/* Peers Table */}
      <Card
        fill={1}
        title={t('wg.peers')}
        count={peers.length}
        actions={
          <ResponsiveActions
            selectedCount={peerSelection.count}
            onClearSelection={peerSelection.clear}
            selectedLabel={t('app.selected', { count: peerSelection.count })}
          >
            {peerSelection.count > 0 && <Button size="sm" variant="danger" onClick={bulkDeletePeers}>{t('app.bulkDelete')}</Button>}
            <ImportExportButton target="wg-peers" />
            {(() => {
              const sel = [...peerSelection.selected];
              if (sel.length !== 1) return null;
              const single = peers.find((p) => p.name === sel[0]);
              if (!single) return null;
              const onEditClick = (e: MouseEvent) => openEditPeer(single, e);
              return (
                <Button size="sm" variant="success" data-testid="edit-selected-btn" onClick={onEditClick}>
                  {t('app.edit')}
                </Button>
              );
            })()}
            <Button size="sm" variant="primary" onClick={openAddPeer}>{t('wg.addPeer')}</Button>
          </ResponsiveActions>
        }
        noPadding
      >
        <div ref={peerListScopeRef} style={{ display: 'contents' }} onClickCapture={(e) => { peerClickPos.current = { x: e.clientX, y: e.clientY }; }}>
        <Table
          id="wg.peers"
          columns={peerColumns}
          data={peers}
          rowKey={(p) => p.name}
          emptyText={t('wg.noPeers')}
          selection={peerSelection}
          onRowClick={(p) => { setClickPos(peerClickPos.current); showPeerDetail(p.name); }}
        />
        </div>
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
            <Input value={peerAllowedIPs} onChange={(e) => setPeerAllowedIPs(e.target.value)} placeholder="10.0.0.2/32, fd00:9::2/128" />
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
