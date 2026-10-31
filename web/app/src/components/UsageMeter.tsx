import { fmtBytes } from '@/hooks/useFormat';

/** "used / limit" traffic with a thin bar; red above 90 %. */
export default function UsageMeter({ used, limit, format = fmtBytes, formatLimit = format }: {
  used: number;
  limit: number;
  format?: (bytes: number) => string;
  formatLimit?: (bytes: number) => string;
}) {
  const pct = limit > 0 ? Math.min(100, (used / limit) * 100) : 0;
  return (
    <>
      <span className={`usage-text${pct > 90 ? ' over' : ''}`}>
        <span className="usage-used">{format(used)}</span>
        <span className="sep">/</span>
        <span className="usage-limit">{limit > 0 ? formatLimit(limit) : '∞'}</span>
      </span>
      {limit > 0 && (
        <div className="usage-bar"><div className="usage-fill" style={{ width: `${pct}%` }} /></div>
      )}
    </>
  );
}
