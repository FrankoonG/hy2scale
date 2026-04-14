import type { SSConfig, ProxyConfig } from '@/api';

/**
 * Proxy registry — single source of truth for all proxy types.
 *
 * To add a new proxy:
 *  1. Add an entry here
 *  2. Add i18n keys if the label needs translation
 *  3. Backend: add the proxy key to the conflict detection list in app.go rebuildUserIndex()
 *
 * That's it — UserDetailModal (links), UserModal (proxy password tab),
 * and conflict detection all derive from this registry automatically.
 */

export interface ProxyContext {
  serverPort: string;
  ssConfig?: SSConfig | null;
  proxies: ProxyConfig[];
}

export interface ProxyUrlParams {
  host: string;
  port: string;
  username: string;
  password: string;
}

export interface ProxyRegistryEntry {
  /** Config key — used in proxy_passwords map and backend conflict detection. */
  key: string;
  /** Display label. */
  label: string;
  /**
   * Auth type:
   * - 'password': password-only (supports per-proxy password override, needs conflict detection)
   * - 'username_password': identified by username (no per-proxy password needed)
   */
  authType: 'password' | 'username_password';
  /**
   * Category:
   * - 'l7': application-layer proxy (hy2, ss, socks5) — shown in user detail modal
   * - 'vpn': tunnel/VPN (l2tp, ikev2) — NOT shown in user detail, has own config page
   */
  category: 'l7' | 'vpn';
  /** Default port if config is unavailable. */
  defaultPort: string;
  /** Resolve the actual port from config. */
  getPort: (ctx: ProxyContext) => string;
  /** Build the connection URL. */
  buildUrl: (params: ProxyUrlParams, ctx: ProxyContext) => string;
}

export const PROXY_REGISTRY: ProxyRegistryEntry[] = [
  {
    key: 'hy2',
    label: 'Hysteria2',
    authType: 'password',
    category: 'l7',
    defaultPort: '5565',
    getPort: (ctx) => ctx.serverPort || '5565',
    buildUrl: ({ host, port, password, username }) =>
      `hysteria2://${encodeURIComponent(password)}@${host}:${port}/?insecure=1#${encodeURIComponent(username)}`,
  },
  {
    key: 'ss',
    label: 'Shadowsocks',
    authType: 'password',
    category: 'l7',
    defaultPort: '8388',
    getPort: (ctx) => ctx.ssConfig?.listen?.replace(/.*:/, '') || '8388',
    buildUrl: ({ host, port, password, username }, ctx) => {
      const method = ctx.ssConfig?.method || 'aes-256-gcm';
      return `ss://${btoa(`${method}:${password}`)}@${host}:${port}#${encodeURIComponent(username)}`;
    },
  },
  {
    key: 'socks5',
    label: 'SOCKS5',
    authType: 'username_password',
    category: 'l7',
    defaultPort: '1080',
    getPort: (ctx) => {
      const sk5 = ctx.proxies.find((p) => p.protocol === 'socks5' && p.listen);
      return sk5?.listen?.replace(/.*:/, '') || '1080';
    },
    buildUrl: ({ host, port, username, password }) =>
      `socks5://${encodeURIComponent(username)}:${encodeURIComponent(password)}@${host}:${port}`,
  },
  {
    key: 'http',
    label: 'HTTP',
    authType: 'username_password',
    category: 'l7',
    defaultPort: '8080',
    getPort: (ctx) => {
      const hp = ctx.proxies.find((p) => p.protocol === 'http' && p.listen);
      return hp?.listen?.replace(/.*:/, '') || '8080';
    },
    buildUrl: ({ host, port, username, password }, ctx) => {
      const hp = ctx.proxies.find((p) => p.protocol === 'http' && p.listen);
      const scheme = hp?.tls_cert ? 'https' : 'http';
      return `${scheme}://${encodeURIComponent(username)}:${encodeURIComponent(password)}@${host}:${port}`;
    },
  },
];

/** Proxy types that support per-proxy password overrides (password-only auth). */
export const PASSWORD_ONLY_PROXIES = PROXY_REGISTRY.filter((p) => p.authType === 'password');

/** L7 proxy types shown in user detail modal (excludes VPN types like l2tp/ikev2). */
export const DETAIL_PROXIES = PROXY_REGISTRY.filter((p) => p.category === 'l7');
