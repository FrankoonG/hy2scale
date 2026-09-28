import { useCallback, useEffect, useRef } from 'react';

// Form state is seeded from server data exactly when the operator starts
// editing — never again because a query refetched. Polling (users list,
// 30 s traffic flush), reconnect refetches and other sections' saves all
// produce new data objects; re-seeding on those overwrote whatever had been
// typed but not saved yet.

/**
 * Dialogs: run `init` when the dialog opens, or when it is re-targeted to a
 * different record (`key` changes) while open.
 */
export function useDialogInit(open: boolean, key: unknown, init: () => void) {
  const prev = useRef<{ open: boolean; key: unknown }>({ open: false, key: undefined });
  const fn = useRef(init);
  fn.current = init;
  useEffect(() => {
    const p = prev.current;
    prev.current = { open, key };
    if (open && (!p.open || p.key !== key)) fn.current();
  }, [open, key]);
}

interface ServerQuery<T> {
  data: T | undefined;
  isFetchedAfterMount: boolean;
  dataUpdatedAt: number;
}

/**
 * Page forms: run `init` once, with the first snapshot fetched after mount
 * (not a possibly stale cache entry). The returned `resync` re-seeds from the
 * next fetch — call it after this form's own successful save so server-side
 * normalisation shows up.
 */
export function useServerFormInit<T>(q: ServerQuery<T>, init: (data: T) => void) {
  const done = useRef(false);
  const resyncAfter = useRef(0);
  const fn = useRef(init);
  fn.current = init;
  const { data, isFetchedAfterMount, dataUpdatedAt } = q;
  useEffect(() => {
    if (data == null || !isFetchedAfterMount) return;
    if (!done.current || (resyncAfter.current && dataUpdatedAt > resyncAfter.current)) {
      done.current = true;
      resyncAfter.current = 0;
      fn.current(data);
    }
  }, [data, isFetchedAfterMount, dataUpdatedAt]);
  return useCallback(() => { resyncAfter.current = Date.now(); }, []);
}
