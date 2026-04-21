import { type ReactNode, type CSSProperties } from 'react';
import clsx from 'clsx';

export interface CardProps {
  title?: ReactNode;
  count?: number;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
  noPadding?: boolean;
  /**
   * When set, the card flexes to share remaining height with sibling cards
   * inside a `.hy-page` container. `fill={1}` + `fill={2}` gives a 1:2 split.
   * The card's body scrolls internally; page-level scrolling is avoided.
   */
  fill?: number | boolean;
}

export function Card({ title, count, actions, children, className, noPadding, fill }: CardProps) {
  const style: CSSProperties | undefined = fill
    ? { flex: `${typeof fill === 'number' ? fill : 1} 1 0` }
    : undefined;
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
