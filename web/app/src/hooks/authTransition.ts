import type { NavigateFunction } from 'react-router-dom';
import { useAuthStore } from '@/store/auth';
import { queryClient } from '@/queryClient';

/*
 * Login ⇄ app hand-off, choreographed around the divider both screens share
 * (login: topology | form, app: sidebar | content):
 *
 *   sign-in   login content fades, the left panel turns to the sidebar's
 *             surface; the app mounts with its sidebar as wide as the login
 *             panel and slides the divider in, the logo and brand glide to
 *             their sidebar place, nav items appear one by one, the page
 *             fades in behind the divider.
 *   sign-out  the same backwards.
 *
 * The leaving screen records where its divider, logo and brand are; the
 * arriving screen animates from there to its own layout (FLIP). Narrow
 * screens (no side panel) and reduced motion just switch.
 */

interface Handoff { from: 'login' | 'app'; divider: number; logo: DOMRect; brand: DOMRect | null }
let handoff: Handoff | null = null;

const EASE = 'cubic-bezier(.2,.75,.15,1)';
const MOVE_MS = 650;

const enabled = () => window.innerWidth > 900
  && !window.matchMedia('(prefers-reduced-motion: reduce)').matches
  && typeof Element.prototype.animate === 'function';
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const rectOf = (sel: string) => document.querySelector(sel)?.getBoundingClientRect() ?? null;
const all = (sel: string) => Array.from(document.querySelectorAll<HTMLElement>(sel));

function fadeOut(els: Element[], ms: number, stagger = 0) {
  els.forEach((el, i) => el.animate([{ opacity: 1 }, { opacity: 0 }], { duration: ms, delay: i * stagger, easing: 'ease-in', fill: 'forwards' }));
}

/** Moves `el` from rect `a` to where it is now (uniform scale by height). */
function flipFrom(el: Element | null, a: DOMRect | null) {
  if (!el || !a) return;
  const b = el.getBoundingClientRect();
  if (!b.width || !b.height) return;
  const s = a.height / b.height;
  el.animate([
    { transformOrigin: '0 0', transform: `translate(${a.left - b.left}px, ${a.top - b.top}px) scale(${s})` },
    { transformOrigin: '0 0', transform: 'none' },
  ], { duration: MOVE_MS, easing: EASE });
}

/** The hand-off left by the other screen, once. */
export function takeHandoff(from: Handoff['from']): Handoff | null {
  if (handoff?.from !== from) return null;
  const h = handoff;
  handoff = null;
  return h;
}

// ------------------------------------------------------------------ sign-in
/** Login succeeded: `apply` swaps to the app (sets the session). */
export async function enterApp(apply: () => void): Promise<void> {
  const left = document.querySelector('.lg-left');
  const logo = rectOf('.lg-brand .lg-logo');
  if (!enabled() || !left || !logo) { apply(); return; }
  handoff = { from: 'login', divider: left.getBoundingClientRect().right, logo, brand: rectOf('.lg-brand > span') };
  document.querySelector('.lg-page')?.classList.add('leaving');
  fadeOut(all('.lg-topo, .lg-foot, .lg-formbox, .lg-theme-btn'), 260);
  await sleep(320);
  apply();
}

/** App just mounted after sign-in: slide the divider in, glide the brand, stagger the nav. */
export function playAppEnter(h: Handoff) {
  const side = document.querySelector<HTMLElement>('.hy-sidebar');
  if (!side) return;
  const w = side.getBoundingClientRect().width;
  side.animate([{ width: `${h.divider}px` }, { width: `${w}px` }], { duration: MOVE_MS, easing: EASE });
  flipFrom(side.querySelector('.hy-sidebar-logo .lg-logo'), h.logo);
  flipFrom(side.querySelector('.hy-sidebar-logo .hy-brand'), h.brand);
  const appear = (els: Element[], start: number, step: number, dx = 0) => els.forEach((el, i) => el.animate(
    [{ opacity: 0, transform: `translateX(${dx}px)` }, { opacity: 1, transform: 'none' }],
    { duration: 320, delay: start + i * step, easing: 'ease-out', fill: 'backwards' }));
  appear(all('.hy-sidebar-logo .version-menu-btn'), 420, 0);
  appear(all('.hy-sidebar-section, .hy-sidebar-item, .hy-sidebar-divider'), 240, 35, -10);
  appear(all('.hy-main'), 200, 0);
}

// ------------------------------------------------------------------ sign-out
/** Sign-out: `apply` swaps to the login page (clears the session). */
export async function leaveApp(apply: () => void): Promise<void> {
  const side = document.querySelector('.hy-sidebar');
  const sideRect = side?.getBoundingClientRect();
  const logo = rectOf('.hy-sidebar-logo .lg-logo');
  if (!enabled() || !side || !sideRect || sideRect.right <= 0 || !logo) { apply(); return; }
  const items = all('.hy-sidebar-section, .hy-sidebar-item, .hy-sidebar-divider').reverse();
  fadeOut(items, 180, 18);
  fadeOut(all('.hy-main, .hy-sidebar-logo .version-menu-btn'), 260);
  await sleep(Math.min(420, 200 + items.length * 18));
  handoff = { from: 'app', divider: sideRect.right, logo, brand: rectOf('.hy-sidebar-logo .hy-brand') };
  apply();
}

/** Login page just mounted after sign-out: slide the divider out, glide the brand back. */
export function playLoginEnter(h: Handoff) {
  const page = document.querySelector<HTMLElement>('.lg-page');
  const left = document.querySelector<HTMLElement>('.lg-left');
  if (!page || !left) return;
  const W = page.getBoundingClientRect().width, L = left.getBoundingClientRect().width;
  page.animate([
    { gridTemplateColumns: `${h.divider}px ${W - h.divider}px` },
    { gridTemplateColumns: `${L}px ${W - L}px` },
  ], { duration: MOVE_MS, easing: EASE });
  flipFrom(document.querySelector('.lg-brand .lg-logo'), h.logo);
  flipFrom(document.querySelector('.lg-brand > span'), h.brand);
}

/**
 * Sign out. When the server still serves this bundle, stay in the page and
 * hand off to the login screen (clearing the query cache); after a
 * redeploy, hard-reload instead so the next login runs the new build.
 */
export async function signOut(navigate: NavigateFunction): Promise<void> {
  const auth = useAuthStore.getState();
  const loaded = (window as any).__BUILD_ID__ as string | undefined;
  let same = false;
  try {
    const r = await fetch(((window as any).__BASE__ || '') + '/api/build-id', { cache: 'no-store' });
    same = !!loaded && r.ok && (await r.json())?.build_id === loaded;
  } catch { /* unreachable: reload */ }
  if (!same) { auth.logout(); return; }
  await leaveApp(() => {
    auth.endSession();
    queryClient.clear();
    navigate('/login', { replace: true });
  });
}
