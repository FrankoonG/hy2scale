import { useEffect, useRef, type RefObject } from 'react';

// Subset of useSelection's return shape that this hook needs. Kept local
// (rather than importing SelectionState from ui-framework) because that
// type is the narrower Table-consumer contract and doesn't expose
// `clear` — which is exactly the field we need to call here.
interface SelectionLike {
  selected: Set<string>;
  clear: () => void;
}

// Clears a single-row selection when the user clicks anywhere considered
// "blank" — that is: outside the registered list region, not on a button
// or other interactive control, and not on a modal that's currently open.
//
// Only triggers when exactly one row is selected. Multi-select stays sticky
// because deselecting it on a stray outside click would silently throw away
// work the user spent clicks gathering.
//
// Usage: pass the selection returned by useSelection plus a ref to the
// element that bounds "inside the list" (typically the Card or table-wrap).
// Clicks inside that element won't deselect — the table's own row-click
// handlers govern that case (selectOnly already does the right thing).
export function useDeselectOnBlankClick(
  selection: SelectionLike,
  insideRef: RefObject<HTMLElement | null>,
) {
  // Latest selection captured in a ref so the document-level handler can
  // read live values without re-binding on every render. (`selection` is
  // a fresh object literal each render, which would otherwise churn the
  // useEffect dependency on every keystroke.)
  const selRef = useRef(selection);
  selRef.current = selection;

  useEffect(() => {
    const handler = (e: MouseEvent) => {
      const sel = selRef.current;
      // Only single-select gets the auto-clear; multi-select sticks.
      if (sel.selected.size !== 1) return;
      const target = e.target as HTMLElement | null;
      if (!target) return;
      const inside = insideRef.current;
      if (inside && inside.contains(target)) return;
      // Buttons / inputs / labels carry intent — they're either action-bar
      // entries that operate on the selection (Edit, Delete, Disable...)
      // or unrelated form controls; either way, deselecting under them
      // would surprise the user.
      if (target.closest('button, a, input, select, textarea, label')) return;
      // Modal overlay / content shouldn't deselect either: a click that
      // closed a modal should leave the underlying row selected so the
      // user can re-open Edit without re-selecting.
      if (target.closest('.hy-modal-overlay, .hy-modal')) return;
      sel.clear();
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [insideRef]);
}
