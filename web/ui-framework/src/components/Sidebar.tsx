import { type ReactNode } from 'react';
import clsx from 'clsx';

export interface SidebarItem {
  key: string;
  label: string;
  icon: ReactNode;
  disabled?: boolean;
}

/** A labelled group of nav items. Omit `label` for an unlabelled group. */
export interface SidebarSection {
  key: string;
  label?: string;
  items: SidebarItem[];
}

export interface SidebarProps {
  /** Flat item list (used when `sections` is not given). */
  items?: SidebarItem[];
  /** Grouped items, each group under a small category label. */
  sections?: SidebarSection[];
  /** Items pinned to the bottom of the sidebar, above the collapse toggle. */
  bottomItems?: SidebarItem[];
  activeKey: string;
  onSelect: (key: string) => void;
  mobile?: boolean;
  onClose?: () => void;
  logo?: ReactNode;
  footer?: ReactNode;
  /** Icon-only rail. */
  collapsed?: boolean;
  /** Shows the collapse/expand toggle as the bottom-most item. */
  onToggleCollapse?: () => void;
  /** Accessible label / tooltip of the toggle. */
  toggleLabel?: string;
}

const toggleIcon = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <polyline points="11 17 6 12 11 7" />
    <polyline points="18 17 13 12 18 7" />
  </svg>
);

export function Sidebar({
  items, sections, bottomItems, activeKey, onSelect, mobile, onClose, logo, footer,
  collapsed, onToggleCollapse, toggleLabel,
}: SidebarProps) {
  const groups: SidebarSection[] = sections ?? [{ key: '_', items: items ?? [] }];

  const renderItem = (item: SidebarItem) => (
    <button
      key={item.key}
      type="button"
      className={clsx('hy-sidebar-item', activeKey === item.key && 'active', item.disabled && 'disabled')}
      onClick={() => { if (!item.disabled) { onSelect(item.key); onClose?.(); } }}
      style={item.disabled ? { opacity: 0.35, pointerEvents: 'none' } : undefined}
      title={collapsed ? item.label : undefined}
      aria-label={collapsed ? item.label : undefined}
      data-nav={item.key}
    >
      {item.icon}
      <span className="hy-sidebar-label">{item.label}</span>
    </button>
  );

  const hasBottom = (bottomItems && bottomItems.length > 0) || !!onToggleCollapse;

  return (
    <>
      {mobile && <div className="hy-modal-overlay" style={{ zIndex: 99 }} onClick={onClose} />}
      <aside className={clsx('hy-sidebar', mobile && 'open', collapsed && 'collapsed')}>
        {logo && <div className="hy-sidebar-logo">{logo}</div>}
        <nav className="hy-sidebar-nav">
          {groups.map((g) => (
            <div key={g.key} className="hy-sidebar-group">
              {g.label && <div className="hy-sidebar-section" aria-hidden={collapsed || undefined}>{g.label}</div>}
              {g.items.map(renderItem)}
            </div>
          ))}
        </nav>
        {hasBottom && (
          <div className="hy-sidebar-bottom">
            {bottomItems?.map(renderItem)}
            {onToggleCollapse && (
              <button
                type="button"
                className="hy-sidebar-item hy-sidebar-toggle"
                onClick={onToggleCollapse}
                title={toggleLabel}
                aria-label={toggleLabel}
                aria-expanded={!collapsed}
                data-testid="sidebar-toggle"
              >
                {toggleIcon}
              </button>
            )}
          </div>
        )}
        {footer && <div className="hy-sidebar-footer">{footer}</div>}
      </aside>
    </>
  );
}
