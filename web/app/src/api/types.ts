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
  ip_statuses?: IPStatus[];
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
}

// ===== Users =====
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
  traffic_limit: number;
  traffic_used: number;
  expiry_date?: string;
  enabled: boolean;
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
}

// ===== Shadowsocks =====
export interface SSConfig {
  listen: string;
  enabled: boolean;
  method: string;
  exit_via?: string;
  exit_paths?: string[];
  exit_mode?: string;
}

// ===== L2TP =====
export interface L2TPConfig {
  capable?: boolean;
  host_network?: boolean;
  listen: string;
  enabled: boolean;
  pool: string;
  psk: string;
  proxy_port?: number;
  mtu: number;
  exit_via?: string;
  exit_paths?: string[];
  exit_mode?: string;
}

// ===== IKEv2 =====
export interface IKEv2Config {
  capable?: boolean;
  host_network?: boolean;
  enabled: boolean;
  mode: 'mschapv2' | 'psk';
  pool: string;
  cert_id: string;
  psk: string;
  local_id: string;
  remote_id: string;
  psk_user_mode?: boolean;
  default_exit: string;
  default_exit_mode?: string;
  default_exit_paths?: string[];
  dns: string;
  proxy_port?: number;
  mtu: number;
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
  dns?: string;
  mtu: number;
  peers: WireGuardPeer[];
  exit_via?: string;
  exit_paths?: string[];
  exit_mode?: string;
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
}

// ===== Rules =====
export interface RoutingRule {
  id: string;
  name: string;
  type: 'ip' | 'domain';
  targets: string[];
  exit_via: string;
  exit_paths?: string[];
  exit_mode?: '' | 'quality' | 'aggregate';
  enabled: boolean;
  priority?: number;    // higher wins on CIDR overlap; default 0
  use_tun?: boolean;    // request full TUN for this rule
  tun_active?: boolean; // live: true only when actually running on TUN path
}

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
  // Nested-discovery hard limits — hot-reloadable on PUT.
  max_nested_depth: number;
  max_response_nodes: number;
  max_cache_entries: number;
  max_response_bytes: number;
  max_fetch_fan_out: number;
  // Off by default; when on, peer-relay-delivered admin requests skip the
  // local password / token check. Trust boundary becomes the relay handshake.
  relay_admin_passthrough: boolean;
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
}

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
