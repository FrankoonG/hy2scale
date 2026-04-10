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
  disabled?: boolean;
  connected?: boolean;
}

// ===== Users =====
export interface UserConfig {
  id: string;
  username: string;
  password: string;
  proxy_passwords?: Record<string, string>;
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
  user: string;
  ip: string;
  proxy: string;
  conns: number;
  tx_bytes: number;
  rx_bytes: number;
  duration: string;
}

// ===== Proxies =====
export interface ProxyConfig {
  id: string;
  protocol: string;
  listen: string;
  enabled: boolean;
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
}

export interface TunModeConfig {
  enabled: boolean;
  mode: 'mixed' | 'full';
  status?: string;
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
}

export interface PortConflict {
  port: number;
  proto: string;
  desc: string;
}
