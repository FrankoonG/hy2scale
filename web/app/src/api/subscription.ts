import { api } from './client';
import { hostPort, type SubProtocol, type ProxyLink } from './links';

export type { SubProtocol, ProxyLink };

/** A user's subscription (read only; set with the user): their links as one list for subscription converters. */

export interface UserSub {
  enabled: boolean;
  /** the entry name template in effect */
  name: string;
  /** lists the LAN-address entries too */
  lan: boolean;
  /** "<base>/sub/<id>/<token>"; '' when off */
  path: string;
  web_port: string;
  /** the host links carry ('' = none known) */
  public_host: string;
  lan_addrs: string[];
  /** a shared user: the same address on every node holding it */
  shared: boolean;
  /** also served over plain http (Settings → Web) */
  http: boolean;
  entries: ProxyLink[];
}

const subPath = (id: string) => `/users/${encodeURIComponent(id)}/subscription`;

export const getUserSub = (id: string) => api<UserSub>(subPath(id));
/** A new address; the old one stops working. */
export const resetUserSubToken = (id: string) => api<UserSub>(`${subPath(id)}/token`, { method: 'POST' });

/** The subscription's https address on host ('' when off). */
export function subURL(s: Pick<UserSub, 'path' | 'web_port'>, host: string, http = false): string {
  if (!s.path || !host) return '';
  const dflt = http ? '80' : '443';
  const hp = s.web_port && s.web_port !== dflt
    ? hostPort(host, s.web_port)
    : (host.includes(':') && !host.startsWith('[') ? `[${host}]` : host);
  return `${http ? 'http' : 'https'}://${hp}${s.path}`;
}
