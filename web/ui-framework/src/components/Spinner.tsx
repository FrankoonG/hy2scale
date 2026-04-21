import clsx from 'clsx';

export interface SpinnerProps {
  size?: 'sm' | 'md' | 'lg';
  className?: string;
}

export function Spinner({ size = 'md', className }: SpinnerProps) {
  return <div className={clsx('hy-spinner', size, className)} />;
}
