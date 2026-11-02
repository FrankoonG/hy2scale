import { index, type Graph, type GNode } from './model';

/** Node sizes the layout assumes (the node CSS keeps them). */
export const SIZE = {
  set: { w: 220, h: 52 },
  rule: { w: 250, h: 62 },
  end: { w: 220, h: 38 },
  source: { w: 190 },
};
export const srcHeight = (n: GNode) => (n.data.kind === 'source' ? 34 + Math.max(1, Math.min(n.data.members.length, 4)) * 17 : 0);

const GX = 70;   // rule → its nested flow / column gap
const GY = 22;   // between rungs of a chain
const SET_GAP = 56;
const END_GAP = 12;
const COL_GAP = 140;

type Pos = { x: number; y: number };

/**
 * Positions for every node: each set a ladder (rules down its else, a nested
 * flow to the right of its rule), hand-over targets in later columns, shared
 * ends in the last column, sources left of (first column) or above their set.
 */
export function autoLayout(g: Graph): Map<string, Pos> {
  const { byId, out, into } = index(g);
  const pos = new Map<string, Pos>();
  const sets = g.nodes.filter((n) => n.data.kind === 'set');

  // which rules hand over to which sets: columns by call depth
  const owner = new Map<string, string>(); // rule → set
  const calls = new Map<string, string[]>();
  const claim = (start: string | undefined, set: string) => {
    let cur = start;
    while (cur) {
      const n = byId.get(cur);
      if (!n || n.data.kind !== 'rule' || owner.has(cur)) return;
      owner.set(cur, set);
      const m = out(cur, 'match');
      const t = m && byId.get(m.target);
      if (t?.data.kind === 'rule') claim(t.id, set);
      if (t?.data.kind === 'set') calls.set(set, [...(calls.get(set) || []), t.id]);
      cur = out(cur, 'else')?.target;
    }
  };
  for (const s of sets) claim(out(s.id, 'out')?.target, s.id);
  const col = new Map(sets.map((s) => [s.id, 0]));
  for (let pass = 0; pass < sets.length; pass++) {
    let changed = false;
    for (const s of sets) {
      for (const c of calls.get(s.id) || []) {
        const want = (col.get(s.id) || 0) + 1;
        if (col.has(c) && want > (col.get(c) || 0) && want <= sets.length) { col.set(c, want); changed = true; }
      }
    }
    if (!changed) break;
  }

  // one chain: returns its bottom and right edge
  const ladder = (start: string | undefined, x: number, y: number): { bottom: number; right: number } => {
    let cur = start;
    let bottom = y;
    let right = x;
    while (cur) {
      const n = byId.get(cur);
      if (!n || n.data.kind !== 'rule' || pos.has(cur)) break;
      pos.set(cur, { x, y });
      right = Math.max(right, x + SIZE.rule.w);
      let next = y + SIZE.rule.h + GY;
      const m = out(cur, 'match');
      const t = m && byId.get(m.target);
      if (t?.data.kind === 'rule' && !pos.has(t.id)) {
        const sub = ladder(t.id, x + SIZE.rule.w + GX, y + SIZE.rule.h / 2 + GY);
        next = Math.max(next, sub.bottom + GY);
        right = Math.max(right, sub.right);
      }
      bottom = next - GY;
      y = next;
      cur = out(cur, 'else')?.target;
    }
    return { bottom, right };
  };

  const srcOf = (setId: string) => into(setId).map((e) => byId.get(e.source)).filter((n): n is GNode => n?.data.kind === 'source');
  const resultEnds: { set: GNode; right: number }[] = [];

  const nCols = Math.max(0, ...col.values()) + 1;
  let colX = 0;
  let maxRight = 0;
  const callerY = (setNode: string) => {
    const ys = into(setNode).filter((e) => e.sourceHandle === 'match').map((e) => pos.get(e.source)?.y).filter((y): y is number => y !== undefined);
    return ys.length ? ys.reduce((a, b) => a + b, 0) / ys.length : undefined;
  };
  for (let c = 0; c < nCols; c++) {
    // condition sets first, then policy sets; later columns near their callers
    let list = sets.filter((s) => col.get(s.id) === c);
    list = [...list.filter((s) => s.data.kind === 'set' && s.data.setKind === 'condition'), ...list.filter((s) => s.data.kind === 'set' && s.data.setKind === 'policy')];
    if (c > 0) list.sort((a, b) => (callerY(a.id) ?? 1e9) - (callerY(b.id) ?? 1e9));
    let y = 0;
    let right = colX;
    for (const s of list) {
      const above = c > 0 ? srcOf(s.id) : [];
      const aboveH = above.reduce((h, n) => h + srcHeight(n) + 16, 0);
      const want = c > 0 ? Math.max(y + aboveH, (callerY(s.id) ?? -1e9) - SIZE.set.h / 2) : y;
      let ay = want - aboveH;
      for (const n of above) { pos.set(n.id, { x: colX, y: ay }); ay += srcHeight(n) + 16; }
      pos.set(s.id, { x: colX, y: want });
      const r = ladder(out(s.id, 'out')?.target, colX + 24, want + SIZE.set.h + GY);
      right = Math.max(right, colX + SIZE.set.w, r.right);
      if (s.data.kind === 'set' && s.data.setKind === 'condition') resultEnds.push({ set: s, right: r.right });
      y = Math.max(want + SIZE.set.h, r.bottom) + SET_GAP;
    }
    maxRight = Math.max(maxRight, right);
    colX = right + COL_GAP;
  }

  // sources of first-column sets: a lane on the left
  let sy = -1e9;
  for (const s of sets.filter((x) => col.get(x.id) === 0).sort((a, b) => pos.get(a.id)!.y - pos.get(b.id)!.y)) {
    for (const n of srcOf(s.id)) {
      const y = Math.max(sy, pos.get(s.id)!.y);
      pos.set(n.id, { x: -SIZE.source.w - 110, y });
      sy = y + srcHeight(n) + 14;
    }
  }

  // ends: a condition set's true/false beside it, shared ends in a last column
  const incomingY = (id: string) => {
    const ys = into(id).map((e) => {
      const p = pos.get(e.source);
      if (!p) return undefined;
      return p.y + (e.sourceHandle === 'else' ? SIZE.rule.h : SIZE.rule.h / 2);
    }).filter((y): y is number => y !== undefined);
    return ys.length ? ys.reduce((a, b) => a + b, 0) / ys.length : 0;
  };
  const pack = (ids: string[], x: number) => {
    let ey = -1e9;
    for (const id of ids.map((i) => ({ i, y: incomingY(i) })).sort((a, b) => a.y - b.y)) {
      const y = Math.max(ey, id.y - SIZE.end.h / 2);
      pos.set(id.i, { x, y });
      ey = y + SIZE.end.h + END_GAP;
    }
  };
  const ends = g.nodes.filter((n) => n.data.kind === 'end');
  for (const { set, right } of resultEnds) {
    const id = set.data.kind === 'set' ? set.data.setId : '';
    pack(ends.filter((e) => e.id.startsWith(`end:${id}:`)).map((e) => e.id), Math.max(right, pos.get(set.id)!.x + SIZE.set.w) + GX);
  }
  pack(ends.filter((e) => !pos.has(e.id)).map((e) => e.id), maxRight + COL_GAP);

  // anything left (unreached rules, unlinked sources): below everything
  let by = Math.max(0, ...[...pos.values()].map((p) => p.y)) + 120;
  for (const n of g.nodes) {
    if (pos.has(n.id)) continue;
    pos.set(n.id, { x: 0, y: by });
    by += 90;
  }
  return pos;
}
