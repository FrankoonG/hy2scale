import { type ReactNode } from 'react';
import { useUiLabels } from './UiLabels';
import { IconMenu } from '../icons';

export interface TopbarProps {
  title?: ReactNode;
  badge?: ReactNode;
  children?: ReactNode;
  onMenuClick?: () => void;
  /** Replaces the default hamburger icon (e.g. the app logo). */
  menuIcon?: ReactNode;
}

export function Topbar({ title, badge, children, onMenuClick, menuIcon }: TopbarProps) {
  const L = useUiLabels();
  return (
    <div className="hy-topbar">
      <div className="hy-topbar-left">
        {onMenuClick && (
          <button className={menuIcon ? 'hy-hamburger hy-hamburger-logo' : 'hy-hamburger'} onClick={onMenuClick} aria-label={L.menu}>
            {menuIcon ?? <IconMenu />}
          </button>
        )}
        {title && <h1>{title}</h1>}
        {badge}
      </div>
      <div className="hy-topbar-right">{children}</div>
    </div>
  );
}
