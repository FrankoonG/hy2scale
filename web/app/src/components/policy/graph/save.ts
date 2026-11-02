import * as api from '@/api';
import {
  createCondition, createPolicySet, deleteCondition, deletePolicySet, updateCondition, updatePolicySet,
  updatePolicySettings, type PolicyConfig,
} from '@/api/policy';
import { compile, isNewSet, normSet, renameSet, type Graph } from './model';

export interface SaveStep { kind: 'create' | 'update' | 'delete' | 'source' | 'direct'; name: string }

/** What a save would send, for the button's count. */
export function pending(g: Graph, base: PolicyConfig): number {
  const c = compile(g);
  let n = 0;
  const was = new Map([...base.conditions.map((s) => [s.id, normSet(s, 'condition')] as const), ...base.policies.map((s) => [s.id, normSet(s, 'policy')] as const)]);
  for (const s of c.conditions) if (was.get(s.id) !== normSet(s, 'condition')) n++;
  for (const s of c.policies) if (was.get(s.id) !== normSet(s, 'policy')) n++;
  const now = new Set([...c.conditions, ...c.policies].map((s) => s.id));
  for (const id of was.keys()) if (!now.has(id)) n++;
  if (c.direct !== (base.direct || '')) n++;
  const { users, proxies } = sourceRefs(base);
  for (const [id, want] of c.users) if ((users.get(id) || '') !== want) n++;
  for (const [id, want] of c.proxies) if ((proxies.get(id) || '') !== want) n++;
  for (const id of users.keys()) if (!c.users.has(id)) n++;
  for (const id of proxies.keys()) if (!c.proxies.has(id)) n++;
  return n;
}

function sourceRefs(base: PolicyConfig) {
  const users = new Map<string, string>();
  const proxies = new Map<string, string>();
  for (const p of base.policies) {
    for (const r of p.refs || []) {
      if (r.kind === 'user') users.set(r.id, p.id);
      if (r.kind === 'proxy') proxies.set(r.id, p.id);
    }
  }
  return { users, proxies };
}

export class SaveError extends Error {
  constructor(message: string, public step: SaveStep, public graph: Graph) { super(message); }
}

/**
 * Write the graph back: new sets first (their ids replace the drafts'),
 * then changed condition sets, policy sets, sources and the direct entry,
 * then removed sets. Each step is one request; on a failure the graph as
 * far as it got (new ids in place) comes back with the error.
 */
export async function saveGraph(g0: Graph, base: PolicyConfig): Promise<Graph> {
  let g = g0;
  let step: SaveStep = { kind: 'create', name: '' };
  try {
    // 1. new sets get their ids
    let c = compile(g);
    for (const s of [...c.conditions, ...c.policies]) {
      if (!isNewSet(s.id)) continue;
      step = { kind: 'create', name: s.name };
      const isCond = c.conditions.includes(s);
      const made = isCond ? await createCondition({ name: s.name, rules: [] }) : await createPolicySet({ name: s.name, rules: [] });
      g = renameSet(g, s.id, made.id);
    }
    c = compile(g);
    const was = new Map([...base.conditions.map((s) => [s.id, normSet(s, 'condition')] as const), ...base.policies.map((s) => [s.id, normSet(s, 'policy')] as const)]);

    // 2. changed sets (condition sets first: policies reference them)
    for (const s of c.conditions) {
      if (was.get(s.id) === normSet(s, 'condition') && !isNewSet(s.id)) continue;
      step = { kind: 'update', name: s.name };
      await updateCondition(s);
    }
    for (const s of c.policies) {
      if (was.get(s.id) === normSet(s, 'policy')) continue;
      step = { kind: 'update', name: s.name };
      await updatePolicySet(s);
    }

    // 3. sources and the direct entry
    const refs = sourceRefs(base);
    const want = (m: Map<string, string>, had: Map<string, string>) => {
      const out = new Map<string, string>();
      for (const [id, v] of m) if ((had.get(id) || '') !== v) out.set(id, v);
      for (const id of had.keys()) if (!m.has(id)) out.set(id, '');
      return out;
    };
    const userChanges = want(c.users, refs.users);
    if (userChanges.size) {
      const users = await api.getUsers();
      for (const [id, policy] of userChanges) {
        const u = users.find((x) => x.id === id);
        if (!u) continue;
        step = { kind: 'source', name: u.username };
        await api.updateUser(id, { ...u, exit_policy: policy });
      }
    }
    const proxyChanges = want(c.proxies, refs.proxies);
    if (proxyChanges.size) {
      const proxies = await api.getProxies();
      for (const [id, policy] of proxyChanges) {
        const p = proxies.find((x) => x.id === id);
        if (!p) continue;
        step = { kind: 'source', name: p.id };
        await api.updateProxy(id, { ...p, exit_policy: policy });
      }
    }
    if (c.direct !== (base.direct || '')) {
      step = { kind: 'direct', name: '' };
      await updatePolicySettings({ direct: c.direct });
    }

    // 4. removed sets (policies first: they may reference conditions)
    const now = new Set([...c.conditions, ...c.policies].map((s) => s.id));
    for (const s of base.policies) {
      if (now.has(s.id)) continue;
      step = { kind: 'delete', name: s.name };
      await deletePolicySet(s.id);
    }
    for (const s of base.conditions) {
      if (now.has(s.id)) continue;
      step = { kind: 'delete', name: s.name };
      await deleteCondition(s.id);
    }
    return g;
  } catch (e: any) {
    throw new SaveError(String(e?.message || e), step, g);
  }
}
