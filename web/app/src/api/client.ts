const basePath = (window as any).__BASE__ || '';

let getToken: () => string | null = () => null;

export function setTokenGetter(fn: () => string | null) {
  getToken = fn;
}

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

export async function api<T = any>(
  path: string,
  opts: RequestInit = {},
): Promise<T> {
  const token = getToken();
  const headers: Record<string, string> = {
    ...(opts.headers as Record<string, string> || {}),
  };
  if (token) headers['Authorization'] = `Bearer ${token}`;
  if (opts.body && typeof opts.body === 'string') {
    headers['Content-Type'] = 'application/json';
  }

  // credentials: 'same-origin' makes the hub-session cookie travel
  // with every request — proxy tabs (loaded under a /scale/remote/...
  // path) start with no Bearer because their proxy-scoped sessionStorage
  // is empty until the user logs in OR a passthrough flow primes it,
  // so the cookie is the only auth carrier on those first hits. Same-
  // origin is also the fetch default, but spelling it out keeps the
  // intent visible alongside the Authorization-header path.
  const res = await fetch(`${basePath}/api${path}`, { ...opts, headers, credentials: 'same-origin' });

  if (res.status === 401) {
    // Session expired
    const event = new CustomEvent('hy2scale:session-expired');
    window.dispatchEvent(event);
    throw new ApiError(401, 'Session expired');
  }

  if (!res.ok) {
    const text = await res.text();
    throw new ApiError(res.status, text || `HTTP ${res.status}`);
  }

  const ct = res.headers.get('content-type') || '';
  if (ct.includes('application/json')) {
    return res.json();
  }
  return res as any;
}

export function getBasePath() {
  return basePath;
}

/** The current session token (for carriers that cannot set headers, e.g. WebSocket). */
export function authToken(): string | null {
  return getToken();
}

/**
 * Subscribes to a server-sent event stream under /api. The browser retries a
 * dropped stream by itself, but an EventSource whose retry is answered with
 * an error (a remote proxy's 502 while the relay is down) closes for good;
 * that one is reopened here with backoff, calling `onGap` first (to fetch
 * what the stream would have said). A tab hidden for a while lets its stream
 * go (over plain HTTP/1.1 a browser has 6 connections per origin for all its
 * tabs) and reopens it when shown; the first frame is the current state.
 * Returns the unsubscribe function.
 */
export function liveStream(path: string, onData: (data: any) => void, onGap?: () => void): () => void {
  let es: EventSource | null = null;
  let timer = 0;
  let idle = 0;
  let delay = 1000;
  let gone = false;
  const close = () => { window.clearTimeout(timer); es?.close(); es = null; };
  const open = () => {
    close();
    const token = getToken();
    const s = new EventSource(`${basePath}/api${path}${token ? (path.includes('?') ? '&' : '?') + 'token=' + encodeURIComponent(token) : ''}`);
    es = s;
    s.onmessage = (ev) => {
      delay = 1000;
      let data: any;
      try { data = JSON.parse(ev.data); } catch { return; /* partial or malformed frame */ }
      onData(data);
    };
    s.onerror = () => {
      if (gone || es !== s || s.readyState !== EventSource.CLOSED) return; // the browser retries by itself
      close();
      onGap?.();
      timer = window.setTimeout(open, delay);
      delay = Math.min(delay * 2, 15000);
    };
  };
  const onVisibility = () => {
    window.clearTimeout(idle);
    if (document.visibilityState === 'hidden') idle = window.setTimeout(close, 30000);
    else if (!es) { delay = 1000; open(); }
  };
  document.addEventListener('visibilitychange', onVisibility);
  open();
  return () => {
    gone = true;
    window.clearTimeout(idle);
    document.removeEventListener('visibilitychange', onVisibility);
    close();
  };
}
