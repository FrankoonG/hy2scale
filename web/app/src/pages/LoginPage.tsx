import { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button, Input, PasswordInput } from '@hy2scale/ui';
import { useAuthStore, getSavedCredentials } from '@/store/auth';
import { sha256 } from '@/hooks/useAuth';
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
      <div style={{ position: 'absolute', top: 16, right: 16 }}>
        <LanguageSwitcher />
      </div>
      <div className="hy-login-box">
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
