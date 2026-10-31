import { useTranslation } from 'react-i18next';
import TabbedPage from '@/components/TabbedPage';
import WireGuardTab from '@/components/endpoints/WireGuardTab';

/** Endpoints: one sub-page per endpoint type (WireGuard for now). */
export default function EndpointsPage() {
  const { t } = useTranslation();
  return <TabbedPage tabs={[{ key: 'wg', label: t('proxies.wireguard'), render: () => <WireGuardTab /> }]} />;
}
