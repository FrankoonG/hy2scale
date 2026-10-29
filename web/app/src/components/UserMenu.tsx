import { useCallback, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import clsx from 'clsx';
import {
  Menu, MenuItem, MenuRadioItem, MenuSeparator, MenuSub, useDismiss,
  IconChevronDown, IconCircleUser, IconLanguages, IconLogOut, IconSunMoon,
} from '@hy2scale/ui';
import i18n from '@/i18n';
import * as api from '@/api';
import { useAuthStore } from '@/store/auth';
import { languages, currentLanguage } from '@/components/PrefSwitcher';
import { useTheme, setThemePref, themePrefs, themeLabelKey } from '@/theme';

/** Top bar account button: language and theme (inline expand) and logout. */
export default function UserMenu() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const logout = useAuthStore((s) => s.logout);
  const loginUser = useAuthStore((s) => s.username);
  const forcePasswordChange = useAuthStore((s) => s.forcePasswordChange);
  const { data: me } = useQuery({ queryKey: ['me'], queryFn: api.getMe, staleTime: 60000, retry: false });
  const username = me?.username || loginUser || t('app.account');

  const [open, setOpen] = useState(false);
  const [sub, setSub] = useState<'lang' | 'theme' | null>(null);
  const { pref } = useTheme();
  const ref = useRef<HTMLDivElement>(null);
  const close = useCallback(() => { setOpen(false); setSub(null); }, []);
  useDismiss(ref, close, { enabled: open });

  const lang = currentLanguage();
  const toggleSub = (s: 'lang' | 'theme') => setSub((v) => (v === s ? null : s));

  return (
    <div ref={ref} className="hy-user-menu">
      <button
        type="button"
        className={clsx('hy-user-btn', open && 'open')}
        aria-haspopup="menu"
        aria-expanded={open}
        title={username}
        data-testid="user-menu"
        onClick={() => (open ? close() : setOpen(true))}
      >
        <IconCircleUser />
        <span className="hy-user-name">{username}</span>
        <IconChevronDown className="hy-user-caret" size={14} />
      </button>
      <Menu open={open} className="hy-user-dropdown">
        <MenuSub icon={IconLanguages} label={t('app.language')} meta={lang.name}
          open={sub === 'lang'} onToggle={() => toggleSub('lang')} testId="user-menu-language">
          {languages.map((l) => (
            <MenuRadioItem key={l.code} checked={l.code === lang.code}
              onClick={() => { i18n.changeLanguage(l.code); close(); }}>{l.name}</MenuRadioItem>
          ))}
        </MenuSub>
        <MenuSub icon={IconSunMoon} label={t('app.theme')} meta={t(themeLabelKey[pref])}
          open={sub === 'theme'} onToggle={() => toggleSub('theme')} testId="user-menu-theme">
          {themePrefs.map((p) => (
            <MenuRadioItem key={p} checked={p === pref} testId={`user-menu-theme-${p}`}
              onClick={() => { setThemePref(p); close(); }}>{t(themeLabelKey[p])}</MenuRadioItem>
          ))}
        </MenuSub>
        <MenuSeparator />
        <MenuItem icon={IconLogOut} disabled={forcePasswordChange} testId="user-menu-logout"
          onClick={() => { close(); logout(); navigate('/login'); }}>
          {t('app.logout')}
        </MenuItem>
      </Menu>
    </div>
  );
}
