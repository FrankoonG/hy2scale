import { api } from './client';
import type {
  NodeConfig, Stats, TopologyNode, ClientEntry, PathStatus,
  UserConfig, TrafficReset, Session, ProxyConfig, SSConfig, TrojanConfig, L2TPConfig,
  IKEv2Config, OpenVPNConfig, WireGuardConfig, WireGuardPeer,
  CertInfo, UISettings, PortConflict, PasswordConflicts,
  UpgradeStatus, UpgradeCheckResult, NodeEgress, NodeEgressFamily,
  PublicIPSettings,
} from './types';

export type { NodeConfig, Stats, TopologyNode, ClientEntry, PathStatus, UserConfig, TrafficReset, Session, ProxyConfig, SSConfig, TrojanConfig, L2TPConfig, IKEv2Config, OpenVPNConfig, WireGuardConfig, WireGuardPeer, CertInfo, UISettings, PortConflict, PasswordConflicts, UpgradeStatus, UpgradeCheckResult, NodeEgress, NodeEgressFamily };

// Auth
export const login = (username: string, password: string) =>
  api<{ token: string; force_password_change?: boolean }>('/login', { method: 'POST', body: JSON.stringify({ username, password }) });
export const getMe = () => api<{ username: string }>('/me');

// Node
export const getNode = () => api<NodeConfig>('/node');
export const updateNode = (data: Partial<NodeConfig>) =>
  api('/node', { method: 'PUT', body: JSON.stringify(data) });

// Stats
export const getStats = () => api<Stats>('/stats');

// Topology
export const getTopology = () => api<TopologyNode[]>('/topology');
export const getPathStatus = () => api<PathStatus[]>('/paths/status');

// Build info — project licence + full compiled-in dependency list.
// Rendered on the Settings → Upgrade page so users can audit what's
// running and under which licence.
export interface BuildInfoDep { path: string; version: string }
export interface BuildInfoNative { name: string; version: string; license: string; source: string }
/** Whether a binary is an official release build. */
export interface BuildStatus { status: 'official' | 'unsigned' | 'invalid'; reason?: string; manifest?: { version: string; platform: string; commit: string; tag: string } }
export interface BuildInfo {
  /** This node's own binary. */
  build?: BuildStatus;
  version: string;
  license: string;
  repository: string;
  go_deps: BuildInfoDep[];
  natives: BuildInfoNative[];
  /** Upstream projects this build is built on. */
  credits?: BuildInfoNative[];
  go_version?: string;
  /** os/arch */
  platform?: string;
  commit?: string;
  commit_time?: string;
  modified?: boolean;
}
export const getBuildInfo = () => api<BuildInfo>('/build-info');

// Topology canvases — user-authored layouts of the Nodes graph (node
// positions, edge bends, label positions). Built-in automatic layouts are
// computed in the browser; only custom canvases are stored on the server,
// shared by every session and pushed live over SSE.
export interface GraphWaypoint { u: number; v: number }
export interface GraphCanvasEdge { waypoints?: GraphWaypoint[]; label_t?: number; wrap?: number }
export interface GraphCanvas {
  id: string;
  name: string;
  nodes: Record<string, { x: number; y: number }> | null;
  edges: Record<string, GraphCanvasEdge> | null;
}
export const getGraphCanvases = () => api<{ canvases: GraphCanvas[] }>('/graph-canvases');
export const putGraphCanvas = (c: GraphCanvas) =>
  api(`/graph-canvases/${encodeURIComponent(c.id)}`, {
    method: 'PUT',
    body: JSON.stringify({ name: c.name, nodes: c.nodes || {}, edges: c.edges || {} }),
  });
export const deleteGraphCanvas = (id: string) =>
  api(`/graph-canvases/${encodeURIComponent(id)}`, { method: 'DELETE' });

// Clients (Peers)
export const getClients = () => api<ClientEntry[]>('/clients');
export const createClient = (data: ClientEntry) =>
  api('/clients', { method: 'POST', body: JSON.stringify(data) });
export const updateClient = (name: string, data: ClientEntry) =>
  api(`/clients/${encodeURIComponent(name)}`, { method: 'PUT', body: JSON.stringify(data) });
