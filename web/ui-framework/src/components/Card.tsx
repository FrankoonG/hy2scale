import { type ReactNode, type CSSProperties } from 'react';
import clsx from 'clsx';

export interface CardProps {
  title?: ReactNode;
  count?: number;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
  noPadding?: boolean;
  /** Legacy: panels now take their content's height (kept for callers). */
  fill?: number | boolean;
  style?: CSSProperties;
}

export function Card({ title, count, actions, children, className, noPadding, fill, style }: CardProps) {
  return (
    <div className={clsx('hy-card', noPadding && 'no-pad', fill && 'fill', className)} style={style}>
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
