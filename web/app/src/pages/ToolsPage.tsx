import { lazy, Suspense, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Tabs } from '@hy2scale/ui';
import { getToolsInfo } from '@/api/tools';
import PingTab from '@/components/tools/PingTab';
import MtrTab from '@/components/tools/MtrTab';
import SpeedTab from '@/components/tools/SpeedTab';

// xterm.js only loads when the terminal is first opened.
const TerminalTab = lazy(() => import('@/components/tools/TerminalTab'));

type Key = 'ping' | 'mtr' | 'speed' | 'terminal';

/**
 * Tools: network tests run by this node, and a terminal on it. A tab stays
 * mounted once visited, so a running test or a shell survives switching.
 */
export default function ToolsPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState<Key>('ping');
  const [seen, setSeen] = useState<Set<Key>>(() => new Set(['ping']));
  const { data: info } = useQuery({ queryKey: ['toolsInfo'], queryFn: getToolsInfo, staleTime: 60_000 });

  const show = (k: string) => {
    setTab(k as Key);
    setSeen((s) => (s.has(k as Key) ? s : new Set(s).add(k as Key)));
  };
  const panel = (k: Key, node: React.ReactNode) => seen.has(k) && (
    <div key={k} className="tool-panel" hidden={tab !== k}>{node}</div>
  );

  return (
    <div className="hy-page tools-page">
      <Tabs
        items={[
          { key: 'ping', label: t('tools.ping') },
          { key: 'mtr', label: t('tools.mtr') },
          { key: 'speed', label: t('tools.speed') },
          { key: 'terminal', label: t('tools.terminal') },
        ]}
        activeKey={tab}
        onChange={show}
      />
      {panel('ping', <PingTab avail={info?.ping} />)}
      {panel('mtr', <MtrTab avail={info?.mtr} />)}
      {panel('speed', <SpeedTab avail={info?.speed} />)}
      {panel('terminal', (
        <Suspense fallback={null}>
          <TerminalTab avail={info?.terminal} active={tab === 'terminal'} />
        </Suspense>
      ))}
    </div>
  );
}
