import { useState, useEffect, type KeyboardEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button, Input, PasswordInput, IconServer, IconCircleAlert } from '@hy2scale/ui';
import { useAuthStore, getSavedCredentials } from '@/store/auth';
import { LanguageSwitcher } from '@/components/PrefSwitcher';
import LoginTopology from '@/components/login/LoginTopology';
import ThemeCycleButton from '@/components/login/ThemeCycleButton';
import BrandLogo from '@/components/login/BrandLogo';
import { enterApp } from '@/hooks/authTransition';

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
  const [caps, setCaps] = useState(false);
  // entered by the logout transition: that moves the brand into place, so skip the page's own entrance
  const [still] = useState(() => document.documentElement.classList.contains('vt-auth'));

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

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    let ok: boolean;
    if (hasSaved && password === '••••••••') {
      // Use saved hash — user didn't change the password field
      const cred = getSavedCredentials();
      if (cred) {
        ok = await loginWithHash(cred.u, cred.h, remember, enterApp);
      } else {
        ok = await login(username, password, remember, enterApp);
      }
    } else {
      // User typed a new password
      setHasSaved(false);
      ok = await login(username, password, remember, enterApp);
    }
    if (ok) navigate('/overview', { replace: true });
  };

  const capsCheck = (e: KeyboardEvent<HTMLInputElement>) => setCaps(e.getModifierState('CapsLock'));

  if (isProxy) {
    return (
      <div className="hy-remote-expired">{t('remote.expired')}</div>
    );
  }

  return (
    <div className={`lg-page${still ? ' still' : ''}`}>
      <LoginTopology />
      <div className="lg-right">
        <ThemeCycleButton />
        <div className="lg-formbox">
          <div className="lg-mobile-brand"><BrandLogo size={24} /><span>HY2 SCALE</span></div>
          <h1 className="lg-title">{t('app.signin')}</h1>
          <div className="lg-sub">{t('app.subtitle')}</div>
          <div className="lg-host"><IconServer size={12} /><span>{window.location.host}</span></div>
          <form className="lg-form" onSubmit={handleSubmit}>
            <label className="lg-label" htmlFor="lg-user">{t('app.username')}</label>
            <Input id="lg-user" name="username" autoComplete="username" spellCheck={false} autoFocus
              value={username} onChange={(e) => setUsername(e.target.value)} />
            <label className="lg-label" htmlFor="lg-pass">{t('app.password')}</label>
            <PasswordInput id="lg-pass" name="password" autoComplete="current-password"
              value={password} onChange={(e) => setPassword(e.target.value)}
              onKeyDown={capsCheck} onKeyUp={capsCheck} onBlur={() => setCaps(false)} />
            {caps && <div className="lg-caps"><IconCircleAlert size={13} />{t('login.caps')}</div>}
            <div className="lg-lang"><LanguageSwitcher /></div>
            <label className="lg-remember">
              <input type="checkbox" checked={remember} onChange={(e) => setRemember(e.target.checked)} />
              {t('app.remember')}
            </label>
            {error && <div className="lg-error" role="alert">{t('error.invalidCredentials')}</div>}
            <Button type="submit" variant="primary" fullWidth loading={loading}>
              {t('app.signin')}
            </Button>
          </form>
        </div>
      </div>
    </div>
  );
}
