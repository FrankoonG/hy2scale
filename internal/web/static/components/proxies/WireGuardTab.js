import { jsx as _jsx, Fragment as _Fragment, jsxs as _jsxs } from "react/jsx-runtime";
import { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Input, Toggle, Table, FormGroup, FormGrid, Badge, Modal, CopyButton, useToast, useConfirm, useSelection, } from '@hy2scale/ui';
import { ExitPathList, exitPathToApi, apiToExitPath } from '@/components/ExitPathList';
import { ExitViaCell } from '@/components/ExitViaCell';
import BulkActionBar from '@/components/BulkActionBar';
import * as api from '@/api';
export default function WireGuardTab({ limited }) {
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
    const [editPeerName, setEditPeerName] = useState(null);
    const [peerName, setPeerName] = useState('');
    const [peerPubKey, setPeerPubKey] = useState('');
    const [peerPrivKey, setPeerPrivKey] = useState('');
    const [peerAllowedIPs, setPeerAllowedIPs] = useState('');
    const [peerKeepalive, setPeerKeepalive] = useState('');
    const [peerExitPath, setPeerExitPath] = useState({ paths: [''], mode: '' });
    const [peerSaving, setPeerSaving] = useState(false);
    // Peer detail modal
    const [detailOpen, setDetailOpen] = useState(false);
    const [detailConfig, setDetailConfig] = useState('');
    const [detailName, setDetailName] = useState('');
    const [detailQR, setDetailQR] = useState('');
    const [clickPos, setClickPos] = useState();
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
            });
            toast.success(t('wg.saved'));
            queryClient.invalidateQueries({ queryKey: ['wireguard'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
        finally {
            setSaving(false);
        }
    };
    const generateServerKey = async () => {
        try {
            const { private_key, public_key } = await api.generateWGKey();
            setPrivKey(private_key);
            setPubKey(public_key);
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    };
    const openAddPeer = async (e) => {
        setClickPos({ x: e.clientX, y: e.clientY });
        setEditPeerName(null);
        setPeerName('');
        setPeerPrivKey('');
        setPeerPubKey('');
        setPeerKeepalive('25');
        setPeerExitPath({ paths: [''], mode: '' });
        // Auto-suggest next IP
        const addr = address;
        if (addr) {
            const base = addr.split('/')[0].split('.');
            const lastOctet = parseInt(base[3]) + (wg?.peers?.length || 0) + 1;
            if (lastOctet < 255)
                base[3] = String(lastOctet);
            setPeerAllowedIPs(base.join('.') + '/32');
        }
        else {
            setPeerAllowedIPs('');
        }
        // Auto-generate keys
        try {
            const { private_key, public_key } = await api.generateWGKey();
            setPeerPrivKey(private_key);
            setPeerPubKey(public_key);
        }
        catch { /* ignore */ }
        setPeerOpen(true);
    };
    const openEditPeer = (peer, e) => {
        setClickPos({ x: e.clientX, y: e.clientY });
        setEditPeerName(peer.name);
        setPeerName(peer.name);
        setPeerPubKey(peer.public_key);
        setPeerPrivKey(peer.private_key);
        setPeerAllowedIPs(peer.allowed_ips);
        setPeerKeepalive(String(peer.keepalive || ''));
        setPeerExitPath(apiToExitPath(peer.exit_via, peer.exit_paths, peer.exit_mode));
        setPeerOpen(true);
    };
    const handleSavePeer = async () => {
        if (!peerName) {
            toast.error(t('wg.nameRequired'));
            return;
        }
        if (!peerPubKey && !peerPrivKey) {
            toast.error(t('wg.pubKeyRequired'));
            return;
        }
        if (!peerAllowedIPs) {
            toast.error(t('wg.allowedIpsRequired'));
            return;
        }
        setPeerSaving(true);
        const exitData = exitPathToApi(peerExitPath);
        const data = {
            name: peerName, public_key: peerPubKey, private_key: peerPrivKey,
            allowed_ips: peerAllowedIPs, keepalive: parseInt(peerKeepalive) || 0, ...exitData,
        };
        try {
            if (editPeerName) {
                await api.updateWGPeer(editPeerName, data);
                toast.success(t('wg.peerUpdated'));
            }
            else {
                await api.createWGPeer(data);
                toast.success(t('wg.peerAdded'));
            }
            queryClient.invalidateQueries({ queryKey: ['wireguard'] });
            setPeerOpen(false);
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
        finally {
            setPeerSaving(false);
        }
    };
    const generatePeerKey = async () => {
        try {
            const { private_key, public_key } = await api.generateWGKey();
            setPeerPrivKey(private_key);
            setPeerPubKey(public_key);
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    };
    const handleDeletePeer = useCallback(async (name) => {
        const ok = await confirm({
            title: t('app.delete'), message: t('wg.deleteConfirm', { name }),
            danger: true, confirmText: t('app.delete'), cancelText: t('app.cancel'),
        });
        if (!ok)
            return;
        try {
            await api.deleteWGPeer(name);
            toast.success(t('wg.peerRemoved'));
            queryClient.invalidateQueries({ queryKey: ['wireguard'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [confirm, t, queryClient, toast]);
    const showPeerDetail = useCallback(async (name, e) => {
        if (e)
            setClickPos({ x: e.clientX, y: e.clientY });
        try {
            const res = await api.getWGPeerConfig(name);
            const text = await res.text();
            setDetailName(name);
            setDetailConfig(text);
            // Fetch QR as blob with auth header
            try {
                const qrRes = await api.getWGQR(text);
                const blob = await qrRes.blob();
                setDetailQR(URL.createObjectURL(blob));
            }
            catch {
                setDetailQR('');
            }
            setDetailOpen(true);
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [toast]);
    const downloadConf = () => {
        const blob = new Blob([detailConfig], { type: 'text/plain' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = `${detailName}.conf`;
        a.click();
        URL.revokeObjectURL(url);
    };
    if (limited || (wg && wg.enabled === undefined && !wg.listen_port)) {
        return (_jsx(Card, { title: t('wg.title'), children: _jsx("div", { className: "hy-limited-overlay", children: _jsx("div", { className: "hy-limited-msg", children: t('l2tp.warnText') }) }) }));
    }
    const peers = wg?.peers || [];
    const peerSelection = useSelection(peers.map((p) => p.name));
    const bulkDeletePeers = useCallback(async () => {
        const ok = await confirm({
            title: t('app.bulkDelete'), message: t('wg.deleteConfirm', { name: `${peerSelection.count} peers` }),
            danger: true, confirmText: t('app.delete'), cancelText: t('app.cancel'),
        });
        if (!ok)
            return;
        try {
            await Promise.all([...peerSelection.selected].map((name) => api.deleteWGPeer(name)));
            toast.success(`${t('app.bulkDelete')}: ${peerSelection.count}`);
            queryClient.invalidateQueries({ queryKey: ['wireguard'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [peerSelection, confirm, queryClient, toast, t]);
    const peerColumns = [
        {
            key: 'name', title: t('wg.peerName'), width: '120px', render: (p) => (_jsx("a", { href: "#", onClick: (e) => { e.preventDefault(); showPeerDetail(p.name, e); }, style: { fontWeight: 600, color: 'var(--primary)', textDecoration: 'none' }, children: p.name })),
        },
        { key: 'exit', title: t('wg.peerExitVia'), render: (p) => _jsx(ExitViaCell, { exitVia: p.exit_via || '', exitPaths: p.exit_paths, exitMode: p.exit_mode }) },
        { key: 'ips', title: t('wg.peerAllowedIPs'), width: '130px', render: (p) => _jsx("span", { className: "mono", style: { fontSize: 12 }, children: p.allowed_ips }) },
        { key: 'ka', title: t('wg.ka'), width: '50px', render: (p) => _jsx(_Fragment, { children: p.keepalive || '—' }) },
        {
            key: 'actions', title: '', width: '40px', render: (p) => (_jsx("button", { className: "hy-row-edit", onClick: (e) => openEditPeer(p, e), title: t('app.edit'), children: _jsxs("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: "2", children: [_jsx("path", { d: "M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7" }), _jsx("path", { d: "M18.5 2.5a2.12 2.12 0 013 3L12 15l-4 1 1-4 9.5-9.5z" })] }) })),
        },
    ];
    return (_jsxs("div", { style: { display: 'flex', flexDirection: 'column', gap: 20 }, children: [_jsx(Card, { title: _jsxs(_Fragment, { children: [t('wg.title'), wg?.running && _jsxs(_Fragment, { children: [" ", _jsx("span", { style: { color: 'var(--green)', fontSize: 12 }, children: "\u25CF" }), " ", _jsx(Badge, { variant: "green", children: wg.connected ? t('wg.connectedStatus', { count: wg.connected }) : t('wg.runningStatus') })] })] }), children: _jsxs("div", { style: { maxWidth: 500, display: 'flex', flexDirection: 'column', gap: 14 }, children: [_jsxs(FormGrid, { children: [_jsx(FormGroup, { label: t('wg.port'), children: _jsx(Input, { type: "number", value: listenPort, onChange: (e) => setListenPort(e.target.value), placeholder: "51820" }) }), _jsx(FormGroup, { label: t('app.enabled'), children: _jsx("div", { style: { paddingTop: 6 }, children: _jsx(Toggle, { checked: enabled, onChange: (e) => setEnabled(e.target.checked) }) }) })] }), _jsx(FormGroup, { label: t('wg.address'), children: _jsx(Input, { value: address, onChange: (e) => setAddress(e.target.value), placeholder: "10.0.0.1/24" }) }), _jsx(FormGroup, { label: t('wg.privKey'), children: _jsxs("div", { style: { display: 'flex', gap: 8, alignItems: 'center' }, children: [_jsx(Input, { value: privKey, onChange: (e) => setPrivKey(e.target.value), style: { flex: 1, fontFamily: 'var(--mono)', fontSize: 11 } }), _jsx("button", { className: "hy-circle-btn", title: t('wg.generateKey'), onClick: generateServerKey, children: _jsx("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: "2", children: _jsx("path", { d: "M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4" }) }) })] }) }), _jsx(FormGroup, { label: t('wg.pubKey'), children: _jsxs("div", { style: { display: 'flex', alignItems: 'center', gap: 8 }, children: [_jsx(Input, { value: pubKey, readOnly: true, style: { fontFamily: 'var(--mono)', fontSize: 11, color: 'var(--text-muted)' } }), pubKey && _jsx(CopyButton, { text: pubKey })] }) }), _jsx(FormGroup, { label: t('wg.mtu'), children: _jsx(Input, { type: "number", value: wgMtu, onChange: (e) => setWgMtu(e.target.value) }) }), _jsx(Button, { variant: "primary", onClick: handleSaveServer, loading: saving, style: { alignSelf: 'flex-start' }, children: t('app.save') })] }) }), _jsx(Card, { title: t('wg.peers'), count: peers.length, actions: _jsxs("div", { style: { display: 'flex', gap: 8, alignItems: 'center' }, children: [_jsx(BulkActionBar, { count: peerSelection.count, onClear: peerSelection.clear, children: _jsx(Button, { size: "sm", variant: "danger", onClick: bulkDeletePeers, children: t('app.bulkDelete') }) }), _jsx(Button, { size: "sm", variant: "primary", onClick: openAddPeer, children: t('wg.addPeer') })] }), noPadding: true, children: _jsx(Table, { columns: peerColumns, data: peers, rowKey: (p) => p.name, emptyText: t('wg.noPeers'), selection: peerSelection }) }), _jsx(Modal, { open: peerOpen, onClose: () => setPeerOpen(false), title: editPeerName ? t('wg.editPeerPrefix', { name: editPeerName }) : t('wg.addPeerTitle'), animateFrom: clickPos, footer: _jsxs(_Fragment, { children: [_jsx(Button, { onClick: () => setPeerOpen(false), children: t('app.cancel') }), _jsx(Button, { variant: "primary", onClick: handleSavePeer, loading: peerSaving, children: editPeerName ? t('app.save') : t('app.add') })] }), children: _jsxs("div", { style: { display: 'flex', flexDirection: 'column', gap: 14 }, children: [_jsx(FormGroup, { label: t('wg.peerName'), required: true, children: _jsx(Input, { value: peerName, onChange: (e) => setPeerName(e.target.value), placeholder: "phone" }) }), _jsx(FormGroup, { label: t('wg.peerPubKey'), required: true, children: _jsxs("div", { style: { display: 'flex', gap: 8, alignItems: 'center' }, children: [_jsx(Input, { value: peerPubKey, onChange: (e) => setPeerPubKey(e.target.value), style: { flex: 1, fontFamily: 'var(--mono)', fontSize: 11 } }), _jsx("button", { className: "hy-circle-btn", title: t('wg.generateKey'), onClick: generatePeerKey, children: _jsx("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: "2", children: _jsx("path", { d: "M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4" }) }) })] }) }), _jsx(FormGroup, { label: t('wg.peerPrivKey'), children: _jsx(Input, { value: peerPrivKey, onChange: (e) => setPeerPrivKey(e.target.value), style: { fontFamily: 'var(--mono)', fontSize: 11 }, placeholder: t('wg.privKeyHint') }) }), _jsx(FormGroup, { label: t('wg.peerAllowedIPs'), required: true, children: _jsx(Input, { value: peerAllowedIPs, onChange: (e) => setPeerAllowedIPs(e.target.value), placeholder: "10.0.0.2/32" }) }), _jsx(ExitPathList, { value: peerExitPath, onChange: setPeerExitPath, label: t('wg.peerExitVia') }), _jsx(FormGroup, { label: t('wg.peerKeepalive'), children: _jsx(Input, { type: "number", value: peerKeepalive, onChange: (e) => setPeerKeepalive(e.target.value), placeholder: "25" }) })] }) }), _jsx(Modal, { open: detailOpen, onClose: () => setDetailOpen(false), title: detailName, animateFrom: clickPos, footer: _jsxs(_Fragment, { children: [_jsx(Button, { onClick: () => setDetailOpen(false), children: t('app.close') }), _jsx(Button, { variant: "primary", onClick: downloadConf, children: t('wg.downloadConf') })] }), children: detailConfig && (_jsxs(_Fragment, { children: [detailQR && (_jsx("div", { style: { textAlign: 'center', marginBottom: 16 }, children: _jsx("img", { src: detailQR, alt: "QR", style: { width: 256, height: 256, border: '1px solid var(--border)', borderRadius: 8 } }) })), _jsx("pre", { className: "hy-code-block", children: detailConfig })] })) })] }));
}
