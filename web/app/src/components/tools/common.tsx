import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@hy2scale/ui';
import type { ToolAvail } from '@/api/tools';

/** One running tool stream: start aborts the previous one; stop aborts. */
export function useToolRun() {
  const ctl = useRef<AbortController | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => () => ctl.current?.abort(), []);

  const start = useCallback(async (fn: (signal: AbortSignal) => Promise<void>) => {
    ctl.current?.abort();
    const c = new AbortController();
    ctl.current = c;
    setRunning(true);
    setError('');
    try {
      await fn(c.signal);
    } catch (e: any) {
      if (!c.signal.aborted) setError(String(e?.message || e));
    } finally {
      if (ctl.current === c) {
        ctl.current = null;
        setRunning(false);
      }
    }
  }, []);
  const stop = useCallback(() => {
    ctl.current?.abort();
    ctl.current = null;
    setRunning(false);
  }, []);
  return { running, error, setError, start, stop };
}

/** A labelled control in a tool's input bar. */
export function Field({ label, grow, children }: { label: string; grow?: boolean; children: ReactNode }) {
  return (
    <label className={`tool-field${grow ? ' grow' : ''}`}>
      <span className="tool-field-label">{label}</span>
      {children}
    </label>
  );
}

/** Start (submits the bar's form) / Stop toggle at the end of an input bar. */
export function RunButton({ running, disabled, onStop, testId }: {
  running: boolean; disabled?: boolean; onStop: () => void; testId?: string;
}) {
  const { t } = useTranslation();
  return running
    ? <Button type="button" onClick={onStop} data-testid={testId && `${testId}-stop`}>{t('tools.stop')}</Button>
    : <Button variant="primary" type="submit" disabled={disabled} data-testid={testId && `${testId}-start`}>{t('tools.start')}</Button>;
}

export function Unavailable({ avail }: { avail?: ToolAvail }) {
  const { t } = useTranslation();
  if (!avail || avail.available) return null;
  return <div className="tool-note warn">{t('tools.unavailable', { reason: avail.reason || '' })}</div>;
}

export function Stats({ items }: { items: [string, ReactNode][] }) {
  return (
    <div className="tool-stats">
      {items.map(([k, v]) => (
        <div className="tool-stat" key={k}>
          <div className="tool-stat-label">{k}</div>
          <div className="tool-stat-value">{v}</div>
        </div>
      ))}
    </div>
  );
}

export const ms = (v: number | undefined) => (v === undefined || !isFinite(v) ? '—' : v < 10 ? v.toFixed(2) : v < 100 ? v.toFixed(1) : v.toFixed(0));

/** Bars of round-trip times (null = no reply), oldest on the left. */
export function RttBars({ values, max = 120 }: { values: (number | null)[]; max?: number }) {
  const shown = values.slice(-max);
  const top = Math.max(0, ...shown.map((v) => v ?? 0)) || 1;
  const w = 6;
  const gap = 2;
  const h = 64;
  return (
    <svg className="tool-bars" viewBox={`0 0 ${max * (w + gap)} ${h}`} preserveAspectRatio="none" aria-hidden>
      {shown.map((v, i) => {
        const x = i * (w + gap);
        if (v === null) return <rect key={i} className="lost" x={x} y={0} width={w} height={h} />;
        const bh = Math.max(2, (v / top) * (h - 2));
        return <rect key={i} className="ok" x={x} y={h - bh} width={w} height={bh} />;
      })}
    </svg>
  );
}

/** Line of throughput samples over the run (x = elapsed / total). */
export function RateLine({ points, total }: { points: { t: number; v: number }[]; total: number }) {
  const W = 600;
  const H = 120;
  if (points.length < 2) return <svg className="tool-line" viewBox={`0 0 ${W} ${H}`} aria-hidden />;
  const top = Math.max(1, ...points.map((p) => p.v)) * 1.1;
  const xy = points.map((p) => `${(Math.min(p.t, total) / total) * W},${H - (p.v / top) * H}`);
  return (
    <svg className="tool-line" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" aria-hidden>
      <polygon className="area" points={`0,${H} ${xy.join(' ')} ${xy[xy.length - 1].split(',')[0]},${H}`} />
      <polyline className="stroke" points={xy.join(' ')} vectorEffect="non-scaling-stroke" />
    </svg>
  );
}
