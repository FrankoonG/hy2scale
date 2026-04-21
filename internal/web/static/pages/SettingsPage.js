import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useState, useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Input, PasswordInput, Toggle, Select, FormGroup, Tabs, TabPanel, useToast, useConfirm, } from '@hy2scale/ui';
import * as api from '@/api';
import { useAuthStore } from '@/store/auth';
import { sha256 } from '@/hooks/useAuth';
export default function SettingsPage() {
    const { t } = useTranslation();
    const toast = useToast();
    const confirm = useConfirm();
    const queryClient = useQueryClient();
    const navigate = useNavigate();
    const logout = useAuthStore((s) => s.logout);
    const { data: settings } = useQuery({ queryKey: ['settings'], queryFn: api.getUISettings });
    const { data: certs = [] } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts });
    const [activeTab, setActiveTab] = useState('system');
    // UI Settings
    const [listen, setListen] = useState('');
    const [basePath, setBasePath] = useState('');
    const [forceHttps, setForceHttps] = useState(false);
    const [httpsCertId, setHttpsCertId] = useState('');
    const [sessionTimeout, setSessionTimeout] = useState('12');
    const [dns, setDns] = useState('');
    const [savingUI, setSavingUI] = useState(false);
    const [savingDns, setSavingDns] = useState(false);
    // Password
    const [curPw, setCurPw] = useState('');
    const [newUser, setNewUser] = useState('');
    const [newPw, setNewPw] = useState('');
    const [confirmPw, setConfirmPw] = useState('');
    const [savingPw, setSavingPw] = useState(false);
    // Restore
    const restoreRef = useRef(null);
    useEffect(() => {
        if (settings) {
            setListen(settings.listen || '');
            setBasePath(settings.base_path || '');
            setForceHttps(settings.force_https || false);
            setHttpsCertId(settings.https_cert_id || '');
            setSessionTimeout(String(settings.session_timeout_h || 12));
            setDns(settings.dns || '');
        }
    }, [settings]);
    const handleSaveWeb = async () => {
        setSavingUI(true);
        try {
            await api.updateUISettings({
                listen, base_path: basePath, force_https: forceHttps,
                https_cert_id: httpsCertId, session_timeout_h: parseInt(sessionTimeout) || 12,
                dns,
            });
            toast.success(t('settings.saved'));
            queryClient.invalidateQueries({ queryKey: ['settings'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
        finally {
            setSavingUI(false);
        }
    };
    const handleSaveDns = async () => {
        setSavingDns(true);
        try {
            await api.updateUISettings({
                listen, base_path: basePath, force_https: forceHttps,
                https_cert_id: httpsCertId, session_timeout_h: parseInt(sessionTimeout) || 12,
                dns,
            });
            toast.success(t('settings.saved'));
            queryClient.invalidateQueries({ queryKey: ['settings'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
        finally {
            setSavingDns(false);
        }
    };
    const handleChangePw = async () => {
        if (!curPw) {
            toast.error(t('settings.passwordRequired'));
            return;
        }
        if (!newUser && !newPw) {
            toast.error(t('settings.enterNewCreds'));
            return;
        }
        if (newPw && newPw !== confirmPw) {
            toast.error(t('error.passwordMismatch'));
            return;
        }
        setSavingPw(true);
        try {
            const curHash = await sha256(curPw);
            const newHash = newPw ? await sha256(newPw) : undefined;
            await api.changePassword({
                current_password: curHash,
                new_username: newUser || undefined,
                new_password: newHash,
            });
            toast.success(t('settings.passwordUpdated'));
            setTimeout(() => { logout(); navigate('/login'); }, 2000);
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
        finally {
            setSavingPw(false);
        }
    };
    const handleBackup = async () => {
        try {
            const blob = await api.downloadBackup();
            const url = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url;
            a.download = 'hy2scale-backup.tar';
            a.click();
            URL.revokeObjectURL(url);
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    };
    const handleRestore = async (file) => {
        const ok = await confirm({
            title: t('settings.backup'),
            message: t('settings.restoreConfirm'),
            danger: true, confirmText: t('app.confirm'), cancelText: t('app.cancel'),
        });
        if (!ok)
            return;
        try {
            toast.info(t('settings.restoreUploading'));
            await api.uploadRestore(file);
            toast.success(t('settings.restoreComplete'));
            setTimeout(() => window.location.reload(), 3000);
        }
        catch (e) {
            toast.error(t('settings.restoreFailed') + ': ' + String(e.message || e));
        }
    };
    const certOptions = [
        { value: '', label: '\u2014' },
        ...certs.map((c) => ({ value: c.id, label: c.name || c.id })),
    ];
    return (_jsxs("div", { style: { display: 'flex', flexDirection: 'column', gap: 20 }, children: [_jsx(Tabs, { items: [
                    { key: 'system', label: t('settings.system') },
                    { key: 'web', label: t('settings.web') },
                ], activeKey: activeTab, onChange: (key) => setActiveTab(key) }), _jsx(TabPanel, { activeKey: activeTab, keys: ['system', 'web'], children: activeTab === 'system' ? (_jsxs(_Fragment, { children: [_jsx(Card, { title: t('settings.system'), children: _jsxs("div", { style: { maxWidth: 400, display: 'flex', flexDirection: 'column', gap: 14 }, children: [_jsx(FormGroup, { label: t('settings.dns'), children: _jsx(Input, { value: dns, onChange: (e) => setDns(e.target.value), placeholder: "8.8.8.8,1.1.1.1" }) }), _jsx(Button, { variant: "primary", onClick: handleSaveDns, loading: savingDns, style: { alignSelf: 'flex-start' }, children: t('app.save') })] }) }), _jsx(Card, { title: t('settings.changePassword'), children: _jsxs("div", { style: { maxWidth: 400, display: 'flex', flexDirection: 'column', gap: 14 }, children: [_jsx(FormGroup, { label: t('settings.currentPassword'), required: true, children: _jsx(PasswordInput, { value: curPw, onChange: (e) => setCurPw(e.target.value) }) }), _jsx(FormGroup, { label: t('settings.newUsernameOpt'), children: _jsx(Input, { value: newUser, onChange: (e) => setNewUser(e.target.value) }) }), _jsx(FormGroup, { label: t('settings.newPassword'), children: _jsx(PasswordInput, { value: newPw, onChange: (e) => setNewPw(e.target.value), onGenerate: (pw) => { setNewPw(pw); setConfirmPw(pw); } }) }), _jsx(FormGroup, { label: t('settings.confirmPassword'), children: _jsx(PasswordInput, { value: confirmPw, onChange: (e) => setConfirmPw(e.target.value) }) }), _jsx(Button, { variant: "primary", onClick: handleChangePw, loading: savingPw, style: { alignSelf: 'flex-start' }, children: t('settings.update') })] }) }), _jsx(Card, { title: t('settings.backup'), children: _jsxs("div", { style: { display: 'flex', flexDirection: 'column', gap: 14 }, children: [_jsx("div", { style: { fontSize: 12, color: 'var(--text-muted)' }, children: t('settings.backupDesc') }), _jsxs("div", { style: { display: 'flex', gap: 12 }, children: [_jsx(Button, { onClick: handleBackup, children: t('settings.downloadBackup') }), _jsx(Button, { variant: "danger", onClick: () => restoreRef.current?.click(), children: t('settings.restoreFromFile') }), _jsx("input", { ref: restoreRef, type: "file", accept: ".tar", style: { display: 'none' }, onChange: (e) => {
                                                    const file = e.target.files?.[0];
                                                    if (file)
                                                        handleRestore(file);
                                                    e.target.value = '';
                                                } })] })] }) })] })) : (_jsx(Card, { title: t('settings.webUi'), children: _jsxs("div", { style: { maxWidth: 400, display: 'flex', flexDirection: 'column', gap: 14 }, children: [_jsx(FormGroup, { label: t('settings.listenAddress'), children: _jsx(Input, { value: listen, onChange: (e) => setListen(e.target.value), placeholder: ":5565" }) }), _jsx(FormGroup, { label: t('settings.basePath'), children: _jsx(Input, { value: basePath, onChange: (e) => setBasePath(e.target.value), placeholder: "/scale" }) }), _jsx(FormGroup, { label: t('settings.forceHttps'), children: _jsx(Toggle, { checked: forceHttps, onChange: (e) => setForceHttps(e.target.checked) }) }), _jsx(FormGroup, { label: t('settings.httpsCert'), children: _jsx(Select, { value: httpsCertId, onChange: (e) => setHttpsCertId(e.target.value), options: certOptions }) }), _jsx(FormGroup, { label: t('settings.sessionTimeout'), children: _jsx(Input, { type: "number", value: sessionTimeout, onChange: (e) => setSessionTimeout(e.target.value), suffix: "h" }) }), _jsx("div", { style: { fontSize: 11, color: 'var(--text-muted)' }, children: t('settings.restartRequired') }), _jsx(Button, { variant: "primary", onClick: handleSaveWeb, loading: savingUI, style: { alignSelf: 'flex-start' }, children: t('app.save') })] }) })) })] }));
}
