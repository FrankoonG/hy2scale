import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Input, Select, Toggle, FormGroup, FormGrid, useToast } from '@hy2scale/ui';
import * as api from '@/api';
export default function SSTab() {
    const { t } = useTranslation();
    const toast = useToast();
    const queryClient = useQueryClient();
    const { data: ss } = useQuery({ queryKey: ['ss'], queryFn: api.getSS });
    const [enabled, setEnabled] = useState(false);
    const [listen, setListen] = useState('');
    const [method, setMethod] = useState('aes-256-gcm');
    const [loading, setLoading] = useState(false);
    useEffect(() => {
        if (ss) {
            setEnabled(ss.enabled);
            setListen(ss.listen || '');
            setMethod(ss.method || 'aes-256-gcm');
        }
    }, [ss]);
    const handleSave = async () => {
        setLoading(true);
        try {
            await api.updateSS({ listen, enabled, method });
            toast.success(t('ss.saved'));
            queryClient.invalidateQueries({ queryKey: ['ss'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
        finally {
            setLoading(false);
        }
    };
    return (_jsx(Card, { title: t('ss.title'), children: _jsxs("div", { style: { maxWidth: 400, display: 'flex', flexDirection: 'column', gap: 14 }, children: [_jsx(FormGroup, { label: t('app.enabled'), children: _jsx(Toggle, { checked: enabled, onChange: (e) => setEnabled(e.target.checked) }) }), _jsxs(FormGrid, { children: [_jsx(FormGroup, { label: t('ss.port'), required: true, children: _jsx(Input, { value: listen, onChange: (e) => setListen(e.target.value), placeholder: ":8388" }) }), _jsx(FormGroup, { label: t('ss.method'), children: _jsx(Select, { value: method, onChange: (e) => setMethod(e.target.value), options: [
                                    { value: 'aes-128-gcm', label: 'aes-128-gcm' },
                                    { value: 'aes-256-gcm', label: 'aes-256-gcm' },
                                    { value: 'chacha20-ietf-poly1305', label: 'chacha20-ietf-poly1305' },
                                    { value: '2022-blake3-aes-128-gcm', label: '2022-blake3-aes-128-gcm' },
                                    { value: '2022-blake3-aes-256-gcm', label: '2022-blake3-aes-256-gcm' },
                                    { value: 'none', label: 'none (no encryption)' },
                                ] }) })] }), _jsx("div", { style: { fontSize: 12, color: 'var(--text-muted)' }, children: t('ss.desc') }), _jsx(Button, { variant: "primary", onClick: handleSave, loading: loading, style: { alignSelf: 'flex-start' }, children: t('app.save') })] }) }));
}
