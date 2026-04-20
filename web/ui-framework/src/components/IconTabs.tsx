import { type ReactNode } from 'react';
import clsx from 'clsx';

export interface IconTabItem {
  key: string;
  icon: ReactNode;
  tooltip?: string;
}

export interface IconTabsProps {
  items: IconTabItem[];
  activeKey: string;
  onChange: (key: string) => void;
  className?: string;
}

/**
 * Tiny icon-only mini-tab switcher, sized to live beside a Card title/count
 * in the header's action area. Distinct from the full Tabs component — here
 * there are no text labels, just two/three compact icon buttons that read as
 * a unit (shared rounded border, segmented look).
 */
export function IconTabs({ items, activeKey, onChange, className }: IconTabsProps) {
  return (
    <div className={clsx('hy-icon-tabs', className)} role="tablist">
      {items.map((item) => (
        <button
          key={item.key}
          type="button"
          role="tab"
          aria-selected={activeKey === item.key}
          title={item.tooltip}
          className={clsx('hy-icon-tab', activeKey === item.key && 'active')}
          onClick={() => onChange(item.key)}
        >
          {item.icon}
        </button>
      ))}
    </div>
  );
}
