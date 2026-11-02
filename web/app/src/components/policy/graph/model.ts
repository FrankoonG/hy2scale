import type { Edge, Node } from '@xyflow/react';
import type { Action, Cond, End, PolicyConfig, PolicySet, Rule } from '@/api/policy';
import { newId, type SetKind } from '../shared';

/**
 * The rule graph: policy and condition sets drawn as decision chains.
 *
 *   source ──▶ [set] ─out─▶ (rule) ─match─▶ end | (rule: nested flow) | [set: hand-over]
 *                              │
 *                            else
 *                              ▼
 *                           (rule) … ─else─▶ end (the set's / flow's otherwise)
 *
 * Every rule has exactly one way in, so the graph is always a tree per set
 * and converts back to the stored sets one to one. Ends are shared: one
 * node per distinct end (per set for true/false). A condition set's `ref`
 * port draws to every rule whose condition references it; those edges are
 * derived from the rules' conditions, not stored.
 */

export type SrcKind = 'user' | 'proxy' | 'wireguard' | 'vpn' | 'direct';
export interface Member { id: string; name: string }

export interface SetData extends Record<string, unknown> { kind: 'set'; setKind: SetKind; setId: string; name: string }
export interface RuleData extends Record<string, unknown> { kind: 'rule'; ruleId: string; name?: string; enabled: boolean; when?: Cond }
export interface EndData extends Record<string, unknown> { kind: 'end'; end: End }
export interface SrcData extends Record<string, unknown> { kind: 'source'; src: SrcKind; members: Member[] }
export type GData = SetData | RuleData | EndData | SrcData;
export type GNode = Node<GData>;
export type GEdge = Edge;

export interface Graph { nodes: GNode[]; edges: GEdge[] }

/** New sets carry this id prefix until the node assigns theirs. */
export const NEW_SET = 'new:';
export const isNewSet = (id: string) => id.startsWith(NEW_SET);

export const setNodeId = (setId: string) => `set:${setId}`;
export const edgeId = (source: string, handle: string) => `${source}:${handle}`;

export const endKey = (e: End): string => {
  switch (e.type) {
    case 'exit': return `exit:${e.mode || ''}:${e.paths.join('|')}`;
    case 'iface': return `iface:${e.iface}:${e.src || ''}`;
    case 'deny': return `deny:${e.reject ? 'reject' : 'drop'}`;
    case 'result': return `result:${e.result}`;
    case 'call': return `call:${e.policy}`;
    default: return e.type;
  }
};
/** Ends of policy sets are shared by value; true/false belong to their set. */
export const endNodeId = (e: End, setId: string) => (e.type === 'result' ? `end:${setId}:${e.result}` : `end:${endKey(e)}`);

export const edge = (source: string, sourceHandle: string, target: string, targetHandle = 'in'): GEdge =>
  ({ id: edgeId(source, sourceHandle), source, sourceHandle, target, targetHandle });

// ---------------------------------------------------------------- sets → graph

