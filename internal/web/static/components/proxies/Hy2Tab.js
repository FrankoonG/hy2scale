import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { Card, FormGroup, FormGrid, Input, Toggle, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import { useNodeStore } from '@/store/node';
export default function Hy2Tab() {
    const { t } = useTranslation();
    const toast = useToast();
    const queryClient = useQueryClient();
    const node = useNodeStore((s) => s.node);
    const serverListen = node?.server?.listen;
    const serverPort = serverListen?.replace(/.*:/, '') || '5565';
    const serverStatus = serverListen ? 'Enabled' : 'Disabled';
    const handleAuthToggle = async () => {
        try {
            await api.updateNode({ hy2_user_auth: !node?.hy2_user_auth });
            toast.success(node?.hy2_user_auth ? t('hy2.authDisabled') : t('hy2.authEnabled'));
            queryClient.invalidateQueries({ queryKey: ['node'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    };
    return (_jsx(Card, { title: t('hy2.title'), children: _jsxs("div", { style: { maxWidth: 400, display: 'flex', flexDirection: 'column', gap: 16 }, children: [_jsxs(FormGrid, { children: [_jsx(FormGroup, { label: t('hy2.port'), children: _jsx(Input, { value: serverPort, readOnly: true, disabled: true }) }), _jsx(FormGroup, { label: t('hy2.serverStatus'), children: _jsx(Input, { value: serverStatus, readOnly: true, disabled: true }) })] }), _jsx("div", { style: { fontSize: 12, color: 'var(--text-muted)', lineHeight: 1.6 }, children: t('hy2.readOnlyNote') }), _jsxs(FormGroup, { label: t('hy2.allowUserAuth'), children: [_jsx(Toggle, { checked: node?.hy2_user_auth || false, onChange: handleAuthToggle }), _jsx("div", { style: { fontSize: 12, color: 'var(--text-muted)', lineHeight: 1.6, marginTop: 8 }, children: t('hy2.userAuthDescLong') })] })] }) }));
}
