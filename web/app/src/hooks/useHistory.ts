import { useEffect, useState } from 'react';

/**
 * Rolling buffer of recent numeric samples. Appends `value` on each update
 * identified by `tick` (typically `dataUpdatedAt` from a React Query poll)
 * and caps at `max` points. Appending on a tick — rather than on value
 * change — ensures the sparkline advances even when a metric plateaus
 * (e.g. tx_rate staying at 0).
 */
export function useHistory(value: number | null | undefined, tick: number, max = 60): number[] {
  const [history, setHistory] = useState<number[]>([]);
  useEffect(() => {
    if (value == null || Number.isNaN(value) || !tick) return;
    setHistory((prev) => {
      const next = prev.length >= max ? prev.slice(prev.length - max + 1) : prev.slice();
      next.push(value);
      return next;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tick]);
  return history;
}
