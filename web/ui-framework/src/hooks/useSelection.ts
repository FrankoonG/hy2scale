import { useState, useEffect, useCallback, useMemo } from 'react';

export function useSelection(allKeys: string[]) {
  const [selected, setSelected] = useState<Set<string>>(new Set());

  // Prune stale keys when allKeys changes (handles polling data refresh)
  const keySet = useMemo(() => new Set(allKeys), [allKeys]);
  useEffect(() => {
    setSelected((prev) => {
      const pruned = new Set<string>();
      for (const k of prev) {
        if (keySet.has(k)) pruned.add(k);
      }
      return pruned.size === prev.size ? prev : pruned;
    });
  }, [keySet]);

  const toggle = useCallback((key: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  // selectOnly drops every other selection and picks only this one. Bound
  // to row-body click in Table/TreeTable: clicking the blank area of a
  // row "focuses" that row exclusively, while the leading checkbox keeps
  // its additive multi-select semantics.
  const selectOnly = useCallback((key: string) => {
    setSelected(new Set([key]));
  }, []);

  const toggleAll = useCallback(() => {
    setSelected((prev) => {
      if (prev.size === allKeys.length && allKeys.length > 0) return new Set();
      return new Set(allKeys);
    });
  }, [allKeys]);

  const clear = useCallback(() => setSelected(new Set()), []);

  const count = selected.size;
  const isAllSelected = count > 0 && count === allKeys.length;
  const isSomeSelected = count > 0 && count < allKeys.length;

  return { selected, toggle, toggleAll, selectOnly, clear, isAllSelected, isSomeSelected, count };
}
