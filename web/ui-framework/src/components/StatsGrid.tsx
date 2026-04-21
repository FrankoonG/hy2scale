import { type ReactNode, useMemo, useId } from 'react';

export interface StatItem {
  label: string;
  value: ReactNode;
  sub?: string;
  /**
   * Historical numeric samples, oldest → newest. Renders a low-opacity
   * sparkline as the card background. Pass two series as `{up, down}`
   * for split metrics like upload/download — up is drawn in blue (matching
   * the `.stat-up` text color), down in green.
   */
  history?: number[] | { up?: number[]; down?: number[] };
  /** Custom accent color for a single-series spark (CSS color or var). */
  sparkColor?: string;
  /**
   * Formatter for the peak-value label shown in the top-right corner.
   * Receives the max across all history samples. When omitted, no peak
   * label is shown.
   */
  formatPeak?: (max: number) => string;
}

export interface StatsGridProps {
  items: StatItem[];
}

export function StatsGrid({ items }: StatsGridProps) {
  return (
    <div className="hy-stats-grid">
      {items.map((item, i) => {
        const peak = computePeak(item.history);
        const peakText = peak != null && item.formatPeak ? item.formatPeak(peak) : null;
        return (
          <div key={i} className="hy-stat-card">
            {item.history && <Spark history={item.history} color={item.sparkColor} />}
            {peakText && <div className="hy-stat-peak" title="peak">{peakText}</div>}
            <div className="hy-stat-label">{item.label}</div>
            <div className="hy-stat-value">{item.value}</div>
            {item.sub && <div className="hy-stat-sub">{item.sub}</div>}
          </div>
        );
      })}
    </div>
  );
}

function computePeak(h: StatItem['history']): number | null {
  if (!h) return null;
  if (Array.isArray(h)) return h.length ? Math.max(...h) : null;
  const all = [...(h.up || []), ...(h.down || [])];
  return all.length ? Math.max(...all) : null;
}

interface SparkProps {
  history: number[] | { up?: number[]; down?: number[] };
  color?: string;
}

function Spark({ history, color }: SparkProps) {
  const baseId = useId().replace(/:/g, '');
  const series = useMemo(() => {
    if (Array.isArray(history)) {
      return [{ data: history, color: color || 'var(--primary)' }];
    }
    // Up → blue (matches .stat-up text color); down → green (matches .stat-down).
    const out: { data: number[]; color: string }[] = [];
    if (history.up && history.up.length > 0) out.push({ data: history.up, color: 'var(--blue)' });
    if (history.down && history.down.length > 0) out.push({ data: history.down, color: 'var(--green)' });
    return out;
  }, [history, color]);

  if (series.length === 0 || series[0].data.length < 2) return null;

  const max = Math.max(1, ...series.flatMap((s) => s.data));
  const W = 100;
  const H = 40;
  // Cap the peak at 2/3 of card height so the line never reaches the very
  // top of the card — leaves visual room for label/value text above.
  const PEAK_H = H * (2 / 3);

  return (
    <div className="hy-stat-spark">
      <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none">
        {series.map((s, idx) => {
          const gid = `${baseId}-${idx}`;
          const n = s.data.length;
          const step = n > 1 ? W / (n - 1) : W;
          const pts = s.data.map((v, i) => `${(i * step).toFixed(2)},${(H - (v / max) * PEAK_H).toFixed(2)}`);
          const linePath = 'M ' + pts.join(' L ');
          const areaPath = linePath + ` L ${W},${H} L 0,${H} Z`;
          return (
            <g key={idx}>
              <defs>
                <linearGradient id={gid} x1="0" x2="0" y1="0" y2="1">
                  <stop offset="0" stopColor={s.color} stopOpacity="0.28" />
                  <stop offset="1" stopColor={s.color} stopOpacity="0" />
                </linearGradient>
              </defs>
              <path d={areaPath} fill={`url(#${gid})`} />
              <path d={linePath} fill="none" stroke={s.color} strokeOpacity="0.55" strokeWidth="1.2" vectorEffect="non-scaling-stroke" />
            </g>
          );
        })}
      </svg>
    </div>
  );
}
