import { type InputHTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';

export interface ToggleProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'type' | 'size'> {
  size?: 'sm' | 'md';
  /** Text beside the switch (the whole row toggles). */
  label?: ReactNode;
  /** Lines the switch up with an input beside it (6px top padding). */
  offset?: boolean;
}

export function Toggle({ size = 'md', className, label, offset, ...props }: ToggleProps) {
  const sw = (
    <label className={clsx('hy-toggle', size === 'sm' && 'sm', !label && className)}>
      <input type="checkbox" {...props} />
      <span className="slider" />
    </label>
  );
  if (!label) return offset ? <div className="hy-toggle-offset">{sw}</div> : sw;
  return (
    <label className={clsx('hy-toggle-row', size === 'sm' && 'sm', className)}>
      <span className={clsx('hy-toggle', size === 'sm' && 'sm')}>
        <input type="checkbox" {...props} />
        <span className="slider" />
      </span>
      <span className="hy-toggle-label">{label}</span>
    </label>
  );
}
