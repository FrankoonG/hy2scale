import { useEffect, useRef, useState, type ReactNode } from 'react';

interface Props {
  /**
   * Items selected via bulk selection. 0 = nothing selected. At wide
   * widths this turns into the usual "N selected [×]" badge; at narrow
   * widths it collapses to a compact blue "N" sitting left of the
   * overflow dropdown.
   */
  selectedCount?: number;
  /** Called when the user taps the × / clear-selection affordance. */
  onClearSelection?: () => void;
  /** Localised "N selected" text (wide mode). */
  selectedLabel?: string;
  /** Action buttons. Rendered inline when there's room, otherwise
   *  stacked vertically inside a dropdown triggered by a ⋯ button. */
  children: ReactNode;
}

/**
 * Card-header action toolbar that collapses into a ⋯ overflow dropdown
 * on narrow viewports. Keeps the selected-items badge visible either
 * way: a full "N selected" at wide widths, a compact blue "N" at narrow
 * widths — so the user can always see how many rows they've marked
 * without opening the menu.
 *
 * Threshold is viewport-based (<=640 px) which covers all common
 * phone widths and matches our CSS mobile breakpoint. A measurement-
 * based approach was considered but leads to thrash: the overflow
 * button then toggles on/off as buttons re-render with different
 * content during normal polling refreshes.
 */
export default function ResponsiveActions({ selectedCount = 0, onClearSelection, selectedLabel, children }: Props) {
  const [narrow, setNarrow] = useState(typeof window !== 'undefined' && window.innerWidth <= 640);
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const evaluate = () => setNarrow(window.innerWidth <= 640);
    evaluate();
    window.addEventListener('resize', evaluate);
    return () => window.removeEventListener('resize', evaluate);
  }, []);

  // Close the dropdown when clicking outside or pressing Escape.
  useEffect(() => {
    if (!open) return;
    const close = (ev: MouseEvent) => {
      if (!rootRef.current?.contains(ev.target as Node)) setOpen(false);
    };
    const esc = (ev: KeyboardEvent) => { if (ev.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', close);
    document.addEventListener('keydown', esc);
    return () => { document.removeEventListener('mousedown', close); document.removeEventListener('keydown', esc); };
  }, [open]);

  if (!narrow) {
    return (
      <div className="hy-actions-row">
        {selectedCount > 0 && (
          <span className="hy-bulk-bar">
            <span className="hy-bulk-count">{selectedLabel ?? `${selectedCount} selected`}</span>
            {onClearSelection && <button className="hy-bulk-clear" onClick={onClearSelection}>×</button>}
          </span>
        )}
        {children}
      </div>
    );
  }

  return (
    <div className="hy-actions-row hy-actions-row--narrow" ref={rootRef}>
      {selectedCount > 0 && (
        <button
          type="button"
          className="hy-actions-count"
          onClick={onClearSelection}
          title={selectedLabel ?? `${selectedCount} selected`}
        >
          {selectedCount}
        </button>
      )}
      <button
        type="button"
        className={`hy-actions-overflow${open ? ' open' : ''}`}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round">
          <circle cx="12" cy="5" r="1" fill="currentColor" stroke="none" />
          <circle cx="12" cy="12" r="1" fill="currentColor" stroke="none" />
          <circle cx="12" cy="19" r="1" fill="currentColor" stroke="none" />
        </svg>
      </button>
      {open && (
        // Intentionally NOT closing the menu on inner clicks. Some
        // children (e.g. ImportExportButton) own their own portal
        // dialog and rely on still being mounted when the user
        // triggers it — unmounting here would tear down the button's
        // state before the modal could open. Close via outside click
        // or Escape, handled by the effect above.
        <div className="hy-actions-menu">
          {children}
        </div>
      )}
    </div>
  );
}
