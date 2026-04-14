import { api } from './client';
// Auth
export const login = (username, password) => api('/login', { method: 'POST', body: JSON.stringify({ username, password }) });
// Node
export const getNode = () => api('/node');
export const updateNode = (data) => api('/node', { method: 'PUT', body: JSON.stringify(data) });
// Stats
export const getStats = () => api('/stats');
// Topology
export const getTopology = () => api('/topology');
// Clients (Peers)
export const getClients = () => api('/clients');
export const createClient = (data) => api('/clients', { method: 'POST', body: JSON.stringify(data) });
export const updateClient = (name, data) => api(`/clients/${encodeURIComponent(name)}`, { method: 'PUT', body: JSON.stringify(data) });
export const deleteClient = (name) => api(`/clients/${encodeURIComponent(name)}`, { method: 'DELETE' });
export const disableClient = (name, disabled) => api(`/clients/${encodeURIComponent(name)}/disable`, { method: 'PUT', body: JSON.stringify({ disabled }) });
// Nested
export const setNested = (name, enabled) => api(`/peers/${encodeURIComponent(name)}/nested`, { method: 'PUT', body: JSON.stringify({ enabled }) });
export const setPeerDisabled = (name, disabled) => api(`/peers/${encodeURIComponent(name)}/disable`, { method: 'PUT', body: JSON.stringify({ disabled }) });
// Users
export const getUsers = () => api('/users');
export const createUser = (data) => api('/users', { method: 'POST', body: JSON.stringify(data) });
export const updateUser = (id, data) => api(`/users/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(data) });
export const deleteUser = (id) => api(`/users/${encodeURIComponent(id)}`, { method: 'DELETE' });
export const toggleUser = (id, enabled) => api(`/users/${encodeURIComponent(id)}/toggle`, { method: 'PUT', body: JSON.stringify({ enabled }) });
export const resetTraffic = (id) => api(`/users/${encodeURIComponent(id)}/reset-traffic`, { method: 'PUT' });
// Sessions
export const getSessions = async () => {
    const res = await api('/sessions');
    return res.devices || [];
};
export const kickSession = (key) => api(`/sessions/${encodeURIComponent(key)}`, { method: 'DELETE' });
// Proxies
export const getProxies = () => api('/proxies');
export const createProxy = (data) => api('/proxies', { method: 'POST', body: JSON.stringify(data) });
export const updateProxy = (id, data) => api(`/proxies/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(data) });
export const deleteProxy = (id) => api(`/proxies/${encodeURIComponent(id)}`, { method: 'DELETE' });
// Shadowsocks
export const getSS = () => api('/ss');
export const updateSS = (data) => api('/ss', { method: 'PUT', body: JSON.stringify(data) });
// L2TP
export const getL2TP = () => api('/l2tp');
export const updateL2TP = (data) => api('/l2tp', { method: 'PUT', body: JSON.stringify(data) });
// IKEv2
export const getIKEv2 = () => api('/ikev2');
export const updateIKEv2 = (data) => api('/ikev2', { method: 'PUT', body: JSON.stringify(data) });
// WireGuard
export const getWireGuard = () => api('/wireguard');
export const updateWireGuard = (data) => api('/wireguard', { method: 'PUT', body: JSON.stringify(data) });
export const generateWGKey = () => api('/wireguard/generate-key', { method: 'POST' });
export const createWGPeer = (data) => api('/wireguard/peers', { method: 'POST', body: JSON.stringify(data) });
export const updateWGPeer = (name, data) => api(`/wireguard/peers/${encodeURIComponent(name)}`, { method: 'PUT', body: JSON.stringify(data) });
export const deleteWGPeer = (name) => api(`/wireguard/peers/${encodeURIComponent(name)}`, { method: 'DELETE' });
export const getWGPeerConfig = (name) => api(`/wireguard/peers/${encodeURIComponent(name)}/config`);
export const getWGQR = (text) => api(`/wireguard/qr?text=${encodeURIComponent(text)}`);
// Rules
export const getRules = () => api('/rules');
export const createRule = (data) => api('/rules', { method: 'POST', body: JSON.stringify(data) });
export const updateRule = (id, data) => api(`/rules/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(data) });
export const deleteRule = (id) => api(`/rules/${encodeURIComponent(id)}`, { method: 'DELETE' });
export const toggleRule = (id, enabled) => api(`/rules/${encodeURIComponent(id)}/toggle`, { method: 'PUT', body: JSON.stringify({ enabled }) });
export const getTunMode = () => api('/rules/tun-mode');
export const updateTunMode = (data) => api('/rules/tun-mode', { method: 'PUT', body: JSON.stringify(data) });
// TLS
export const getCerts = () => api('/tls');
export const importCert = (data) => api('/tls/import', { method: 'POST', body: JSON.stringify(data) });
export const importCertPath = (data) => api('/tls/import-path', { method: 'POST', body: JSON.stringify(data) });
export const generateCert = (data) => api('/tls/generate', { method: 'POST', body: JSON.stringify(data) });
export const signCert = (data) => api('/tls/sign', { method: 'POST', body: JSON.stringify(data) });
export const getCertPem = (id) => api(`/tls/${encodeURIComponent(id)}/pem`);
export const deleteCert = (id) => api(`/tls/${encodeURIComponent(id)}`, { method: 'DELETE' });
// Settings
export const getUISettings = () => api('/settings/ui');
export const updateUISettings = (data) => api('/settings/ui', { method: 'PUT', body: JSON.stringify(data) });
export const changePassword = (data) => api('/settings/password', { method: 'PUT', body: JSON.stringify(data) });
// Backup/Restore
export const downloadBackup = async () => {
    const base = window.__BASE__ || '';
    const token = sessionStorage.getItem('token:' + base);
    const res = await fetch(`${base}/api/backup`, {
        headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (!res.ok)
        throw new Error('Backup failed');
    return res.blob();
};
export const uploadRestore = async (file) => {
    const base = window.__BASE__ || '';
    const token = sessionStorage.getItem('token:' + base);
    const res = await fetch(`${base}/api/restore`, {
        method: 'POST',
        headers: {
            ...(token ? { Authorization: `Bearer ${token}` } : {}),
            'Content-Type': 'application/x-tar',
        },
        body: file,
    });
    if (!res.ok)
        throw new Error(await res.text());
    return res.json();
};
// Port Check
export const checkPorts = (ports) => api('/check-ports', { method: 'POST', body: JSON.stringify({ ports }) });
