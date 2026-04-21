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
          <button className="hy-hamburger" onClick={onMenuClick} aria-label="menu">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
              <line x1="4" y1="7" x2="20" y2="7" />
              <line x1="4" y1="12" x2="20" y2="12" />
              <line x1="4" y1="17" x2="20" y2="17" />
            </svg>
          </button>
        )}
        {title && <h1>{title}</h1>}
        {badge}
      </div>
      <div className="hy-topbar-right">{children}</div>
    </div>
  );
}
