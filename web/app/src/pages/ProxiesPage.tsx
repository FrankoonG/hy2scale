import { useTranslation } from 'react-i18next';
import TabbedPage from '@/components/TabbedPage';
import Hy2Tab from '@/components/proxies/Hy2Tab';
import ListenProxyTab from '@/components/proxies/ListenProxyTab';
import SSTab from '@/components/proxies/SSTab';
import TrojanTab from '@/components/proxies/TrojanTab';
import L2TPTab from '@/components/proxies/L2TPTab';
import IKEv2Tab from '@/components/proxies/IKEv2Tab';
import OpenVPNTab from '@/components/proxies/OpenVPNTab';

/** Proxies: one sub-page per inbound proxy / VPN service. */
export default function ProxiesPage() {
  const { t } = useTranslation();
  return (
    <TabbedPage tabs={[
      { key: 'hy2', label: t('proxies.hy2'), render: () => <Hy2Tab /> },
      { key: 'socks5', label: t('proxies.socks5'), render: () => <ListenProxyTab key="socks5" protocol="socks5" /> },
      { key: 'http', label: t('proxies.http'), render: () => <ListenProxyTab key="http" protocol="http" /> },
      { key: 'ss', label: t('proxies.ss'), render: () => <SSTab /> },
      { key: 'trojan', label: t('proxies.trojan'), render: () => <TrojanTab /> },
      { key: 'l2tp', label: t('proxies.l2tp'), render: () => <L2TPTab /> },
      { key: 'ikev2', label: t('proxies.ikev2'), render: () => <IKEv2Tab /> },
      { key: 'openvpn', label: t('proxies.openvpn'), render: () => <OpenVPNTab /> },
    ]} />
  );
}