export const deleteClient = (name: string) =>
  api(`/clients/${encodeURIComponent(name)}`, { method: 'DELETE' });
export const disableClient = (name: string, disabled: boolean) =>
  api(`/clients/${encodeURIComponent(name)}/disable`, { method: 'PUT', body: JSON.stringify({ disabled }) });

// Nested
export const setNested = (name: string, enabled: boolean) =>
  api(`/peers/${encodeURIComponent(name)}/nested`, { method: 'PUT', body: JSON.stringify({ enabled }) });
export const setPeerDisabled = (name: string, disabled: boolean) =>
  api(`/peers/${encodeURIComponent(name)}/disable`, { method: 'PUT', body: JSON.stringify({ disabled }) });

// Users
export const getUsers = () => api<UserConfig[]>('/users');
export const getUserConflicts = () => api<PasswordConflicts>('/users/conflicts');
export const createUser = (data: Partial<UserConfig>) =>
  api('/users', { method: 'POST', body: JSON.stringify(data) });
export const updateUser = (id: string, data: Partial<UserConfig>) =>
  api(`/users/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(data) });
export const deleteUser = (id: string) =>
  api(`/users/${encodeURIComponent(id)}`, { method: 'DELETE' });
export const toggleUser = (id: string, enabled: boolean) =>
  api(`/users/${encodeURIComponent(id)}/toggle`, { method: 'PUT', body: JSON.stringify({ enabled }) });
export const resetTraffic = (id: string) =>
  api(`/users/${encodeURIComponent(id)}/reset-traffic`, { method: 'PUT' });

// Sessions
export const getSessions = async (): Promise<Session[]> => {
  const res = await api<{ devices: Session[]; total: number }>('/sessions');
  return res.devices || [];
};
export const kickSession = (key: string) =>
  api(`/sessions/${encodeURIComponent(key)}`, { method: 'DELETE' });

// Proxies
export const getProxies = () => api<ProxyConfig[]>('/proxies');
export const createProxy = (data: Partial<ProxyConfig>) =>
  api('/proxies', { method: 'POST', body: JSON.stringify(data) });
export const updateProxy = (id: string, data: Partial<ProxyConfig>) =>
  api(`/proxies/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(data) });
export const deleteProxy = (id: string) =>
  api(`/proxies/${encodeURIComponent(id)}`, { method: 'DELETE' });

// Shadowsocks
export const getSS = () => api<SSConfig>('/ss');
export const updateSS = (data: Partial<SSConfig>) =>
  api('/ss', { method: 'PUT', body: JSON.stringify(data) });

// Trojan
export const getTrojan = () => api<TrojanConfig>('/trojan');
export const updateTrojan = (data: Partial<TrojanConfig>) =>
  api('/trojan', { method: 'PUT', body: JSON.stringify(data) });

// L2TP
export const getL2TP = () => api<L2TPConfig>('/l2tp');
export const updateL2TP = (data: Partial<L2TPConfig>) =>
  api('/l2tp', { method: 'PUT', body: JSON.stringify(data) });

// IKEv2
export const getIKEv2 = () => api<IKEv2Config>('/ikev2');
export const updateIKEv2 = (data: Partial<IKEv2Config>) =>
  api('/ikev2', { method: 'PUT', body: JSON.stringify(data) });

// OpenVPN
export const getOpenVPN = () => api<OpenVPNConfig>('/openvpn');
export const updateOpenVPN = (data: Partial<OpenVPNConfig>) =>
  api('/openvpn', { method: 'PUT', body: JSON.stringify(data) });
export const downloadOpenVPNProfile = (endpoint: string) =>
  api<Response>(`/openvpn/profile?endpoint=${encodeURIComponent(endpoint)}`);

// WireGuard
export const getWireGuard = () => api<WireGuardConfig>('/wireguard');
export const updateWireGuard = (data: Partial<WireGuardConfig>) =>
  api('/wireguard', { method: 'PUT', body: JSON.stringify(data) });
export const generateWGKey = () =>
  api<{ private_key: string; public_key: string }>('/wireguard/generate-key', { method: 'POST' });
