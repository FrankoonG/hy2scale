const basePath = window.__BASE__ || '';
let getToken = () => null;
export function setTokenGetter(fn) {
    getToken = fn;
}
export class ApiError extends Error {
    constructor(status, message) {
        super(message);
        this.status = status;
    }
}
export async function api(path, opts = {}) {
    const token = getToken();
    const headers = {
        ...(opts.headers || {}),
    };
    if (token)
        headers['Authorization'] = `Bearer ${token}`;
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
    return res;
}
export function getBasePath() {
    return basePath;
}
