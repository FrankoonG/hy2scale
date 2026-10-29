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

// Strictly newer x.y.z ("1.3.2" > "1.3.1"); false when either side is not
// three numeric parts. Mirrors the backend's semverGreater upgrade gate.
export function versionGreater(a: string, b: string): boolean {
  const pa = a.split('.').map((s) => parseInt(s, 10));
  const pb = b.split('.').map((s) => parseInt(s, 10));
  if (pa.length !== 3 || pb.length !== 3 || pa.some(isNaN) || pb.some(isNaN)) return false;
  for (let i = 0; i < 3; i++) {
    if (pa[i] !== pb[i]) return pa[i] > pb[i];
  }
  return false;
}
