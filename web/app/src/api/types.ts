// ===== Node & Topology =====
export interface NodeConfig {
  node_id: string;
  name: string;
  exit_node: boolean;
  server: ServerConfig | null;
  version: string;
  limited: boolean;
  compat: boolean;
  hy2_user_auth: boolean;
  active_paths: Record<string, string>;
  lossless_min_version?: string;
}

export interface ServerConfig {
  listen: string;
  password: string;
  tls_cert: string;
  tls_key: string;
}

export interface Stats {
  tx_bytes: number;
  rx_bytes: number;
  tx_rate: number;
  rx_rate: number;
  conns: number;
  exit_clients: number;
}

export interface TopologyNode {
  name: string;
  addr: string;
  addrs?: string[];
  // QUIC connections to this node from its parent in the tree (one per
  // address); absent = one.
  conns?: number;
  ip_statuses?: IPStatus[];
  // who set up the live hole-punched connection(s) to this direct peer
  punched?: string[];
  // reached through node access by user (traffic only: no management)
  restricted?: boolean;
  // attached by node access by user (not registered there): gateway entry
  invisible?: boolean;
  exit_node: boolean;
  direction: 'inbound' | 'outbound' | 'local' | '';
  connected: boolean;
  disabled: boolean;
  nested: boolean;
  native: boolean;
  version?: string;
  compat?: boolean;
  incompatible?: boolean;
  conflict?: boolean;
  unsupported?: boolean;
  // Configured bandwidth ceiling (bytes/sec) for the edge between
  // this node and its parent. 0 / undefined → unknown; the graph
  // falls back to observed-peak thickness.
  max_rate?: number;
  latency_ms: number;
  tx_rate: number;
  rx_rate: number;
  via?: string;
  is_self: boolean;
  children?: TopologyNode[];
  // Selected direct-egress of this node as it reports it. undefined / null =
  // unknown (older node); a family set to null = not provided.
  egress?: NodeEgress | null;
  // egress found by this node through the peer (it does not report one)
  egress_probed?: boolean;
}

export interface NodeEgressFamily { ip: string; ok: boolean }
export interface NodeEgress { ipv4: NodeEgressFamily | null; ipv6: NodeEgressFamily | null }

// Whether a configured exit path can be dialed now (GET /paths/status). The
// backend applies the same rules as routing; the UI only renders it.
export interface PathStatus {
  path: string;
  ok: boolean;
  hop: number; // index of the hop that stops the path (segments as written)
  reason?: 'loop' | 'nested' | 'disabled' | 'offline';
}

export interface IPStatus {
  addr: string;
  status: string;
  latency_ms?: number;
}

// ===== Clients (Peers) =====
export interface ClientEntry {
  name: string;
  addr: string;
  addrs?: string[];
  password: string;
  /** 'user': node access by user (password = a user's hy2 password there) */
  access?: 'user';
  sni?: string;
  insecure?: boolean;
  ca?: string;
  max_tx?: number;
  max_rx?: number;
  init_stream_window?: number;
  max_stream_window?: number;
  init_conn_window?: number;
  max_conn_window?: number;
  conn_mode?: '' | 'quality' | 'aggregate';
  fast_open?: boolean;
  bbr_profile?: '' | 'standard' | 'conservative' | 'aggressive';
  disabled?: boolean;
  connected?: boolean;
  punch?: PunchConfig;
  advanced?: PeerAdvanced;
}

// Hole punching: signalling paths (fallback order, each ending at the node),
// the target's reflectors, and local bindings (each its own route).
export interface PunchConfig {
  signal: string[];
  reflectors: string[];
  binds?: string[];
  // address family used end to end; absent = IPv4
  family?: 'ipv4' | 'ipv6';
}

export interface PeerAdvanced {
  punch_retry_min?: number;
  punch_retry_max?: number;
  probe_timeout?: number;
  punch_window?: number;
}