export function fromConfig(cfg: PolicyConfig): Graph {
  const nodes: GNode[] = [];
  const edges: GEdge[] = [];
  const seen = new Set<string>();
  const add = (n: GNode) => { if (!seen.has(n.id)) { seen.add(n.id); nodes.push(n); } };
  const pos = { x: 0, y: 0 };

  const endNode = (e: End, setId: string) => {
    const id = endNodeId(e, setId);
    add({ id, type: 'end', position: pos, data: { kind: 'end', end: e } });
    return id;
  };

  // A flow: its rules chained by else; returns the first node.
  const chain = (rules: Rule[], def: End | undefined, setId: string, kind: SetKind, top: boolean): string | undefined => {
    let first: string | undefined;
    let prev: string | undefined;
    for (const r of rules) {
      let id = `rule:${r.id}`;
      while (seen.has(id)) id = `rule:${r.id}~${newId()}`;
      add({ id, type: 'rule', position: pos, data: { kind: 'rule', ruleId: r.id, name: r.name, enabled: r.enabled, when: r.when } });
      if (prev) edges.push(edge(prev, 'else', id));
      else first = id;
      prev = id;
      const then = r.then;
      if (then.type === 'flow') {
        const sub = chain(then.rules, then.default, setId, kind, false);
        if (sub) edges.push(edge(id, 'match', sub));
        else if (then.default) edges.push(edge(id, 'match', endNode(then.default, setId)));
      } else if (then.type === 'call') {
        edges.push(edge(id, 'match', setNodeId(then.policy)));
      } else {
        edges.push(edge(id, 'match', endNode(then, setId)));
      }
    }
    // the otherwise: a set always ends somewhere (its built-in default);
    // a flow without one continues outside
    const d = def ?? (top ? (kind === 'policy' ? { type: 'default' as const } : { type: 'result' as const, result: false }) : undefined);
    if (d) {
      const e = endNode(d, setId);
      if (prev) edges.push(edge(prev, 'else', e));
      else first = e;
    }
    return first;
  };

  const addSet = (s: PolicySet, kind: SetKind) => {
    const id = setNodeId(s.id);
    add({ id, type: 'set', position: pos, data: { kind: 'set', setKind: kind, setId: s.id, name: s.name } });
    const first = chain(s.rules, s.default, s.id, kind, true);
    if (first) edges.push(edge(id, 'out', first));
  };
  cfg.conditions.forEach((s) => addSet(s, 'condition'));
  cfg.policies.forEach((s) => addSet(s, 'policy'));

  // sources: one group per kind and set
  for (const p of cfg.policies) {
    const by = new Map<SrcKind, Member[]>();
    for (const r of p.refs || []) {
      if (r.kind === 'user' || r.kind === 'proxy' || r.kind === 'wireguard' || r.kind === 'vpn') {
        by.set(r.kind, [...(by.get(r.kind) || []), { id: r.id, name: r.name }]);
      }
    }
    for (const [src, members] of by) {
      const id = `src:${src}:${p.id}`;
      add({ id, type: 'source', position: pos, data: { kind: 'source', src, members } });
      edges.push(edge(id, 'out', setNodeId(p.id)));
    }
  }
  const direct = 'src:direct';
  add({ id: direct, type: 'source', position: pos, data: { kind: 'source', src: 'direct', members: [] } });
  if (cfg.direct && cfg.policies.some((p) => p.id === cfg.direct)) edges.push(edge(direct, 'out', setNodeId(cfg.direct)));

  // a hand-over to a set that does not exist is dropped
  return { nodes, edges: edges.filter((e) => seen.has(e.target)) };
}

// ---------------------------------------------------------------- graph → sets

export interface GraphError { node: string; code: 'noMatch' | 'kind' | 'loop' | 'name' | 'callCond' | 'flowDepth' }

export interface Compiled {
  conditions: PolicySet[];
  policies: PolicySet[];
  direct: string;
  /** desired set per user / proxy id ('' = none), for the members drawn */
  users: Map<string, string>;
  proxies: Map<string, string>;
  errors: GraphError[];
  /** rule nodes no set reaches (dropped on save) */
  orphans: string[];
}

export function index(g: Graph) {
  const byId = new Map(g.nodes.map((n) => [n.id, n]));
  const out = new Map<string, GEdge>();
  const into = new Map<string, GEdge[]>();
  for (const e of g.edges) {
    out.set(edgeId(e.source, e.sourceHandle || 'out'), e);
    into.set(e.target, [...(into.get(e.target) || []), e]);
  }
  return { byId, out: (id: string, h: string) => out.get(edgeId(id, h)), into: (id: string) => into.get(id) || [] };
}

export function compile(g: Graph): Compiled {
  const { byId, out } = index(g);
  const errors: GraphError[] = [];
  const reached = new Set<string>();

  const walk = (start: string | undefined, kind: SetKind, depth: number): { rules: Rule[]; def?: End } => {
    const rules: Rule[] = [];
    let cur = start;
    while (cur) {
      const n = byId.get(cur);
      if (!n) break;
      if (n.data.kind === 'end') {
        const e = n.data.end;
        if ((e.type === 'result') !== (kind === 'condition')) errors.push({ node: n.id, code: 'kind' });
        return { rules, def: e };
      }
      if (n.data.kind !== 'rule') break;
      if (reached.has(cur)) { errors.push({ node: cur, code: 'loop' }); break; }
      reached.add(cur);
      const d = n.data;
      const m = out(cur, 'match');
      const t = m && byId.get(m.target);
      let then: Action;
      if (!t) {
        errors.push({ node: cur, code: 'noMatch' });
        then = kind === 'policy' ? { type: 'default' } : { type: 'result', result: false };
      } else if (t.data.kind === 'end') {
        const e = t.data.end;
        if ((e.type === 'result') !== (kind === 'condition')) errors.push({ node: t.id, code: 'kind' });
        then = e;
      } else if (t.data.kind === 'set') {
        if (kind !== 'policy' || t.data.setKind !== 'policy') errors.push({ node: cur, code: 'callCond' });
        then = { type: 'call', policy: t.data.setId };
      } else if (depth >= 8) {
        errors.push({ node: cur, code: 'flowDepth' });
        then = { type: 'default' };
      } else {
        const sub = walk(t.id, kind, depth + 1);
        then = sub.def ? { type: 'flow', rules: sub.rules, default: sub.def } : { type: 'flow', rules: sub.rules };
      }
      const r: Rule = { id: d.ruleId, enabled: d.enabled, then };
      if (d.name) r.name = d.name;
      if (d.when) r.when = d.when;
      rules.push(r);
      cur = out(cur, 'else')?.target;
    }
    return { rules };
  };

  const conditions: PolicySet[] = [];
  const policies: PolicySet[] = [];
  for (const n of g.nodes) {
    if (n.data.kind !== 'set') continue;
    const d = n.data;
    if (!d.name.trim()) errors.push({ node: n.id, code: 'name' });
    const { rules, def } = walk(out(n.id, 'out')?.target, d.setKind, 0);
    const s: PolicySet = { id: d.setId, name: d.name.trim(), rules };
    if (def) s.default = def;
    (d.setKind === 'policy' ? policies : conditions).push(s);
  }

  // sources
  const users = new Map<string, string>();
  const proxies = new Map<string, string>();
  let direct = '';
  for (const n of g.nodes) {
    if (n.data.kind !== 'source') continue;
    const t = byId.get(out(n.id, 'out')?.target || '');
    const target = t && t.data.kind === 'set' && t.data.setKind === 'policy' ? t.data.setId : '';
    if (n.data.src === 'direct') direct = target;
    const into = n.data.src === 'user' ? users : n.data.src === 'proxy' ? proxies : null;
    if (into) for (const m of n.data.members) if (!into.get(m.id)) into.set(m.id, target);
  }

  const orphans = g.nodes.filter((n) => n.data.kind === 'rule' && !reached.has(n.id)).map((n) => n.id);
  return { conditions, policies, direct, users, proxies, errors, orphans };
}

