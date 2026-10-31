import type { TFunction } from 'i18next';
import type { PunchStatus } from '@/api/types';

/** An address for display: hole-punched routes ("@punch:name#tag") read as "Punch route · tag". */
export function fmtPunchAddr(t: TFunction, a: string): string {
  return a.startsWith('@punch:')
    ? `${t('punch.route')}${a.includes('#') ? ` · ${a.slice(a.indexOf('#') + 1)}` : ''}`
    : a;
}

/** Hole-punch routes of peer `name`. */
export function punchesFor(list: PunchStatus[], name: string): PunchStatus[] {
  return list.filter((p) => p.addr === `@punch:${name}` || p.addr.startsWith(`@punch:${name}#`));
}
