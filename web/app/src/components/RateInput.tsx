import { Input, Select } from '@hy2scale/ui';

// Rate limits are stored in bytes per second; the form edits them in a
// chosen unit.
export const RATE_UNITS = [
  { value: 'Kbps', bytes: 125 },
  { value: 'Mbps', bytes: 125_000 },
  { value: 'KB/s', bytes: 1024 },
  { value: 'MB/s', bytes: 1024 * 1024 },
] as const;

export type RateUnit = (typeof RATE_UNITS)[number]['value'];

const unitBytes = (u: RateUnit) => RATE_UNITS.find((x) => x.value === u)!.bytes;

export interface RateValue { amount: string; unit: RateUnit }

/** Form value for a stored rate: the largest unit it is a whole number of. */
export function rateToForm(bps: number | undefined, fallback: RateUnit = 'Mbps'): RateValue {
  if (!bps || bps <= 0) return { amount: '', unit: fallback };
  for (const u of ['Mbps', 'MB/s', 'Kbps', 'KB/s'] as RateUnit[]) {
    if (bps % unitBytes(u) === 0) return { amount: String(bps / unitBytes(u)), unit: u };
  }
  return { amount: String(+(bps / 1024).toFixed(3)), unit: 'KB/s' };
}

/** Stored rate (bytes per second, 0 = unlimited) for a form value. */
export function formToRate(v: RateValue): number {
  const n = parseFloat(v.amount);
  return n > 0 ? Math.round(n * unitBytes(v.unit)) : 0;
}

/** "12 Mbps"-style text for a stored rate. */
export function fmtRateLimit(bps: number): string {
  const v = rateToForm(bps);
  return `${v.amount} ${v.unit}`;
}

export function RateInput({ value, onChange, testId }: { value: RateValue; onChange: (v: RateValue) => void; testId?: string }) {
  return (
    <div className="hy-rate-input" data-testid={testId}>
      <Input type="number" min={0} step="any" value={value.amount} placeholder="0"
        onChange={(e) => onChange({ ...value, amount: e.target.value })} />
      <Select value={value.unit} onChange={(e) => onChange({ ...value, unit: e.target.value as RateUnit })}
        options={RATE_UNITS.map((u) => ({ value: u.value, label: u.value }))} />
    </div>
  );
}
