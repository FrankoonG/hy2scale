import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Input, Toggle, FormGroup, FormGrid, useToast } from '@hy2scale/ui';
import * as api from '@/api';
export default function L2TPTab({ limited }) {
    const { t } = useTranslation();
    const toast = useToast();
    const queryClient = useQueryClient();
    const { data: l2tp } = useQuery({ queryKey: ['l2tp'], queryFn: api.getL2TP });
    const [enabled, setEnabled] = useState(false);
    const [listen, setListen] = useState('');
    const [pool, setPool] = useState('');
    const [psk, setPsk] = useState('');
    const [mtu, setMtu] = useState('1400');
    const [loading, setLoading] = useState(false);
    useEffect(() => {
        if (l2tp) {
            setEnabled(l2tp.enabled);
            setListen(l2tp.listen || '');
            setPool(l2tp.pool || '');
            setPsk(l2tp.psk || '');
            setMtu(String(l2tp.mtu || 1400));
        }
    }, [l2tp]);
    const handleSave = async () => {
        if (!listen || !pool || !psk) {
            toast.error(t('l2tp.portPoolPskRequired'));
            return;
        }
        setLoading(true);
        try {
            await api.updateL2TP({ listen, enabled, pool, psk, mtu: parseInt(mtu) || 1400 });
            toast.success(t('l2tp.saved'));
            queryClient.invalidateQueries({ queryKey: ['l2tp'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
        finally {
            setLoading(false);
        }
    };
    if (limited || (l2tp && !l2tp.capable)) {
        return (_jsx(Card, { title: t('l2tp.title'), children: _jsx("div", { className: "hy-limited-overlay", children: _jsx("div", { className: "hy-limited-msg", children: l2tp?.host_network === false ? t('l2tp.warnHostNetwork') : t('l2tp.warnText') }) }) }));
    }
    return (_jsx(Card, { title: t('l2tp.title'), children: _jsxs("div", { style: { maxWidth: 500, display: 'flex', flexDirection: 'column', gap: 14 }, children: [_jsx(FormGroup, { label: t('app.enabled'), children: _jsx(Toggle, { checked: enabled, onChange: (e) => setEnabled(e.target.checked) }) }), _jsxs(FormGrid, { children: [_jsx(FormGroup, { label: t('l2tp.port'), required: true, children: _jsx(Input, { value: listen, onChange: (e) => setListen(e.target.value), placeholder: "1701" }) }), _jsx(FormGroup, { label: t('l2tp.pool'), required: true, children: _jsx(Input, { value: pool, onChange: (e) => setPool(e.target.value), placeholder: "10.10.0.0/24" }) }), _jsx(FormGroup, { label: t('l2tp.ipsecPsk'), required: true, children: _jsx(Input, { value: psk, onChange: (e) => setPsk(e.target.value) }) }), _jsx(FormGroup, { label: t('l2tp.mtu'), children: _jsx(Input, { type: "number", value: mtu, onChange: (e) => setMtu(e.target.value) }) })] }), _jsx("div", { style: { fontSize: 12, color: 'var(--text-muted)' }, children: t('l2tp.desc') }), _jsx(Button, { variant: "primary", onClick: handleSave, loading: loading, style: { alignSelf: 'flex-start' }, children: t('app.save') })] }) }));
}
