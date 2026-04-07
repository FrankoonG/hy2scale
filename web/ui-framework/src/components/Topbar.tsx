import { type ReactNode } from 'react';

export interface TopbarProps {
  title?: ReactNode;
  badge?: ReactNode;
  children?: ReactNode;
  onMenuClick?: () => void;
}

export function Topbar({ title, badge, children, onMenuClick }: TopbarProps) {
  return (
    <div className="hy-topbar">
      <div className="hy-topbar-left">
        {onMenuClick && (
          <button className="hy-hamburger" onClick={onMenuClick}>☰</button>
        )}
        {title && <h1>{title}</h1>}
        {badge}
      </div>
      <div className="hy-topbar-right">{children}</div>
    </div>
  );
}