export interface PunchStatus {
  addr: string;
  state: 'punching' | 'connected' | 'failed' | 'waiting';
  reason?: string;
  mapping?: string;
  since: number;
  retries?: number;
}

// ===== Users =====
export type TrafficReset = '' | 'daily' | 'weekly' | 'monthly';

export interface UserConfig {
  id: string;
  username: string;
  password: string;
  proxy_passwords?: Record<string, string>;
  // Proxy keys this user is forbidden to authenticate on. Empty/undefined =
  // allowed everywhere. Backend short-circuits each protocol's auth path
  // when the proxy key appears in this list (hot-effective).
  proxy_disabled?: string[];
  exit_via: string;
  exit_paths?: string[];
  exit_mode?: '' | 'quality' | 'aggregate';
  /** Policy set id; non-empty = the policy set is chosen instead of the exit group. */
  exit_policy?: string;
  traffic_limit: number;
  traffic_used: number;
  /** Rate limits in bytes per second (from / to the user); 0 = unlimited. */
  rate_up?: number;
  rate_down?: number;
  /** Automatic reset of traffic_used at 00:00 node time; '' = never. */
  traffic_reset?: TrafficReset;
  /** weekly: ISO weekday 1 (Mon)..7 (Sun); monthly: 1..31 (clamped to the month). */
  traffic_reset_day?: number;
  /** Last reset (RFC 3339, server-managed). */
  traffic_reset_at?: string;
  /** Next automatic reset (RFC 3339, read-only). */
  traffic_reset_next?: string;
  expiry_date?: string;
  enabled: boolean;
  /** may use this node's visible nodes as exits (node access by user) */
  node_access?: boolean;
  /** 'restricted' (default) | 'full' */
  node_access_mode?: 'restricted' | 'full';
  /** flows may not reach internal destinations */
  deny_internal?: boolean;
}

/** Password conflict info: username → proxy → list of conflicting usernames */
export type PasswordConflicts = Record<string, Record<string, string[]>>;

// ===== Sessions =====
export interface Session {
  key: string;
  username: string;
  remote_ip: string;
  protocol: string;
  connect_at: number;
  conn_count: number;
  tx_bytes: number;
  rx_bytes: number;
  /** Elapsed seconds since the session began. */
  duration: number;
}

// ===== Proxies =====
export interface ProxyConfig {
  id: string;
  protocol: string;
  listen: string;
  enabled: boolean;
  tls_cert?: string;
  exit_via?: string;
  exit_paths?: string[];
  exit_mode?: '' | 'quality' | 'aggregate';
  /** Policy set id; non-empty = the policy set is chosen instead of the exit group. */
  exit_policy?: string;
}

// ===== Shadowsocks =====
export interface SSConfig {
  listen: string;
  enabled: boolean;
  method: string;
  exit_via?: string;
  exit_paths?: string[];
  exit_mode?: string;
  /** Policy set id; non-empty = the policy set is chosen instead of the exit group. */
  exit_policy?: string;
}

// ===== Trojan =====
export interface TrojanConfig {
  listen: string;
  enabled: boolean;
  /** TLS certificate id; '' = the hy2 server's certificate. */
  cert_id: string;
  /** host:port for connections that fail authentication; '' = close them. */
  fallback: string;
}

// ===== L2TP =====
export interface L2TPConfig {
  capable?: boolean;
  host_network?: boolean;
  running?: boolean;
  connected?: number;
  listen: string;
  enabled: boolean;
  pool: string;
  psk: string;
  proxy_port?: number;
  mtu: number;
  exit_via?: string;
  exit_paths?: string[];
  exit_mode?: string;
  /** Policy set id; non-empty = the policy set is chosen instead of the exit group. */
  exit_policy?: string;
}

