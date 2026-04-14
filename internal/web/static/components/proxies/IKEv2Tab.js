import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Input, Select, Toggle, FormGroup, FormGrid, useToast } from '@hy2scale/ui';
import { ExitPathList, exitPathToApi, apiToExitPath } from '@/components/ExitPathList';
import * as api from '@/api';
export default function IKEv2Tab({ limited }) {
    const { t } = useTranslation();
    const toast = useToast();
    const queryClient = useQueryClient();
    const { data: ikev2 } = useQuery({ queryKey: ['ikev2'], queryFn: api.getIKEv2 });
    const { data: certs = [] } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts });
    const [enabled, setEnabled] = useState(false);
    const [mode, setMode] = useState('mschapv2');
    const [pool, setPool] = useState('');
    const [certId, setCertId] = useState('');
    const [psk, setPsk] = useState('');
    const [localId, setLocalId] = useState('');
    const [mtu, setMtu] = useState('1400');
    const [exitPath, setExitPath] = useState({ paths: [''], mode: '' });
    const [loading, setLoading] = useState(false);
    useEffect(() => {
        if (ikev2) {
            setEnabled(ikev2.enabled);
            setMode(ikev2.mode || 'mschapv2');
            setPool(ikev2.pool || '');
            setCertId(ikev2.cert_id || '');
            setPsk(ikev2.psk || '');
            setLocalId(ikev2.local_id || '');
            setMtu(String(ikev2.mtu || 1400));
            setExitPath(apiToExitPath(ikev2.default_exit, ikev2.default_exit_paths, ikev2.default_exit_mode));
        }
    }, [ikev2]);
    const handleSave = async () => {
        if (!pool) {
            toast.error(t('ikev2.poolRequired'));
            return;
        }
        if (mode === 'mschapv2' && !certId) {
            toast.error(t('ikev2.certRequired'));
            return;
        }
        if (mode === 'psk' && !psk) {
            toast.error(t('ikev2.pskRequired'));
            return;
        }
        setLoading(true);
        try {
            const exitData = mode === 'psk' ? exitPathToApi(exitPath) : {};
            await api.updateIKEv2({
                enabled, mode, pool, cert_id: certId, psk, local_id: localId,
                mtu: parseInt(mtu) || 1400,
                default_exit: exitData.exit_via || '',
                default_exit_paths: exitData.exit_paths,
                default_exit_mode: exitData.exit_mode,
            });
            toast.success(t('ikev2.saved'));
            queryClient.invalidateQueries({ queryKey: ['ikev2'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
        finally {
            setLoading(false);
        }
    };
    if (limited || (ikev2 && !ikev2.capable)) {
        return (_jsx(Card, { title: t('ikev2.title'), children: _jsx("div", { className: "hy-limited-overlay", children: _jsx("div", { className: "hy-limited-msg", children: ikev2?.host_network === false ? t('ikev2.warnHostNetwork') : t('ikev2.warnText') }) }) }));
    }
    const certOptions = [
        { value: '', label: t('ikev2.selectCert') },
        ...certs.map((c) => ({ value: c.id, label: c.name || c.id })),
    ];
    return (_jsx(Card, { title: t('ikev2.title'), children: _jsxs("div", { style: { maxWidth: 450, display: 'flex', flexDirection: 'column', gap: 14 }, children: [_jsxs(FormGrid, { children: [_jsx(FormGroup, { label: t('ikev2.mode'), children: _jsx(Select, { value: mode, onChange: (e) => setMode(e.target.value), options: [
                                    { value: 'mschapv2', label: t('ikev2.modeMschapv2') },
                                    { value: 'psk', label: t('ikev2.modePsk') },
                                ] }) }), _jsx(FormGroup, { label: t('app.enabled'), children: _jsx(Toggle, { checked: enabled, onChange: (e) => setEnabled(e.target.checked) }) })] }), _jsx(FormGroup, { label: t('ikev2.localId'), children: _jsx(Input, { value: localId, onChange: (e) => setLocalId(e.target.value), placeholder: "node ID" }) }), _jsx(FormGroup, { label: t('ikev2.pool'), required: true, children: _jsx(Input, { value: pool, onChange: (e) => setPool(e.target.value), placeholder: "192.168.26.1/24" }) }), _jsx(FormGroup, { label: t('ikev2.mtu'), children: _jsx(Input, { type: "number", value: mtu, onChange: (e) => setMtu(e.target.value), placeholder: "1400" }) }), mode === 'mschapv2' ? (_jsxs(_Fragment, { children: [_jsx(FormGroup, { label: t('ikev2.cert'), required: true, children: _jsx(Select, { value: certId, onChange: (e) => setCertId(e.target.value), options: certOptions }) }), _jsx("div", { style: { fontSize: 12, color: 'var(--text-muted)' }, children: t('ikev2.certDesc') })] })) : (_jsxs(_Fragment, { children: [_jsx(FormGroup, { label: t('ikev2.psk'), required: true, children: _jsx(Input, { value: psk, onChange: (e) => setPsk(e.target.value) }) }), _jsx(ExitPathList, { value: exitPath, onChange: setExitPath, label: t('ikev2.defaultExit') }), _jsx("div", { style: { fontSize: 12, color: 'var(--text-muted)' }, children: t('ikev2.pskDesc') })] })), _jsx(Button, { variant: "primary", onClick: handleSave, loading: loading, style: { alignSelf: 'flex-start' }, children: t('app.save') })] }) }));
}
