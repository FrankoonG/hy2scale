import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Card, Input, SegmentedControl, Progress } from '@hy2scale/ui';
import { streamTool, type SpeedLine, type SpeedPath, type ToolAvail } from '@/api/tools';
import { ExitPathList, type ExitPathValue } from '@/components/ExitPathList';
import { fmtBytes } from '@/hooks/useFormat';
import { Field, RateLine, RunButton, Stats, Unavailable, useToolRun } from './common';

type Mode = 'node' | 'url';
type Dir = 'down' | 'up';

const mbps = (v: number | undefined) => (v === undefined ? '—' : v < 10 ? v.toFixed(2) : v < 100 ? v.toFixed(1) : v.toFixed(0));

export default function SpeedTab({ avail }: { avail?: ToolAvail }) {
  const { t } = useTranslation();
  const run = useToolRun();
  const [mode, setMode] = useState<Mode>('node');
  // as a user's exit: one path, or several tested by quality / aggregate
  const [exit, setExit] = useState<ExitPathValue>({ paths: [''], mode: '' });
  const [url, setUrl] = useState('');
  const [dir, setDir] = useState<Dir>('down');
  const [seconds, setSeconds] = useState('10');
  const [total, setTotal] = useState(10);
  const [points, setPoints] = useState<{ t: number; v: number }[]>([]);
  const [last, setLast] = useState<{ bytes: number; elapsed: number; mbps: number } | null>(null);
  const [done, setDone] = useState<Extract<SpeedLine, { type: 'done' }> | null>(null);

  const [path, setPath] = useState('');
  const [perPath, setPerPath] = useState<SpeedPath[]>([]);

  const paths = exit.paths.map((x) => x.trim()).filter(Boolean);
  const target = mode === 'node' ? paths.join(',') : url.trim();
  const go = () => {
    if (!target || run.running) return;
    const secs = Math.min(30, Math.max(1, Number(seconds) || 10));
    setTotal(secs);
    setPoints([]);
    setLast(null);
    setDone(null);
    setPath('');
    setPerPath([]);
    const params = mode === 'url'
      ? { mode, url: target, seconds: secs }
      : paths.length > 1
        ? { mode, paths: target, exit_mode: exit.mode || 'quality', dir, seconds: secs }
        : { mode, chain: target, dir, seconds: secs };
    run.start((signal) => streamTool<SpeedLine>('/tools/speed', params, (l) => {
      if (l.type === 'path') setPath(l.path);
      else if (l.type === 'progress') {
        setPoints((p) => [...p, { t: l.elapsed_ms / 1000, v: l.cur_mbps ?? l.mbps }]);
        setLast({ bytes: l.bytes, elapsed: l.elapsed_ms / 1000, mbps: l.mbps });
        if (l.paths) setPerPath(l.paths);
      } else if (l.type === 'done') {
        setDone(l);
        if (l.paths) setPerPath(l.paths);
      }
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
            <div className="tool-paths" data-testid="speed-chain">
              <ExitPathList label={t('tools.node')} value={exit} onChange={setExit} placeholder={t('tools.nodePh')} />
            </div>
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
              ...(path ? [[t('tools.path'), <span className="mono" data-testid="speed-path">{path}</span>] as [string, ReactNode]] : []),
            ]} />
          </div>
          {!done && <Progress value={Math.min(100, ((last?.elapsed ?? 0) / total) * 100)} />}
          <RateLine points={points} total={total} />
          {perPath.length > 0 && (
            <div className="tool-table-wrap">
              <table className="tool-table" data-testid="speed-paths">
                <thead><tr><th className="left">{t('tools.path')}</th><th>Mbps</th><th>{t('tools.transferred')}</th></tr></thead>
                <tbody>
                  {perPath.map((x) => (
                    <tr key={x.path}>
                      <td className="left mono">{x.path}</td>
                      {x.error
                        ? <td className="bad" colSpan={2} title={x.error}>{t('tools.failed')}</td>
                        : <><td>{mbps(x.mbps)}</td><td>{fmtBytes(x.bytes)}</td></>}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      )}
    </div>
  );
}
