import { type ReactNode } from 'react';
import clsx from 'clsx';

export interface TabItem {
  key: string;
  label: string;
  disabled?: boolean;
}

export interface TabsProps {
  items: TabItem[];
  activeKey: string;
  onChange: (key: string) => void;
  variant?: 'button-group' | 'underline';
  className?: string;
  /** Content rendered next to the tab bar (e.g. action buttons) */
  addon?: ReactNode;
}

export function Tabs({ items, activeKey, onChange, variant = 'button-group', className, addon }: TabsProps) {
  return (
    <div className="hy-tabs-row">
      <div className={clsx('hy-tabs', variant === 'underline' && 'underline', className)}>
        {items.map((item) => (
          <button
            key={item.key}
            className={clsx('hy-tab', activeKey === item.key && 'active')}
            disabled={item.disabled}
            onClick={() => onChange(item.key)}
          >
            {item.label}
          </button>
        ))}
      </div>
      {addon}
    </div>
  );
}
