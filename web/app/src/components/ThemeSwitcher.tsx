import { useTranslation } from 'react-i18next';
import { DropdownMenu } from '@hy2scale/ui';
import { useTheme, setThemePref, themePrefs, themeLabelKey } from '@/theme';

/** Compact theme picker for the login page (same look as LanguageSwitcher). */
export default function ThemeSwitcher() {
  const { t } = useTranslation();
  const { pref } = useTheme();
  return (
    <DropdownMenu
      trigger={
        <button className="hy-lang-btn" data-testid="login-theme">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" width="14" height="14">
            <circle cx="12" cy="12" r="9" /><path d="M12 3a9 9 0 010 18z" fill="currentColor" />
          </svg>
          <span>{t(themeLabelKey[pref])}</span>
        </button>
      }
      items={themePrefs.map((p) => ({
        key: p,
        label: <span style={{ fontWeight: p === pref ? 600 : undefined, color: p === pref ? 'var(--primary)' : undefined }}>{t(themeLabelKey[p])}</span>,
        onClick: () => setThemePref(p),
      }))}
    />
  );
}