// ===== IKEv2 =====
export interface IKEv2Config {
  capable?: boolean;
  host_network?: boolean;
  running?: boolean;
  connected?: number;
  enabled: boolean;
  mode: 'mschapv2' | 'psk';
  pool: string;
  pool6?: string;
  cert_id: string;
  psk: string;
  local_id: string;
  remote_id: string;
  psk_user_mode?: boolean;
  default_exit: string;
  default_exit_mode?: string;
  default_exit_policy?: string;
  default_exit_paths?: string[];
  dns: string;
  proxy_port?: number;
  mtu: number;
}

// ===== OpenVPN =====
export interface OpenVPNConfig {
  running?: boolean;
  connected?: number;
  enabled: boolean;
  port: number;
  /** transport: udp (default), tcp, or both on one port */
  proto?: 'udp' | 'tcp' | 'both';
  pool: string;
  pool6?: string;
  cert_id: string;
  mtu: number;
  listen_ip?: string;
  dns?: string[];
  /** Control channel protection. */
  control?: '' | 'tls-crypt' | 'tls-auth';
  auth?: '' | 'SHA1' | 'SHA256';
  shape?: boolean;
  /** A static key exists (it is never returned). */
  has_key?: boolean;
  full_tunnel?: boolean;
  tunnel_ipv6?: boolean;
  routes?: string[];
  profile_extra?: string;
  /** Write only: replace the static key. */
  rotate_key?: boolean;
}

// ===== WireGuard =====
export interface WireGuardConfig {
  running?: boolean;
  connected?: number;
  enabled: boolean;
  listen_port: number;
  private_key: string;
  public_key?: string;
  address: string;
  address6?: string;
  dns?: string;
  mtu: number;
  peers: WireGuardPeer[];
  exit_via?: string;
  exit_paths?: string[];
  exit_mode?: string;
  /** Policy set id; non-empty = the policy set is chosen instead of the exit group. */
  exit_policy?: string;
}

export interface WireGuardPeer {
  name: string;
  public_key: string;
  private_key: string;
  allowed_ips: string;
  keepalive: number;
  exit_via?: string;
  exit_paths?: string[];
  exit_mode?: string;
  /** Policy set id; non-empty = the policy set is chosen instead of the exit group. */
  exit_policy?: string;
}

// ===== Rules =====

// ===== TLS =====
export interface CertInfo {
  id: string;
  name: string;
  subject: string;
  issuer: string;
  not_after: string;
  is_ca?: boolean;
  key_file?: string;
  cert_file?: string;
}

// ===== Settings =====
export interface UISettings {
  listen: string;
  base_path: string;
  dns: string;
  force_https: boolean;
  https_cert_id: string;
  session_timeout_h: number;
  path_loss_grace_s?: number;
  // Nested-discovery hard limits — hot-reloadable on PUT.
  max_nested_depth: number;
  max_response_nodes: number;
  max_cache_entries: number;
  max_response_bytes: number;
  max_fetch_fan_out: number;
  // Off by default; when on, peer-relay-delivered admin requests skip the
  // local password / token check. Trust boundary becomes the relay handshake.
  relay_admin_passthrough: boolean;
  accept_punch?: boolean;
  // DNS resolver (relay-routed) — reroutes hy2scale's own internal name
  // lookups (rules-engine domain rules etc.) through a clean upstream
  // DNS reachable via the rule's own exit, so a polluted local
  // resolv.conf does not contaminate which IPs land in iptables.
  // Disabled by default; when off, hy2scale uses net.LookupHost (host's
  // resolv.conf). No "DNS exit pin" — each rule's DNS rides its own
  // exit. Wire path is just TCP/53 forwarded by the relay, so it works
  // against any hy2-compatible peer (vanilla hy2 server included).
  dns_resolver_enabled: boolean;
  // Upstream resolver list reuses the top-level `dns` field above —
  // no separate knob.
  dns_resolver_cache_ttl: number;         // seconds, default 300
  dns_resolver_negative_ttl: number;      // seconds, default 30
  dns_resolver_cache_size: number;        // entries, default 1024
  dns_resolver_query_timeout_ms: number;  // ms, default 3000
  public_ip?: PublicIPSettings;
}

