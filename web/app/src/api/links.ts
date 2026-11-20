import { api } from './client';

/** Share links of a user, and the host links of this node carry. */

export type SubProtocol = 'hy2' | 'ss' | 'trojan' | 'socks5' | 'http';

/** One share link of a user. */
export interface ProxyLink {
  proto: SubProtocol;
  user: string;
  name: string;
  host: string;
  lan?: boolean;
  url: string;
}

export interface ShareHost {
  /** the setting, else the detected public address ('' = none) */
  host: string;
  detected: string;
  lan_addrs: string[];
}

/** fallback: the host used when the node knows no public host. */
export const getUserLinks = (id: string, fallback = '') =>
  api<ProxyLink[]>(`/users/${encodeURIComponent(id)}/links?fallback=${encodeURIComponent(fallback)}`);
export const getShareHost = () => api<ShareHost>('/share-host');

/** "host:port" with an IPv6 host bracketed. */
export function hostPort(host: string, port: string): string {
  return host.includes(':') && !host.startsWith('[') ? `[${host}]:${port}` : `${host}:${port}`;
}
