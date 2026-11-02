// Rules as a file: condition sets, policy sets and the direct entry in one
// YAML document. Sets reference each other by name and users by username,
// so a file moves between nodes. Loaded on demand (with the YAML parser).
import { parse, stringify } from 'yaml';
import type { Action, Cond, End, PolicyConfig, PolicySet, Rule } from '@/api/policy';

export interface FileSet { name: string; rules: Rule[]; default?: End }
export interface RulesFile { version: 1; direct?: string; conditions: FileSet[]; policies: FileSet[] }

type Names = { cond: (v: string) => string | undefined; policy: (v: string) => string | undefined; user: (v: string) => string };

function mapCond(c: Cond, n: Names, miss: string[]): Cond {
  switch (c.type) {
    case 'ref': { const r = n.cond(c.ref); if (r === undefined) miss.push(c.ref); return { type: 'ref', ref: r ?? c.ref }; }
    case 'not': return { type: 'not', item: mapCond(c.item, n, miss) };
    case 'all':
    case 'any': return { type: c.type, items: c.items.map((x) => mapCond(x, n, miss)) };
    case 'match': return c.field === 'user' ? { ...c, values: c.values.map(n.user) } : c;
  }
}
function mapAction(a: Action, n: Names, miss: string[]): Action {
  if (a.type === 'call') {
    const p = n.policy(a.policy);
    if (p === undefined) miss.push(a.policy);
    return { type: 'call', policy: p ?? a.policy };
  }
  if (a.type === 'flow') return { ...a, rules: mapRules(a.rules, n, miss) };
  return a;
}
function mapRules(rules: Rule[], n: Names, miss: string[]): Rule[] {
  return rules.map((r) => {
    const out: Rule = { id: r.id, enabled: r.enabled, then: mapAction(r.then, n, miss) };
    if (r.name) out.name = r.name;
    if (r.when) out.when = mapCond(r.when, n, miss);
    return out;
  });
}
const setOf = (s: PolicySet, n: Names): FileSet => {
  const out: FileSet = { name: s.name, rules: mapRules(s.rules, n, []) };
  if (s.default) out.default = s.default;
  return out;
};
// `enabled` only when false, as in the set dialog's config tab
const lean = (rules: Rule[]): unknown[] => rules.map(({ id, name, enabled, when, then }) => ({
  id, ...(name ? { name } : {}), ...(enabled ? {} : { enabled: false }), ...(when ? { when } : {}),
  then: then.type === 'flow' ? { ...then, rules: lean(then.rules) } : then,
}));

/** The node's rules as file text. */
export function exportRules(cfg: PolicyConfig, users: { id: string; username: string }[]): string {
  const cond = new Map(cfg.conditions.map((s) => [s.id, s.name]));
  const pol = new Map(cfg.policies.map((s) => [s.id, s.name]));
  const user = new Map(users.map((u) => [u.id, u.username]));
  const n: Names = { cond: (v) => cond.get(v) ?? v, policy: (v) => pol.get(v) ?? v, user: (v) => user.get(v) ?? v };
  const sets = (list: PolicySet[]) => list.map((s) => setOf(s, n)).map((s) => ({ ...s, rules: lean(s.rules) }));
  const doc: Record<string, unknown> = { version: 1 };
  if (cfg.direct && pol.has(cfg.direct)) doc.direct = pol.get(cfg.direct);
  doc.conditions = sets(cfg.conditions);
  doc.policies = sets(cfg.policies);
  return stringify(doc, { lineWidth: 0 });
}

const asRules = (v: unknown): Rule[] => (Array.isArray(v) ? v : []).map((r: any) => ({
  ...r, id: String(r?.id ?? ''), enabled: r?.enabled !== false,
  ...(r?.then?.type === 'flow' ? { then: { ...r.then, rules: asRules(r.then.rules) } } : {}),
}));

/** Parses file text; throws with a message on a malformed file. */
export function parseRules(text: string): RulesFile {
  const doc = parse(text) as any;
  if (!doc || typeof doc !== 'object' || Array.isArray(doc)) throw new Error('not a rules file');
  const sets = (v: unknown, key: string): FileSet[] => {
    if (v === undefined || v === null) return [];
    if (!Array.isArray(v)) throw new Error(`${key}: expected a list`);
    return v.map((s: any, i) => {
      const name = typeof s?.name === 'string' ? s.name.trim() : '';
      if (!name) throw new Error(`${key}[${i}]: name required`);
      return { name, rules: asRules(s.rules), ...(s.default ? { default: s.default as End } : {}) };
    });
  };
  const conditions = sets(doc.conditions, 'conditions');
  const policies = sets(doc.policies, 'policies');
  if (!conditions.length && !policies.length) throw new Error('no sets');
  return { version: 1, direct: typeof doc.direct === 'string' ? doc.direct : undefined, conditions, policies };
}

/** A file set's body with names resolved to this node's ids; `missing` lists unknown set names. */
export function resolveSet(s: FileSet, ids: { cond: Map<string, string>; policy: Map<string, string>; user: Map<string, string> }) {
  const missing: string[] = [];
  const n: Names = { cond: (v) => ids.cond.get(v), policy: (v) => ids.policy.get(v), user: (v) => ids.user.get(v) ?? v };
  const rules = mapRules(s.rules, n, missing);
  return { rules, default: s.default, missing: [...new Set(missing)] };
}
