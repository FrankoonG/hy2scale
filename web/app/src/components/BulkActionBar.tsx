import { type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

interface Props {
  count: number;
  onClear: () => void;
  children: ReactNode;
}

export default function BulkActionBar({ count, onClear, children }: Props) {
  const { t } = useTranslation();
  if (count === 0) return null;

  return (
    <div className="hy-bulk-bar">
      <span className="hy-bulk-count">{t('app.selected', { count })}</span>
      {children}
      <button className="hy-bulk-clear" onClick={onClear}>×</button>
    </div>
  );
}
