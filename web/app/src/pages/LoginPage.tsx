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

  // Tab title for the login page: `Login - HY2 SCALE` (i18n) so the
  // unauthenticated screen doesn't accidentally inherit the previous
  // session's `<node_id> - HY2 SCALE` from when the user was logged in.
  useEffect(() => {
    document.title = `${t('app.signin')} - HY2 SCALE`;
  }, [t]);

  // In proxy mode (a remote node's UI rendered inside /scale/remote/.../)
  // we never show the login UI. The remote-connect handshake happens on
  // the parent tab's RemoteConnectModal BEFORE this tab is opened, so the
  // only ways we reach this page are: direct URL typing, session expiry,
  // or the user clicked Logout. All three should land on a plain-text
  // "session expired" screen — the user has to go back to the parent tab
  // to re-establish a session.
  const isProxy = !!(window as any).__PROXY__;

  const { login, loginWithHash, loading, error } = useAuthStore();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [remember, setRemember] = useState(false);
  const [hasSaved, setHasSaved] = useState(false);

  // Local login: pre-fill from saved credentials (don't auto-submit).
  // Skip entirely in proxy mode — the plain-text screen rendered below
  // doesn't use these state values.
  useEffect(() => {
    if (isProxy) return;
    const saved = getSavedCredentials();
    if (saved) {
      setUsername(saved.u);
      setPassword('••••••••');
      setRemember(true);
      setHasSaved(true);
    }
  }, [isProxy]);
  // Suppress unused-var warning for the no-op proxy branch.
  void getSessionHash; void loginWithHash; void navigate;

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

  if (isProxy) {
    return (
      <div
        style={{
          position: 'fixed', inset: 0,
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          padding: 24, fontFamily: 'var(--mono)',
          fontSize: 14, lineHeight: 1.6, color: 'var(--text-secondary)',
          textAlign: 'center', whiteSpace: 'pre-wrap',
          background: 'var(--bg)',
        }}
      >
        {t('remote.expired')}
      </div>
    );
  }

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
