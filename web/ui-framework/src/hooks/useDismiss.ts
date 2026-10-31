import { useEffect, useRef, type RefObject } from 'react';

export interface DismissOptions {
  /** Listen only while true (default true). */
  enabled?: boolean;
  /** Escape also dismisses (default true). */
  escape?: boolean;
}

/**
 * Calls `onDismiss` on a pointer press outside every element in `refs`
 * (e.g. a trigger and its portalled popover) and, optionally, on Escape.
 */
export function useDismiss(
  refs: RefObject<HTMLElement | null> | RefObject<HTMLElement | null>[],
  onDismiss: () => void,
  { enabled = true, escape = true }: DismissOptions = {},
) {
  const cb = useRef(onDismiss);
  cb.current = onDismiss;
  const list = Array.isArray(refs) ? refs : [refs];
  const listRef = useRef(list);
  listRef.current = list;

  useEffect(() => {
    if (!enabled) return;
    const outside = (e: MouseEvent | TouchEvent) => {
      const t = e.target as Node;
      if (listRef.current.some((r) => r.current?.contains(t))) return;
      cb.current();
    };
    // Escape is consumed here: a dialog around the popover stays open.
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape' && !e.defaultPrevented) { e.preventDefault(); cb.current(); } };
    document.addEventListener('mousedown', outside);
    document.addEventListener('touchstart', outside);
    if (escape) document.addEventListener('keydown', key);
    return () => {
      document.removeEventListener('mousedown', outside);
      document.removeEventListener('touchstart', outside);
      if (escape) document.removeEventListener('keydown', key);
    };
  }, [enabled, escape]);
}