// ---------------------------------------------------------------- diff

/** A set as compared: built-in defaults spelled out, empty fields dropped. */
export function normSet(s: PolicySet, kind: SetKind): string {
  const end = (e: End): End => (e.type === 'exit' ? { type: 'exit', paths: e.paths, ...(e.mode ? { mode: e.mode } : {}) } : e);
  const act = (a: Action): Action => (a.type === 'flow'
    ? { type: 'flow', rules: a.rules.map(rule), ...(a.default ? { default: end(a.default) } : {}) }
    : end(a));
  function rule(r: Rule): Rule {
    return { id: r.id, enabled: r.enabled, ...(r.name ? { name: r.name } : {}), ...(r.when ? { when: r.when } : {}), then: act(r.then) };
  }
  const def = s.default ?? (kind === 'policy' ? { type: 'default' } : { type: 'result', result: false });
  return JSON.stringify({ name: s.name, rules: s.rules.map(rule), default: end(def as End) });
}

// ---------------------------------------------------------------- derived edges

/** Condition-set ids a condition references (anywhere in it). */
export function condRefs(c: Cond | undefined, out: string[] = []): string[] {
  if (!c) return out;
  if (c.type === 'ref') out.push(c.ref);
  else if (c.type === 'not') condRefs(c.item, out);
  else if (c.type === 'all' || c.type === 'any') c.items.forEach((x) => condRefs(x, out));
  return out;
}

/** The condition with every reference to `ref` removed. */
export function dropRef(c: Cond | undefined, ref: string): Cond | undefined {
  if (!c) return c;
  switch (c.type) {
    case 'ref': return c.ref === ref ? undefined : c;
    case 'not': { const i = dropRef(c.item, ref); return i ? { type: 'not', item: i } : undefined; }
    case 'all':
    case 'any': {
      const items = c.items.map((x) => dropRef(x, ref)).filter((x): x is Cond => !!x);
      if (!items.length) return undefined;
      return items.length === 1 && c.type === 'all' ? items[0] : { type: c.type, items };
    }
    default: return c;
  }
}

/** The condition and a reference to `ref` (all of them). */
export function addRef(c: Cond | undefined, ref: string): Cond {
  const r: Cond = { type: 'ref', ref };
  if (!c) return r;
  if (condRefs(c).includes(ref)) return c;
  if (c.type === 'all') return { type: 'all', items: [...c.items, r] };
  return { type: 'all', items: [c, r] };
}

