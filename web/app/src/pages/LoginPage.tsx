import { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button, Input, PasswordInput } from '@hy2scale/ui';
import { useAuthStore, getSavedCredentials } from '@/store/auth';
import { sha256, getSessionHash } from '@/hooks/useAuth';
import LanguageSwitcher from '@/components/LanguageSwitcher';
import LoginBackground from '@/components/LoginBackground';

export default function LoginPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { login, loginWithHash, loading, error } = useAuthStore();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [remember, setRemember] = useState(false);
  const [hasSaved, setHasSaved] = useState(false);

  // Pre-fill from saved credentials (don't auto-login).
  // Exception: in proxy mode (viewing a remote node's UI through the local
  // one) try the local node's credentials against the remote. If they match
  // (same admin/password), we skip the login step entirely. If the remote
  // rejects them, the user is left on the login form as normal.
  //
  // Auto-login tries saved "remember me" credentials first, then falls back
  // to the current tab's session hash (stored on local login regardless of
  // remember-me). Either path makes visiting a peer node frictionless when
  // they share the same password.
  useEffect(() => {
    const saved = getSavedCredentials();
    if (saved) {
      setUsername(saved.u);
      setPassword('••••••••');
      setRemember(true);
      setHasSaved(true);
    }
    if ((window as any).__PROXY__) {
      const attempt = saved || getSessionHash();
      if (attempt) {
        (async () => {
          const ok = await loginWithHash(attempt.u, attempt.h, !!saved);
          if (ok) navigate('/nodes', { replace: true });
        })();
      }
    }
  }, [loginWithHash, navigate]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    let ok: boolean;
    if (hasSaved && password === '••••••••') {
      // Use saved hash — user didn't change the password field
      const cred = getSavedCredentials();
      if (cred) {
        ok = await loginWithHash(cred.u, cred.h, remember);
      } else {
        ok = await login(username, password, remember);
      }
    } else {
      // User typed a new password
      setHasSaved(false);
      ok = await login(username, password, remember);
    }
    if (ok) navigate('/nodes', { replace: true });
  };

  return (
    <div className="hy-login-wrap">
      <LoginBackground />
      <div style={{ position: 'absolute', top: 16, right: 16, zIndex: 1 }}>
        <LanguageSwitcher />
      </div>
      <div className="hy-login-box" style={{ position: 'relative', zIndex: 1 }}>
        <div className="hy-login-logo">
          <img src="./logo.min.svg" alt="logo" />
        </div>
        <div className="hy-login-title">{t('app.title')}</div>
        <div className="hy-login-sub">{t('app.subtitle')}</div>
        <form className="hy-login-form" onSubmit={handleSubmit}>
          <Input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder={t('app.username')}
            autoFocus
          />
          <PasswordInput
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder={t('app.password')}
          />
          <label className="hy-login-remember">
            <input
              type="checkbox"
              checked={remember}
              onChange={(e) => setRemember(e.target.checked)}
            />
            {t('app.remember')}
          </label>
          {error && <div style={{ color: 'var(--red)', fontSize: 13 }}>{t('error.invalidCredentials')}</div>}
          <Button type="submit" variant="primary" fullWidth loading={loading}>
            {t('app.signin')}
          </Button>
        </form>
      </div>
    </div>
  );
}