// Public-IP detection (GET/PUT /settings/ui). Empty servers = built-in list.
export interface PublicIPSettings {
  method: 'public' | 'peer+public';
  interval: number; // seconds
  servers: string[];
}

// ===== Interfaces (GET /interfaces) =====
export type IfaceKind = 'physical' | 'system' | 'child';
export type IfaceFamilyKey = 'ipv4' | 'ipv6';
export interface IfaceAddr {
  prefix: string;
  source: 'dhcp' | 'static' | 'slaac' | 'system' | string;
  expires: number; // unix seconds; 0 = no expiry
}
export interface IfaceFamily {
  mode: 'off' | 'dhcp' | 'static' | 'slaac' | 'system' | string;
  state: 'off' | 'acquiring' | 'ok' | 'failed' | string;
  error: string;
  addrs: IfaceAddr[];
  gateway: string;
  dns: string[];
  hostname?: string;
  client_id?: string;
}
export interface IfacePublic {
  ip: string;
  ok: boolean;
  error: string;
  checked: number;
  direct: boolean;
}
export interface IfaceUsable {
  ok: boolean;
  reason: '' | 'family_off' | 'no_address' | 'no_route' | 'down' | string;
}
export interface IfaceEntry {
  name: string;
  kind: IfaceKind;
  parent: string;
  container?: boolean;
  /** Link type: ethernet, wlan, bridge, veth, vlan, tun, … (system interfaces). */
  type?: string;
  /** UniFi OS: the interface's role in the gateway's own network. */
  role?: 'wan' | 'lan';
  up: boolean;
  mac: string;
  mtu: number;
  wireless: boolean;
  can_have_children: boolean;
  // child only
  mode?: 'builtin' | 'system';
  enabled?: boolean;
  vlan?: { enabled: boolean; id: number };
  error?: string;
  ipv4: IfaceFamily;
  ipv6: IfaceFamily;
  public: { ipv4: IfacePublic | null; ipv6: IfacePublic | null };
  egress_usable: { ipv4: IfaceUsable; ipv6: IfaceUsable };
  rx_bytes: number;
  tx_bytes: number;
}
export interface Egress { ipv4: string; ipv6: string }
export interface InterfacesResponse {
  capability: { children: boolean; reason: string };
  egress: Egress;
  interfaces: IfaceEntry[];
}
// Body of POST /interfaces and PUT /interfaces/{name}.
export interface ChildIfaceBody {
  name: string;
  parent: string;
  mode: 'builtin' | 'system';
  mac: string;
  vlan: { enabled: boolean; id: number };
  mtu: number;
  enabled: boolean;
  ipv4: { mode: 'off' | 'dhcp' | 'static'; addrs: string[]; gateway: string; dns: string[]; hostname: string; client_id: string };
  ipv6: { mode: 'off' | 'slaac' | 'static'; addrs: string[]; gateway: string; dns: string[] };
}
export type IfaceReference =
  | { type: 'egress'; family: IfaceFamilyKey }
  | { type: 'node_address'; node: string; address: string }
  | { type: string; [k: string]: any };

// ===== Online Update =====
// Mirrors the upgradeJobSnapshot served by /api/upgrade/{status,events}.
// Singleton on the server, so two browser sessions on the same node see
// identical progress without triggering duplicate downloads.
export interface UpgradeStatus {
  state: 'idle' | 'checking' | 'downloading' | 'ready' | 'applying' | 'error';
  current: string;
  latest: string;
  asset: string;
  download_url: string;
  progress: number; // 0–100
  bytes_done: number;
  bytes_total: number;
  error?: string;
}

export interface UpgradeCheckResult {
  current: string;
  latest: string;
  asset: string;
  download_url: string;
  size: number;
  update_available: boolean;
}

export interface PortConflict {
  port: number;
  proto: string;
  desc: string;
}
