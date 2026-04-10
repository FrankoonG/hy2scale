import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button, Input, PasswordInput } from '@hy2scale/ui';
import { useAuthStore, getSavedCredentials } from '@/store/auth';
import LanguageSwitcher from '@/components/LanguageSwitcher';
export default function LoginPage() {
    const { t } = useTranslation();
    const navigate = useNavigate();
    const { login, loginWithHash, loading, error } = useAuthStore();
    const [username, setUsername] = useState('');
    const [password, setPassword] = useState('');
    const [remember, setRemember] = useState(false);
    const [hasSaved, setHasSaved] = useState(false);
    // Pre-fill from saved credentials (don't auto-login)
    useEffect(() => {
        const cred = getSavedCredentials();
        if (cred) {
            setUsername(cred.u);
            setPassword('••••••••'); // masked placeholder
            setRemember(true);
            setHasSaved(true);
        }
    }, []);
    const handleSubmit = async (e) => {
        e.preventDefault();
        let ok;
        if (hasSaved && password === '••••••••') {
            // Use saved hash — user didn't change the password field
            const cred = getSavedCredentials();
            if (cred) {
                ok = await loginWithHash(cred.u, cred.h, remember);
            }
            else {
                ok = await login(username, password, remember);
            }
        }
        else {
            // User typed a new password
            setHasSaved(false);
            ok = await login(username, password, remember);
        }
        if (ok)
            navigate('/nodes', { replace: true });
    };
    return (_jsxs("div", { className: "hy-login-wrap", children: [_jsx("div", { style: { position: 'absolute', top: 16, right: 16 }, children: _jsx(LanguageSwitcher, {}) }), _jsxs("div", { className: "hy-login-box", children: [_jsx("div", { className: "hy-login-logo", children: _jsx("img", { src: "./logo.min.svg", alt: "logo" }) }), _jsx("div", { className: "hy-login-title", children: t('app.title') }), _jsx("div", { className: "hy-login-sub", children: t('app.subtitle') }), _jsxs("form", { className: "hy-login-form", onSubmit: handleSubmit, children: [_jsx(Input, { value: username, onChange: (e) => setUsername(e.target.value), placeholder: t('app.username'), autoFocus: true }), _jsx(PasswordInput, { value: password, onChange: (e) => setPassword(e.target.value), placeholder: t('app.password') }), _jsxs("label", { className: "hy-login-remember", children: [_jsx("input", { type: "checkbox", checked: remember, onChange: (e) => setRemember(e.target.checked) }), t('app.remember')] }), error && _jsx("div", { style: { color: 'var(--red)', fontSize: 13 }, children: t('error.invalidCredentials') }), _jsx(Button, { type: "submit", variant: "primary", fullWidth: true, loading: loading, children: t('app.signin') })] })] })] }));
}
