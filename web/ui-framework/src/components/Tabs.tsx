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
}

export function Tabs({ items, activeKey, onChange, variant = 'button-group', className }: TabsProps) {
  return (
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
  );
}
