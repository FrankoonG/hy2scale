import { type ReactNode } from 'react';

export interface StatItem {
  label: string;
  value: ReactNode;
  sub?: string;
}

export interface StatsGridProps {
  items: StatItem[];
}

export function StatsGrid({ items }: StatsGridProps) {
  return (
    <div className="hy-stats-grid">
      {items.map((item, i) => (
        <div key={i} className="hy-stat-card">
          <div className="hy-stat-label">{item.label}</div>
          <div className="hy-stat-value">{item.value}</div>
          {item.sub && <div className="hy-stat-sub">{item.sub}</div>}
        </div>
      ))}
    </div>
  );
}
