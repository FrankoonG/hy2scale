// Login-page topology: pure-data spec drawn by engine.ts, produced by generate.ts.
// Coordinates are in a 640×420 view box.

export const VBW = 640;
export const VBH = 420;

export type Role = 'self' | 'device' | 'internet' | 'admin' | 'relay' | 'exit' | 'peer' | 'remote' | 'native';
export type LinkKind = 'access' | 'peer' | 'egress' | 'native' | 'punch';
export type LinkState = 'up' | 'pending' | 'degraded' | 'offline' | 'dim';
export type DeviceIcon = 'laptop' | 'phone' | 'monitor' | 'router';

export interface SpecNode {
  id: string;
  role: Role;
  x: number;
  y: number;
  /** Short suffix after the role label (a / b / c, x). */
  tag?: string;
  icon?: DeviceIcon;
  /** false hides the role label. */
  label?: false;
  badge?: 'lock' | 'key' | 'quota';
  /** dim: behind a hop with nested off; off: offline. */
  state?: 'dim' | 'off';
}

/** A label on a link at curve position t; `key` is an i18n key, `text` literal. */
export interface Chip { t: number; text?: string; key?: string; dy?: number }

export interface SpecLink {
  id: string;
  a: string;
  b: string;
  kind: LinkKind;
  /** Bend as a fraction of the link length (signed). */
  curve?: number;
  state?: LinkState;
  /** Seconds after the topology appears (punch attempts). */
  appear?: number;
  chips?: Chip[];
}

export interface Zone { x: number; y: number; w: number; h: number; label: string }
/** A floating label; `key` is an i18n key (login.topo.chip.*), `text` literal. */
export interface Note { x: number; y: number; text?: string; key?: string }
export interface SpecEvent { at: number; link?: string; node?: string; state: LinkState | 'off' }

/** Packets along node paths; `only` restricts parallel links by id. */
export interface Flow {
  paths: string[][];
  dir: 'both' | 'up' | 'down';
  every?: number;
  from?: number;
  until?: number;
  only?: string[];
}

export interface TopoSpec {
  seed: string;
  /** i18n keys under login.topo.tag.* */
  tags: string[];
  nodes: SpecNode[];
  links: SpecLink[];
  zones: Zone[];
  notes: Note[];
  events: SpecEvent[];
  flows: Flow[];
}

/**
 * Structural rules a generated topology must keep, mirroring the nested
 * discovery rules: no repeated node on a path, self never a descendant, no
 * path through an un-dialable (dim) node or link, native peers stay leaves.
 */
export function validateSpec(s: TopoSpec): string[] {
  const P: string[] = [];
  const N = new Map<string, SpecNode>();
  s.nodes.forEach((n) => { if (N.has(n.id)) P.push('dup node ' + n.id); N.set(n.id, n); });
  if (s.nodes.filter((n) => n.role === 'self').length !== 1) P.push('need exactly one self');
  s.links.forEach((l) => {
    if (!N.has(l.a) || !N.has(l.b)) P.push('dangling link ' + l.id);
    if (l.kind === 'peer' && (N.get(l.a)?.role === 'native' || N.get(l.b)?.role === 'native')) P.push('native node on peer link ' + l.id);
  });
  const linked = (a: string, b: string) => s.links.filter((l) => (l.a === a && l.b === b) || (l.a === b && l.b === a));
  s.flows.forEach((f, fi) => f.paths.forEach((p) => {
    if (new Set(p).size !== p.length) P.push(`flow ${fi}: repeated node`);
    p.forEach((id, i) => {
      const n = N.get(id);
      if (!n) { P.push(`flow ${fi}: unknown ${id}`); return; }
      if (n.state === 'dim') P.push(`flow ${fi}: passes dim node ${id}`);
      if (n.role === 'self' && i > 1) P.push(`flow ${fi}: self as descendant`);
      if (n.role === 'native' && i < p.length - 1 && N.get(p[i + 1])?.role !== 'internet') P.push(`flow ${fi}: transit through native`);
      if (i) {
        const ls = linked(p[i - 1], id);
        if (!ls.length) P.push(`flow ${fi}: no link ${p[i - 1]}-${id}`);
        else if (ls.every((l) => l.state === 'dim')) P.push(`flow ${fi}: dim link ${p[i - 1]}-${id}`);
      }
    });
  }));
  return P;
}
