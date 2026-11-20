import { useEffect, useRef } from 'react';
import { useLocation } from 'react-router-dom';

/** What another page may ask of a list page it opens. */
export interface NavSelect {
  /** Row keys to select once they are listed. */
  select?: string | string[];
}

/**
 * Selects the rows another page asked for (navigation state { select }),
 * once they are listed; taken once per navigation.
 */
export function useNavSelect(keys: string[], selection: { selectKeys: (keys: string[]) => void }) {
  const location = useLocation();
  const pending = useRef<string[] | null>(null);
  useEffect(() => {
    const s = (location.state as NavSelect | null)?.select;
    pending.current = s == null ? null : Array.isArray(s) ? s : [s];
  }, [location.key]);
  useEffect(() => {
    const want = pending.current;
    if (!want) return;
    const have = want.filter((k) => keys.includes(k));
    if (!have.length) return;
    pending.current = null;
    selection.selectKeys(have);
  });
}
