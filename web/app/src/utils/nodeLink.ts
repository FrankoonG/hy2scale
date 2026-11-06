import { parseHy2Url, type ParsedHy2 } from './parseHy2Url';

/**
 * Node links. A hy2scale link carries what another hy2scale node needs to
 * connect to this one: every public address, the server password, TLS
 * options, and — when this node accepts punched connections — the nodes it
 * is connected to, through which an importer can signal a punch:
 *
 *   hy2scale://<base64url(JSON)>#<name>
 *
 * Imports also take hysteria2:// / hy2:// links (single address, no punch).
 */

export interface ScaleLink {
  v: 1;
  name: string;
  addrs: string[];
  password: string;
  sni?: string;
  insecure?: boolean;
  /** Nodes this one is connected to and that may carry punch signalling. */
  punch?: { via: string[] };
}

export type ParsedLink =
  | { kind: 'hy2scale'; link: ScaleLink }
  | { kind: 'hy2'; hy2: ParsedHy2 };

const PREFIX = 'hy2scale://';

const b64url = (s: string) => {
  const bytes = new TextEncoder().encode(s);
  let bin = '';
  bytes.forEach((b) => { bin += String.fromCharCode(b); });
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
};
const unb64url = (s: string) => {
  const b = s.replace(/-/g, '+').replace(/_/g, '/');
  const bin = atob(b + '='.repeat((4 - (b.length % 4)) % 4));
  return new TextDecoder().decode(Uint8Array.from(bin, (c) => c.charCodeAt(0)));
};

export function buildScaleLink(l: Omit<ScaleLink, 'v'>): string {
  const body: ScaleLink = { v: 1, name: l.name, addrs: l.addrs, password: l.password };
  if (l.sni) body.sni = l.sni;
  if (l.insecure) body.insecure = true;
  if (l.punch?.via.length) body.punch = { via: l.punch.via };
  return `${PREFIX}${b64url(JSON.stringify(body))}#${encodeURIComponent(l.name)}`;
}

function parseScaleLink(raw: string): ScaleLink | null {
  const body = raw.slice(PREFIX.length).split('#')[0].trim();
  if (!body) return null;
  let o: any;
  try { o = JSON.parse(unb64url(body)); } catch { return null; }
  if (!o || typeof o !== 'object' || o.v !== 1 || typeof o.name !== 'string' || !o.name.trim()) return null;
  const strs = (v: unknown) => (Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string' && x.trim() !== '').map((x) => x.trim()) : []);
  const link: ScaleLink = { v: 1, name: o.name.trim(), addrs: strs(o.addrs), password: typeof o.password === 'string' ? o.password : '' };
  if (typeof o.sni === 'string' && o.sni) link.sni = o.sni;
  if (o.insecure === true) link.insecure = true;
  const via = strs(o.punch?.via);
  if (via.length) link.punch = { via };
  if (!link.addrs.length && !link.punch) return null;
  return link;
}

/** A pasted link of either kind; null when it is neither. */
export function parseNodeLink(input: string): ParsedLink | null {
  const raw = (input || '').trim();
  if (!raw) return null;
  if (raw.toLowerCase().startsWith(PREFIX)) {
    const link = parseScaleLink(raw);
    return link ? { kind: 'hy2scale', link } : null;
  }
  const hy2 = parseHy2Url(raw);
  return hy2 ? { kind: 'hy2', hy2 } : null;
}

/** "host:port" with an IPv6 host bracketed. */
export function joinAddr(host: string, port: string): string {
  const h = host.replace(/^\[|\]$/g, '');
  return h.includes(':') ? `[${h}]:${port}` : `${h}:${port}`;
}

/**
 * Signalling paths from this node to `target` for a punch: through each of
 * the target's `via` nodes this node can reach (shortest first), and
 * directly when the target is reachable already. Paths are as a node
 * entry stores them (without this node's id).
 */
export function punchSignalPaths(target: string, via: string[], exitPaths: string[], selfId?: string, max = 4): string[] {
  const strip = (p: string) => (selfId && p.startsWith(selfId + '/') ? p.slice(selfId.length + 1) : p);
  const out: string[] = [];
  const byLen = exitPaths.map(strip).filter(Boolean).sort((a, b) => a.split('/').length - b.split('/').length);
  for (const p of byLen) {
    const hops = p.split('/');
    const last = hops[hops.length - 1];
    let sig = '';
    if (last === target && hops.length > 1) sig = p;
    else if (via.includes(last) && !hops.includes(target)) sig = `${p}/${target}`;
    if (sig && !out.includes(sig)) out.push(sig);
    if (out.length >= max) break;
  }
  return out;
}
