import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { Modal, Button, Input, PasswordInput, Toggle, FormGroup, FormGrid, useToast } from '@hy2scale/ui';
import { ExitPathList, exitPathToApi, apiToExitPath } from './ExitPathList';
import * as api from '@/api';
export default function UserModal({ open, onClose, editingId, animateFrom }) {
    const { t } = useTranslation();
    const toast = useToast();
    const queryClient = useQueryClient();
    const [loading, setLoading] = useState(false);
    const [username, setUsername] = useState('');
    const [password, setPassword] = useState('');
    const [exitPath, setExitPath] = useState({ paths: [''], mode: '' });
    const [trafficLimit, setTrafficLimit] = useState('');
    const [expiryDate, setExpiryDate] = useState('');
    const [enabled, setEnabled] = useState(true);
    useEffect(() => {
        if (!open)
            return;
        if (!editingId) {
            setUsername('');
            setPassword('');
            setExitPath({ paths: [''], mode: '' });
            setTrafficLimit('');
            setExpiryDate('');
            setEnabled(true);
            return;
        }
        api.getUsers().then((users) => {
            const u = users.find((u) => u.id === editingId);
            if (!u)
                return;
            setUsername(u.username);
            setPassword(u.password);
            setExitPath(apiToExitPath(u.exit_via, u.exit_paths, u.exit_mode));
            setTrafficLimit(u.traffic_limit > 0 ? String(u.traffic_limit / 1073741824) : '');
            setExpiryDate(u.expiry_date || '');
            setEnabled(u.enabled);
        });
    }, [open, editingId]);
    const handleSubmit = async () => {
        if (!username || !password) {
            toast.error(t('users.usernamePassRequired'));
            return;
        }
        setLoading(true);
        const exitData = exitPathToApi(exitPath);
        const data = {
            username,
            password,
            ...exitData,
            traffic_limit: trafficLimit ? parseFloat(trafficLimit) * 1073741824 : 0,
            expiry_date: expiryDate || undefined,
            enabled,
        };
        try {
            if (editingId) {
                await api.updateUser(editingId, data);
                toast.success(t('users.updated', { name: username }));
            }
            else {
                await api.createUser(data);
                toast.success(t('users.added', { name: username }));
            }
            queryClient.invalidateQueries({ queryKey: ['users'] });
            onClose();
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
        finally {
            setLoading(false);
        }
    };
    const title = editingId ? t('users.editPrefix', { name: username }) : t('users.addTitle');
    return (_jsx(Modal, { open: open, onClose: onClose, title: title, animateFrom: animateFrom, footer: _jsxs(_Fragment, { children: [_jsx(Button, { onClick: onClose, children: t('app.cancel') }), _jsx(Button, { variant: "primary", onClick: handleSubmit, loading: loading, children: t('app.save') })] }), children: _jsxs("div", { style: { display: 'flex', flexDirection: 'column', gap: 14 }, children: [_jsxs(FormGrid, { children: [_jsx(FormGroup, { label: t('users.username'), required: true, children: _jsx(Input, { value: username, onChange: (e) => setUsername(e.target.value) }) }), _jsx(FormGroup, { label: t('users.password'), required: true, children: _jsx(PasswordInput, { value: password, onChange: (e) => setPassword(e.target.value), onGenerate: setPassword }) })] }), _jsx(ExitPathList, { value: exitPath, onChange: setExitPath }), _jsxs(FormGrid, { children: [_jsx(FormGroup, { label: t('users.trafficLimit'), children: _jsx(Input, { type: "number", value: trafficLimit, onChange: (e) => setTrafficLimit(e.target.value), placeholder: "0", suffix: "GB" }) }), _jsx(FormGroup, { label: t('users.expiryDate'), children: _jsx(Input, { type: "date", value: expiryDate, onChange: (e) => setExpiryDate(e.target.value) }) })] }), _jsx(FormGroup, { label: t('app.enabled'), children: _jsx(Toggle, { checked: enabled, onChange: (e) => setEnabled(e.target.checked) }) })] }) }));
}
