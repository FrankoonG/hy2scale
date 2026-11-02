import type { Action, Cond, End, PolicyConfig, PolicySet, Rule, SetRef } from '@/api/policy';

/**
 * The rule topology: where flows come from (users, proxies, WireGuard peers,
 * VPN defaults, the direct entry), the policy sets they enter (one card per
 * set, a row per rule), and where each row leads (another set it hands over
 * to, an exit group, an interface, deny, the default route). Pure: model +
 * layout; RulesTopoView draws it.
 */

export type SourceKind = 'user' | 'proxy' | 'wireguard' | 'vpn' | 'direct';
export interface Source { key: string; kind: SourceKind; names: string[]; target: string; extra?: string[] }

export type RowKind = 'rule' | 'default';
export interface Row {
  key: string;
  kind: RowKind;
  depth: number;         // nesting inside flows
  index: string;         // "2" or "2.1"
  name?: string;
  when?: Cond;
  enabled: boolean;
  /** the end this row decides; undefined for a flow head (its rows follow) */
  end?: End;
  /** the row opens a nested flow */
  flow?: boolean;
}

export type EndKind = 'exit' | 'iface' | 'deny' | 'default';
export interface EndNode { key: string; kind: EndKind; end: End }

export interface Card { set: PolicySet; rows: Row[]; col: number }

export interface Box { x: number; y: number; w: number; h: number }
export interface Edge { key: string; from: { x: number; y: number }; to: { x: number; y: number }; kind: 'src' | 'call' | 'end'; row?: string; target: string; vertical?: boolean }

export interface RulesLayout {
  sources: (Source & { box: Box })[];
  cards: (Card & { box: Box; rowY: Map<string, number> })[];
  ends: (EndNode & { box: Box })[];
  edges: Edge[];
  w: number; h: number;
}

// geometry
export const SRC_W = 190;
export const CARD_W = 300;
export const END_W = 260;
export const COL_GAP = 110;
export const HEAD_H = 34;
export const ROW_H = 24;
export const CARD_PAD = 8;
export const SRC_LINE = 18;
export const SRC_HEAD = 26;
export const END_H = 34;
const V_GAP = 22;
const SRC_GAP = 18;

export const endKey = (e: End): string => {
  switch (e.type) {
    case 'exit': return `exit:${e.mode || ''}:${e.paths.join('|')}`;
    case 'iface': return `iface:${e.iface}:${e.src || ''}`;
    case 'deny': return `deny:${e.reject ? 'reject' : 'drop'}`;
    case 'call': return `call:${e.policy}`;
    default: return e.type;
  }
};

function flatten(rules: Rule[], def: End | undefined, depth: number, prefix: string, out: Row[], setDefault: boolean) {
  rules.forEach((r, i) => {
    const index = prefix ? `${prefix}.${i + 1}` : `${i + 1}`;
    const a: Action = r.then;
    if (a.type === 'flow') {
      out.push({ key: `${r.id}`, kind: 'rule', depth, index, name: r.name, when: r.when, enabled: r.enabled, flow: true });
      flatten(a.rules, a.default, depth + 1, index, out, false);
    } else {
      out.push({ key: `${r.id}`, kind: 'rule', depth, index, name: r.name, when: r.when, enabled: r.enabled, end: a });
    }
  });
  if (def || setDefault) {
    out.push({ key: `${prefix || 'set'}#default`, kind: 'default', depth, index: '', enabled: true, end: def || { type: 'default' } });
  }
}

/** The rows of a policy set: its rules, nested flows indented, and defaults. */
export function setRows(s: PolicySet): Row[] {
  const out: Row[] = [];
  flatten(s.rules, s.default, 0, '', out, true);
  return out;
}

/** Where flows enter: the set's refs grouped by kind, plus the direct entry. */
export function sourcesOf(cfg: PolicyConfig, localLabel: string[], ): Source[] {
  const out: Source[] = [];
  for (const p of cfg.policies) {
    const by = new Map<SourceKind, string[]>();
    for (const r of (p.refs || []) as SetRef[]) {
      if (r.kind === 'policy' || r.kind === 'condition' || r.kind === 'direct') continue;
      const k = r.kind as SourceKind;
      by.set(k, [...(by.get(k) || []), r.name]);
    }
    for (const [kind, names] of by) out.push({ key: `src:${kind}:${p.id}`, kind, names, target: p.id });
  }
  if (cfg.direct && cfg.policies.some((p) => p.id === cfg.direct)) {
    out.unshift({ key: 'src:direct', kind: 'direct', names: [], target: cfg.direct, extra: localLabel });
  }
  return out;
}

