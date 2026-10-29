import type { WireGuardPeer } from '@/api';

// ---- peer address suggestions (IPv4 and IPv6) ----

function parseIP(s: string): { n: bigint; bits: 32 | 128 } | null {
  s = s.trim();
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(s)) {
    let n = 0n;
    for (const o of s.split('.')) {
      const v = Number(o);
      if (v > 255) return null;
      n = (n << 8n) + BigInt(v);
    }
    return { n, bits: 32 };
  }
  const halves = s.split('::');
  if (halves.length > 2 || s.includes('.')) return null;
  const head = halves[0] ? halves[0].split(':') : [];
  const tail = halves.length === 2 && halves[1] ? halves[1].split(':') : [];
  const fill = 8 - head.length - tail.length;
  if (halves.length === 1 ? head.length !== 8 : fill < 0) return null;
  let n = 0n;
  for (const g of [...head, ...Array(halves.length === 2 ? fill : 0).fill('0'), ...tail]) {
    if (!/^[0-9a-fA-F]{1,4}$/.test(g)) return null;
    n = (n << 16n) + BigInt(parseInt(g, 16));
  }
  return { n, bits: 128 };
}

function formatIP(n: bigint, bits: 32 | 128): string {
  if (bits === 32) return [24n, 16n, 8n, 0n].map((sh) => String((n >> sh) & 255n)).join('.');
  const g: string[] = [];
  for (let i = 7; i >= 0; i--) g.push(((n >> BigInt(i * 16)) & 0xffffn).toString(16));
  let best = -1, bestLen = 1;
  for (let i = 0; i < 8;) {
    if (g[i] !== '0') { i++; continue; }
    let j = i;
    while (j < 8 && g[j] === '0') j++;
    if (j - i > bestLen) { best = i; bestLen = j - i; }
    i = j;
  }
  return best < 0 ? g.join(':') : `${g.slice(0, best).join(':')}::${g.slice(best + bestLen).join(':')}`;
}

// nextPeerIP is the first host after the server's address in its subnet
// that no peer holds yet, as a host route ("/32" or "/128"); '' if none.
export function nextPeerIP(serverCIDR: string, peers: WireGuardPeer[]): string {
  const [addr, len] = serverCIDR.split('/');
  const a = parseIP(addr || '');
  if (!a) return '';
  const plen = len === undefined ? a.bits : Number(len);
  if (!Number.isInteger(plen) || plen < 0 || plen > a.bits) return '';
  const hostBits = BigInt(a.bits - plen);
  const last = ((a.n >> hostBits) << hostBits) + (1n << hostBits) - (a.bits === 32 ? 2n : 1n);
  const used = new Set<string>([a.n.toString()]);
  for (const p of peers) {
    for (const e of (p.allowed_ips || '').split(',')) {
      const ip = parseIP(e.split('/')[0]);
      if (ip && ip.bits === a.bits) used.add(ip.n.toString());
    }
  }
  for (let c = a.n + 1n, i = 0; c <= last && i < 65536; c++, i++) {
    if (!used.has(c.toString())) return `${formatIP(c, a.bits)}/${a.bits}`;
  }
  return '';
}
