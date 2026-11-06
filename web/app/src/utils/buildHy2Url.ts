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

// Moved to utils/clipboard; re-exported for existing importers.
export { copyToClipboard } from './clipboard';
