import { type KeyboardEvent, type ReactNode } from 'react';
import clsx from 'clsx';

/** A label / value pair; `key` defaults to the index. */
export type DetailItem = [label: ReactNode, value: ReactNode] | { key?: string; label: ReactNode; value: ReactNode };

export interface DetailGridProps {
  items: DetailItem[];
  /**
   * boxed: tinted panel, mono values (record details) ·
   * plain: no frame, regular text (summary fields).
   */
  variant?: 'boxed' | 'plain';
  /** Fixed column count; default = as many ≥ minColumn wide as fit. */
  columns?: number;
  /** Minimum column width in px when `columns` is not set. */
  minColumn?: number;
  className?: string;
  testId?: string;
}

const norm = (it: DetailItem, i: number) =>
  Array.isArray(it) ? { key: String(i), label: it[0], value: it[1] } : { key: it.key ?? String(i), label: it.label, value: it.value };

/** Labels above values in a responsive grid. */
export function DetailGrid({ items, variant = 'boxed', columns, minColumn, className, testId }: DetailGridProps) {
  const style = columns
    ? { gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }
    : minColumn ? { gridTemplateColumns: `repeat(auto-fill, minmax(${minColumn}px, 1fr))` } : undefined;
  return (
    <div className={clsx('hy-detail', variant === 'plain' && 'plain', className)} style={style} data-testid={testId}>
      {items.map((it, i) => {
        const { key, label, value } = norm(it, i);
        return (
          <div key={key} className="hy-detail-item">
            <span className="hy-detail-label">{label}</span>
            <span className="hy-detail-value">{value}</span>
          </div>
        );
      })}
    </div>
  );
}

/** Small uppercase caption above a block (same look as DetailGrid labels). */
export function FieldLabel({ children }: { children: ReactNode }) {
  return <div className="hy-field-label">{children}</div>;
}

export interface KeyValueListProps {
  children: ReactNode;
  /** compact: tighter rows, every row underlined, values may wrap. */
  variant?: 'default' | 'compact';
  className?: string;
  testId?: string;
}

/** One label / value pair per row, value right-aligned. */
export function KeyValueList({ children, variant = 'default', className, testId }: KeyValueListProps) {
  return <div className={clsx('hy-kv', variant === 'compact' && 'compact', className)} data-testid={testId}>{children}</div>;
}

export interface KeyValueProps {
  label: ReactNode;
  children: ReactNode;
  mono?: boolean;
  /** Makes the row a link (keyboard accessible). */
  onClick?: () => void;
}

export function KeyValue({ label, children, mono, onClick }: KeyValueProps) {
  const link = onClick ? {
    role: 'button' as const,
    tabIndex: 0,
    onClick,
    onKeyDown: (e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onClick(); } },
  } : {};
  return (
    <div className={clsx('hy-kv-row', onClick && 'clickable')} {...link}>
      <span className="hy-kv-label">{label}</span>
      <span className={clsx('hy-kv-value', mono && 'mono')}>{children}</span>
    </div>
  );
}
