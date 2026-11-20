import type { TFunction } from 'i18next';
import type { TrafficReset } from '@/api';

/** Today (local time) as YYYY-MM-DD. */
export function todayISO(): string {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/** A stored reset unit (the server also takes day / week / month). */
export function resetUnit(v?: string): TrafficReset {
  switch (v) {
    case 'daily': case 'day': return 'daily';
    case 'weekly': case 'week': return 'weekly';
    case 'monthly': case 'month': return 'monthly';
  }
  return '';
}

/** "Every 2 weeks"-style text for a reset setting ('' when never). */
export function resetText(t: TFunction, unit: TrafficReset, every?: number): string {
  if (!unit) return '';
  const n = every && every > 1 ? every : 1;
  return n === 1 ? t(`users.reset.${unit}`) : t(`users.resetEvery.${unit}`, { count: n });
}

/** The expiry date extending moves `expiry` to (as the server does): one
 * reset period on (a month without one), from today (UTC) when passed. */
export function extendedExpiry(expiry: string, unit: TrafficReset, every?: number): string {
  const n = unit && every && every > 1 ? every : 1;
  const now = new Date();
  const today = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());
  let base = Date.parse(expiry + 'T00:00:00Z');
  if (!(base > today)) base = today;
  const d = new Date(base);
  if (unit === 'daily' || unit === 'weekly') {
    d.setUTCDate(d.getUTCDate() + (unit === 'weekly' ? 7 : 1) * n);
  } else {
    const day = d.getUTCDate();
    const m = d.getUTCMonth() + (unit === 'monthly' ? n : 1);
    const last = new Date(Date.UTC(d.getUTCFullYear(), m + 1, 0)).getUTCDate();
    d.setUTCDate(1);
    d.setUTCMonth(m);
    d.setUTCDate(Math.min(day, last));
  }
  return d.toISOString().slice(0, 10);
}

/** "Extend by 2 months"-style text: the reset unit, a month without one. */
export function extendByText(t: TFunction, unit: TrafficReset, every?: number): string {
  const u = unit || 'monthly';
  const n = unit && every && every > 1 ? every : 1;
  return n === 1 ? t(`users.extendBy1.${u}`) : t(`users.extendBy.${u}`, { count: n });
}
