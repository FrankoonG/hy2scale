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

// SemVer precedence, a > b: a release beats its own pre-releases
// ("1.4.1" > "1.4.1-dev.x"), pre-releases compare field by field, build
// metadata is ignored; false when either side is malformed. Mirrors the
// backend's selfupdate.Newer upgrade gate.
export function versionGreater(a: string, b: string): boolean {
  const parse = (s: string) => {
    s = s.replace(/^v/, '').split('+')[0];
    const dash = s.indexOf('-');
    const pre = dash === -1 ? [] : s.slice(dash + 1).split('.');
    const core = (dash === -1 ? s : s.slice(0, dash)).split('.');
    if (core.length !== 3 || core.some((x) => !/^\d+$/.test(x)) || (dash !== -1 && pre.some((x) => !x))) return null;
    return { core: core.map(Number), pre };
  };
  const x = parse(a), y = parse(b);
  if (!x || !y) return false;
  for (let i = 0; i < 3; i++) {
    if (x.core[i] !== y.core[i]) return x.core[i] > y.core[i];
  }
  if (!x.pre.length) return y.pre.length > 0;
  if (!y.pre.length) return false;
  for (let i = 0; i < Math.min(x.pre.length, y.pre.length); i++) {
    const p = x.pre[i], q = y.pre[i];
    if (p === q) continue;
    const pn = /^\d+$/.test(p), qn = /^\d+$/.test(q);
    if (pn && qn) return Number(p) > Number(q);
    if (pn) return false; // numeric identifiers sort first
    if (qn) return true;
    return p > q;
  }
  return x.pre.length > y.pre.length;
}