export const createWGPeer = (data: WireGuardPeer) =>
  api('/wireguard/peers', { method: 'POST', body: JSON.stringify(data) });
export const updateWGPeer = (name: string, data: WireGuardPeer) =>
  api(`/wireguard/peers/${encodeURIComponent(name)}`, { method: 'PUT', body: JSON.stringify(data) });
export const deleteWGPeer = (name: string) =>
  api(`/wireguard/peers/${encodeURIComponent(name)}`, { method: 'DELETE' });
export const getWGPeerConfig = (name: string, endpoint?: string) =>
  api<Response>(`/wireguard/peers/${encodeURIComponent(name)}/config${endpoint ? `?endpoint=${encodeURIComponent(endpoint)}` : ''}`);
export const getWGQR = (text: string) =>
  api<Response>(`/wireguard/qr?text=${encodeURIComponent(text)}`);

// TLS
export const getCerts = () => api<CertInfo[]>('/tls');

// Interfaces this node can bind a peer address to. Child interfaces come
// with kind "child" and their parent NIC.
export interface IfaceInfo { name: string; up: boolean; loopback: boolean; addrs: string[]; kind?: string; parent?: string; }
export const getInterfaces = () => api<IfaceInfo[]>('/system/interfaces');
export const getPunchStatus = () => api<import('./types').PunchStatus[]>('/punch/status');
export const importCert = (data: { id: string; name: string; cert: string; key?: string }) =>
  api('/tls/import', { method: 'POST', body: JSON.stringify(data) });
export const importCertPath = (data: { id: string; name: string; cert_path: string; key_path?: string }) =>
  api('/tls/import-path', { method: 'POST', body: JSON.stringify(data) });
export const generateCert = (data: { id: string; name: string; domains: string[]; days?: number }) =>
  api('/tls/generate', { method: 'POST', body: JSON.stringify(data) });
export const signCert = (data: { ca_id: string; id: string; name: string; cn: string; days?: number }) =>
  api('/tls/sign', { method: 'POST', body: JSON.stringify(data) });
export const generateCA = (data: { id: string; name: string; cn: string; org?: string; days: number; key: string }) =>
  api('/tls/ca', { method: 'POST', body: JSON.stringify(data) });
export const getCertPem = (id: string) =>
  api<{ cert: string; key: string }>(`/tls/${encodeURIComponent(id)}/pem`);
export const deleteCert = (id: string) =>
  api(`/tls/${encodeURIComponent(id)}`, { method: 'DELETE' });

// Interfaces (system NICs + child interfaces) and egress selection
export const listInterfaces = () => api<import('./types').InterfacesResponse>('/interfaces');
export const createChildIface = (data: import('./types').ChildIfaceBody) =>
  api('/interfaces', { method: 'POST', body: JSON.stringify(data) });
export const updateChildIface = (name: string, data: import('./types').ChildIfaceBody) =>
  api(`/interfaces/${encodeURIComponent(name)}`, { method: 'PUT', body: JSON.stringify(data) });
export const getIfaceReferences = (name: string) =>
  api<import('./types').IfaceReference[]>(`/interfaces/${encodeURIComponent(name)}/references`);
export const deleteChildIface = (name: string, replacement: string) =>
  api(`/interfaces/${encodeURIComponent(name)}?replacement=${encodeURIComponent(replacement)}`, { method: 'DELETE' });
export const renewIface = (name: string) =>
  api(`/interfaces/${encodeURIComponent(name)}/renew`, { method: 'POST' });
export const refreshPublicIP = (name: string) =>
  api(`/interfaces/${encodeURIComponent(name)}/public-ip`, { method: 'POST' });
export const setEgress = (data: import('./types').Egress) =>
  api('/egress', { method: 'PUT', body: JSON.stringify(data) });

// Settings
export const getUISettings = () => api<UISettings>('/settings/ui');
export const updateUISettings = (data: Partial<UISettings>) =>
  api('/settings/ui', { method: 'PUT', body: JSON.stringify(data) });
// Settings by owner: the node (core), the web UI, public-address detection.
export const updateNodeSettings = (data: Partial<UISettings>) =>
  api('/settings/node', { method: 'PUT', body: JSON.stringify(data) });
