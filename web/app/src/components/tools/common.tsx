import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Button, Input } from '@hy2scale/ui';
import { clearToolsHistory, getToolsHistory, setToolsHistoryMax, type ToolAvail, type ToolsHistory } from '@/api/tools';

const HISTORY_KEY = ['toolsHistory'];

/** One running tool stream: start aborts the previous one; stop aborts. */
export function useToolRun() {
  const ctl = useRef<AbortController | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState('');
  const qc = useQueryClient();
  useEffect(() => () => ctl.current?.abort(), []);

  const start = useCallback(async (fn: (signal: AbortSignal) => Promise<void>) => {
    ctl.current?.abort();
    const c = new AbortController();
    ctl.current = c;
    setRunning(true);
    setError('');
    // the node keeps the target once the test is under way
    const seen = setTimeout(() => qc.invalidateQueries({ queryKey: HISTORY_KEY }), 1500);
    try {
      await fn(c.signal);
    } catch (e: any) {
      if (!c.signal.aborted) setError(String(e?.message || e));
    } finally {
      clearTimeout(seen);
      qc.invalidateQueries({ queryKey: HISTORY_KEY });
      if (ctl.current === c) {
        ctl.current = null;
        setRunning(false);
      }
    }
  }, [qc]);
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
  // Distinct keys: Stop must not turn into the submit button under the
  // pointer during its own click (the click's default action would then
  // submit the form and start a new run).
  return running
    ? <Button key="stop" type="button" onClick={(e) => { e.preventDefault(); onStop(); }} data-testid={testId && `${testId}-stop`}>{t('tools.stop')}</Button>
    : <Button key="start" variant="primary" type="submit" disabled={disabled} data-testid={testId && `${testId}-start`}>{t('tools.start')}</Button>;
}

export function Unavailable({ avail }: { avail?: ToolAvail }) {
  const { t } = useTranslation();
  if (!avail || avail.available) return null;
  return <div className="tool-note warn">{t('tools.unavailable', { reason: avail.reason || '' })}</div>;
}

/** Targets a tool ran against on this node, newest first. */
export function useToolHistory(kind: keyof ToolsHistory['items']): string[] {
  const { data } = useQuery({ queryKey: HISTORY_KEY, queryFn: getToolsHistory, staleTime: 30_000 });
  return data?.items[kind] ?? NONE;
}
const NONE: string[] = [];

/** How many targets each tool keeps, and clearing them (tab bar addon). */
export function HistoryLimit() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { data } = useQuery({ queryKey: HISTORY_KEY, queryFn: getToolsHistory, staleTime: 30_000 });
  const [max, setMax] = useState('');
  useEffect(() => { if (data) setMax(String(data.max)); }, [data?.max]);
  const save = async () => {
    const n = Math.round(Number(max));
    if (!data || max.trim() === '' || !isFinite(n) || n < 0 || n > 200) { setMax(data ? String(data.max) : ''); return; }
    if (n === data.max) return;
    qc.setQueryData(HISTORY_KEY, await setToolsHistoryMax(n));
  };
  const empty = !data || Object.values(data.items).every((l) => l.length === 0);
  return (
    <div className="tool-history">
      <span className="tool-field-label">{t('tools.historyMax')}</span>
      <Input className="tool-num" type="number" min={0} max={200} value={max} data-testid="tools-history-max"
        onChange={(e) => setMax(e.target.value)} onBlur={save}
        onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); (e.target as HTMLInputElement).blur(); } }} />
      <Button size="sm" disabled={empty} data-testid="tools-history-clear"
        onClick={async () => qc.setQueryData(HISTORY_KEY, await clearToolsHistory())}>{t('tools.historyClear')}</Button>
    </div>
  );
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

type Pt = { t: number; v: number };
const lerp = (a: number, b: number, k: number) => a + (b - a) * k;
const LINE_STEP_MS = 450;

/**
 * The series as drawn: each new sample grows out of the previous last
 * point and the vertical scale follows, eased over LINE_STEP_MS; a series
 * that got shorter (a new run) is drawn at once, as is everything under
 * reduced motion.
 */
function useEasedSeries(points: Pt[]): { pts: Pt[]; top: number } {
  const target = useMemo(() => ({ pts: points, top: Math.max(1, ...points.map((p) => p.v)) * 1.1 }), [points]);
  const [view, setView] = useState(target);
  const cur = useRef(target);
  useEffect(() => {
    const from = cur.current;
    const reduced = typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;
    if (reduced || target.pts.length < from.pts.length || from.pts.length === 0) {
      cur.current = target;
      setView(target);
      return;
    }
    const last = from.pts[from.pts.length - 1];
    const fromPts = target.pts.map((p, i) => from.pts[i] ?? last ?? p);
    const t0 = performance.now();
    let raf = 0;
    const step = (now: number) => {
      const k = Math.min(1, (now - t0) / LINE_STEP_MS);
      const e = 1 - Math.pow(1 - k, 3);
      const v = {
        pts: target.pts.map((p, i) => ({ t: lerp(fromPts[i].t, p.t, e), v: lerp(fromPts[i].v, p.v, e) })),
        top: lerp(from.top, target.top, e),
      };
      cur.current = v;
      setView(v);
      if (k < 1) raf = requestAnimationFrame(step);
    };
    raf = requestAnimationFrame(step);
    return () => cancelAnimationFrame(raf);
  }, [target]);
  return view;
}

/** Line of throughput samples over the run (x = elapsed / total). */
export function RateLine({ points, total }: { points: Pt[]; total: number }) {
  const W = 600;
  const H = 120;
  const { pts, top } = useEasedSeries(points);
  if (pts.length < 2) return <svg className="tool-line" viewBox={`0 0 ${W} ${H}`} aria-hidden />;
  const xy = pts.map((p) => `${(Math.min(p.t, total) / total) * W},${H - (Math.min(p.v, top) / top) * H}`);
  return (
    <svg className="tool-line" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" aria-hidden data-testid="rate-line">
      <polygon className="area" points={`0,${H} ${xy.join(' ')} ${xy[xy.length - 1].split(',')[0]},${H}`} />
      <polyline className="stroke" points={xy.join(' ')} vectorEffect="non-scaling-stroke" />
    </svg>
  );
}
