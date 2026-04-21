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

  const res = await fetch(`${basePath}/api${path}`, { ...opts, headers });

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