/** Rename a set id everywhere in the graph (a new set saved). */
export function renameSet(g: Graph, from: string, to: string): Graph {
  const fromNode = setNodeId(from);
  const toNode = setNodeId(to);
  const swap = (id: string) => (id === fromNode ? toNode : id);
  const swapCond = (c: Cond | undefined): Cond | undefined => {
    if (!c) return c;
    switch (c.type) {
      case 'ref': return c.ref === from ? { type: 'ref', ref: to } : c;
      case 'not': return { type: 'not', item: swapCond(c.item)! };
      case 'all':
      case 'any': return { type: c.type, items: c.items.map((x) => swapCond(x)!) };
      default: return c;
    }
  };
  return {
    nodes: g.nodes.map((n) => {
      if (n.id === fromNode && n.data.kind === 'set') return { ...n, id: toNode, data: { ...n.data, setId: to } };
      if (n.data.kind === 'rule' && condRefs(n.data.when).includes(from)) return { ...n, data: { ...n.data, when: swapCond(n.data.when) } };
      if (n.data.kind === 'end' && n.data.end.type === 'result' && n.id.startsWith(`end:${from}:`)) {
        return { ...n, id: `end:${to}:${n.data.end.result}` };
      }
      return n;
    }),
    edges: g.edges.map((e) => {
      let { source, target } = e;
      source = swap(source);
      target = swap(target);
      if (source.startsWith(`end:${from}:`)) source = source.replace(`end:${from}:`, `end:${to}:`);
      if (target.startsWith(`end:${from}:`)) target = target.replace(`end:${from}:`, `end:${to}:`);
      return source === e.source && target === e.target ? e : { ...e, id: edgeId(source, e.sourceHandle || 'out'), source, target };
    }),
  };
}

// ---------------------------------------------------------------- analysis

export interface Analysis {
  /** rule → "2" / "2.1" */
  index: Map<string, string>;
  /** rule / end → the set node whose chain reaches it */
  owner: Map<string, string>;
  /** rule → what its unlinked else means: the set's built-in end or continue */
  tail: Map<string, 'default' | 'false' | 'continue'>;
  /** dashed edges: a flow's last else continues with the next outer rule */
  ghosts: GEdge[];
  /** set nodes some rule hands over to */
  called: Set<string>;
}

export function analyze(g: Graph): Analysis {
  const { byId, out } = index(g);
  const a: Analysis = { index: new Map(), owner: new Map(), tail: new Map(), ghosts: [], called: new Set() };
  const walk = (start: string | undefined, set: GNode, prefix: string, next: string | undefined) => {
    const kind = set.data.kind === 'set' ? set.data.setKind : 'policy';
    let cur = start;
    let i = 0;
    while (cur) {
      const n = byId.get(cur);
      if (!n || a.owner.has(cur)) return;
      a.owner.set(cur, set.id);
      if (n.data.kind !== 'rule') return;
      i++;
      a.index.set(cur, prefix ? `${prefix}.${i}` : `${i}`);
      const e = out(cur, 'else')?.target;
      const resolved = e ?? next;
      const m = out(cur, 'match');
      const t = m && byId.get(m.target);
      if (t?.data.kind === 'rule') walk(t.id, set, a.index.get(cur)!, resolved);
      else if (t?.data.kind === 'end') a.owner.set(t.id, a.owner.get(t.id) ?? set.id);
      else if (t?.data.kind === 'set') a.called.add(t.id);
      if (!e) {
        if (prefix) {
          a.tail.set(cur, 'continue');
          if (next) a.ghosts.push({ id: `ghost:${cur}`, source: cur, sourceHandle: 'else', target: next, targetHandle: 'in' });
        } else a.tail.set(cur, kind === 'policy' ? 'default' : 'false');
      }
      cur = e;
    }
  };
  for (const n of g.nodes) if (n.data.kind === 'set') walk(out(n.id, 'out')?.target, n, '', undefined);
  return a;
}

/** The set kind whose chain reaches a node, if any. */
export function kindAt(g: Graph, a: Analysis, id: string): SetKind | undefined {
  const s = g.nodes.find((n) => n.id === a.owner.get(id));
  return s?.data.kind === 'set' ? s.data.setKind : undefined;
}

/** Nodes upstream of id (along stored edges into rules and ends). */
export function ancestors(g: Graph, id: string): Set<string> {
  const { into } = index(g);
  const seen = new Set<string>();
  const stack = [id];
  while (stack.length) {
    const cur = stack.pop()!;
    for (const e of into(cur)) {
      if (e.targetHandle !== 'in' || seen.has(e.source)) continue;
      seen.add(e.source);
      stack.push(e.source);
    }
  }
  return seen;
}

/** Stable key of a node across reloads (positions are stored by it). */
export function canon(n: GNode, g: Graph): string {
  switch (n.data.kind) {
    case 'set': return `set:${n.data.setId}`;
    case 'rule': return `rule:${n.data.ruleId}`;
    case 'end': return n.data.end.type === 'result' ? n.id : `end:${endKey(n.data.end)}`;
    case 'source': {
      if (n.data.src === 'direct') return 'src:direct';
      const e = g.edges.find((x) => x.source === n.id);
      const t = e && g.nodes.find((x) => x.id === e.target);
      return t?.data.kind === 'set' ? `src:${n.data.src}:${t.data.setId}` : n.id;
    }
  }
}
