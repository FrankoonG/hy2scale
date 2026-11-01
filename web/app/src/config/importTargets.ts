import * as api from '@/api';
import { GiB } from '@/hooks/useFormat';

/** What a list can export to / import from a spreadsheet. */
export interface ImportTarget {
  /** Sheet name and file base name. */
  sheet: string;
  headers: string[];
  /** i18n key of the import dialog title. */
  titleKey: string;
  /** Query refetched after an import. */
  queryKey: string;
  exportRows: () => Promise<Record<string, unknown>[]>;
  /**
   * Imports one row. Without `overwrite`, an existing entry is skipped
   * ('skipped'); with it, the entry is replaced. `cache` lives for one
   * import run (lookups shared by its rows).
   */
  importRow: (row: any, overwrite: boolean, cache: Map<string, unknown>) => Promise<'added' | 'skipped'>;
}

const paths = (v: unknown) => String(v || '').split(',').filter(Boolean);

/**
 * Create, or (overwrite) update falling back to create when the entry does
 * not exist yet. Without overwrite a failed create counts as skipped.
 */
async function upsert(overwrite: boolean, update: () => Promise<unknown>, create: () => Promise<unknown>): Promise<'added' | 'skipped'> {
  if (overwrite) {
    try { await update(); } catch { await create(); }
    return 'added';
  }
  try { await create(); return 'added'; } catch { return 'skipped'; }
}

export const IMPORT_TARGETS = {
  nodes: {
    sheet: 'nodes',
    headers: ['name', 'addrs', 'password', 'sni', 'insecure', 'conn_mode', 'max_tx', 'max_rx'],
    titleKey: 'import.importNodes',
    queryKey: 'topology',
    exportRows: async () => (await api.getClients()).map((c) => ({
      name: c.name, addrs: (c.addrs || [c.addr]).join(','),
      password: c.password, sni: c.sni || '', insecure: c.insecure ? 'yes' : 'no',
      conn_mode: c.conn_mode || '', max_tx: c.max_tx || 0, max_rx: c.max_rx || 0,
    })),
    importRow: (row, overwrite) => {
      const addrs = paths(row.addrs || row.addr);
      const data: any = {
        name: row.name, addr: addrs[0], addrs: addrs.length > 1 ? addrs : undefined,
        password: row.password, sni: row.sni || undefined,
        insecure: row.insecure === 'yes', conn_mode: row.conn_mode || undefined,
        max_tx: Number(row.max_tx) || undefined, max_rx: Number(row.max_rx) || undefined,
      };
      return upsert(overwrite, () => api.updateClient(data.name, data), () => api.createClient(data));
    },
  },
  users: {
    sheet: 'users',
    headers: ['username', 'password', 'exit_via', 'exit_mode', 'traffic_limit_gb', 'rate_up', 'rate_down', 'expiry_date', 'enabled'],
    titleKey: 'import.importUsers',
    queryKey: 'users',
    exportRows: async () => (await api.getUsers()).map((u) => ({
      username: u.username, password: u.password,
      exit_via: u.exit_paths?.join(',') || u.exit_via || '',
      exit_mode: u.exit_mode || '', traffic_limit_gb: u.traffic_limit ? u.traffic_limit / GiB : 0,
      rate_up: u.rate_up || 0, rate_down: u.rate_down || 0,
      expiry_date: u.expiry_date || '', enabled: u.enabled ? 'yes' : 'no',
    })),
    importRow: async (row, overwrite, cache) => {
      const p = paths(row.exit_via);
      const data = {
        username: row.username, password: row.password,
        exit_via: p[0] || '', exit_paths: p.length > 1 ? p : undefined,
        exit_mode: row.exit_mode || undefined,
        traffic_limit: (Number(row.traffic_limit_gb) || 0) * GiB,
        rate_up: Math.max(0, Math.round(Number(row.rate_up) || 0)),
        rate_down: Math.max(0, Math.round(Number(row.rate_down) || 0)),
        expiry_date: row.expiry_date || undefined,
        enabled: row.enabled !== 'no',
      };
      // Users are keyed by id: find an existing one by username.
      let users = cache.get('users') as api.UserConfig[] | undefined;
      if (overwrite && !users) { users = await api.getUsers(); cache.set('users', users); }
      const existing = users?.find((u) => u.username === data.username);
      return upsert(overwrite,
        () => (existing ? api.updateUser(existing.id, data) : Promise.reject(new Error('new'))),
        () => api.createUser(data));
    },
  },
  'wg-peers': {
    sheet: 'wg-peers',
    headers: ['name', 'public_key', 'allowed_ips', 'exit_via', 'keepalive'],
    titleKey: 'import.importWg-peers',
    queryKey: 'wireguard',
    exportRows: async () => ((await api.getWireGuard()).peers || []).map((p) => ({
      name: p.name, public_key: p.public_key,
      allowed_ips: p.allowed_ips, exit_via: p.exit_via || '',
      keepalive: p.keepalive || 0,
    })),
    importRow: (row, overwrite) => {
      const data: any = {
        name: row.name, public_key: row.public_key,
        allowed_ips: row.allowed_ips || '0.0.0.0/0',
        exit_via: row.exit_via || undefined,
        keepalive: Number(row.keepalive) || 0,
      };
      return upsert(overwrite, () => api.updateWGPeer(data.name, data), () => api.createWGPeer(data));
    },
  },
} satisfies Record<string, ImportTarget>;

export type ImportTargetKey = keyof typeof IMPORT_TARGETS;
