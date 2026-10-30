import { useEffect, useRef, useState, type ReactNode } from 'react';
import { useDismiss } from '../hooks/useDismiss';
import { IconButton } from './Button';
import { IconEllipsisVertical, IconSlidersHorizontal, IconX } from '../icons';
import { ActionBarMenuContext } from './ExpandButton';

export interface ActionBarLabels {
  /** "N selected" (wide layout) and title of the narrow count pill. */
  selected?: string;
  /** Clear-selection button. */
  clear?: string;
  /** Narrow overflow button (all actions). */
  actions?: string;
  /** Wide "view options" button. */
  more?: string;
}

export interface ActionBarProps {
  /** Selected rows; 0 hides the selection badge. */
  selectedCount?: number;
  onClearSelection?: () => void;
  labels?: ActionBarLabels;
  /** Action buttons: inline when wide, stacked in an overflow menu when narrow. */
  children?: ReactNode;
  /** View options: behind a trailing button when wide, at the end of the
   *  overflow menu when narrow. */
  more?: ReactNode;
  /** Viewport width at or below which the bar collapses (default 640). */
  narrowAt?: number;
}

/**
 * Card-header toolbar. Wide: "N selected ×", the actions, and an optional
 * view-options button. Narrow (phones): a count pill and one overflow
 * button holding everything.
 *
 * The overflow menu deliberately stays open on clicks inside it: some
 * children (an import button) own a dialog that must stay mounted while it
 * opens. It closes on an outside press or Escape.
 */
export function ActionBar({ selectedCount = 0, onClearSelection, labels = {}, children, more, narrowAt = 640 }: ActionBarProps) {
  const [narrow, setNarrow] = useState(typeof window !== 'undefined' && window.innerWidth <= narrowAt);
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const evaluate = () => setNarrow(window.innerWidth <= narrowAt);
    evaluate();
    window.addEventListener('resize', evaluate);
    return () => window.removeEventListener('resize', evaluate);
  }, [narrowAt]);

  useDismiss(rootRef, () => setOpen(false), { enabled: open });

  const selectedText = labels.selected ?? `${selectedCount} selected`;

  if (!narrow) {
    return (
      <div className="hy-actions-row" ref={rootRef}>
        {selectedCount > 0 && (
          <span className="hy-bulk-bar">
            <span className="hy-bulk-count">{selectedText}</span>
            {onClearSelection && (
              <button type="button" className="hy-bulk-clear" onClick={onClearSelection}
                title={labels.clear} aria-label={labels.clear ?? 'Clear selection'}>
                <IconX size={12} strokeWidth={2.5} />
              </button>
            )}
          </span>
        )}
        {children}
        {more && (
          <span className="hy-actions-more">
            <IconButton icon={IconSlidersHorizontal} label={labels.more ?? 'View options'} variant="outline"
              className="hy-actions-overflow" active={open} aria-expanded={open} data-testid="actions-more"
              onClick={() => setOpen((v) => !v)} />
            {open && <div className="hy-actions-menu">{more}</div>}
          </span>
        )}
      </div>
    );
  }

  return (
    <div className="hy-actions-row hy-actions-row--narrow" ref={rootRef}>
      {selectedCount > 0 && (
        <button type="button" className="hy-actions-count" onClick={onClearSelection} title={selectedText}>
          {selectedCount}
        </button>
      )}
      <IconButton icon={IconEllipsisVertical} label={labels.actions ?? 'Actions'} variant="outline" iconSize={18}
        className="hy-actions-overflow" active={open} aria-expanded={open} onClick={() => setOpen((v) => !v)} />
      {open && (
        <div className="hy-actions-menu">
          <ActionBarMenuContext.Provider value>
            {children}
            {more && <div className="hy-actions-menu-sep" />}
            {more}
          </ActionBarMenuContext.Provider>
        </div>
      )}
    </div>
  );
}
