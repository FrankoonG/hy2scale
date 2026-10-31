import type { MouseEvent, ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@hy2scale/ui';

interface BulkProps<T> {
  /** The selected rows. */
  selected: T[];
  /** Enable / Disable buttons; shown for rows in the other state. */
  isEnabled?: (row: T) => boolean;
  onToggle?: (enabled: boolean) => void;
  onDelete?: () => void;
  /** Extra bulk buttons, between Disable and Delete. */
  children?: ReactNode;
}

/** Enable / Disable / … / Delete for the selected rows of a list. */
export function BulkButtons<T>({ selected, isEnabled, onToggle, onDelete, children }: BulkProps<T>) {
  const { t } = useTranslation();
  if (selected.length === 0) return null;
  const toggles = onToggle && isEnabled;
  return (
    <>
      {toggles && selected.some((r) => !isEnabled(r)) && <Button size="sm" onClick={() => onToggle(true)}>{t('app.bulkEnable')}</Button>}
      {toggles && selected.some((r) => isEnabled(r)) && <Button size="sm" onClick={() => onToggle(false)}>{t('app.bulkDisable')}</Button>}
      {children}
      {onDelete && <Button size="sm" variant="danger" onClick={onDelete}>{t('app.bulkDelete')}</Button>}
    </>
  );
}

/** The Edit button, shown while exactly one row is selected. */
export function EditSelectedButton<T>({ selected, onEdit }: { selected: T[]; onEdit: (row: T, e: MouseEvent) => void }) {
  const { t } = useTranslation();
  if (selected.length !== 1) return null;
  return (
    <Button size="sm" variant="success" data-testid="edit-selected-btn" onClick={(e: MouseEvent) => onEdit(selected[0], e)}>
      {t('app.edit')}
    </Button>
  );
}

/** Rows of `rows` whose key is in `keys`, in selection order. */
export function pickSelected<T>(rows: T[], keys: Set<string>, keyOf: (r: T) => string): T[] {
  const byKey = new Map(rows.map((r) => [keyOf(r), r]));
  return [...keys].map((k) => byKey.get(k)).filter((r): r is T => r !== undefined);
}
