import { useCallback, useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { AnimatePresence, motion } from 'framer-motion';
import clsx from 'clsx';
import { useClickOutside } from '@hy2scale/ui';
import i18n from '@/i18n';
import * as api from '@/api';
import { useAuthStore } from '@/store/auth';
import { languages } from '@/components/LanguageSwitcher';

const userIcon = <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="12" cy="8" r="4"/><path d="M4 21v-1a6 6 0 016-6h4a6 6 0 016 6v1"/></svg>;
const caretDown = <svg className="hy-user-caret" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="6 9 12 15 18 9"/></svg>;
const caretRight = <svg className="hy-dd-caret" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="9 6 15 12 9 18"/></svg>;
const globeIcon = <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14 14 0 010 18M12 3a14 14 0 000 18"/></svg>;
const checkIcon = <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><polyline points="5 12 10 17 19 7"/></svg>;
const logoutIcon = <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></svg>;

/** Top bar account button: language (inline expand) and logout. */
export default function UserMenu() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const logout = useAuthStore((s) => s.logout);
  const loginUser = useAuthStore((s) => s.username);
  const forcePasswordChange = useAuthStore((s) => s.forcePasswordChange);
  const { data: me } = useQuery({ queryKey: ['me'], queryFn: api.getMe, staleTime: 60000, retry: false });
  const username = me?.username || loginUser || t('app.account');

  const [open, setOpen] = useState(false);
  const [langOpen, setLangOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const close = useCallback(() => { setOpen(false); setLangOpen(false); }, []);
  useClickOutside(ref, close);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') close(); };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [open, close]);

  const lang = i18n.language || 'en';
  const current = languages.find((l) => lang.startsWith(l.code))?.code || 'en';
  const currentName = languages.find((l) => l.code === current)?.name || current;

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
        {userIcon}
        <span className="hy-user-name">{username}</span>
        {caretDown}
      </button>
      <AnimatePresence>
        {open && (
          <motion.div
            className="hy-dropdown hy-user-dropdown"
            role="menu"
            initial={{ opacity: 0, y: -8 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -8 }}
            transition={{ duration: 0.15, ease: 'easeOut' }}
          >
            <button
              type="button"
              className="hy-dropdown-item"
              aria-expanded={langOpen}
              data-testid="user-menu-language"
              onClick={() => setLangOpen((v) => !v)}
            >
              {globeIcon}
              <span>{t('app.language')}</span>
              <span className="hy-dd-meta">{currentName}</span>
              {caretRight}
            </button>
            {langOpen && (
              <div className="hy-dd-sub" role="group">
                {languages.map((l) => (
                  <button
                    key={l.code}
                    type="button"
                    role="menuitemradio"
                    aria-checked={l.code === current}
                    className={clsx('hy-dropdown-item', l.code === current && 'current')}
                    onClick={() => { i18n.changeLanguage(l.code); close(); }}
                  >
                    <span className="hy-dd-check">{l.code === current && checkIcon}</span>
                    <span>{l.name}</span>
                  </button>
                ))}
              </div>
            )}
            <div className="hy-dd-divider" />
            <button
              type="button"
              role="menuitem"
              className="hy-dropdown-item"
              disabled={forcePasswordChange}
              data-testid="user-menu-logout"
              onClick={() => { close(); logout(); navigate('/login'); }}
            >
              {logoutIcon}
              <span>{t('app.logout')}</span>
            </button>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  );
}
