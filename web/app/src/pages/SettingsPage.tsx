import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Tabs, TabPanel } from '@hy2scale/ui';
import * as api from '@/api';
import { useAuthStore } from '@/store/auth';
import { useSystemForm, useWebForm, usePasswordForm } from './settings/forms';
import SystemTab from './settings/SystemTab';
import WebTab from './settings/WebTab';
import UpgradeTab from './settings/UpgradeTab';

type SettingsTab = 'system' | 'web' | 'upgrade';

export default function SettingsPage() {
  const { t } = useTranslation();
  const forcePasswordChange = useAuthStore((s) => s.forcePasswordChange);
  const settingsQ = useQuery({ queryKey: ['settings'], queryFn: api.getUISettings });
  const [activeTab, setActiveTab] = useState<SettingsTab>('system');

  // Form state stays here so switching tabs keeps unsaved edits.
  const sys = useSystemForm(settingsQ);
  const web = useWebForm(settingsQ);
  const pw = usePasswordForm();

  // One list for the tab bar and the panel: the slide direction follows it.
  const tabs = [
    { key: 'system', label: t('settings.system') },
    { key: 'web', label: t('settings.web'), disabled: forcePasswordChange },
    { key: 'upgrade', label: t('settings.upgrade'), disabled: forcePasswordChange },
  ];

  return (
    <div className="hy-page">
      <Tabs
        items={tabs}
        activeKey={activeTab}
        onChange={(key) => { if (!forcePasswordChange || key === 'system') setActiveTab(key as SettingsTab); }}
      />
      <TabPanel fill activeKey={activeTab} keys={tabs.map((tb) => tb.key)}>
        {activeTab === 'system' ? <SystemTab sys={sys} pw={pw} forcePasswordChange={forcePasswordChange} />
          : activeTab === 'web' ? <WebTab web={web} />
          : <UpgradeTab />}
      </TabPanel>
    </div>
  );
}
