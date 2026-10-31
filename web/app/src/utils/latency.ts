/** Latency colour tier shared by tables, details and the topology views. */
export type LatencyTier = 'good' | 'med' | 'bad';

/** < 80 ms good, < 200 ms medium, otherwise bad (ms > 0). */
export function latencyTier(ms: number): LatencyTier {
  return ms < 80 ? 'good' : ms < 200 ? 'med' : 'bad';
}

/** CSS class for a latency value: latency-{good,med,bad}; ≤ 0 → off / na. */
export function latencyClass(ms: number): string {
  if (ms < 0) return 'latency-off';
  if (ms === 0) return 'latency-na';
  return `latency-${latencyTier(ms)}`;
}

/** Path-panel class (lat-ok / lat-mid / lat-bad / lat-na) for a summed latency. */
export function pathLatencyClass(ms: number, offline = false): string {
  if (offline || ms < 0) return 'lat-bad';
  if (ms === 0) return 'lat-na';
  return ({ good: 'lat-ok', med: 'lat-mid', bad: 'lat-bad' } as const)[latencyTier(ms)];
}
