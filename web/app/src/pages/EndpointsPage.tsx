import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Tabs, TabPanel } from '@hy2scale/ui';
import WireGuardTab from '@/components/endpoints/WireGuardTab';

/** Endpoints: one sub-page per endpoint type (WireGuard for now). */
export default function EndpointsPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState('wg');

  const tabs = [
    { key: 'wg', label: t('proxies.wireguard') },
  ];

  const renderTab = () => {
    switch (tab) {
      case 'wg': return <WireGuardTab />;
      default: return null;
    }
  };

  return (
    <div className="hy-page">
      <Tabs items={tabs} activeKey={tab} onChange={setTab} />
      <TabPanel fill activeKey={tab} keys={tabs.map((x) => x.key)}>
        {renderTab()}
      </TabPanel>
    </div>
  );
}
