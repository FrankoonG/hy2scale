import clsx from 'clsx';

export interface ProgressProps {
  value: number;
  max?: number;
  variant?: 'default' | 'green' | 'orange' | 'red';
  className?: string;
}

export function Progress({ value, max = 100, variant = 'default', className }: ProgressProps) {
  const pct = Math.min(100, Math.max(0, (value / max) * 100));
  return (
    <div className={clsx('hy-progress', className)}>
      <div
        className={clsx('hy-progress-bar', variant !== 'default' && variant)}
        style={{ width: `${pct}%` }}
      />
    </div>
  );
}