export const updateWebSettings = (data: Partial<UISettings>) =>
  api('/settings/web', { method: 'PUT', body: JSON.stringify(data) });
export const updatePublicIPSettings = (data: PublicIPSettings) =>
  api('/settings/public-ip', { method: 'PUT', body: JSON.stringify(data) });
export const changePassword = (data: { current_password: string; new_username?: string; new_password?: string }) =>
  api('/settings/password', { method: 'PUT', body: JSON.stringify(data) });

// Backup/Restore
export const downloadBackup = async () => {
  const base = (window as any).__BASE__ || '';
  const token = sessionStorage.getItem('token:' + base);
  const res = await fetch(`${base}/api/backup`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  });
  if (!res.ok) throw new Error('Backup failed');
  return res.blob();
};
export const uploadRestore = async (file: File) => {
  const base = (window as any).__BASE__ || '';
  const token = sessionStorage.getItem('token:' + base);
  const res = await fetch(`${base}/api/restore`, {
    method: 'POST',
    headers: {
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      'Content-Type': 'application/x-tar',
    },
    body: file,
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
};

// Upgrade
export const getSystemArch = () => api<{ os: string; arch: string; version: string; binary: string; binary_size?: number; in_docker: boolean; upgradable?: boolean; official?: boolean }>('/system/arch');
/** Thrown when an official node is offered a binary that is not an official build (HTTP 409). */
export class UnofficialBuildError extends Error {
  constructor(public status: string, public reason: string) { super(reason || status); }
}
export const uploadUpgrade = async (file: File, allowUnofficial = false) => {
  const base = (window as any).__BASE__ || '';
  const token = sessionStorage.getItem('token:' + base);
  const res = await fetch(`${base}/api/upgrade${allowUnofficial ? '?allow_unofficial=1' : ''}`, {
    method: 'POST',
    headers: {
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      'Content-Type': 'application/gzip',
    },
    body: file,
  });
  if (res.status === 409) {
    const d = await res.json().catch(() => ({}));
    if (d?.error === 'unofficial') throw new UnofficialBuildError(d.status, d.reason || '');
  }
  if (!res.ok) throw new Error(await res.text());
  return res.json();
};

// Online Update — server-side singleton, all sessions observe the same job
export const checkUpdate = () => api<UpgradeCheckResult>('/upgrade/check');
export const startUpgradeDownload = () => api<UpgradeStatus>('/upgrade/download', { method: 'POST' });
export const getUpgradeStatus = () => api<UpgradeStatus>('/upgrade/status');
export const applyUpgrade = () => api('/upgrade/apply', { method: 'POST' });
export const restartNode = () => api('/system/restart', { method: 'POST' });

// Port Check
export const checkPorts = (ports: PortConflict[]) =>
  api<{ conflicts: PortConflict[] }>('/check-ports', { method: 'POST', body: JSON.stringify({ ports }) });

// Diag — peer history rings (1m / 1h / 1d). Backs the PathInfoExpand
// status bars. ts is unix-seconds aligned to the precision; arrays are
// ordered oldest→newest and capped at 60 entries per precision.
export interface DiagHistoryBucket {
  ts: number;
  latencyMs: number;
  onlinePct: number;
  txPeak: number;
  rxPeak: number;
  samples: number;
}
export interface DiagPeerHistory {
  m1: DiagHistoryBucket[];
  h1: DiagHistoryBucket[];
  d1: DiagHistoryBucket[];
}
export const getPeerHistory = () =>
  api<{ peers: Record<string, DiagPeerHistory> }>('/diag/peer-history');

// Diag — process counters (subset used by the Overview page).
export interface DiagRuntime {
  uptime_seconds: number;
  goroutines: number;
  heap_alloc: number;
  sys: number;
  num_gc: number;
}
export const getRuntime = () => api<DiagRuntime>('/diag/runtime');
export interface LogEntry { seq: number; ts: number; level: 'info' | 'warn' | 'error'; src: string; msg: string }
export interface LogsResponse { entries: LogEntry[]; last: number; gap: boolean }
export const getLogs = (after = 0, limit = 5000) => api<LogsResponse>(`/logs?after=${after}&limit=${limit}`);
