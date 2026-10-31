import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Autocomplete, Card, Input, SegmentedControl, Progress } from '@hy2scale/ui';
import { streamTool, type SpeedLine, type ToolAvail } from '@/api/tools';
import { useExitPaths } from '@/hooks/useExitPaths';
import { fmtBytes } from '@/hooks/useFormat';
import { Field, RateLine, RunButton, Stats, Unavailable, useToolRun } from './common';

type Mode = 'node' | 'url';
type Dir = 'down' | 'up';

const mbps = (v: number | undefined) => (v === undefined ? '—' : v < 10 ? v.toFixed(2) : v < 100 ? v.toFixed(1) : v.toFixed(0));

export default function SpeedTab({ avail }: { avail?: ToolAvail }) {
  const { t } = useTranslation();
  const run = useToolRun();
  const { exitPaths } = useExitPaths();
  const [mode, setMode] = useState<Mode>('node');
  const [chain, setChain] = useState('');
  const [url, setUrl] = useState('');
  const [dir, setDir] = useState<Dir>('down');
  const [seconds, setSeconds] = useState('10');
  const [total, setTotal] = useState(10);
  const [points, setPoints] = useState<{ t: number; v: number }[]>([]);
  const [last, setLast] = useState<{ bytes: number; elapsed: number; mbps: number } | null>(null);
  const [done, setDone] = useState<Extract<SpeedLine, { type: 'done' }> | null>(null);

  const target = mode === 'node' ? chain.trim() : url.trim();
  const go = () => {
    if (!target || run.running) return;
    const secs = Math.min(30, Math.max(1, Number(seconds) || 10));
    setTotal(secs);
    setPoints([]);
    setLast(null);
    setDone(null);
    run.start((signal) => streamTool<SpeedLine>('/tools/speed', mode === 'node'
      ? { mode, chain: target, dir, seconds: secs }
      : { mode, url: target, seconds: secs }, (l) => {
      if (l.type === 'progress') {
        setPoints((p) => [...p, { t: l.elapsed_ms / 1000, v: l.cur_mbps ?? l.mbps }]);
        setLast({ bytes: l.bytes, elapsed: l.elapsed_ms / 1000, mbps: l.mbps });
      } else if (l.type === 'done') setDone(l);
      else if (l.type === 'error') run.setError(l.error);
    }, signal));
  };

  const shown = done ? { bytes: done.bytes, elapsed: done.seconds, mbps: done.mbps } : last;

  return (
    <div className="tool-tab" data-testid="tool-speed">
      <Card>
        <form className="tool-bar" onSubmit={(e) => { e.preventDefault(); go(); }}>
          <Field label={t('tools.speedTarget')}>
            <SegmentedControl<Mode> items={[{ key: 'node', label: t('tools.node') }, { key: 'url', label: 'URL' }]} value={mode} onChange={setMode} />
          </Field>
          {mode === 'node' ? (
            <Field label={t('tools.node')} grow>
              <div data-testid="speed-chain"><Autocomplete options={exitPaths} value={chain} onChange={setChain} placeholder={t('tools.nodePh')} /></div>
            </Field>
          ) : (
            <Field label="URL" grow>
              <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://" data-testid="speed-url" />
            </Field>
          )}
          {mode === 'node' && (
            <Field label={t('tools.direction')}>
              <SegmentedControl<Dir> items={[{ key: 'down', label: t('tools.down') }, { key: 'up', label: t('tools.up') }]} value={dir} onChange={setDir} />
            </Field>
          )}
          <Field label={t('tools.duration')}>
            <Input className="tool-num" type="number" min={1} max={30} value={seconds} onChange={(e) => setSeconds(e.target.value)} suffix={t('tools.sec')} />
          </Field>
          <RunButton running={run.running} disabled={!target || avail?.available === false} onStop={run.stop} testId="speed" />
        </form>
        <Unavailable avail={avail} />
        {run.error && <div className="tool-note error" data-testid="speed-error">{run.error}</div>}
      </Card>

      {(shown || run.running) && (
        <Card>
          <div className="tool-speed-head">
            <div className="tool-speed-value" data-testid="speed-mbps">
              {mbps(shown?.mbps)}<span>Mbps</span>
            </div>
            <Stats items={[
              [t('tools.transferred'), shown ? fmtBytes(shown.bytes) : '—'],
              [t('tools.elapsed'), shown ? `${shown.elapsed.toFixed(1)} ${t('tools.sec')}` : '—'],
            ]} />
          </div>
          {!done && <Progress value={Math.min(100, ((last?.elapsed ?? 0) / total) * 100)} />}
          <RateLine points={points} total={total} />
        </Card>
      )}
    </div>
  );
}
