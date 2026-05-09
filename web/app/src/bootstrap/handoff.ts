// Runs as a side effect on module load. If the URL contains a `#tok=<...>`
// fragment (planted by RemoteConnectModal when the parent page hands a
// fresh remote-node session token to a window.open'd tab), copy it into
// sessionStorage under the per-basePath token key the auth store reads
// at boot, then strip the fragment so the token doesn't sit in the
// address bar / history.
//
// SessionStorage is a per-tab store: a parent tab's sessionStorage is
// invisible to a tab created by window.open. The URL hash is the
// channel — hashes are never sent over HTTP, so the token doesn't leak
// into proxy / server access logs. We strip it via replaceState before
// the bundle's first navigation so it stops showing up after one tick.
//
// This module is imported first from main.tsx so its top-level code runs
// BEFORE the auth store's module body, which calls getToken() inside
// create() and would otherwise miss the token planted here.

const m = window.location.hash.match(/^#tok=([^&]+)/);
if (m) {
  try {
    const tok = decodeURIComponent(m[1]);
    const base = (window as any).__BASE__ || '';
    if (tok) sessionStorage.setItem('token:' + base, tok);
  } catch {
    /* ignore malformed fragment */
  }
  // Drop the hash without triggering a route change.
  try {
    history.replaceState(null, '', window.location.pathname + window.location.search);
  } catch { /* ignore (some embed contexts) */ }
}

export {};
