import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Input, Toggle, FormGroup, useToast } from '@hy2scale/ui';
import * as api from '@/api';
export default function Socks5Tab() {
    const { t } = useTranslation();
    const toast = useToast();
    const queryClient = useQueryClient();
    const { data: proxies = [] } = useQuery({ queryKey: ['proxies'], queryFn: api.getProxies });
    const socks5 = proxies.find((p) => p.protocol === 'socks5');
    const [enabled, setEnabled] = useState(false);
    const [listen, setListen] = useState('');
    const [loading, setLoading] = useState(false);
    useEffect(() => {
        if (socks5) {
            setEnabled(socks5.enabled);
            setListen(socks5.listen || '');
        }
    }, [socks5]);
    const handleSave = async () => {
        setLoading(true);
        try {
            if (socks5) {
                await api.updateProxy(socks5.id, { ...socks5, listen, enabled });
            }
            else {
                await api.createProxy({ protocol: 'socks5', listen, enabled });
            }
            toast.success(t('socks5.saved'));
            queryClient.invalidateQueries({ queryKey: ['proxies'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
        finally {
            setLoading(false);
        }
    };
    return (_jsx(Card, { title: t('socks5.title'), children: _jsxs("div", { style: { maxWidth: 400, display: 'flex', flexDirection: 'column', gap: 14 }, children: [_jsx(FormGroup, { label: t('app.enabled'), children: _jsx(Toggle, { checked: enabled, onChange: (e) => setEnabled(e.target.checked) }) }), _jsx(FormGroup, { label: t('socks5.port'), required: true, children: _jsx(Input, { value: listen, onChange: (e) => setListen(e.target.value), placeholder: ":1080" }) }), _jsx("div", { style: { fontSize: 12, color: 'var(--text-muted)' }, children: t('socks5.desc') }), _jsx(Button, { variant: "primary", onClick: handleSave, loading: loading, style: { alignSelf: 'flex-start' }, children: t('app.save') })] }) }));
}
