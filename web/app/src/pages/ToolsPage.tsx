import { lazy, memo, Suspense, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Tabs } from '@hy2scale/ui';
import { getToolsInfo } from '@/api/tools';
import PingTabView from '@/components/tools/PingTab';
import MtrTabView from '@/components/tools/MtrTab';
import SpeedTabView from '@/components/tools/SpeedTab';
import { HistoryLimit } from '@/components/tools/common';

// xterm.js only loads when the terminal is first opened.
const TerminalTab = lazy(() => import('@/components/tools/TerminalTab'));

// Switching tabs does not render the tabs again: a render while a panel is
// still hidden measured its draggable path rows at zero size, and they then
// flew in from the top-left corner once it showed.
const PingTab = memo(PingTabView);
const MtrTab = memo(MtrTabView);
const SpeedTab = memo(SpeedTabView);

type Key = 'ping' | 'mtr' | 'speed' | 'terminal';
const ORDER: Key[] = ['ping', 'mtr', 'speed', 'terminal'];

/**
 * Tools: network tests run by this node, and a terminal on it. A tab stays
 * mounted once visited, so a running test or a shell survives switching;
 * the panel shown slides in from the side of the tab it replaces.
 */
export default function ToolsPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState<Key>('ping');
  const [seen, setSeen] = useState<Set<Key>>(() => new Set(['ping']));
  // direction of the last switch (none before the first one)
  const dir = useRef<'' | 'in-l' | 'in-r'>('');
  const { data: info } = useQuery({ queryKey: ['toolsInfo'], queryFn: getToolsInfo, staleTime: 60_000 });

  const show = (k: string) => {
    const next = k as Key;
    if (next === tab) return;
    dir.current = ORDER.indexOf(next) > ORDER.indexOf(tab) ? 'in-r' : 'in-l';
    setTab(next);
    setSeen((s) => (s.has(next) ? s : new Set(s).add(next)));
  };
  const panel = (k: Key, node: React.ReactNode) => seen.has(k) && (
    <div key={k} className={`tool-panel ${tab === k ? dir.current : ''}`} hidden={tab !== k} data-testid={`tool-panel-${k}`}>{node}</div>
  );

  return (
    <div className="hy-page tools-page">
      <Tabs
        items={ORDER.map((key) => ({ key, label: t(`tools.${key}`) }))}
        activeKey={tab}
        onChange={show}
        addon={tab !== 'terminal' && <HistoryLimit />}
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
