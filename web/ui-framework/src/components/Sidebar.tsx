import { type ReactNode } from 'react';
import clsx from 'clsx';

export interface SidebarItem {
  key: string;
  label: string;
  icon: ReactNode;
}

export interface SidebarProps {
  items: SidebarItem[];
  activeKey: string;
  onSelect: (key: string) => void;
  mobile?: boolean;
  onClose?: () => void;
  logo?: ReactNode;
  footer?: ReactNode;
}

export function Sidebar({ items, activeKey, onSelect, mobile, onClose, logo, footer }: SidebarProps) {
  return (
    <>
      {mobile && <div className="hy-modal-overlay" style={{ zIndex: 99 }} onClick={onClose} />}
      <aside className={clsx('hy-sidebar', mobile && 'open')}>
        {logo && <div className="hy-sidebar-logo">{logo}</div>}
        <nav className="hy-sidebar-nav">
          {items.map((item) => (
            <button
              key={item.key}
              className={clsx('hy-sidebar-item', activeKey === item.key && 'active')}
              onClick={() => { onSelect(item.key); onClose?.(); }}
            >
              {item.icon}
              {item.label}
            </button>
          ))}
        </nav>
        {footer && <div className="hy-sidebar-footer">{footer}</div>}
      </aside>
    </>
  );
}
