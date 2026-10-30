import { useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import { IconMoon, IconSun, IconSunMoon } from '@hy2scale/ui';
import { useTheme, setThemePref, themeLabelKey, type ThemePref } from '@/theme';

const ORDER: ThemePref[] = ['system', 'light', 'dark'];
const ICONS = { system: IconSunMoon, light: IconSun, dark: IconMoon };

/** Login: one round button cycling system → light → dark; the icon swaps with a turn. */
export default function ThemeCycleButton() {
  const { t } = useTranslation();
  const { pref } = useTheme();
  const [flash, setFlash] = useState(false);
  const timer = useRef(0);
  const label = `${t('app.theme')}: ${t(themeLabelKey[pref])}`;
  return (
    <>
      <button type="button" className="lg-theme-btn" title={label} aria-label={label} data-testid="login-theme"
        onClick={() => {
          setThemePref(ORDER[(ORDER.indexOf(pref) + 1) % ORDER.length]);
          setFlash(true);
          window.clearTimeout(timer.current);
          timer.current = window.setTimeout(() => setFlash(false), 1100);
        }}>
        {ORDER.map((p) => { const I = ICONS[p]; return <I key={p} size={20} className={clsx(p === pref && 'on')} />; })}
      </button>
      <span className={clsx('lg-theme-tip', flash && 'flash')} aria-hidden="true">{label}</span>
    </>
  );
}
