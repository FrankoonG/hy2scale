import { useEffect, useState, type ReactNode } from 'react';
import { useLocation } from 'react-router-dom';
import { Tabs, TabPanel } from '@hy2scale/ui';

export interface PageTab {
  key: string;
  label: string;
  disabled?: boolean;
  render: () => ReactNode;
}

/** A page made of sub-pages under a tab strip; panels slide by tab order. */
export default function TabbedPage({ tabs, initial }: { tabs: PageTab[]; initial?: string }) {
  // another page may open a given sub-page (state: { tab })
  const location = useLocation();
  const asked = (location.state as { tab?: string } | null)?.tab;
  const [tab, setTab] = useState(asked ?? initial ?? tabs[0].key);
  useEffect(() => { if (asked) setTab(asked); }, [location.key]); // eslint-disable-line react-hooks/exhaustive-deps
  const active = tabs.find((x) => x.key === tab) ?? tabs[0];
  return (
    <div className="hy-page">
      <Tabs items={tabs} activeKey={active.key} onChange={setTab} />
      <TabPanel fill activeKey={active.key} keys={tabs.map((x) => x.key)}>
        {active.render()}
      </TabPanel>
    </div>
  );
}
