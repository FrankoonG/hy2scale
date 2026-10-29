/** True when `date` (any Date.parse format) lies in the past. */
export function isPast(date?: string | null): boolean {
  if (!date) return false;
  const ts = Date.parse(date);
  return !Number.isNaN(ts) && ts < Date.now();
}
