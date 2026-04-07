import { type ReactNode } from 'react';
import clsx from 'clsx';

export interface CardProps {
  title?: ReactNode;
  count?: number;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
  noPadding?: boolean;
}

export function Card({ title, count, actions, children, className, noPadding }: CardProps) {
  return (
    <div className={clsx('hy-card', noPadding && 'no-pad', className)}>
      {(title || actions) && (
        <div className="hy-card-header">
          <span className="hy-card-title">{title}</span>
          {count !== undefined && <span className="hy-card-count">{count}</span>}
          {actions && <div className="hy-card-actions">{actions}</div>}
        </div>
      )}
      <div className="hy-card-body">{children}</div>
    </div>
  );
}
