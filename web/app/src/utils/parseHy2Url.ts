// parseHy2Url decodes a hysteria2:// (or hy2://) URI into the fields the
// node-add form needs. The format follows the de facto standard used by
// Hysteria 2 itself and clients like sing-box / mihomo:
//
//   hysteria2://[auth@]host[:port][/]?[params]#[name]
//
// auth: URL-encoded password. Some clients embed `user:pass`; we treat the
//       whole token before `@` as the password since hy2 has no username.
// params: sni, insecure, obfs, obfs-password, pinSHA256, mport,
//         up / down / bandwidth, fastopen.
// name (#fragment): friendly node name.
//
// Returns null when the input clearly isn't a hy2 URI. Returns a populated
// object with `unsupportedNotes` for fields the local config can't honour
// (currently obfs, since `internal/app/config.go` has no Obfs field).

export interface ParsedHy2 {
  name: string;
  host: string;
  port: string;          // single port or comma/range list (mport)
  password: string;
  sni?: string;
  insecure?: boolean;
  upMbps?: number;
  downMbps?: number;
  fastOpen?: boolean;
  pinSHA256?: string;
  unsupportedNotes: string[];
}

const HY2_RE = /^hysteria2:\/\//i;
const HY2_ALIAS_RE = /^hy2:\/\//i;

export function parseHy2Url(input: string): ParsedHy2 | null {
  let raw = (input || '').trim();
  if (!raw) return null;
  if (HY2_ALIAS_RE.test(raw)) raw = 'hysteria2://' + raw.slice(6);
  if (!HY2_RE.test(raw)) return null;

  // Manually peel off auth + host:port section so passwords with literal
  // ':' or '@' (rare but legal when URL-encoded) don't confuse the URL
  // parser.
  let body = raw.slice('hysteria2://'.length);
  let fragment = '';
  let query = '';
  // Hash before query? Spec says fragment is last, but be lenient and
  // accept either order so common client output ("hy2://...#name?...")
  // doesn't fail.
  const hashIdx = body.indexOf('#');
  const queryIdx = body.indexOf('?');
  let cutoff = body.length;
  if (hashIdx >= 0 && (queryIdx < 0 || hashIdx < queryIdx)) {
    cutoff = Math.min(cutoff, hashIdx);
    fragment = body.slice(hashIdx + 1);
    if (queryIdx > hashIdx) {
      const qInFrag = fragment.indexOf('?');
      if (qInFrag >= 0) {
        query = fragment.slice(qInFrag + 1);
        fragment = fragment.slice(0, qInFrag);
      }
    }
  } else if (queryIdx >= 0) {
    cutoff = Math.min(cutoff, queryIdx);
    query = body.slice(queryIdx + 1);
    if (hashIdx > queryIdx) {
      const hInQuery = query.indexOf('#');
      if (hInQuery >= 0) {
        fragment = query.slice(hInQuery + 1);
        query = query.slice(0, hInQuery);
      }
    }
  }
  body = body.slice(0, cutoff);
  // Trim trailing slash
  if (body.endsWith('/')) body = body.slice(0, -1);

  let auth = '';
  const atIdx = body.lastIndexOf('@');
  let hostPort: string;
  if (atIdx >= 0) {
    auth = body.slice(0, atIdx);
    hostPort = body.slice(atIdx + 1);
  } else {
    hostPort = body;
  }
  if (!hostPort) return null;

  // Split host / port. Brackets-wrap IPv6.
  let host = hostPort;
  let port = '';
  if (hostPort.startsWith('[')) {
    const close = hostPort.indexOf(']');
    if (close < 0) return null;
    host = hostPort.slice(0, close + 1); // keep brackets so the form receives `[v6]`
    const rest = hostPort.slice(close + 1);
    if (rest.startsWith(':')) port = rest.slice(1);
  } else {
    const colonIdx = hostPort.lastIndexOf(':');
    if (colonIdx >= 0) {
      host = hostPort.slice(0, colonIdx);
      port = hostPort.slice(colonIdx + 1);
    }
  }
  if (!host) return null;
  // Refuse to import URLs whose host is a hy2scale-internal relay
  // sentinel (e.g. `_relay_api_` or sing-box-stripped `relay_api`).
  // These can only originate from a buggy share-URL builder and would
  // poison ClientEntry.addr if accepted, propagating the leak further.
  if (isRelayInternalHost(host)) return null;

  const params = new URLSearchParams(query);
  const get = (k: string) => params.get(k) ?? undefined;

  const insecureRaw = get('insecure');
  const insecure = insecureRaw === '1' || insecureRaw === 'true';
  const fastOpenRaw = get('fastopen');
  const fastOpen = fastOpenRaw === '1' || fastOpenRaw === 'true' || undefined;

  // mport overrides the bare port — it's a comma/range list spec like
  // `5000-6000` or `5000,5001,5002`. The form's port field already
  // accepts that exact spec, so we forward it verbatim.
  const mport = get('mport');
  if (mport) port = mport;
  if (!port) port = '443';

  const upRaw = get('up') ?? get('bandwidth');
  const downRaw = get('down');

  const unsupportedNotes: string[] = [];
  if (get('obfs')) unsupportedNotes.push('obfs');
  if (get('obfs-password')) unsupportedNotes.push('obfs-password');

  return {
    name: fragment ? safeDecode(fragment) : '',
    host,
    port,
    password: safeDecode(auth),
    sni: get('sni'),
    insecure: insecure || undefined,
    upMbps: parseBwMbps(upRaw),
    downMbps: parseBwMbps(downRaw),
    fastOpen,
    pinSHA256: get('pinSHA256'),
    unsupportedNotes,
  };
}

function safeDecode(s: string): string {
  try { return decodeURIComponent(s); } catch { return s; }
}

// isRelayInternalHost matches the same set as buildHy2Url's guard, plus
// the underscore-stripped variants that some clients (sing-box, mihomo)
// produce when their URL parser drops `_` from hostnames per RFC.
function isRelayInternalHost(host: string): boolean {
  const h = host.replace(/^\[|\]$/g, '');
  if (
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
  ) return true;
  // Underscore-stripped (sing-box / mihomo path):
  if (
    h === 'relay_api' || h === 'relayapi' ||
    h === 'relay_register' || h === 'relayregister' ||
    h === 'relay_ctrl_s2c' || h === 'relayctrls2c' ||
    h === 'relay_listpeers' || h === 'relaylistpeers' ||
    h === 'relay_ping' || h === 'relayping' ||
    h === 'relay_latency' || h === 'relaylatency' ||
    h === 'relay_rebind' || h === 'relayrebind' ||
    h.startsWith('relay_via_') || h.startsWith('relayvia') ||
    h.startsWith('relay_data_') || h.startsWith('relaydata') ||
    h.startsWith('relay_iptun_') || h.startsWith('relayiptun')
  ) return true;
  return false;
}

// parseBwMbps converts a Hysteria-style bandwidth literal to Mbps. Accepts
// "100mbps", "200 Mbps", "1gbps", "500kbps", or a bare number (assumed Mbps).
function parseBwMbps(s: string | null | undefined): number | undefined {
  if (!s) return undefined;
  const m = s.trim().match(/^([\d.]+)\s*([a-zA-Z/]*)$/);
  if (!m) return undefined;
  const v = parseFloat(m[1]);
  if (!isFinite(v) || v <= 0) return undefined;
  const unit = (m[2] || 'mbps').toLowerCase();
  if (unit.startsWith('g')) return v * 1000;
  if (unit.startsWith('k')) return v / 1000;
  // mbps, mb/s, m, or empty
  return v;
}
