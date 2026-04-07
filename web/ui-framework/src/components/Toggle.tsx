import { type InputHTMLAttributes } from 'react';
import clsx from 'clsx';

export interface ToggleProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'type' | 'size'> {
  size?: 'sm' | 'md';
}

export function Toggle({ size = 'md', className, ...props }: ToggleProps) {
  return (
    <label className={clsx('hy-toggle', size === 'sm' && 'sm', className)}>
      <input type="checkbox" {...props} />
      <span className="slider" />
    </label>
  );
}
