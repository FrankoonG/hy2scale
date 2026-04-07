import { useState, type ReactNode } from 'react';
import clsx from 'clsx';

export interface TooltipProps {
  content: ReactNode;
  placement?: 'top' | 'bottom';
  children: ReactNode;
}

export function Tooltip({ content, placement = 'bottom', children }: TooltipProps) {
  const [show, setShow] = useState(false);
  return (
    <span
      className="hy-tooltip-wrap"
      onMouseEnter={() => setShow(true)}
      onMouseLeave={() => setShow(false)}
    >
      {children}
      {show && content && (
        <div className={clsx('hy-tooltip', placement)}>{content}</div>
      )}
    </span>
  );
}
