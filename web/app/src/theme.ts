// Colour theme: user preference (system / dark / light) persisted in
// localStorage, resolved to an effective light|dark set as
// <html data-theme>. index.html runs the same resolution inline before
// first paint; this module keeps it live (preference changes and OS
// scheme changes while "system" is selected).
//
// Dark Reader: the light theme keeps `color-scheme: light` and no lock,
// so dark-mode extensions transform the page as before. The dark theme
// inserts <meta name="darkreader-lock"> so Dark Reader leaves our own
// dark palette alone.
import { useSyncExternalStore } from 'react';

export type ThemePref = 'system' | 'dark' | 'light';
export type Theme = 'dark' | 'light';

const KEY = 'hy-theme';
const mq = typeof window !== 'undefined' && window.matchMedia
  ? window.matchMedia('(prefers-color-scheme: dark)')
  : null;

function readPref(): ThemePref {
  try {
    const v = localStorage.getItem(KEY);
    if (v === 'dark' || v === 'light') return v;
  } catch { /* storage blocked */ }
  return 'system';
}

let pref: ThemePref = readPref();
const listeners = new Set<() => void>();

function resolve(p: ThemePref): Theme {
  if (p === 'system') return mq?.matches ? 'dark' : 'light';
  return p;
}

function apply() {
  const theme = resolve(pref);
  const root = document.documentElement;
  if (root.getAttribute('data-theme') !== theme) root.setAttribute('data-theme', theme);
  const head = document.head;
  let cs = head.querySelector<HTMLMetaElement>('meta[name="color-scheme"]');
  if (!cs) {
    cs = document.createElement('meta');
    cs.name = 'color-scheme';
    head.appendChild(cs);
  }
  if (cs.content !== theme) cs.content = theme;
  const lock = head.querySelector('meta[name="darkreader-lock"]');
  if (theme === 'dark' && !lock) {
    const m = document.createElement('meta');
    m.name = 'darkreader-lock';
    head.appendChild(m);
  } else if (theme === 'light' && lock) {
    lock.remove();
  }
}

function emit() {
  apply();
  listeners.forEach((l) => l());
}

apply();
mq?.addEventListener('change', () => { if (pref === 'system') emit(); });
// Another tab changed the preference.
window.addEventListener('storage', (e) => {
  if (e.key === KEY) { pref = readPref(); emit(); }
});

export function setThemePref(p: ThemePref) {
  pref = p;
  try {
    if (p === 'system') localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, p);
  } catch { /* storage blocked */ }
  emit();
}

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => { listeners.delete(cb); };
}

/** Current preference and the effective theme it resolves to. */
export function useTheme(): { pref: ThemePref; theme: Theme } {
  const p = useSyncExternalStore(subscribe, () => pref);
  const theme = useSyncExternalStore(subscribe, () => resolve(pref));
  return { pref: p, theme };
}

export const themePrefs: ThemePref[] = ['system', 'dark', 'light'];

/** i18n key for each preference's label. */
export const themeLabelKey: Record<ThemePref, string> = {
  system: 'app.themeSystem',
  dark: 'app.themeDark',
  light: 'app.themeLight',
};
