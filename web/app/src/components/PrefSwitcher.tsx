import { useTranslation } from 'react-i18next';
import { DropdownMenu, IconChevronDown, IconLanguages, IconSunMoon, type IconComponent } from '@hy2scale/ui';
import i18n from '@/i18n';
import { useTheme, setThemePref, themePrefs, themeLabelKey } from '@/theme';

export const languages = [
  { code: 'en', name: 'English' },
  { code: 'ko', name: '한국어' },
  { code: 'zh', name: '简体中文' },
];

/** The language the UI currently shows. */
export function currentLanguage(): { code: string; name: string } {
  const lang = i18n.language || 'en';
  return languages.find((l) => lang.startsWith(l.code)) || languages[0];
}

function Switcher({ icon: Icon, value, label, testId, items }: {
  icon: IconComponent;
  value: string;
  label: string;
  testId: string;
  items: { key: string; label: string; checked: boolean; onClick: () => void }[];
}) {
  return (
    <DropdownMenu
      trigger={(open) => (
        <button type="button" className={`hy-pref-btn${open ? ' open' : ''}`} aria-haspopup="menu" aria-expanded={open}
          title={label} data-testid={testId}>
          <Icon size={14} />
          <span>{value}</span>
          <IconChevronDown className="hy-pref-caret" size={14} />
        </button>
      )}
      items={items}
    />
  );
}

/** Login page: language and theme pickers (same look). */
export function LanguageSwitcher() {
  const { t } = useTranslation();
  const cur = currentLanguage();
  return (
    <Switcher icon={IconLanguages} value={cur.name} label={t('app.language')} testId="login-language"
      items={languages.map((l) => ({ key: l.code, label: l.name, checked: l.code === cur.code, onClick: () => i18n.changeLanguage(l.code) }))} />
  );
}

export function ThemeSwitcher() {
  const { t } = useTranslation();
  const { pref } = useTheme();
  return (
    <Switcher icon={IconSunMoon} value={t(themeLabelKey[pref])} label={t('app.theme')} testId="login-theme"
      items={themePrefs.map((p) => ({ key: p, label: t(themeLabelKey[p]), checked: p === pref, onClick: () => setThemePref(p) }))} />
  );
}
