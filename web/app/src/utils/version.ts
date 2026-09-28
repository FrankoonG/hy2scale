// Dotted numeric version compare ("1.10.0" >= "1.3.0"); mirrors relay.VersionAtLeast.
export function versionAtLeast(version: string, min: string): boolean {
  const num = (s: string) => parseInt(s, 10) || 0;
  const a = version.split('.'), b = min.split('.');
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const x = num(a[i] ?? '0'), y = num(b[i] ?? '0');
    if (x !== y) return x > y;
  }
  return true;
}