/** Column of each set: entry sets 0, a set handed over to sits right of its callers. */
function columns(cfg: PolicyConfig): Map<string, number> {
  const col = new Map<string, number>(cfg.policies.map((p) => [p.id, 0]));
  const calls = new Map<string, string[]>();
  for (const p of cfg.policies) {
    const out: string[] = [];
    for (const r of setRows(p)) if (r.end?.type === 'call') out.push(r.end.policy);
    calls.set(p.id, out);
  }
  // longest call chain, bounded (calls are depth-limited at run time; a
  // cycle must not hang the layout)
  for (let pass = 0; pass < cfg.policies.length; pass++) {
    let changed = false;
    for (const p of cfg.policies) {
      for (const c of calls.get(p.id) || []) {
        if (!col.has(c)) continue;
        const want = (col.get(p.id) || 0) + 1;
        if (want > (col.get(c) || 0) && want <= cfg.policies.length) { col.set(c, want); changed = true; }
      }
    }
    if (!changed) break;
  }
  return col;
}

function srcHeight(s: Source) {
  const lines = s.kind === 'direct' ? (s.extra?.length || 0) : Math.min(s.names.length, 3) + (s.names.length > 3 ? 1 : 0);
  return SRC_HEAD + Math.max(1, lines) * SRC_LINE + 6;
}
const cardHeight = (rows: Row[]) => HEAD_H + rows.length * ROW_H + CARD_PAD;

