// Pre-render auth bootstrap. Runs as a top-level side effect of the
// import in main.tsx, BEFORE any React module evaluates the auth
// store's create() — which means whatever we plant in sessionStorage
// here is what the auth-store reads on initial state.
//
// Two ways a proxy-loaded tab can pick up a token:
//
// (1) URL hash hand-off: RemoteConnectModal already minted a token
//     for us (e.g. via /api/login with saved-cred hash) and put it in
//     `#tok=<...>`. Decode it, write to sessionStorage, strip the
//     hash. Hashes never travel over the wire so the token doesn't
//     land in any access log.
//
// (2) Self-mint via the relay-passthrough endpoint. Spawn-a-tab is
//     authenticated to the hub-proxy through the hy2_session cookie
//     inherited from the parent. If the remote node has
//     RelayAdminPassthrough on, POST /api/relay-passthrough-token
//     mints a session token without ever touching its web password.
//     This path runs only in proxy mode and only when (1) didn't
//     already succeed.
//
// On success, App.tsx renders the authenticated routes immediately.
// On failure (passthrough off + no hash + no saved-cred match), the
// auth-store sees no token and App.tsx routes to LoginPage.

const base = (window as any).__BASE__ || '';
const tokenStorageKey = 'token:' + base;
const isProxy = !!(window as any).__PROXY__;

// Path 1: URL hash hand-off.
const m = window.location.hash.match(/^#tok=([^&]+)/);
if (m) {
  try {
    const tok = decodeURIComponent(m[1]);
    if (tok) sessionStorage.setItem(tokenStorageKey, tok);
  } catch {
    /* malformed fragment — ignore */
  }
  try {
    history.replaceState(null, '', window.location.pathname + window.location.search);
  } catch {
    /* embed contexts that disallow history.replaceState */
  }
}

// Path 2: relay-passthrough self-mint. Only attempts in proxy mode
// when path 1 didn't already produce a token. Synchronous XHR blocks
// bundle eval until the round-trip completes — typically a single
// 2-hop relay open (~5–50 ms on local docker, ~RTT on WAN). We
// accept the brief block because it lets the auth-store's create()
// observe the planted token in its initial state, instead of having
// to bolt on an extra "loading auth" UI.
if (isProxy && !sessionStorage.getItem(tokenStorageKey) && base.includes('/remote/')) {
  try {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', base + '/api/relay-passthrough-token', false);
    xhr.withCredentials = true;
    xhr.send(null);
    if (xhr.status === 200) {
      const data = JSON.parse(xhr.responseText);
      if (data && data.token) {
        sessionStorage.setItem(tokenStorageKey, data.token);
      }
    }
  } catch {
    /* network blip / 403 / 404 — drop to LoginPage */
  }
}

export {};
