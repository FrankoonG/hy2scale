import { useEffect, useState } from 'react';
import { useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Tabs, TabPanel } from '@hy2scale/ui';
import * as api from '@/api';
import { useAuthStore } from '@/store/auth';
import { useSystemForm, useWebForm, usePasswordForm } from './settings/forms';
import NetworkTab from './settings/NetworkTab';
import DnsTab from './settings/DnsTab';
import NestedTab from './settings/NestedTab';
import WebTab from './settings/WebTab';
import AccountTab from './settings/AccountTab';
import UpgradeTab from './settings/UpgradeTab';
import MaintenanceTab from './settings/MaintenanceTab';
import AboutTab from './settings/AboutTab';

type SettingsTab = 'network' | 'dns' | 'nested' | 'web' | 'account' | 'upgrade' | 'maintenance' | 'about';

export default function SettingsPage() {
  const { t } = useTranslation();
  const forcePasswordChange = useAuthStore((s) => s.forcePasswordChange);
  const settingsQ = useQuery({ queryKey: ['settings'], queryFn: api.getUISettings });
  // Other pages may open a given sub-page (state: { settingsTab }).
  const location = useLocation();
  const wanted = (location.state as { settingsTab?: SettingsTab } | null)?.settingsTab;
  const [tab, setTab] = useState<SettingsTab>(wanted ?? 'network');
  useEffect(() => { if (wanted) setTab(wanted); }, [location.key, wanted]);
  // A forced password change leaves only the account page.
  const activeTab: SettingsTab = forcePasswordChange ? 'account' : tab;

  // Form state stays here so switching tabs keeps unsaved edits.
  const sys = useSystemForm(settingsQ);
  const web = useWebForm(settingsQ);
  const pw = usePasswordForm();

  // One list for the tab bar and the panel: the slide direction follows it.
  const pages: [SettingsTab, string][] = [
    ['network', t('settings.network')],
    ['dns', t('settings.dnsTab')],
    ['nested', t('settings.nestedTab')],
    ['web', t('settings.web')],
    ['account', t('settings.account')],
    ['upgrade', t('settings.upgrade')],
    ['maintenance', t('settings.maintenance')],
    ['about', t('settings.about')],
  ];
  const tabs = pages.map(([key, label]) => ({ key, label, disabled: forcePasswordChange && key !== 'account' }));

  const panel = () => {
    switch (activeTab) {
      case 'network': return <NetworkTab sys={sys} />;
      case 'dns': return <DnsTab sys={sys} />;
      case 'nested': return <NestedTab sys={sys} />;
      case 'web': return <WebTab web={web} />;
      case 'account': return <AccountTab pw={pw} forcePasswordChange={forcePasswordChange} />;
      case 'upgrade': return <UpgradeTab />;
      case 'maintenance': return <MaintenanceTab />;
      case 'about': return <AboutTab />;
    }
  };

  return (
    <div className="hy-page">
      <Tabs
        items={tabs}
        activeKey={activeTab}
        onChange={(key) => { if (!forcePasswordChange) setTab(key as SettingsTab); }}
      />
      <TabPanel fill activeKey={activeTab} keys={tabs.map((tb) => tb.key)}>
        {panel()}
      </TabPanel>
    </div>
  );
}
