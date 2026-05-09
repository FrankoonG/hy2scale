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
