import { api, authToken, getBasePath } from './client';

/** Settings → Storage: what this node keeps (logs, audit) and the GeoIP databases. */

export interface StorageKind { enabled: boolean; retention_days: number; max_mb: number; used_bytes?: number; dropped?: number }
export interface StorageSettings { logs: StorageKind; audit: StorageKind }
export type StorageUpdate = { logs?: Partial<StorageKind>; audit?: Partial<StorageKind> };

export const getStorageSettings = () => api<StorageSettings>('/settings/storage');
export const updateStorageSettings = (data: StorageUpdate) =>
  api('/settings/storage', { method: 'PUT', body: JSON.stringify(data) });
export const wipeStorage = (kind: 'logs' | 'audit') => api(`/storage/${kind}`, { method: 'DELETE' });

// ---------------------------------------------------------------- GeoIP

export type GeoKind = 'city' | 'country' | 'asn';
export type GeoSource = 'dbip' | 'iplocate' | 'upload';
export interface GeoDatabase { kind: GeoKind; type: string; build?: string; size: number; languages?: string[]; ip_version?: number; nodes?: number; source?: GeoSource }
export type GeoMode = 'manual' | 'auto';
export interface GeoAuto {
  state: 'idle' | 'checking' | 'error';
  source: '' | 'dbip' | 'iplocate';
  last_check?: string; last_update?: string; next_check?: string; error?: string;
}
export interface GeoStatus { available: boolean; databases: GeoDatabase[]; mode?: GeoMode; auto?: GeoAuto }
export interface GeoInfo {
  country?: string; continent?: string; region?: string; city?: string; postal?: string;
  lat?: number; lon?: number; tz?: string; asn?: number; org?: string; names_lang?: string;
  private?: boolean; error?: string;
}

export const getGeoStatus = () => api<GeoStatus>('/geoip/status');
export const setGeoMode = (mode: GeoMode) =>
  api<GeoStatus>('/geoip/settings', { method: 'PUT', body: JSON.stringify({ mode }) });
export const updateGeoNow = () => api('/geoip/update', { method: 'POST' });
export const deleteGeoDatabase = (kind: GeoKind) => api(`/geoip/${kind}`, { method: 'DELETE' });
export const lookupGeo = (ips: string[], lang: string) =>
  api<{ results: Record<string, GeoInfo> }>('/geoip/lookup', { method: 'POST', body: JSON.stringify({ ips, lang }) });

/** Uploads a database file (.mmdb, .mmdb.gz, .tar.gz) with progress. */
export function uploadGeoDatabase(file: File, onProgress?: (pct: number) => void): Promise<{ kind: GeoKind; type: string; build?: string }> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', `${getBasePath()}/api/geoip/upload`);
    const tok = authToken();
    if (tok) xhr.setRequestHeader('Authorization', `Bearer ${tok}`);
    xhr.setRequestHeader('Content-Type', 'application/octet-stream');
    xhr.withCredentials = true;
    xhr.upload.onprogress = (e) => { if (e.lengthComputable && onProgress) onProgress((e.loaded / e.total) * 100); };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        try { resolve(JSON.parse(xhr.responseText)); } catch { reject(new Error('bad response')); }
      } else reject(new Error(xhr.responseText.trim() || `HTTP ${xhr.status}`));
    };
    xhr.onerror = () => reject(new Error('network error'));
    xhr.send(file);
  });
}

// ---------------------------------------------------------------- audit

export interface AuditRecord {
  id: string; start: number; end: number; partial?: boolean;
  proto: 'tcp' | 'udp';
  inbound: string; user?: string; src?: string; src_node?: string;
  dst?: string; dst_host?: string; dst_ip?: string; rule?: string;
  exit?: 'direct' | 'node' | 'blocked'; exit_path?: string; exit_mode?: string;
  egress_iface?: string; egress_src?: string;
  up?: number; down?: number; result?: 'ok' | 'error' | 'refused' | 'blocked'; error?: string;
}
export interface AuditFilter {
  from?: number; to?: number; user?: string; src?: string; dst?: string;
  inbound?: string; exit?: string; proto?: string; result?: string; q?: string;
}
export interface AuditPage { enabled: boolean; records: AuditRecord[]; next_cursor?: string; scanned?: number; truncated?: boolean }
export interface AuditTop { key: string; bytes: number; flows?: number; up?: number; down?: number }
export interface AuditStats {
  flows: number; up: number; down: number;
  top_users?: AuditTop[]; top_sources?: AuditTop[]; top_destinations?: AuditTop[];
  truncated?: boolean;
}

const auditQuery = (f: AuditFilter, extra: Record<string, string | number | undefined> = {}) => {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries({ ...f, ...extra })) if (v !== undefined && v !== '') q.set(k, String(v));
  return q.toString();
};
export const getAudit = (f: AuditFilter, limit: number, cursor?: string) =>
  api<AuditPage>(`/audit?${auditQuery(f, { limit, cursor })}`, { cache: 'no-store' });
export const getAuditStats = (f: Pick<AuditFilter, 'from' | 'to'>) =>
  api<AuditStats>(`/audit/stats?${auditQuery(f)}`, { cache: 'no-store' });
/** Export download (the session cookie authorises a plain navigation). */
export function auditExportUrl(f: AuditFilter, format: 'csv' | 'jsonl'): string {
  const tok = authToken();
  return `${getBasePath()}/api/audit/export?${auditQuery(f, { format, token: tok || undefined })}`;
}
