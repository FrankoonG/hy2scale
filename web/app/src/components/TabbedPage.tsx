import { useState, type ReactNode } from 'react';
import { Tabs, TabPanel } from '@hy2scale/ui';

export interface PageTab {
  key: string;
  label: string;
  disabled?: boolean;
  render: () => ReactNode;
}

/** A page made of sub-pages under a tab strip; panels slide by tab order. */
export default function TabbedPage({ tabs, initial }: { tabs: PageTab[]; initial?: string }) {
  const [tab, setTab] = useState(initial ?? tabs[0].key);
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
