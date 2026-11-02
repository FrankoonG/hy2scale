import { useTranslation } from 'react-i18next';
import type { IfaceEntry } from '@/api/types';

// Link types the backend reports (vnic.LinkType), plus 'child' for our own
// sub-interfaces and 'virtual' when the kernel doesn't say.
export const IFACE_TYPES = [
  'ethernet', 'wlan', 'child', 'bridge', 'bond', 'vlan', 'veth', 'macvlan', 'ipvlan',
  'tun', 'wireguard', 'vxlan', 'ovs', 'dummy', 'loopback', 'virtual',
] as const;
export type IfaceType = (typeof IFACE_TYPES)[number];

export function ifaceType(x: IfaceEntry): IfaceType {
  if (x.kind === 'child') return 'child';
  const t = x.type as IfaceType | undefined;
  if (t && (IFACE_TYPES as readonly string[]).includes(t)) return t;
  if (x.kind === 'physical') return x.wireless ? 'wlan' : 'ethernet';
  return 'virtual';
}

/** Hidden by default: anything that is neither a NIC, one of our sub-interfaces nor a router's WAN / LAN. */
export const isHiddenIface = (x: IfaceEntry) => x.kind !== 'physical' && x.kind !== 'child' && !x.role;

/** Coloured link-type badge shown beside an interface name. */
export default function IfaceTypeBadge({ iface }: { iface: IfaceEntry }) {
  const { t } = useTranslation();
  const ty = ifaceType(iface);
  return (
    <span className="iface-type" data-t={ty} data-testid="iface-type">
      {t(`iface.type.${ty}`)}
    </span>
  );
}
