import { flushSync } from 'react-dom';
import type { NavigateFunction } from 'react-router-dom';
import { useAuthStore } from '@/store/auth';
import { queryClient } from '@/queryClient';

/*
 * Login ⇄ app as a same-document view transition. While `html.vt-auth` is
 * set, login.css names the elements both screens have — logo, brand text,
 * and the topology panel / sidebar — so they move between their positions;
 * everything else crossfades. Unsupported browsers and reduced motion just
 * switch.
 */

type StartVT = (update: () => Promise<void>) => { finished: Promise<void>; updateCallbackDone: Promise<void> };

/** Waits until `selector` is in the DOM (rendering is paused meanwhile), up to `ms`. */
function rendered(selector: string, ms: number): Promise<void> {
  return new Promise((done) => {
    const t0 = performance.now();
    const poll = () => (document.querySelector(selector) || performance.now() - t0 > ms ? done() : setTimeout(poll, 16));
    poll();
  });
}

/** Runs `update` (the state change that swaps screens) inside the transition. */
export async function authTransition(update: () => void, readySelector: string): Promise<void> {
  const start = (document as Document & { startViewTransition?: StartVT }).startViewTransition?.bind(document);
  if (!start || window.matchMedia('(prefers-reduced-motion: reduce)').matches) { update(); return; }
  const root = document.documentElement;
  root.classList.add('vt-auth');
  const vt = start(async () => {
    flushSync(update);
    await rendered(readySelector, 800);
  });
  vt.finished.catch(() => {}).finally(() => root.classList.remove('vt-auth'));
  await vt.updateCallbackDone;
}

/** Login succeeded: enter the app (the sidebar is the shared anchor). */
export const enterApp = (apply: () => void) => authTransition(apply, '.hy-sidebar');

/** Logout: back to the login page (its topology panel is the shared anchor). */
export const leaveApp = (apply: () => void) => authTransition(apply, '.lg-left, .lg-formbox');

/**
 * Sign out. When the server still serves this bundle, stay in the page and
 * transition back to the login screen (clearing the query cache); after a
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
