import { api } from './client';
import type {
  NodeConfig, Stats, TopologyNode, ClientEntry,
  UserConfig, Session, ProxyConfig, SSConfig, L2TPConfig,
  IKEv2Config, WireGuardConfig, WireGuardPeer,
  RoutingRule, TunModeConfig, CertInfo, UISettings, PortConflict,
} from './types';

export type { NodeConfig, Stats, TopologyNode, ClientEntry, UserConfig, Session, ProxyConfig, SSConfig, L2TPConfig, IKEv2Config, WireGuardConfig, WireGuardPeer, RoutingRule, TunModeConfig, CertInfo, UISettings, PortConflict };

// Auth
export const login = (username: string, password: string) =>
  api<{ token: string }>('/login', { method: 'POST', body: JSON.stringify({ username, password }) });

// Node
export const getNode = () => api<NodeConfig>('/node');
export const updateNode = (data: Partial<NodeConfig>) =>
  api('/node', { method: 'PUT', body: JSON.stringify(data) });

// Stats
export const getStats = () => api<Stats>('/stats');

// Topology
export const getTopology = () => api<TopologyNode[]>('/topology');

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

// L2TP
export const getL2TP = () => api<L2TPConfig>('/l2tp');
export const updateL2TP = (data: Partial<L2TPConfig>) =>
  api('/l2tp', { method: 'PUT', body: JSON.stringify(data) });

// IKEv2
export const getIKEv2 = () => api<IKEv2Config>('/ikev2');
export const updateIKEv2 = (data: Partial<IKEv2Config>) =>
  api('/ikev2', { method: 'PUT', body: JSON.stringify(data) });

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
export const getWGPeerConfig = (name: string) =>
  api<Response>(`/wireguard/peers/${encodeURIComponent(name)}/config`);
export const getWGQR = (text: string) =>
  api<Response>(`/wireguard/qr?text=${encodeURIComponent(text)}`);

// Rules
export const getRules = () => api<{ available: boolean; rules: RoutingRule[] }>('/rules');
export const createRule = (data: Partial<RoutingRule>) =>
  api('/rules', { method: 'POST', body: JSON.stringify(data) });
export const updateRule = (id: string, data: Partial<RoutingRule>) =>
  api(`/rules/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(data) });
export const deleteRule = (id: string) =>
  api(`/rules/${encodeURIComponent(id)}`, { method: 'DELETE' });
export const toggleRule = (id: string, enabled: boolean) =>
  api(`/rules/${encodeURIComponent(id)}/toggle`, { method: 'PUT', body: JSON.stringify({ enabled }) });
export const getTunMode = () => api<TunModeConfig>('/rules/tun-mode');
export const updateTunMode = (data: Partial<TunModeConfig>) =>
  api('/rules/tun-mode', { method: 'PUT', body: JSON.stringify(data) });

// TLS
export const getCerts = () => api<CertInfo[]>('/tls');
export const importCert = (data: { id: string; name: string; cert: string; key?: string }) =>
  api('/tls/import', { method: 'POST', body: JSON.stringify(data) });
export const importCertPath = (data: { id: string; name: string; cert_path: string; key_path?: string }) =>
  api('/tls/import-path', { method: 'POST', body: JSON.stringify(data) });
export const generateCert = (data: { id: string; name: string; domains: string[]; days?: number }) =>
  api('/tls/generate', { method: 'POST', body: JSON.stringify(data) });
export const signCert = (data: { ca_id: string; id: string; name: string; cn: string; days?: number }) =>
  api('/tls/sign', { method: 'POST', body: JSON.stringify(data) });
export const getCertPem = (id: string) =>
  api<{ cert: string; key: string }>(`/tls/${encodeURIComponent(id)}/pem`);
export const deleteCert = (id: string) =>
  api(`/tls/${encodeURIComponent(id)}`, { method: 'DELETE' });

// Settings
export const getUISettings = () => api<UISettings>('/settings/ui');
export const updateUISettings = (data: Partial<UISettings>) =>
  api('/settings/ui', { method: 'PUT', body: JSON.stringify(data) });
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

// Port Check
export const checkPorts = (ports: PortConflict[]) =>
  api<{ conflicts: PortConflict[] }>('/check-ports', { method: 'POST', body: JSON.stringify({ ports }) });
