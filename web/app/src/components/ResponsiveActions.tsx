import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { ActionBar } from '@hy2scale/ui';

interface Props {
  /** Selected rows (0 = none). */
  selectedCount?: number;
  onClearSelection?: () => void;
  /** Overrides the "N selected" text. */
  selectedLabel?: string;
  /** Action buttons (inline when wide, overflow menu when narrow). */
  children?: ReactNode;
  /** View options (behind a trailing button when wide). */
  more?: ReactNode;
}

/** The framework ActionBar with this app's translated labels. */
export default function ResponsiveActions({ selectedCount = 0, onClearSelection, selectedLabel, children, more }: Props) {
  const { t } = useTranslation();
  return (
    <ActionBar
      selectedCount={selectedCount}
      onClearSelection={onClearSelection}
      more={more}
      labels={{
        selected: selectedLabel ?? t('app.selected', { count: selectedCount }),
        clear: t('app.clearSelection'),
        actions: t('app.actions'),
        more: t('app.viewOptions'),
      }}
    >
      {children}
    </ActionBar>
  );
}
