/**
 * The proxies a user signs in on: the key (a user's proxy_disabled, the
 * backend's conflict detection) and the label the UI shows.
 *
 * To add a proxy: add an entry here, then the key to the conflict
 * detection list in app.go rebuildUserIndex().
 */

export interface ProxyRegistryEntry {
  key: string;
  label: string;
}

export const PROXY_REGISTRY: ProxyRegistryEntry[] = [
  { key: 'hy2', label: 'Hysteria2' },
  { key: 'ss', label: 'Shadowsocks' },
  { key: 'trojan', label: 'Trojan' },
  { key: 'socks5', label: 'SOCKS5' },
  { key: 'http', label: 'HTTP' },
  { key: 'l2tp', label: 'L2TP/IPsec' },
  { key: 'ikev2', label: 'IKEv2' },
  { key: 'openvpn', label: 'OpenVPN' },
];

/** The label of a proxy key (the key itself when unknown). */
export const proxyLabel = (key: string) => PROXY_REGISTRY.find((p) => p.key === key)?.label || key;
