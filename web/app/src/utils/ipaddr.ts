// Address helpers for the interface forms. Pure functions, no deps.

const V4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;

export function isIPv4(s: string): boolean {
  return V4.test(s);
}

export function isIPv6(s: string): boolean {
  if (!s.includes(':') || s.includes('%') || /[^0-9a-fA-F:.]/.test(s)) return false;
  try {
    // The URL parser implements the full IPv6 grammar (::, embedded v4).
    new URL(`http://[${s}]/`);
    return true;
  } catch {
    return false;
  }
}

function v4ToInt(s: string): number {
  return s.split('.').reduce((a, o) => ((a << 8) | parseInt(o, 10)) >>> 0, 0);
}

/** Parses "a.b.c.d/nn" (nn 1..32). */
export function parseCIDR4(s: string): { ip: string; bits: number } | null {
  const m = s.trim().match(/^([^/]+)\/(\d{1,2})$/);
  if (!m || !isIPv4(m[1])) return null;
  const bits = parseInt(m[2], 10);
  if (bits < 1 || bits > 32) return null;
  return { ip: m[1], bits };
}

/** Parses "x::y/nn" (nn 1..128). */
export function parseCIDR6(s: string): { ip: string; bits: number } | null {
  const m = s.trim().match(/^([^/]+)\/(\d{1,3})$/);
  if (!m || !isIPv6(m[1])) return null;
  const bits = parseInt(m[2], 10);
  if (bits < 1 || bits > 128) return null;
  return { ip: m[1], bits };
}

/** True when ip lies inside the IPv4 prefix. */
export function inSubnet4(ip: string, prefix: { ip: string; bits: number }): boolean {
  const mask = prefix.bits === 0 ? 0 : (0xffffffff << (32 - prefix.bits)) >>> 0;
  return ((v4ToInt(ip) & mask) >>> 0) === ((v4ToInt(prefix.ip) & mask) >>> 0);
}

/** Host part of a v4 prefix is the network or broadcast address (/1–/30). */
export function isNetOrBroadcast4(p: { ip: string; bits: number }): boolean {
  if (p.bits >= 31) return false;
  const host = (v4ToInt(p.ip) & (~((0xffffffff << (32 - p.bits)) >>> 0) >>> 0)) >>> 0;
  const max = (2 ** (32 - p.bits)) - 1;
  return host === 0 || host === max;
}

export function isLinkLocal6(s: string): boolean {
  return /^fe[89ab][0-9a-f]:/i.test(s.trim());
}

const MAC = /^([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$/;

/** Valid unicast MAC (multicast bit clear, not all-zero). */
export function isUnicastMAC(s: string): boolean {
  if (!MAC.test(s)) return false;
  const first = parseInt(s.slice(0, 2), 16);
  if (first & 1) return false;
  return !/^(00:){5}00$/.test(s);
}

/** Random locally-administered unicast MAC. */
export function randomMAC(): string {
  const b = new Uint8Array(6);
  crypto.getRandomValues(b);
  b[0] = (b[0] & 0xfc) | 0x02;
  return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join(':');
}

/** Host name for DHCP option 12: labels of [A-Za-z0-9-], ≤253 chars. */
export function isHostname(s: string): boolean {
  if (s.length > 253) return false;
  return s.split('.').every((l) => /^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/.test(l));
}

/** Public-IP detection server entry: "stun:host:port" or "http(s)://...". */
export function isPublicIPServer(s: string): boolean {
  const v = s.trim();
  const stun = v.match(/^stun:(\[[0-9a-fA-F:.]+\]|[^\s:/[\]]+):(\d{1,5})$/);
  if (stun) {
    const port = parseInt(stun[2], 10);
    const host = stun[1];
    if (port < 1 || port > 65535) return false;
    return host.startsWith('[') ? isIPv6(host.slice(1, -1)) : true;
  }
  if (!/^https?:\/\/\S+$/.test(v)) return false;
  try {
    const u = new URL(v);
    return !!u.hostname;
  } catch {
    return false;
  }
}

/** A string that sorts addresses numerically: IPv4 before IPv6, prefix length ignored. */
export function ipSortKey(addr?: string): string | null {
  if (!addr) return null;
  const ip = addr.split('/')[0];
  if (isIPv4(ip)) return '4' + ip.split('.').map((o) => o.padStart(3, '0')).join('.');
  if (!isIPv6(ip)) return ip;
  const [head, tail = ''] = ip.split('::');
  const h = head ? head.split(':') : [];
  const t = tail ? tail.split(':') : [];
  const groups = ip.includes('::') ? [...h, ...Array(8 - h.length - t.length).fill('0'), ...t] : h;
  return '6' + groups.map((g) => g.padStart(4, '0')).join(':');
}

/** A rule target: IPv4/IPv6 address, CIDR, or a-b range of one family ('' passes). */
export function validateIP(val: string): boolean {
  if (!val) return true;
  const slash = val.indexOf('/');
  if (slash >= 0) {
    const ip = val.slice(0, slash), bits = val.slice(slash + 1);
    if (!/^\d{1,3}$/.test(bits)) return false;
    if (isIPv4(ip)) return Number(bits) <= 32;
    return isIPv6(ip) && Number(bits) <= 128;
  }
  const dash = val.indexOf('-');
  if (dash >= 0) {
    const a = val.slice(0, dash), b = val.slice(dash + 1);
    return (isIPv4(a) && isIPv4(b)) || (isIPv6(a) && isIPv6(b));
  }
  return isIPv4(val) || isIPv6(val);
}