export function layoutRules(cfg: PolicyConfig, localLabel: string[]): RulesLayout {
  const col = columns(cfg);
  const nCols = Math.max(0, ...col.values()) + 1;
  const srcs = sourcesOf(cfg, localLabel);
  const bySet = new Map<string, Source[]>();
  for (const s of srcs) bySet.set(s.target, [...(bySet.get(s.target) || []), s]);
  // sets in the first column take their sources from a lane on the left;
  // a deeper set (also entered via hand-over) carries them above its card
  const hasSrc = srcs.some((s) => col.get(s.target) === 0);
  const colX = (c: number) => (hasSrc ? SRC_W + COL_GAP : 0) + c * (CARD_W + COL_GAP);

  // cards, per column in config order; deeper columns follow their callers
  const cards: RulesLayout['cards'] = [];
  const sources: RulesLayout['sources'] = [];
  const cardBy = new Map<string, RulesLayout['cards'][number]>();
  const callerY = new Map<string, number[]>();
  for (let c = 0; c < nCols; c++) {
    let sets = cfg.policies.filter((p) => col.get(p.id) === c);
    if (c > 0) {
      const at = (id: string) => { const ys = callerY.get(id) || []; return ys.length ? ys.reduce((a, b) => a + b, 0) / ys.length : 1e9; };
      sets = [...sets].sort((a, b) => at(a.id) - at(b.id));
    }
    let y = 0;
    for (const s of sets) {
      const rows = setRows(s);
      const h = cardHeight(rows);
      const above = c > 0 ? bySet.get(s.id) || [] : [];
      // side by side above the card, two per row
      const aboveRows: Source[][] = [];
      above.forEach((x, i) => { if (i % 2 === 0) aboveRows.push([]); aboveRows[aboveRows.length - 1].push(x); });
      const rowH = (r: Source[]) => Math.max(...r.map(srcHeight));
      const aboveH = aboveRows.reduce((t, r) => t + rowH(r) + SRC_GAP, 0);
      const want = c > 0 && callerY.get(s.id)?.length ? Math.max(y + aboveH, (callerY.get(s.id)!.reduce((a, b) => a + b, 0) / callerY.get(s.id)!.length) - HEAD_H / 2) : y + aboveH;
      const box = { x: colX(c), y: want, w: CARD_W, h };
      let ay = box.y - aboveH;
      for (const r of aboveRows) {
        const total = r.length * SRC_W + (r.length - 1) * 12;
        r.forEach((src, i) => {
          sources.push({ ...src, box: { x: box.x + CARD_W / 2 - total / 2 + i * (SRC_W + 12), y: ay, w: SRC_W, h: srcHeight(src) } });
        });
        ay += rowH(r) + SRC_GAP;
      }
      const rowY = new Map<string, number>();
      rows.forEach((r, i) => rowY.set(r.key, box.y + HEAD_H + i * ROW_H + ROW_H / 2));
      const card = { set: s, rows, col: c, box, rowY };
      cards.push(card);
      cardBy.set(s.id, card);
      for (const r of rows) {
        if (r.end?.type === 'call') callerY.set(r.end.policy, [...(callerY.get(r.end.policy) || []), rowY.get(r.key)!]);
      }
      y = box.y + h + V_GAP;
    }
  }

  // sources beside the first-column set they enter
  let sy = 0;
  for (const card of [...cards].filter((c) => c.col === 0).sort((a, b) => a.box.y - b.box.y)) {
    const list = bySet.get(card.set.id) || [];
    const total = list.reduce((a, s) => a + srcHeight(s), 0) + Math.max(0, list.length - 1) * 10;
    let y = Math.max(sy, card.box.y + HEAD_H / 2 - total / 2);
    for (const s of list) {
      const h = srcHeight(s);
      sources.push({ ...s, box: { x: 0, y, w: SRC_W, h } });
      y += h + 10;
    }
    sy = y + 8;
  }

  // ends: one box per distinct end, in the order rows reach them
  const endX = colX(nCols);
  const ends = new Map<string, { node: EndNode; ys: number[] }>();
  for (const card of cards) {
    for (const r of card.rows) {
      if (!r.end || r.end.type === 'call' || r.end.type === 'result') continue;
      const k = endKey(r.end);
      const e = ends.get(k) || { node: { key: k, kind: r.end.type as EndKind, end: r.end }, ys: [] };
      e.ys.push(card.rowY.get(r.key)!);
      ends.set(k, e);
    }
  }
  const endList = [...ends.values()].map((e) => ({ ...e, at: e.ys.reduce((a, b) => a + b, 0) / e.ys.length }))
    .sort((a, b) => a.at - b.at);
  const placed: RulesLayout['ends'] = [];
  let ey = 0;
  for (const e of endList) {
    const y = Math.max(ey, e.at - END_H / 2);
    placed.push({ ...e.node, box: { x: endX, y, w: END_W, h: END_H } });
    ey = y + END_H + 12;
  }
  const endBy = new Map(placed.map((e) => [e.key, e]));

  // edges
  const edges: Edge[] = [];
  for (const s of sources) {
    const c = cardBy.get(s.target);
    if (!c) continue;
    if (c.col > 0) {
      const x = s.box.x + s.box.w / 2;
      edges.push({ key: `e:${s.key}`, kind: 'src', target: c.set.id, vertical: true,
        from: { x, y: s.box.y + s.box.h }, to: { x, y: c.box.y } });
      continue;
    }
    edges.push({ key: `e:${s.key}`, kind: 'src', target: c.set.id,
      from: { x: s.box.x + s.box.w, y: s.box.y + SRC_HEAD / 2 }, to: { x: c.box.x, y: c.box.y + HEAD_H / 2 } });
  }
  for (const card of cards) {
    for (const r of card.rows) {
      if (!r.end) continue;
      const from = { x: card.box.x + card.box.w, y: card.rowY.get(r.key)! };
      if (r.end.type === 'call') {
        const c = cardBy.get(r.end.policy);
        if (c) edges.push({ key: `c:${card.set.id}:${r.key}`, kind: 'call', row: `${card.set.id}:${r.key}`, target: c.set.id,
          from, to: { x: c.box.x, y: c.box.y + HEAD_H / 2 } });
        continue;
      }
      const e = endBy.get(endKey(r.end));
      if (e) edges.push({ key: `r:${card.set.id}:${r.key}`, kind: 'end', row: `${card.set.id}:${r.key}`, target: e.key,
        from, to: { x: e.box.x, y: e.box.y + END_H / 2 } });
    }
  }

  const all = [...sources.map((s) => s.box), ...cards.map((c) => c.box), ...placed.map((e) => e.box)];
  const w = all.length ? Math.max(...all.map((b) => b.x + b.w)) : 0;
  const h = all.length ? Math.max(...all.map((b) => b.y + b.h)) : 0;
  return { sources, cards, ends: placed, edges, w, h };
}
