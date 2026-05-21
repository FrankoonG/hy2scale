// Status-graph rows for the bottom-left selected-path overlay.
//
// Data flows from `/api/diag/peer-history` (the in-process recorder in
// internal/history records 1m/1h/1d rings per direct peer). The caller
// converts the backend Snapshot shape into the metric-typed buckets
// this component renders.
//
// Bar semantics: each tick represents one unit of the SELECTED precision —
//   1m → each tick = 1 minute  (last 60 minutes ago)
//   1h → each tick = 1 hour    (last 60 hours ago)
//   1d → each tick = 1 day     (last 60 days ago, the retention horizon)
// Newer time is on the right. The visible tick count adapts to the
// panel's width (ResizeObserver-driven), with a hard cap at
// BUCKETS_PER_ROW.
//
// Color literals (NOT vars) so Dark Reader picks them up reliably — see
// docs/dark-reader-testing.md.

import { useState, useRef, useLayoutEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { Tooltip } from '@hy2scale/ui';

/** Loose alias for the i18next translate function returned by
 *  useTranslation(); matches the runtime call signatures we use here
 *  (key-only, key+interpolation) without depending on the deeper
 *  generics react-i18next exposes. */
type Tx = (key: string, opts?: Record<string, unknown>) => string;

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

/** Online% → color per user spec.
 *
 * Sensitivity bias: any sample-level offline event in the bucket window
 * should be visually obvious. Previously pct=0 collided with pct<0 on
 * gray (so fully-offline read the same as "no data"), and the
 * intermediate tiers were loose enough that 11-of-12 online (= one
 * dropped probe) still painted light-green. New tiers:
 *
 *   pct < 0   → gray   (no data — fresh recorder, no samples yet)
 *   pct = 0   → red    (every sample offline — was gray, BUG)
 *   pct = 1   → green  (every sample online — unchanged)
 *   pct ≥ .95 → light green (≤ 1 dropped probe per 20)
 *   pct ≥ .50 → amber  (any meaningful flapping)
 *   pct < .50 → red    (mostly offline)
 */
function onlineColor(pct: number): string {
  if (pct < 0)     return '#6b7280';
  if (pct === 0)   return '#ef4444';
  if (pct >= 1)    return '#22c55e';
  if (pct >= 0.95) return '#86efac';
  if (pct >= 0.50) return '#eab308';
  return '#ef4444';
}

/** Bandwidth peak → color tier based on a windowed maximum so the same
 *  row reads as "relative intensity". Gray is reserved for missing data
 *  only; a real 0 bps bucket renders as the faintest blue (peer online
 *  but idle) — same as the "below 10% of window max" tier. */
function rateColor(bps: number, windowMax: number): string {
  if (bps < 0) return '#6b7280';
  if (windowMax === 0) return '#93c5fd';
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

/** Build a `bucketAgeLabel(idx, total, prec) → string` closure bound to
 *  the active i18n function. Each tick is exactly one unit of the
 *  selected precision, so the spans-ago math is just (total-1) - idx. */
function makeBucketAgeLabel(t: Tx) {
  return (idx: number, total: number, prec: Precision): string => {
    const ago = total - 1 - idx;
    if (ago <= 0) return t('nodes.graph.tooltipNow');
    switch (prec) {
      case 'm1': return t('nodes.graph.tooltipAgoMin',  { n: ago });
      case 'h1': return t('nodes.graph.tooltipAgoHour', { n: ago });
      case 'd1': return t('nodes.graph.tooltipAgoDay',  { n: ago });
    }
  };
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
  const precLabels: Record<Precision, string> = {
    m1: t('nodes.graph.precisionMinute'),
    h1: t('nodes.graph.precisionHour'),
    d1: t('nodes.graph.precisionDay'),
  };
  const bucketAgeLabel = makeBucketAgeLabel(t as unknown as Tx);
  const labelOffline = t('nodes.offline');
  const labelNoData  = t('nodes.graph.tooltipNoData');

  // Build a row of `visibleCount` ticks. The right end is "now"; older
  // slots fall to the left. When the data array has fewer entries than
  // visibleCount, we pad the LEFT (older) side with gray placeholders
  // labeled "no data" — because gray-as-offline is a per-bucket signal,
  // not a stand-in for "this slot is before recording started".
  function buildRow<T>(
    data: T[],
    toColor: (b: T) => string,
    toValueLabel: (b: T) => string,
    prec: Precision,
  ): TickProps[] {
    const out: TickProps[] = [];
    const shown = data.slice(-visibleCount);
    const gap = Math.max(0, visibleCount - shown.length);
    for (let i = 0; i < gap; i++) {
      out.push({
        color: '#6b7280',
        ageLabel: bucketAgeLabel(i, visibleCount, prec),
        valueLabel: labelNoData,
      });
    }
    for (let i = 0; i < shown.length; i++) {
      out.push({
        color: toColor(shown[i]),
        ageLabel: bucketAgeLabel(gap + i, visibleCount, prec),
        valueLabel: toValueLabel(shown[i]),
      });
    }
    return out;
  }

  const latTicks    = buildRow(latency[latPrec],  b => latColor(b.ms),
                                b => b.ms < 0 ? labelOffline : b.ms + ' ms', latPrec);
  const onlineTicks = buildRow(online[onlinePrec], b => onlineColor(b.pct),
                                b => b.pct < 0 ? labelNoData : Math.round(b.pct * 100) + '%', onlinePrec);
  // Window-max excludes negatives (placeholders / missing samples) so
  // the colour scale is set by real observed peaks only.
  const txWinMax = Math.max(0, ...txPeak[txPrec].map(b => Math.max(0, b.bps)));
  const txTicks  = buildRow(txPeak[txPrec], b => rateColor(b.bps, txWinMax), b => fmtRate(b.bps), txPrec);
  const rxWinMax = Math.max(0, ...rxPeak[rxPrec].map(b => Math.max(0, b.bps)));
  const rxTicks  = buildRow(rxPeak[rxPrec], b => rateColor(b.bps, rxWinMax), b => fmtRate(b.bps), rxPrec);

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

