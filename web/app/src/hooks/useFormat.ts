export function fmtBytes(b: number): string {
  if (b < 1024) return b + ' B';
  if (b < 1048576) return (b / 1024).toFixed(1) + ' KB';
  if (b < 1073741824) return (b / 1048576).toFixed(1) + ' MB';
  return (b / 1073741824).toFixed(2) + ' GB';
}

export function fmtRate(b: number): string {
  if (b < 1024) return b + ' B/s';
  if (b < 1048576) return (b / 1024).toFixed(1) + ' KB/s';
  if (b < 1073741824) return (b / 1048576).toFixed(1) + ' MB/s';
  return (b / 1073741824).toFixed(2) + ' GB/s';
}

export function fmtTraffic(tx: number, rx: number): string {
  return `↑${fmtRate(tx)} ↓${fmtRate(rx)}`;
}

export function fmtDuration(secs: number): string {
  if (!secs || secs < 0) return '—';
  if (secs < 60) return secs + 's';
  const m = Math.floor(secs / 60);
  const s = secs % 60;
  if (m < 60) return s ? `${m}m ${s}s` : `${m}m`;
  const h = Math.floor(m / 60);
  const mm = m % 60;
  if (h < 24) return mm ? `${h}h ${mm}m` : `${h}h`;
  const d = Math.floor(h / 24);
  const hh = h % 24;
  return hh ? `${d}d ${hh}h` : `${d}d`;
}
