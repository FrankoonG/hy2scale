import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient, useQuery } from '@tanstack/react-query';
import { Modal, Button, Input, PasswordInput, Select, FormGroup, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import { useNodeStore } from '@/store/node';
export default function EditSelfModal({ open, onClose, animateFrom }) {
    const { t } = useTranslation();
    const toast = useToast();
    const queryClient = useQueryClient();
    const node = useNodeStore((s) => s.node);
    const [loading, setLoading] = useState(false);
    const [nodeId, setNodeId] = useState('');
    const [listenIp, setListenIp] = useState('');
    const [listenPort, setListenPort] = useState('');
    const [password, setPassword] = useState('');
    const [tlsCertId, setTlsCertId] = useState('');
    // Fetch TLS certs for dropdown
    const { data: certs } = useQuery({
        queryKey: ['tls'],
        queryFn: () => api.getCerts(),
        enabled: open,
    });
    useEffect(() => {
        if (open && node) {
            setNodeId(node.node_id || '');
            const listen = node.server?.listen || '0.0.0.0:5565';
            const match = listen.match(/^(.+):(.+)$/);
            setListenIp(match ? match[1] : '0.0.0.0');
            setListenPort(match ? match[2] : '5565');
            setPassword(node.server?.password || '');
            // Resolve tls_cert path to cert id: /data/tls/{id}.crt → id
            const certPath = node.server?.tls_cert || '';
            const certMatch = certPath.match(/\/data\/tls\/(.+)\.crt$/);
            setTlsCertId(certMatch ? certMatch[1] : '');
        }
    }, [open, node]);
    const handleSave = async () => {
        if (!nodeId.trim()) {
            toast.error(t('nodes.nodeIdRequired'));
            return;
        }
        setLoading(true);
        try {
            const listen = `${listenIp.trim() || '0.0.0.0'}:${listenPort.trim() || '5565'}`;
            const tls_cert = tlsCertId ? `/data/tls/${tlsCertId}.crt` : '';
            const tls_key = tlsCertId ? `/data/tls/${tlsCertId}.key` : '';
            await api.updateNode({
                node_id: nodeId.trim(),
                name: nodeId.trim(),
                server: { listen, password, tls_cert, tls_key },
            });
            toast.success(t('nodes.settingsSaved'));
            queryClient.invalidateQueries({ queryKey: ['node'] });
            queryClient.invalidateQueries({ queryKey: ['topology'] });
            onClose();
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
        finally {
            setLoading(false);
        }
    };
    // Build TLS cert options: self-signed (auto) + certs with private key
    const tlsOptions = [
        { value: '', label: t('nodes.selfSignedAuto') },
        ...(certs || [])
            .filter((c) => !!c.key_file)
            .map((c) => ({ value: c.id, label: `${c.name} (${c.subject})` })),
    ];
    return (_jsx(Modal, { open: open, onClose: onClose, title: t('nodes.editSelf'), animateFrom: animateFrom, footer: _jsxs(_Fragment, { children: [_jsx(Button, { onClick: onClose, children: t('app.cancel') }), _jsx(Button, { variant: "primary", onClick: handleSave, loading: loading, children: t('app.save') })] }), children: _jsxs("div", { style: { display: 'flex', flexDirection: 'column', gap: 14 }, children: [_jsx(FormGroup, { label: t('settings.nodeId'), required: true, children: _jsx(Input, { value: nodeId, onChange: (e) => setNodeId(e.target.value) }) }), _jsx("div", { className: "section-divider", children: t('nodes.hy2Server') }), _jsx(FormGroup, { label: t('nodes.listenUdp'), children: _jsxs("div", { style: { display: 'flex', gap: 8 }, children: [_jsx(Input, { value: listenIp, onChange: (e) => setListenIp(e.target.value), placeholder: "0.0.0.0", style: { flex: 3 } }), _jsx(Input, { value: listenPort, onChange: (e) => setListenPort(e.target.value), placeholder: "5565 or 5000-6000", style: { flex: 2 } })] }) }), _jsx(FormGroup, { label: t('nodes.password'), children: _jsx(PasswordInput, { value: password, onChange: (e) => setPassword(e.target.value), onGenerate: setPassword }) }), _jsx(FormGroup, { label: t('settings.tlsCert'), children: _jsx(Select, { value: tlsCertId, onChange: (e) => setTlsCertId(e.target.value), options: tlsOptions }) })] }) }));
}
