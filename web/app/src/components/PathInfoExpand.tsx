// Status-graph rows for the bottom-left selected-path overlay.
//
// Concept-only: data is currently mocked. Wiring to /api/diag/peer-latency
// (raw 1-minute ring already exists, ~120 samples × 5 s = 10 min) and a
// new downsampled history endpoint for 1-hour and 1-day rows comes after
// concept approval.
//
// Bar semantics: in every precision tab there are exactly BUCKETS_PER_ROW
// (=24) ticks. The CURRENT precision selects what each tick represents:
//   1m → each tick = 1 minute  (so 24 ticks = last 24 minutes)
//   1h → each tick = 1 hour    (so 24 ticks = last 24 hours)
//   1d → each tick = 1 day     (so 24 ticks = last 24 days)
// Newer time is on the right.
//
// Color literals (NOT vars) so Dark Reader picks them up reliably — see
// docs/dark-reader-testing.md.

import { useState, useRef, useLayoutEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { Tooltip } from '@hy2scale/ui';

/** Maximum number of ticks generated per precision. The actual visible
 *  count adapts to whatever the bar-row width can hold at TICK_WIDTH_PX
 *  + GAP_PX per tick — narrower panel → fewer bars, wider panel → more
 *  bars, up to this cap. Set well above any realistic panel width so
 *  the row always fills its full width rather than leaving empty space
 *  on the right; the slice happens at render time. */
export const BUCKETS_PER_ROW = 60;
/** Pixel width of one tick. Must match `.hy-topo-pathinfo-statusbar-tick`
 *  flex-basis in components.css. */
const TICK_WIDTH_PX = 10;
/** Pixel gap between ticks. Must match `.hy-topo-pathinfo-statusrow-bars`
 *  CSS `gap`. */
const GAP_PX = 2;

/** ms-bucketed latency sample. Negative `ms` = offline / probe failed. */
export interface LatBucket { ms: number }
/** 0..1 fraction online. <0 means missing. */
export interface OnlineBucket { pct: number }
/** Peak bytes/sec observed in the bucket window. <0 means missing. */
export interface RateBucket { bps: number }

export type Precision = 'm1' | 'h1' | 'd1';

export interface PathInfoExpandProps {
  /** Per-precision buckets — newest on the right. Each precision has
   *  BUCKETS_PER_ROW entries. */
  latency: Record<Precision, LatBucket[]>;
  online:  Record<Precision, OnlineBucket[]>;
  txPeak:  Record<Precision, RateBucket[]>;
  rxPeak:  Record<Precision, RateBucket[]>;
  /** Per-hop segment latencies (ms) — same order as displayPath hops
   *  starting from self. Each entry is the hop's own contribution.
   *  Negative = offline / unknown. */
  perHopLatencyMs: number[];
  /** Realtime up / down rates in bytes/sec at the path's egress point. */
  realtimeUpBps: number;
  realtimeDownBps: number;
  /** Cumulative bytes through the path. */
  totalUpBytes: number;
  totalDownBytes: number;
}

/** Latency → discrete color tier. Matches the list-view `latClass`
 *  palette: <80ms green, <200ms amber, ≥200ms red. Only offline /
 *  missing is gray. */
function latColor(ms: number): string {
  if (ms < 0)   return '#6b7280';
  if (ms < 80)  return '#22c55e';
  if (ms < 200) return '#d97706';
  return '#ef4444';
}

/** Online% → color per user spec. */
function onlineColor(pct: number): string {
  if (pct < 0)     return '#6b7280';
  if (pct === 0)   return '#6b7280';
  if (pct >= 1)    return '#22c55e';
  if (pct >= 0.75) return '#86efac';
  if (pct >= 0.35) return '#eab308';
  return '#ef4444';
}

/** Bandwidth peak → color tier based on a windowed maximum so the same
 *  row reads as "relative intensity". Empty bucket → gray. */
function rateColor(bps: number, windowMax: number): string {
  if (bps < 0)  return '#6b7280';
  if (bps === 0 || windowMax === 0) return '#6b7280';
  const frac = bps / windowMax;
  if (frac >= 0.66) return '#1d4ed8';
  if (frac >= 0.33) return '#3b82f6';
  if (frac >= 0.10) return '#60a5fa';
  return '#93c5fd';
}

/** Format bytes/sec as "1.2 MB/s" etc. */
function fmtRate(bps: number): string {
  if (bps < 0) return '—';
  if (bps === 0) return '0 B/s';
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s'];
  let v = bps;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return v.toFixed(v < 10 ? 1 : 0) + ' ' + units[i];
}

/** Format a byte total as "14.2 MB" etc. */
function fmtBytes(bytes: number): string {
  if (bytes < 0) return '—';
  if (bytes === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = bytes;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return v.toFixed(v < 10 ? 1 : 0) + ' ' + units[i];
}

/** "N <unit> ago" / "now" label. Each tick is exactly one unit of the
 *  selected precision, so the spans-ago math is just (total-1) - idx. */
function bucketAgeLabel(idx: number, total: number, prec: Precision): string {
  const ago = total - 1 - idx;
  if (ago <= 0) return 'now';
  switch (prec) {
    case 'm1': return `${ago} min ago`;
    case 'h1': return `${ago} h ago`;
    case 'd1': return `${ago} d ago`;
  }
}

interface PrecisionTabsProps {
  value: Precision;
  onChange: (p: Precision) => void;
  labels: Record<Precision, string>;
}
function PrecisionTabs({ value, onChange, labels }: PrecisionTabsProps) {
  const tabs: Precision[] = ['m1', 'h1', 'd1'];
  return (
    <div className="hy-topo-pathinfo-precision-tabs" role="tablist">
      {tabs.map(p => (
        <button
          key={p}
          type="button"
          role="tab"
          aria-selected={value === p}
          className={`hy-topo-pathinfo-precision-tab${value === p ? ' active' : ''}`}
          onClick={() => onChange(p)}
        >
          {labels[p]}
        </button>
      ))}
    </div>
  );
}

interface TickProps {
  color: string;
  ageLabel: string;
  valueLabel: string;
}
function Tick({ color, ageLabel, valueLabel }: TickProps) {
  return (
    <Tooltip
      content={
        <div className="hy-topo-pathinfo-tooltip">
          <span className="hy-topo-pathinfo-tooltip-age">{ageLabel}</span>
          <span className="hy-topo-pathinfo-tooltip-value">{valueLabel}</span>
        </div>
      }
    >
      <span className="hy-topo-pathinfo-statusbar-tick" style={{ color }} />
    </Tooltip>
  );
}

interface StatusBarProps {
  ticks: TickProps[];
  ariaLabel: string;
  innerRef?: React.RefObject<HTMLDivElement>;
}
function StatusBar({ ticks, ariaLabel, innerRef }: StatusBarProps) {
  return (
    <div
      ref={innerRef}
      className="hy-topo-pathinfo-statusrow-bars"
      role="img"
      aria-label={ariaLabel}
    >
      {ticks.map((t, i) => <Tick key={i} {...t} />)}
    </div>
  );
}

export function PathInfoExpand({
  latency, online, txPeak, rxPeak,
  perHopLatencyMs, realtimeUpBps, realtimeDownBps, totalUpBytes, totalDownBytes,
}: PathInfoExpandProps) {
  const { t } = useTranslation();
  const [latPrec, setLatPrec] = useState<Precision>('m1');
  const [onlinePrec, setOnlinePrec] = useState<Precision>('m1');
  const [txPrec, setTxPrec] = useState<Precision>('m1');
  const [rxPrec, setRxPrec] = useState<Precision>('m1');

  // Real adaptive count: ResizeObserver on the first bar row measures
  // available width; the derived `visibleCount` is the number of ticks
  // that fit at TICK_WIDTH_PX + GAP_PX per slot. All four bar rows
  // share the same panel width, so we measure once and slice each
  // section's data array to the newest `visibleCount` entries.
  const firstRowRef = useRef<HTMLDivElement>(null);
  const [visibleCount, setVisibleCount] = useState(BUCKETS_PER_ROW);
  useLayoutEffect(() => {
    if (!firstRowRef.current) return;
    const recompute = (w: number) => {
      // Each tick consumes TICK_WIDTH_PX + GAP_PX of horizontal space
      // EXCEPT the last one which has no trailing gap; add GAP_PX back
      // to the available width to account for that.
      const fit = Math.max(1, Math.floor((w + GAP_PX) / (TICK_WIDTH_PX + GAP_PX)));
      setVisibleCount(Math.min(BUCKETS_PER_ROW, fit));
    };
    recompute(firstRowRef.current.getBoundingClientRect().width);
    const obs = new ResizeObserver(entries => {
      recompute(entries[0].contentRect.width);
    });
    obs.observe(firstRowRef.current);
    return () => obs.disconnect();
  }, []);
  /** Slice the newest `visibleCount` buckets from an array. The
   *  buckets are stored oldest-on-the-left, so .slice(-N) gives us
   *  the most-recent N — exactly what an operator wants to see. */
  const tail = <T,>(arr: T[]) => arr.slice(-visibleCount);

  const precLabels: Record<Precision, string> = {
    m1: t('nodes.graph.precisionMinute'),
    h1: t('nodes.graph.precisionHour'),
    d1: t('nodes.graph.precisionDay'),
  };

  // Build ticks from only the most-recent `visibleCount` buckets. The
  // bucketAgeLabel total stays at BUCKETS_PER_ROW so "X min ago" math
  // remains aligned to the full bucket window — index `i` here is the
  // position WITHIN the visible slice, and the displayed slot equals
  // (BUCKETS_PER_ROW - visibleCount + i), so we shift accordingly.
  const ageShift = BUCKETS_PER_ROW - visibleCount;
  const latTicks: TickProps[] = tail(latency[latPrec]).map((b, i) => ({
    color: latColor(b.ms),
    ageLabel: bucketAgeLabel(i + ageShift, BUCKETS_PER_ROW, latPrec),
    valueLabel: b.ms < 0 ? 'offline' : b.ms + ' ms',
  }));
  const onlineTicks: TickProps[] = tail(online[onlinePrec]).map((b, i) => ({
    color: onlineColor(b.pct),
    ageLabel: bucketAgeLabel(i + ageShift, BUCKETS_PER_ROW, onlinePrec),
    valueLabel: b.pct < 0 ? 'no data' : Math.round(b.pct * 100) + '%',
  }));
  const txWinMax = Math.max(0, ...txPeak[txPrec].map(b => b.bps));
  const txTicks: TickProps[] = tail(txPeak[txPrec]).map((b, i) => ({
    color: rateColor(b.bps, txWinMax),
    ageLabel: bucketAgeLabel(i + ageShift, BUCKETS_PER_ROW, txPrec),
    valueLabel: fmtRate(b.bps),
  }));
  const rxWinMax = Math.max(0, ...rxPeak[rxPrec].map(b => b.bps));
  const rxTicks: TickProps[] = tail(rxPeak[rxPrec]).map((b, i) => ({
    color: rateColor(b.bps, rxWinMax),
    ageLabel: bucketAgeLabel(i + ageShift, BUCKETS_PER_ROW, rxPrec),
    valueLabel: fmtRate(b.bps),
  }));

  return (
    <div className="hy-topo-pathinfo-expanded">
      {/* Info zone — per-hop latency breakdown + realtime / total
          traffic. Visually separated from the status bars below. */}
      <div className="hy-topo-pathinfo-info">
        <div className="hy-topo-pathinfo-info-row">
          <span className="hy-topo-pathinfo-info-label">{t('nodes.graph.infoHops')}</span>
          <span className="hy-topo-pathinfo-info-hopsum">
            {perHopLatencyMs.length === 0 ? (
              <span style={{ color: '#6b7280' }}>—</span>
            ) : perHopLatencyMs.map((ms, i) => (
              <span key={i}>
                {i > 0 && <span className="hy-topo-pathinfo-info-plus">+</span>}
                <span style={{ color: latColor(ms) }}>{ms < 0 ? '—' : `${ms}ms`}</span>
              </span>
            ))}
          </span>
        </div>
        <div className="hy-topo-pathinfo-info-row">
          <span className="hy-topo-pathinfo-info-label">{t('nodes.graph.infoSpeed')}</span>
          <span className="hy-topo-pathinfo-info-vals">
            <span className="hy-topo-pathinfo-info-arrow" style={{ color: '#2563eb' }}>↑</span>
            <span className="hy-topo-pathinfo-info-val">{fmtRate(realtimeUpBps)}</span>
            <span className="hy-topo-pathinfo-info-arrow" style={{ color: '#22c55e', marginLeft: 10 }}>↓</span>
            <span className="hy-topo-pathinfo-info-val">{fmtRate(realtimeDownBps)}</span>
          </span>
        </div>
        <div className="hy-topo-pathinfo-info-row">
          <span className="hy-topo-pathinfo-info-label">{t('nodes.graph.infoTotal')}</span>
          <span className="hy-topo-pathinfo-info-vals">
            <span className="hy-topo-pathinfo-info-arrow" style={{ color: '#2563eb' }}>↑</span>
            <span className="hy-topo-pathinfo-info-val">{fmtBytes(totalUpBytes)}</span>
            <span className="hy-topo-pathinfo-info-arrow" style={{ color: '#22c55e', marginLeft: 10 }}>↓</span>
            <span className="hy-topo-pathinfo-info-val">{fmtBytes(totalDownBytes)}</span>
          </span>
        </div>
      </div>

      {/* Status bars zone — 4 metrics × 24 time-buckets each */}
      <div className="hy-topo-pathinfo-section">
        <div className="hy-topo-pathinfo-section-header">
          <span className="hy-topo-pathinfo-section-title">{t('nodes.graph.metricLatency')}</span>
          <PrecisionTabs value={latPrec} onChange={setLatPrec} labels={precLabels} />
        </div>
        <StatusBar ticks={latTicks} ariaLabel={t('nodes.graph.metricLatency')} innerRef={firstRowRef} />
      </div>

      <div className="hy-topo-pathinfo-section">
        <div className="hy-topo-pathinfo-section-header">
          <span className="hy-topo-pathinfo-section-title">{t('nodes.graph.metricOnline')}</span>
          <PrecisionTabs value={onlinePrec} onChange={setOnlinePrec} labels={precLabels} />
        </div>
        <StatusBar ticks={onlineTicks} ariaLabel={t('nodes.graph.metricOnline')} />
      </div>

      <div className="hy-topo-pathinfo-section">
        <div className="hy-topo-pathinfo-section-header">
          <span className="hy-topo-pathinfo-section-title">{t('nodes.graph.metricTxPeak')}</span>
          <PrecisionTabs value={txPrec} onChange={setTxPrec} labels={precLabels} />
        </div>
        <StatusBar ticks={txTicks} ariaLabel={t('nodes.graph.metricTxPeak')} />
      </div>

      <div className="hy-topo-pathinfo-section">
        <div className="hy-topo-pathinfo-section-header">
          <span className="hy-topo-pathinfo-section-title">{t('nodes.graph.metricRxPeak')}</span>
          <PrecisionTabs value={rxPrec} onChange={setRxPrec} labels={precLabels} />
        </div>
        <StatusBar ticks={rxTicks} ariaLabel={t('nodes.graph.metricRxPeak')} />
      </div>
    </div>
  );
}

/** Concept-stage mock generator. Replaced by real /api/diag/peer-history
 *  buckets once the backend lands. Deterministic via seeded LCG so
 *  screenshots stay reproducible. */
export function mockPathHistory(seed: number): PathInfoExpandProps {
  let s = seed | 0;
  const rand = () => { s = (s * 1664525 + 1013904223) | 0; return ((s >>> 0) % 10000) / 10000; };

  const N = BUCKETS_PER_ROW;
  const latRow = (baseMs: number, jitter: number, dropRate: number): LatBucket[] => {
    const out: LatBucket[] = [];
    for (let i = 0; i < N; i++) {
      if (rand() < dropRate) out.push({ ms: -1 });
      else out.push({ ms: Math.max(1, Math.round(baseMs + (rand() - 0.5) * 2 * jitter)) });
    }
    return out;
  };
  const onlineRow = (dropRate: number): OnlineBucket[] => {
    const out: OnlineBucket[] = [];
    for (let i = 0; i < N; i++) {
      const dip = Math.sin(i / Math.max(2, N / 6)) * 0.15 + (rand() - 0.5) * 0.1;
      const pct = Math.max(0, Math.min(1, 1 - dropRate * 1.5 - Math.max(0, dip)));
      out.push({ pct });
    }
    return out;
  };
  const rateRow = (peakBps: number, idleRate: number): RateBucket[] => {
    const out: RateBucket[] = [];
    for (let i = 0; i < N; i++) {
      if (rand() < idleRate) { out.push({ bps: 0 }); continue; }
      const wave = (Math.sin(i / Math.max(2, N / 5)) + 1) / 2;
      const noise = rand() * 0.4;
      const bps = Math.round(peakBps * (wave * 0.7 + noise * 0.3));
      out.push({ bps });
    }
    return out;
  };

  return {
    latency: {
      m1: latRow(22, 8,  0.0),
      h1: latRow(40, 25, 0.05),
      d1: latRow(80, 60, 0.10),
    },
    online: {
      m1: onlineRow(0.0),
      h1: onlineRow(0.04),
      d1: onlineRow(0.12),
    },
    txPeak: {
      m1: rateRow(8_000_000,  0.10),
      h1: rateRow(12_000_000, 0.15),
      d1: rateRow(50_000_000, 0.20),
    },
    rxPeak: {
      m1: rateRow(25_000_000, 0.05),
      h1: rateRow(40_000_000, 0.10),
      d1: rateRow(90_000_000, 0.15),
    },
    // Info-zone mocks. Caller (NodesGraphView) overrides these with
    // real per-hop / final-hop values when they're available.
    perHopLatencyMs: [],
    realtimeUpBps: 0,
    realtimeDownBps: 0,
    totalUpBytes: 0,
    totalDownBytes: 0,
  };
}
