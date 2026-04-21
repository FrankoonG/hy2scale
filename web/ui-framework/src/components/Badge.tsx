import { type ReactNode } from 'react';
import clsx from 'clsx';

export interface BadgeProps {
  variant?: 'green' | 'red' | 'orange' | 'blue' | 'muted' | 'warn';
  children: ReactNode;
  className?: string;
}

export function Badge({ variant = 'muted', children, className }: BadgeProps) {
  return <span className={clsx('hy-badge', variant, className)}>{children}</span>;
}
