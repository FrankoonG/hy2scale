import type { ClientEntry } from '@/api';

// isRelayInternalAddr mirrors backend `relay.IsRelayStream`. These hostnames
// are sentinels that the local relay layer intercepts before any DNS or
// dial happens — they MUST NOT leak into a hy2:// share URL or sing-box /
// mihomo will try to actually resolve them and fail with NXDOMAIN.
//
// Trailing-`:0` suffixed forms (`_relay_api_:0`) are the targets-as-typed;
// after splitting host:port we only see the bare hostname, hence we match
// the prefixes too.
function isRelayInternalAddr(host: string): boolean {
  if (!host) return false;
  const h = host.replace(/^\[|\]$/g, ''); // strip IPv6 brackets if any
  return (
    h === '_relay_api_' ||
    h === '_relay_register_' ||
    h === '_relay_ctrl_s2c_' ||
    h === '_relay_listpeers_' ||
    h === '_relay_ping_' ||
    h === '_relay_latency_' ||
    h === '_relay_rebind_' ||
    h.startsWith('_relay_via_') ||
    h.startsWith('_relay_data_') ||
    h.startsWith('_relay_iptun_')
  );
}

// canExportAsHy2Url returns whether `addr` is suitable for a public share
// URL. Used by the UI to disable the export button (and surface a tooltip)
// before the user copies an unusable URL.
export function canExportAsHy2Url(addr: string | undefined): boolean {
  if (!addr) return false;
  const { host, port } = splitAddr(addr);
  if (!host) return false;
  if (isRelayInternalAddr(host)) return false;
  // 0.0.0.0 / :: as host is a bind-all wildcard; a remote client cannot
  // dial it. Catch this so self-export doesn't ship a useless URL.
  const bare = host.replace(/^\[|\]$/g, '');
  if (bare === '0.0.0.0' || bare === '::' || bare === '0:0:0:0:0:0:0:0') return false;
  // Port must be present and non-zero (`_relay_api_:0` collapses to host
  // sentinel above, but a stray `:0` on a real host is also unusable).
  if (port === '0') return false;
  return true;
}

// buildHy2Url renders a ClientEntry back into a hysteria2:// share URL,
// the inverse of utils/parseHy2Url. When the entry has multiple addresses,
// only the first is used (the URL format has no native multi-address slot,
// and a single primary endpoint is what end users typically paste).
//
// Multi-port specs (e.g. `5000-6000` or `5000,5001`) are surfaced via the
// `mport` query parameter instead of stuffing commas/dashes into the URL
// authority section, which most parsers reject.
//
// Throws when the entry's primary address is a relay-internal sentinel
// (e.g. `_relay_api_:0`) or a wildcard bind. The caller should gate the
// export button on `canExportAsHy2Url(addr)` first; the throw is a
// last-resort safety net so the sentinel can never reach a clipboard.
export function buildHy2Url(entry: Pick<ClientEntry,
  'name' | 'addr' | 'addrs' | 'password' | 'sni' | 'insecure' | 'max_tx' | 'max_rx' | 'fast_open'>): string {
  const primary = (entry.addrs && entry.addrs.length > 0) ? entry.addrs[0] : entry.addr;
  if (!canExportAsHy2Url(primary)) {
    throw new Error(`address "${primary}" is not exportable (relay-internal sentinel or wildcard bind)`);
  }
  const { host, port } = splitAddr(primary);

  // Bracket-wrap bare IPv6 so the host:port section parses unambiguously.
  const urlHost = host.includes(':') && !host.startsWith('[') ? `[${host}]` : host;
  const isMultiPort = /[,\-]/.test(port);
  const urlPort = isMultiPort ? '443' : (port || '443');

  const params = new URLSearchParams();
  if (isMultiPort) params.set('mport', port);
  if (entry.sni) params.set('sni', entry.sni);
  if (entry.insecure) params.set('insecure', '1');
  if (entry.max_tx) params.set('up', `${Math.round(entry.max_tx / 125000)}mbps`);
  if (entry.max_rx) params.set('down', `${Math.round(entry.max_rx / 125000)}mbps`);
  if (entry.fast_open) params.set('fastopen', '1');

  const auth = encodeURIComponent(entry.password || '');
  let out = `hysteria2://${auth}@${urlHost}:${urlPort}`;
  const qs = params.toString();
  if (qs) out += `/?${qs}`;
  if (entry.name) out += `#${encodeURIComponent(entry.name)}`;
  return out;
}

function splitAddr(addr: string | undefined): { host: string; port: string } {
  if (!addr) return { host: '', port: '' };
  // Bracketed IPv6 first
  if (addr.startsWith('[')) {
    const close = addr.indexOf(']');
    if (close > 0) {
      const host = addr.slice(0, close + 1);
      const rest = addr.slice(close + 1);
      const port = rest.startsWith(':') ? rest.slice(1) : '';
      return { host, port };
    }
  }
  const idx = addr.lastIndexOf(':');
  if (idx < 0) return { host: addr, port: '' };
  return { host: addr.slice(0, idx), port: addr.slice(idx + 1) };
}

// copyToClipboard tries the modern async API, falling back to the textarea
// trick for non-secure contexts (HTTP over LAN IP — common for production
// nodes accessed by IP without a TLS termination in front).
export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch { /* fall through */ }
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.left = '-9999px';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    document.body.removeChild(ta);
    return ok;
  } catch {
    return false;
  }
}
