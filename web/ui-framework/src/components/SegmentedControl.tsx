import { type ReactNode } from 'react';
import clsx from 'clsx';

export interface SegmentedItem<K extends string = string> {
  key: K;
  label: ReactNode;
  /** Small number after the label. */
  count?: number;
  /** Colour of the selected segment: warn (orange) / danger (red). */
  tone?: 'warn' | 'danger';
  disabled?: boolean;
  testId?: string;
}

export interface SegmentedControlProps<K extends string = string> {
  items: SegmentedItem<K>[];
  value: K;
  onChange: (key: K) => void;
  className?: string;
}

/** A compact single-choice switch (range, level, mode …). */
export function SegmentedControl<K extends string = string>({ items, value, onChange, className }: SegmentedControlProps<K>) {
  return (
    <div className={clsx('hy-seg', className)} role="tablist">
      {items.map((it) => (
        <button
          key={it.key}
          type="button"
          role="tab"
          aria-selected={value === it.key}
          disabled={it.disabled}
          className={clsx(value === it.key && 'on', it.tone && `tone-${it.tone}`, it.count !== undefined && 'has-count')}
          onClick={() => onChange(it.key)}
          data-testid={it.testId}
        >
          {it.label}
          {it.count !== undefined && <b>{it.count}</b>}
        </button>
      ))}
    </div>
  );
}
