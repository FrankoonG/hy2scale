import { nodeRadiusFor, R_HUB_OUTER, type GraphEdge, type GraphNode, type Pos } from './graphModel';

/*
 * Built-in automatic layouts for the Nodes topology, plus the edge routing
 * and curve geometry shared by presets and custom canvases. Everything here
 * is deterministic: the same graph always produces the same picture.
 */

export type PresetId = 'layered' | 'layered-lr' | 'radial' | 'ring';
export const PRESET_IDS: PresetId[] = ['layered', 'layered-lr', 'radial', 'ring'];

export interface Graph { nodes: Map<string, GraphNode>; edges: Map<string, GraphEdge> }
export interface Waypoint { u: number; v: number }

// ---------------------------------------------------------------- helpers

function selfKeyOf(g: Graph): string | undefined {
  for (const n of g.nodes.values()) if (n.isSelf) return n.key;
  let best: GraphNode | undefined;
  for (const n of g.nodes.values()) if (!best || n.depth < best.depth) best = n;
  return best?.key;
}

function adjacency(g: Graph): Map<string, Set<string>> {
  const adj = new Map<string, Set<string>>();
  for (const k of g.nodes.keys()) adj.set(k, new Set());
  for (const e of g.edges.values()) {
    if (!g.nodes.has(e.from) || !g.nodes.has(e.to) || e.from === e.to) continue;
    adj.get(e.from)!.add(e.to);
    adj.get(e.to)!.add(e.from);
  }
  return adj;
}

const byName = (g: Graph) => (a: string, b: string) => {
  const na = g.nodes.get(a)?.name || a;
  const nb = g.nodes.get(b)?.name || b;
  return na < nb ? -1 : na > nb ? 1 : a < b ? -1 : a > b ? 1 : 0;
};

function layersOf(g: Graph): string[][] {
  const layers: string[][] = [];
  for (const n of g.nodes.values()) (layers[n.depth] ||= []).push(n.key);
  for (let i = 0; i < layers.length; i++) layers[i] = (layers[i] || []).sort(byName(g));
  return layers.filter((l) => l.length > 0);
}

function segCross(p1: Pos, p2: Pos, p3: Pos, p4: Pos): boolean {
  const d = (p2.x - p1.x) * (p4.y - p3.y) - (p2.y - p1.y) * (p4.x - p3.x);
  if (Math.abs(d) < 1e-9) return false;
  const u = ((p3.x - p1.x) * (p4.y - p3.y) - (p3.y - p1.y) * (p4.x - p3.x)) / d;
  const v = ((p3.x - p1.x) * (p2.y - p1.y) - (p3.y - p1.y) * (p2.x - p1.x)) / d;
  return u > 0.01 && u < 0.99 && v > 0.01 && v < 0.99;
}

/** Undirected unique pairs, for crossing counts. */
function pairs(g: Graph): Array<[string, string]> {
  const seen = new Set<string>();
  const out: Array<[string, string]> = [];
  for (const e of g.edges.values()) {
    if (!g.nodes.has(e.from) || !g.nodes.has(e.to)) continue;
    const k = e.from < e.to ? `${e.from}|${e.to}` : `${e.to}|${e.from}`;
    if (seen.has(k)) continue;
    seen.add(k);
    out.push([e.from, e.to]);
  }
  return out;
}

function crossings(ps: Array<[string, string]>, pos: Record<string, Pos>): number {
  let c = 0;
  for (let i = 0; i < ps.length; i++) {
    const [a, b] = ps[i];
    for (let j = i + 1; j < ps.length; j++) {
      const [x, y] = ps[j];
      if (a === x || a === y || b === x || b === y) continue;
      if (segCross(pos[a], pos[b], pos[x], pos[y])) c++;
    }
  }
  return c;
}

/**
 * Barycenter ordering inside each hop layer: alternate down / up sweeps,
 * each node sorted by the mean slot of its neighbours in the layers above
 * (down) or below (up); keep the order with the fewest crossings.
 */
function orderLayers(g: Graph, layers: string[][]): string[][] {
  const adj = adjacency(g);
  const depth = (k: string) => g.nodes.get(k)!.depth;
  const ps = pairs(g);
  const slot = (ls: string[][]) => {
    const m: Record<string, number> = {};
    ls.forEach((l) => l.forEach((k, i) => { m[k] = i - (l.length - 1) / 2; }));
    return m;
  };
  const evalPos = (ls: string[][]) => {
    const s = slot(ls);
    const m: Record<string, Pos> = {};
    ls.forEach((l) => l.forEach((k) => { m[k] = { x: s[k], y: depth(k) }; }));
    return m;
  };
  let cur = layers.map((l) => [...l]);
  let best = cur.map((l) => [...l]);
  const canCount = ps.length <= 700;
  let bestC = canCount ? crossings(ps, evalPos(cur)) : Infinity;
  for (let it = 0; it < 12; it++) {
    const down = it % 2 === 0;
    const seq = down ? cur.map((_, i) => i).slice(1) : cur.map((_, i) => i).reverse().slice(1);
    for (const li of seq) {
      const s = slot(cur);
      const d = depth(cur[li][0]);
      const score = (k: string) => {
        const ns = [...(adj.get(k) || [])].filter((m) => (down ? depth(m) < d : depth(m) > d));
        return ns.length ? ns.reduce((a, m) => a + s[m], 0) / ns.length : s[k];
      };
      const sc: Record<string, number> = {};
      cur[li].forEach((k) => { sc[k] = score(k); });
      const idx: Record<string, number> = {};
      cur[li].forEach((k, i) => { idx[k] = i; });
      cur[li] = [...cur[li]].sort((a, b) => sc[a] - sc[b] || idx[a] - idx[b]);
    }
    if (canCount) {
      const c = crossings(ps, evalPos(cur));
      if (c < bestC) { bestC = c; best = cur.map((l) => [...l]); }
    } else {
      best = cur.map((l) => [...l]);
    }
  }
  return best;
}

// ---------------------------------------------------------------- density

/*
 * Room the layouts make for text, in world units (zoom 1): a node name is
 * 11px mono under its dot, an edge carries a latency-over-rate label pair.
 * Rates change on every poll while a layout only follows the graph's
 * shape, so every edge is planned with a full label pair.
 */
const NAME_CH = 6.6;
const LABEL_W = 62;
const LABEL_H = 27;
/** Space one label takes in a row of labels (with a margin). */
const LABEL_SLOT = (LABEL_W + LABEL_H) / 2 + 10;
/** Canvas the size penalty is measured against (a common window). */
const REF_W = 1190;
const REF_H = 730;
/** Above this, layouts keep the density estimate and skip the search;
 *  above SEARCH_FINE_EDGES the search tries fewer spreads. */
const SEARCH_MAX_EDGES = 120;
const SEARCH_FINE_EDGES = 60;

const nameW = (g: Graph, k: string) => (g.nodes.get(k)?.name.length || 0) * NAME_CH;

/** Edges per layer band (band i joins layers i and i+1). */
function bandLoad(g: Graph, ls: string[][]): number[] {
  const li = new Map<string, number>();
  ls.forEach((l, i) => l.forEach((k) => li.set(k, i)));
  const band = new Array(Math.max(0, ls.length - 1)).fill(0);
  for (const e of g.edges.values()) {
    const a = li.get(e.from), b = li.get(e.to);
    if (a === undefined || b === undefined || a === b) continue;
    band[Math.min(a, b)]++;
  }
  return band;
}

interface Box { x0: number; y0: number; x1: number; y1: number }
const boxHit = (a: Box, b: Box) => a.x0 < b.x1 && b.x0 < a.x1 && a.y0 < b.y1 && b.y0 < a.y1;

/** Whether segment a→b passes through the box (Liang–Barsky). */
function segBox(a: Pos, b: Pos, bx: Box): boolean {
  let t0 = 0, t1 = 1;
  const dx = b.x - a.x, dy = b.y - a.y;
  const clip = (p: number, q: number) => {
    if (Math.abs(p) < 1e-12) return q >= 0;
    const r = q / p;
    if (p < 0) { if (r > t1) return false; if (r > t0) t0 = r; } else { if (r < t0) return false; if (r < t1) t1 = r; }
    return true;
  };
  return clip(-dx, a.x - bx.x0) && clip(dx, bx.x1 - a.x) && clip(-dy, a.y - bx.y0) && clip(dy, bx.y1 - a.y);
}

/**
 * How hard a placement is to read, as the view would draw it: text or dots
 * overlapping (heavy), lines running through labels and names (light), and
 * how much a canvas of REF size would have to shrink it to fit. Nodes weigh
 * more than edges: dots and names of different nodes covering each other
 * cost most, a node and an edge label less, two edge labels least.
 */
function crowding(g: Graph, pos: Record<string, Pos>): { issues: number; score: number; area: number } {
  const geoms = routeEdges(g, pos, { avoid: true });
  const spots = placeLabels(g, pos, geoms, {});
  type Mark = Box & { own: string; label: boolean };
  const marks: Mark[] = [];
  for (const n of g.nodes.values()) {
    const p = pos[n.key];
    if (!p) continue;
    const r = nodeRadiusFor(n), w = (n.name.length * NAME_CH) / 2, ty = p.y + r + 14;
    marks.push({ x0: p.x - r, x1: p.x + r, y0: p.y - r, y1: p.y + r, own: n.key, label: false });
    marks.push({ x0: p.x - w, x1: p.x + w, y0: ty - 8.5, y1: ty + 2.5, own: n.key, label: false });
  }
  spots.forEach((s, key) => {
    const x0 = s.anchor === 'start' ? s.x - 1 : s.anchor === 'end' ? s.x - LABEL_W + 1 : s.x - LABEL_W / 2;
    marks.push({ x0, x1: x0 + LABEL_W, y0: s.y - 13, y1: s.y + LABEL_H - 13, own: key, label: true });
  });
  let clash = 0, clashW = 0;
  for (let i = 0; i < marks.length; i++) {
    for (let j = i + 1; j < marks.length; j++) {
      const a = marks[i], b = marks[j];
      if (!a.label && !b.label && a.own === b.own) continue;
      if (!boxHit(a, b)) continue;
      clash++;
      clashW += a.label && b.label ? 1.5 : a.label || b.label ? 3 : 6;
    }
  }
  let through = 0;
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (const k in pos) {
    const p = pos[k];
    minX = Math.min(minX, p.x); maxX = Math.max(maxX, p.x); minY = Math.min(minY, p.y); maxY = Math.max(maxY, p.y);
  }
  geoms.forEach((ge) => {
    const e = g.edges.get(ge.key)!;
    let ex0 = Infinity, ey0 = Infinity, ex1 = -Infinity, ey1 = -Infinity;
    for (const p of ge.poly) { ex0 = Math.min(ex0, p.x); ex1 = Math.max(ex1, p.x); ey0 = Math.min(ey0, p.y); ey1 = Math.max(ey1, p.y); }
    for (const b of ge.bends) { minX = Math.min(minX, b.x); maxX = Math.max(maxX, b.x); minY = Math.min(minY, b.y); maxY = Math.max(maxY, b.y); }
    const eb = { x0: ex0, y0: ey0, x1: ex1, y1: ey1 };
    for (let i = 1; i < marks.length; i += 1) {
      const m = marks[i];
      // names (odd node marks) and labels; never the edge's own ends / label
      if (m.label ? m.own === ge.key : (i % 2 === 0 || m.own === e.from || m.own === e.to)) continue;
      if (!boxHit(eb, m)) continue;
      for (let s = 0; s < ge.poly.length - 1; s++) {
        if (segBox(ge.poly[s], ge.poly[s + 1], m)) { through++; break; }
      }
    }
  });
  const w = maxX - minX + 2 * (R_HUB_OUTER + 40), h = maxY - minY + 2 * R_HUB_OUTER + 60;
  const k = Math.min(REF_W / w, REF_H / h, 1.2);
  // Shrinking the picture shrinks its text: worth it only while it buys
  // clearly fewer clashes, and much less so once text gets small.
  // (text to keep legible: names count more than edge labels)
  const T = g.nodes.size * 0.6 + g.edges.size * 0.3;
  const shrink = (8 + T) * Math.log(1.2 / k) + T * 6 * Math.max(0, Math.log(0.75 / k));
  return { issues: clash + through, score: clashW + through * 0.5 + shrink, area: w * h };
}

/**
 * Picks the spread of `place(along, across)` (multipliers of the density
 * estimate) that reads best. Deterministic: a fixed grid of candidates,
 * first best wins; skipped when the estimate is already clean.
 */
function spread(g: Graph, place: (along: number, across: number) => Record<string, Pos>, axes: 1 | 2): Record<string, Pos> {
  const base = place(1, 1);
  if (g.edges.size > SEARCH_MAX_EDGES || g.nodes.size < 3) return base;
  const c0 = crowding(g, base);
  if (c0.issues === 0) return base;
  // Growing the picture, even within the canvas, moves it away from the
  // compact estimate: a mild cost so it grows only for a clear gain.
  const grown = (area: number) => 3 * Math.log(Math.max(1, area / c0.area));
  let best = { pos: base, s: c0.score };
  const fine = g.edges.size <= SEARCH_FINE_EDGES;
  const steps = axes === 2
    ? (fine ? [0.8, 1, 1.25, 1.55, 1.9, 2.3] : [0.8, 1, 1.4, 1.9])
    : (fine ? [0.85, 1, 1.15, 1.3, 1.5, 1.75, 2, 2.35, 2.75] : [0.85, 1, 1.3, 1.75, 2.35]);
  for (const a of steps) {
    for (const b of axes === 2 ? steps : [1]) {
      if (a === 1 && b === 1) continue;
      const pos = place(a, b);
      const c = crowding(g, pos);
      const sc = c.score + grown(c.area) + c.area * 1e-7;
      if (sc < best.s) best = { pos, s: sc };
    }
  }
  return best.pos;
}

// ---------------------------------------------------------------- layered

/**
 * Positions for an ordered row, at least `gap` apart and in order, as close
 * as possible (weighted least squares) to `want`: pool-adjacent-violators
 * on want[i] - i·gap.
 */
function packAlong(want: number[], wt: number[], gap: number): number[] {
  const blocks: Array<{ v: number; w: number; n: number }> = [];
  want.forEach((d, i) => {
    blocks.push({ v: d - i * gap, w: wt[i], n: 1 });
    while (blocks.length > 1 && blocks[blocks.length - 2].v > blocks[blocks.length - 1].v) {
      const b = blocks.pop()!, a = blocks.pop()!;
      const w = a.w + b.w;
      blocks.push({ v: w > 0 ? (a.v * a.w + b.v * b.w) / w : (a.v + b.v) / 2, w, n: a.n + b.n });
    }
  });
  const out: number[] = [];
  for (const b of blocks) for (let j = 0; j < b.n; j++) out.push(b.v + out.length * gap);
  return out;
}

interface LayeredSpec {
  /** Distance between layers. */
  level: number;
  /** Same-row neighbour distance inside a layer. */
  sib: number;
  /** Offset between the staggered rows of a wide layer. */
  stagger: number;
  maxPerRow: number;
  /** Layers run left to right (rows are columns). */
  lr: boolean;
}
const TD: LayeredSpec = { level: 130, sib: 92, stagger: 48, maxPerRow: 12, lr: false };
const LR: LayeredSpec = { level: 200, sib: 56, stagger: 84, maxPerRow: 14, lr: true };

function layered(g: Graph, S: LayeredSpec): Record<string, Pos> {
  const ls = orderLayers(g, layersOf(g));
  const band = bandLoad(g, ls);
  const adj = adjacency(g);
  const rowsOf = (l: string[]) => Math.ceil(l.length / S.maxPerRow);
  const widest = ls.map((l) => Math.max(0, ...l.map((k) => nameW(g, k))));
  // Names sit side by side (top-down) or the staggered columns hold them
  // (left-right).
  const sib = ls.map((_, i) => (S.lr ? S.sib : Math.max(S.sib, widest[i] + 16)));
  const stagger = ls.map((_, i) => (S.lr ? Math.max(S.stagger, widest[i] + 18) : S.stagger));
  const place = (fa: number, fb: number) => {
    const sibF = sib.map((v, i) => Math.max(v * fa, S.lr ? 2 * R_HUB_OUTER + 18 : widest[i] + 14));
    const span = ls.map((l, i) => ((l.length - 1) * sibF[i]) / rowsOf(l));
    // A band's labels line up across its middle: as many fit side by side
    // as its mean width holds; more need further rows, i.e. a deeper band.
    const gap = band.map((E, i) => {
      const room = (span[i] + span[i + 1]) / 2 + (S.lr ? LABEL_H : LABEL_W);
      const per = Math.max(1, Math.floor(room / (S.lr ? LABEL_H + 9 : LABEL_W + 10)));
      const rows = Math.ceil(E / per);
      const need = S.lr ? 2 * R_HUB_OUTER + (rows * (LABEL_W + 12)) / 0.7 : 2 * R_HUB_OUTER + 30 + (rows * (LABEL_H + 9)) / 0.7;
      return fb < 1 ? Math.max(S.level, need * fb) : Math.max(S.level, need) * fb;
    });
    // Along each layer: in barycenter order, `step` apart, each dot as
    // close as it can get to the mean of its neighbours in the layer
    // above (then below, then above again) so edges run short and steep.
    const step = ls.map((l, i) => sibF[i] / rowsOf(l));
    const along: Record<string, number> = {};
    ls.forEach((l, i) => l.forEach((k, j) => { along[k] = (j - (l.length - 1) / 2) * step[i]; }));
    for (const down of [true, false, true]) {
      const seq = ls.map((_, i) => i);
      if (!down) seq.reverse();
      for (const li of seq.slice(1)) {
        const ref = new Set(ls[down ? li - 1 : li + 1]);
        const want: number[] = [], wt: number[] = [];
        ls[li].forEach((k) => {
          const ns = [...(adj.get(k) || [])].filter((m) => ref.has(m));
          want.push(ns.length ? ns.reduce((a, m) => a + along[m], 0) / ns.length : along[k]);
          wt.push(ns.length ? 1 : 1e-3);
        });
        packAlong(want, wt, step[li]).forEach((v, j) => { along[ls[li][j]] = v; });
      }
    }
    const pos: Record<string, Pos> = {};
    let acc = 0;
    ls.forEach((l, i) => {
      // Wide layers zig-zag over several staggered rows: the order is
      // kept, same-row neighbours stay `sib` apart, and edges from the
      // previous layer pass between the dots of the front row.
      const rows = rowsOf(l);
      l.forEach((k, j) => {
        const across = acc + (j % rows) * stagger[i];
        pos[k] = S.lr ? { x: across, y: along[k] } : { x: along[k], y: across };
      });
      acc += (rows - 1) * stagger[i] + (gap[i] || 0);
    });
    return pos;
  };
  return spread(g, place, 2);
}

// ---------------------------------------------------------------- radial / ring

/** Shortest-path spanning tree: each node hangs under one neighbour one hop
 *  closer to self (by name when several qualify). */
function spanningTree(g: Graph, root: string) {
  const adj = adjacency(g);
  const cmp = byName(g);
  const parent = new Map<string, string>();
  const kids = new Map<string, string[]>();
  for (const k of g.nodes.keys()) kids.set(k, []);
  const keys = [...g.nodes.keys()].sort((a, b) => g.nodes.get(a)!.depth - g.nodes.get(b)!.depth || cmp(a, b));
  for (const k of keys) {
    if (k === root) continue;
    const d = g.nodes.get(k)!.depth;
    const cands = [...(adj.get(k) || [])].filter((m) => g.nodes.get(m)!.depth < d);
    cands.sort((a, b) => g.nodes.get(b)!.depth - g.nodes.get(a)!.depth || cmp(a, b));
    const p = cands[0] || root;
    parent.set(k, p);
    kids.get(p)!.push(k);
  }
  for (const l of kids.values()) l.sort(cmp);
  return { parent, kids, adj };
}

const RING = 120;
const MIN_ARC = 62;

function circMean(angles: number[], around: number): number {
  let sx = 0, sy = 0;
  for (const a of angles) { sx += Math.cos(a - around); sy += Math.sin(a - around); }
  return Math.atan2(sy, sx);
}

/** Orders siblings by the mean angle of their non-tree neighbours so cross
 *  links stay short; a few passes, deterministic. */
function orderSiblings(
  g: Graph, root: string, tree: ReturnType<typeof spanningTree>,
  placeAngles: () => Record<string, number>,
) {
  for (let it = 0; it < 4; it++) {
    const ang = placeAngles();
    for (const [p, list] of tree.kids) {
      if (list.length < 2) continue;
      const pa = ang[p] ?? 0;
      const sc: Record<string, number> = {};
      const idx: Record<string, number> = {};
      list.forEach((k, i) => {
        idx[k] = i;
        const ns = [...(tree.adj.get(k) || [])].filter((m) => m !== p && tree.parent.get(m) !== k && ang[m] !== undefined && m !== root);
        sc[k] = ns.length ? circMean(ns.map((m) => ang[m]), pa) : circMean([ang[k] ?? pa], pa);
      });
      list.sort((a, b) => sc[a] - sc[b] || idx[a] - idx[b]);
    }
  }
}

function radial(g: Graph): Record<string, Pos> {
  const root = selfKeyOf(g);
  if (!root) return {};
  const tree = spanningTree(g, root);
  const depthOf = (k: string) => g.nodes.get(k)!.depth;
  const counts: number[] = [];
  const widest: number[] = [];
  for (const n of g.nodes.values()) {
    counts[n.depth] = (counts[n.depth] || 0) + 1;
    widest[n.depth] = Math.max(widest[n.depth] || 0, nameW(g, n.key));
  }
  // Labels inside ring d: edges from the ring further in (the spokes of
  // ring 1 included) and the chords between dots of ring d.
  const band: number[] = [];
  for (const e of g.edges.values()) {
    const a = g.nodes.get(e.from), b = g.nodes.get(e.to);
    if (!a || !b || e.from === e.to) continue;
    const d = Math.max(a.depth, b.depth);
    band[d] = (band[d] || 0) + 1;
  }
  const arcOf = (d: number) => Math.max(MIN_ARC, (widest[d] || 0) * 0.75 + 14);
  /** Ring radii: a ring holds its dots (`fa`), the band inside it holds
   *  its labels around its middle circle, and rings stay RING apart (`fb`). */
  const radii = (fa: number, fb: number) => {
    const R: number[] = [0];
    for (let d = 1; d < counts.length; d++) {
      const prev = R[d - 1] || 0;
      const labels = (((band[d] || 0) * LABEL_SLOT) / (2 * Math.PI)) * fb; // middle radius they need
      R[d] = Math.max(prev + RING * Math.max(1, fb), 2 * labels - prev, ((counts[d] || 0) * arcOf(d) * fa) / (2 * Math.PI));
    }
    return R;
  };
  const weight = new Map<string, number>();
  const w = (k: string): number => {
    const ks = tree.kids.get(k) || [];
    const v = ks.length ? ks.reduce((a, c) => a + w(c), 0) : 1;
    weight.set(k, v);
    return v;
  };
  const allocate = (R: number[]) => {
    weight.clear();
    w(root);
    const ang: Record<string, number> = { [root]: -Math.PI / 2 };
    const place = (k: string, a0: number, a1: number) => {
      const ks = tree.kids.get(k) || [];
      if (!ks.length) return;
      const d = depthOf(k);
      const center = (a0 + a1) / 2;
      let span = a1 - a0;
      // Keep a subtree inside the wedge its parent can reach without the
      // edges cutting back across the inner ring.
      if (k !== root && R[d + 1]) span = Math.min(span, 2 * Math.acos(Math.min(1, R[d] / R[d + 1])));
      const total = ks.reduce((a, c) => a + (weight.get(c) || 1), 0);
      let a = center - span / 2;
      for (const c of ks) {
        const s = (span * (weight.get(c) || 1)) / total;
        ang[c] = a + s / 2;
        place(c, a, a + s);
        a += s;
      }
    };
    place(root, -Math.PI / 2 - Math.PI, -Math.PI / 2 + Math.PI);
    return ang;
  };
  const R0 = radii(1, 1);
  orderSiblings(g, root, tree, () => allocate(R0));
  const placeAt = (fa: number, fb: number) => {
    const R = radii(fa, fb);
    const ang = allocate(R);
    // Per ring, enforce a minimum angular gap (circular sweep) so compressed
    // subtrees never stack dots.
    const byDepth: string[][] = [];
    for (const k of Object.keys(ang)) if (k !== root) (byDepth[depthOf(k)] ||= []).push(k);
    byDepth.forEach((ring, d) => {
      if (!ring || ring.length < 2) return;
      const gap = (arcOf(d) * fa) / R[d];
      ring.sort((a, b) => ang[a] - ang[b]);
      const orig = ring.map((k) => ang[k]);
      for (let i = 1; i < ring.length; i++) {
        if (ang[ring[i]] - ang[ring[i - 1]] < gap) ang[ring[i]] = ang[ring[i - 1]] + gap;
      }
      const span = ang[ring[ring.length - 1]] - ang[ring[0]];
      if (span > 2 * Math.PI - gap) {
        // Too crowded to keep subtree spacing: spread evenly in that order.
        const a0 = orig[0];
        ring.forEach((k, i) => { ang[k] = a0 + (2 * Math.PI * i) / ring.length; });
        return;
      }
      const shift = ring.reduce((s, k, i) => s + (orig[i] - ang[k]), 0) / ring.length;
      ring.forEach((k) => { ang[k] += shift; });
    });
    const pos: Record<string, Pos> = { [root]: { x: 0, y: 0 } };
    for (const k of Object.keys(ang)) {
      if (k === root) continue;
      const r = R[depthOf(k)] || RING;
      pos[k] = { x: Math.cos(ang[k]) * r, y: Math.sin(ang[k]) * r };
    }
    // Nodes outside the tree (should not happen) go on an outer ring.
    let extra = 0;
    for (const k of g.nodes.keys()) {
      if (pos[k]) continue;
      const r = (R[R.length - 1] || 0) + RING;
      const a = -Math.PI / 2 + extra++ * (MIN_ARC / r);
      pos[k] = { x: Math.cos(a) * r, y: Math.sin(a) * r };
    }
    return pos;
  };
  return spread(g, placeAt, 2);
}

function ring(g: Graph): Record<string, Pos> {
  const root = selfKeyOf(g);
  if (!root) return {};
  const tree = spanningTree(g, root);
  // Sibling order from the radial pass keeps cross links short; then a
  // depth-first walk puts every subtree in one contiguous arc.
  const order = () => {
    const seq: string[] = [];
    const walk = (k: string) => { for (const c of tree.kids.get(k) || []) { seq.push(c); walk(c); } };
    walk(root);
    for (const k of g.nodes.keys()) if (k !== root && !seq.includes(k)) seq.push(k);
    return seq;
  };
  const angles = () => {
    const seq = order();
    const ang: Record<string, number> = { [root]: -Math.PI / 2 };
    seq.forEach((k, i) => { ang[k] = -Math.PI / 2 + (2 * Math.PI * (i + 0.5)) / seq.length; });
    return ang;
  };
  orderSiblings(g, root, tree, angles);
  const seq = order();
  // The circle holds its dots and names; the spokes' labels around half
  // the radius; the chords' labels inside it.
  let spokes = 0;
  for (const e of g.edges.values()) if (e.from === root || e.to === root) spokes++;
  const arc = Math.max(MIN_ARC * 1.15, ...seq.map((k) => nameW(g, k) * 0.75 + 14));
  const chords = g.edges.size - spokes;
  const r0 = Math.max(
    170,
    (seq.length * arc) / (2 * Math.PI),
    (spokes * LABEL_SLOT) / Math.PI,
    Math.sqrt((chords * LABEL_SLOT * LABEL_SLOT) / Math.PI),
  );
  const place = (f: number) => {
    const r = r0 * f;
    const pos: Record<string, Pos> = { [root]: { x: 0, y: 0 } };
    seq.forEach((k, i) => {
      const a = -Math.PI / 2 + (2 * Math.PI * (i + 0.5)) / seq.length;
      pos[k] = { x: Math.cos(a) * r, y: Math.sin(a) * r };
    });
    return pos;
  };
  return spread(g, (f) => place(f), 1);
}

export function presetLayout(id: PresetId, g: Graph): Record<string, Pos> {
  if (g.nodes.size === 0) return {};
  switch (id) {
    case 'layered-lr': return layered(g, LR);
    case 'radial': return radial(g);
    case 'ring': return ring(g);
    default: return layered(g, TD);
  }
}

/** Structural signature: layouts only change when this does (topology
 *  polls rebuild the graph every few seconds with the same shape). */
export function graphSignature(g: Graph): string {
  const ns = [...g.nodes.values()].map((n) => `${n.key}:${n.depth}:${n.degree}`).sort().join(',');
  const es = [...g.edges.keys()].sort().join(',');
  return ns + '#' + es;
}

// ---------------------------------------------------------------- geometry

type Cubic = [Pos, Pos, Pos, Pos];

/** Catmull-Rom through the points, as cubic Béziers. Reversal-symmetric. */
function spline(P: Pos[]): Cubic[] {
  const out: Cubic[] = [];
  for (let i = 0; i < P.length - 1; i++) {
    const p0 = P[i - 1] || P[i], p1 = P[i], p2 = P[i + 1], p3 = P[i + 2] || P[i + 1];
    out.push([
      p1,
      { x: p1.x + (p2.x - p0.x) / 6, y: p1.y + (p2.y - p0.y) / 6 },
      { x: p2.x - (p3.x - p1.x) / 6, y: p2.y - (p3.y - p1.y) / 6 },
      p2,
    ]);
  }
  return out;
}

export function bez(s: Cubic, t: number): Pos {
  const m = 1 - t;
  return {
    x: m * m * m * s[0].x + 3 * m * m * t * s[1].x + 3 * m * t * t * s[2].x + t * t * t * s[3].x,
    y: m * m * m * s[0].y + 3 * m * m * t * s[1].y + 3 * m * t * t * s[2].y + t * t * t * s[3].y,
  };
}

const f1 = (v: number) => (Math.round(v * 10) / 10).toString();
function pathD(segs: Cubic[]): string {
  if (!segs.length) return '';
  let d = `M${f1(segs[0][0].x)} ${f1(segs[0][0].y)}`;
  for (const s of segs) d += `C${f1(s[1].x)} ${f1(s[1].y)} ${f1(s[2].x)} ${f1(s[2].y)} ${f1(s[3].x)} ${f1(s[3].y)}`;
  return d;
}

function sample(segs: Cubic[], per = 14): Pos[] {
  const out: Pos[] = [];
  segs.forEach((s, i) => {
    for (let j = i === 0 ? 0 : 1; j <= per; j++) out.push(bez(s, j / per));
  });
  return out;
}

const unit = (a: Pos, b: Pos) => {
  const dx = b.x - a.x, dy = b.y - a.y, L = Math.hypot(dx, dy) || 1;
  return { x: dx / L, y: dy / L };
};

/** Waypoint stored relative to the chord between the node centres. */
export function toAbs(p: Pos, q: Pos, w: Waypoint): Pos {
  const dx = q.x - p.x, dy = q.y - p.y;
  return { x: p.x + dx * w.u - dy * w.v, y: p.y + dy * w.u + dx * w.v };
}
export function toRel(p: Pos, q: Pos, pt: Pos): Waypoint {
  const dx = q.x - p.x, dy = q.y - p.y, L2 = dx * dx + dy * dy || 1;
  const rx = pt.x - p.x, ry = pt.y - p.y;
  return { u: (rx * dx + ry * dy) / L2, v: (-rx * dy + ry * dx) / L2 };
}

export interface EdgeGeom {
  key: string;
  /** Points the curve passes through: rim A, bends…, rim B. */
  pts: Pos[];
  /** Bend points (absolute); custom waypoints or the automatic bend. */
  bends: Pos[];
  custom: boolean;
  segs: Cubic[];
  d: string;
  /** Same curve drawn from the `to` end. */
  dRev: string;
  poly: Pos[];
  cum: number[];
  len: number;
}

function geomOf(key: string, pts: Pos[], bends: Pos[], custom: boolean): EdgeGeom {
  const segs = spline(pts);
  const rev = spline([...pts].reverse());
  const poly = sample(segs);
  const cum = [0];
  for (let i = 1; i < poly.length; i++) cum.push(cum[i - 1] + Math.hypot(poly[i].x - poly[i - 1].x, poly[i].y - poly[i - 1].y));
  return { key, pts, bends, custom, segs, d: pathD(segs), dRev: pathD(rev), poly, cum, len: cum[cum.length - 1] || 1 };
}

/** Point and unit tangent at arc length s. */
export function pointAt(g: EdgeGeom, s: number): { x: number; y: number; tx: number; ty: number } {
  const { poly, cum } = g;
  if (poly.length < 2) return { x: poly[0]?.x || 0, y: poly[0]?.y || 0, tx: 1, ty: 0 };
  s = Math.max(0, Math.min(g.len, s));
  let lo = 0, hi = cum.length - 1;
  while (hi - lo > 1) { const mid = (lo + hi) >> 1; if (cum[mid] <= s) lo = mid; else hi = mid; }
  const segLen = cum[hi] - cum[lo] || 1;
  const t = (s - cum[lo]) / segLen;
  const a = poly[lo], b = poly[hi];
  const u = unit(a, b);
  return { x: a.x + (b.x - a.x) * t, y: a.y + (b.y - a.y) * t, tx: u.x, ty: u.y };
}

/** Arc length of the point on the curve nearest to `pt`. */
export function project(g: EdgeGeom, pt: Pos): number {
  let best = Infinity, bestS = 0;
  for (let i = 0; i < g.poly.length - 1; i++) {
    const a = g.poly[i], b = g.poly[i + 1];
    const dx = b.x - a.x, dy = b.y - a.y, l2 = dx * dx + dy * dy;
    let t = l2 ? ((pt.x - a.x) * dx + (pt.y - a.y) * dy) / l2 : 0;
    t = Math.max(0, Math.min(1, t));
    const d = Math.hypot(pt.x - (a.x + dx * t), pt.y - (a.y + dy * t));
    if (d < best) { best = d; bestS = g.cum[i] + Math.sqrt(l2) * t; }
  }
  return bestS;
}

/** Midpoints of the curve between consecutive pass-through points: where
 *  a new bend can be inserted (index = insert position in the waypoints). */
export function ghostPoints(g: EdgeGeom): Array<{ at: Pos; index: number }> {
  return g.segs.map((s, i) => ({ at: bez(s, 0.5), index: i }));
}

export interface RouteOpts {
  /** Custom waypoints per edge key (custom canvases). */
  waypoints?: Record<string, Waypoint[] | undefined>;
  /** Bend straight edges that would pass through other dots. */
  avoid?: boolean;
  /** Horizontal period of a wrapping world (map view): each edge takes
   *  the shorter way round, dots repeat every `wrap` units. */
  wrap?: number;
}

/**
 * Horizontal shift of an edge's far end on a world repeating every `w`
 * units: the end lands at most half a world away from the near end. Both
 * directions of a pair agree (the tie at exactly half a world included).
 */
export function wrapDx(p: Pos, q: Pos, w: number | undefined, from: string, to: string): number {
  if (!w) return 0;
  const sgn = from < to ? 1 : -1;
  const d = (q.x - p.x) * sgn;
  const s = Math.floor(d / w + 0.5) * w; // d - s in [-w/2, w/2)
  return -sgn * (d - s === -w / 2 ? s - w : s);
}

/**
 * Routes every edge: custom waypoints when present; otherwise a straight
 * line, split into two arcs for a bidirectional pair, and bent around any
 * dot it would pass through (both sides, growing offset, first clear wins).
 */
export function routeEdges(g: Graph, pos: Record<string, Pos>, opts: RouteOpts = {}): Map<string, EdgeGeom> {
  const out = new Map<string, EdgeGeom>();
  // Obstacles: every dot, plus its name label just below it, bucketed in
  // a uniform grid so a curve only tests the obstacles near it.
  const placedNodes = [...g.nodes.values()].filter((n) => pos[n.key]);
  const stamps = new Int32Array(placedNodes.length);
  const shifts = opts.wrap ? [0, -opts.wrap, opts.wrap] : [0];
  const obstacles = placedNodes.flatMap((n, ni) => {
    const p0 = pos[n.key], r = nodeRadiusFor(n);
    return shifts.flatMap((dx) => {
      const p = { x: p0.x + dx, y: p0.y };
      return [{ key: n.key, ni, p, r }, { key: n.key, ni, p: { x: p.x, y: p.y + r + 10 }, r: 7 }];
    });
  });
  const CELL = 64;
  const grid = new Map<string, typeof obstacles>();
  const occupied: { cx: number; cy: number; l: typeof obstacles }[] = [];
  for (const o of obstacles) {
    const cx = Math.floor(o.p.x / CELL), cy = Math.floor(o.p.y / CELL);
    const k = `${cx},${cy}`;
    let l = grid.get(k);
    if (!l) {
      grid.set(k, (l = []));
      occupied.push({ cx, cy, l });
    }
    l.push(o);
  }
  // The obstacle lists in cells x0..x1 × y0..y1. A box larger than the
  // occupied cells (a long line on a zoomed-in map spans millions of cells)
  // walks the occupied cells instead.
  const near = (x0: number, x1: number, y0: number, y1: number): (typeof obstacles)[] => {
    const out: (typeof obstacles)[] = [];
    if ((x1 - x0 + 1) * (y1 - y0 + 1) > occupied.length) {
      for (const c of occupied) if (c.cx >= x0 && c.cx <= x1 && c.cy >= y0 && c.cy <= y1) out.push(c.l);
      return out;
    }
    for (let cx = x0; cx <= x1; cx++) {
      for (let cy = y0; cy <= y1; cy++) {
        const l = grid.get(`${cx},${cy}`);
        if (l) out.push(l);
      }
    }
    return out;
  };
  let stamp = 0;
  const hits = (pts: Pos[], skipA: string, skipB: string, limit = Infinity): number => {
    const poly = sample(spline(pts), 10);
    stamp++;
    let n = 0;
    for (let i = 0; i < poly.length - 1; i++) {
      const a = poly[i], b = poly[i + 1];
      const pad = R_HUB_OUTER + 5;
      const x0 = Math.floor((Math.min(a.x, b.x) - pad) / CELL), x1 = Math.floor((Math.max(a.x, b.x) + pad) / CELL);
      const y0 = Math.floor((Math.min(a.y, b.y) - pad) / CELL), y1 = Math.floor((Math.max(a.y, b.y) + pad) / CELL);
      const dx = b.x - a.x, dy = b.y - a.y, l2 = dx * dx + dy * dy;
      for (const l of near(x0, x1, y0, y1)) {
        for (const o of l) {
          if (stamps[o.ni] === stamp || o.key === skipA || o.key === skipB) continue;
          let t = l2 ? ((o.p.x - a.x) * dx + (o.p.y - a.y) * dy) / l2 : 0;
          t = Math.max(0, Math.min(1, t));
          if (Math.hypot(o.p.x - (a.x + dx * t), o.p.y - (a.y + dy * t)) < o.r + 5) {
            // Count each dot once (its label obstacle shares the index).
            stamps[o.ni] = stamp;
            if (++n >= limit) return n;
          }
        }
      }
    }
    return n;
  };
  for (const e of g.edges.values()) {
    const p = pos[e.from], q0 = pos[e.to];
    if (!p || !q0) continue;
    const q = opts.wrap ? { x: q0.x + wrapDx(p, q0, opts.wrap, e.from, e.to), y: q0.y } : q0;
    const rA = nodeRadiusFor(g.nodes.get(e.from));
    const rB = nodeRadiusFor(g.nodes.get(e.to));
    const trim = (W: Pos[]) => {
      const ua = unit(p, W[0] || q), ub = unit(q, W[W.length - 1] || p);
      return [{ x: p.x + ua.x * rA, y: p.y + ua.y * rA }, ...W, { x: q.x + ub.x * rB, y: q.y + ub.y * rB }];
    };
    const wps = opts.waypoints?.[e.key];
    if (wps && wps.length) {
      const W = wps.map((w) => toAbs(p, q, w));
      out.set(e.key, geomOf(e.key, trim(W), W, true));
      continue;
    }
    const L = Math.hypot(q.x - p.x, q.y - p.y) || 1;
    // Canonical normal (from the lexically smaller end) so the two
    // directions of a pair bend to opposite sides.
    const [lo, hi] = e.from < e.to ? [p, q] : [q, p];
    const nx = -(hi.y - lo.y) / L, ny = (hi.x - lo.x) / L;
    const own = e.from < e.to ? 1 : -1;
    const pair = e.directionKnown && g.edges.has(`${e.to}→${e.from}`);
    const mid = { x: (p.x + q.x) / 2, y: (p.y + q.y) / 2 };
    const bendAt = (off: number, side: number) => ({ x: mid.x + nx * off * side, y: mid.y + ny * off * side });
    let W: Pos[] = pair ? [bendAt(Math.min(18, L * 0.22), own)] : [];
    if (opts.avoid) {
      const h0 = hits(trim(W), e.from, e.to);
      if (h0 > 0) {
        // Bend only when it actually clears more dots, and never further
        // out than a fraction of the edge's length (no wild loops).
        let best = { W, h: h0 };
        const maxOff = Math.max(40, L * 0.4);
        outer: for (const off of [26, 40, 56, 76, 100, 130, 170]) {
          if (off > maxOff) break;
          for (const side of pair ? [own] : [own, -own]) {
            const cand = [bendAt(off, side)];
            const h = hits(trim(cand), e.from, e.to, best.h);
            if (h < best.h) best = { W: cand, h };
            if (h === 0) break outer;
          }
        }
        W = best.W;
      }
    }
    out.set(e.key, geomOf(e.key, trim(W), W, false));
  }
  return out;
}

// ---------------------------------------------------------------- labels

export interface LabelSpot {
  x: number; y: number;
  /** Arc-length fraction the label sits at. */
  t: number;
  anchor: 'start' | 'middle' | 'end';
}

/** Box a label pair takes at a spot (both lines, a full-size rate). */
function labelBox(s: { x: number; y: number; anchor: LabelSpot['anchor'] }): Box {
  const x0 = s.anchor === 'start' ? s.x - 1 : s.anchor === 'end' ? s.x - LABEL_W + 1 : s.x - LABEL_W / 2;
  return { x0, x1: x0 + LABEL_W, y0: s.y - 13, y1: s.y + LABEL_H - 13 };
}

/**
 * Places each edge's latency / rate label: at the stored fraction when the
 * user dragged it, otherwise near the middle on the side with more room,
 * slid along the curve (or moved to the other side) until it covers no dot,
 * node name or already placed label; the least covered spot otherwise.
 */
export function placeLabels(
  g: Graph, pos: Record<string, Pos>, geoms: Map<string, EdgeGeom>, labelT: Record<string, number | undefined>,
): Map<string, LabelSpot> {
  const out = new Map<string, LabelSpot>();
  const nodeList = [...g.nodes.values()].filter((n) => pos[n.key]).map((n) => ({ key: n.key, p: pos[n.key], r: nodeRadiusFor(n) }));
  // Dots and the names under them.
  const fixed: Box[] = [];
  for (const n of g.nodes.values()) {
    const p = pos[n.key];
    if (!p) continue;
    const r = nodeRadiusFor(n), w = (n.name.length * NAME_CH) / 2, ty = p.y + r + 14;
    fixed.push({ x0: p.x - r - 2, x1: p.x + r + 2, y0: p.y - r - 2, y1: p.y + r + 2 });
    fixed.push({ x0: p.x - w, x1: p.x + w, y0: ty - 8.5, y1: ty + 2.5 });
  }
  const placed: Box[] = [];
  const covered = (b: Box) => {
    let c = 0;
    for (const f of fixed) if (boxHit(b, f)) c++;
    for (const f of placed) if (boxHit(b, f)) c++;
    return c;
  };
  const SLIDE = [0.5];
  for (let step = 1; step <= 7; step++) SLIDE.push(0.5 + step * 0.05, 0.5 - step * 0.05);
  // User-placed labels first so automatic ones avoid them.
  const keys = [...geoms.keys()].sort((a, b) => (labelT[a] !== undefined ? 0 : 1) - (labelT[b] !== undefined ? 0 : 1));
  for (const key of keys) {
    const geo = geoms.get(key)!;
    const e = g.edges.get(key);
    if (!e) continue;
    const bend = geo.bends.length === 1 && !geo.custom ? geo.bends[0] : null;
    const spotAt = (t: number, flip: boolean): LabelSpot => {
      const at = pointAt(geo, geo.len * t);
      const nx = -at.ty, ny = at.tx;
      // Side: the outer side of a bend (keeps the two labels of a split
      // pair apart), otherwise the side farther from other dots.
      let side = 1;
      if (bend) {
        const dot = (bend.x - at.x) * nx + (bend.y - at.y) * ny;
        const chordMid = { x: (geo.pts[0].x + geo.pts[geo.pts.length - 1].x) / 2, y: (geo.pts[0].y + geo.pts[geo.pts.length - 1].y) / 2 };
        const out2 = (bend.x - chordMid.x) * nx + (bend.y - chordMid.y) * ny;
        side = Math.abs(out2) > 1 ? Math.sign(out2) : dot >= 0 ? 1 : -1;
      } else {
        let bestC = -Infinity;
        for (const s of [1, -1]) {
          const cx = at.x + nx * 12 * s, cy = at.y + ny * 12 * s;
          let m = Infinity;
          for (const o of nodeList) {
            if (o.key === e.from || o.key === e.to) continue;
            m = Math.min(m, Math.hypot(o.p.x - cx, o.p.y - cy));
          }
          for (const pl of placed) m = Math.min(m, Math.hypot((pl.x0 + pl.x1) / 2 - cx, (pl.y0 + pl.y1) / 2 - cy) + 6);
          if (m > bestC) { bestC = m; side = s; }
        }
      }
      if (flip) side = -side;
      const sx = nx * side, sy = ny * side;
      let anchor: LabelSpot['anchor'] = 'middle';
      let off = 8 + Math.abs(sy) * 10;
      if (Math.abs(sx) > 0.75) { anchor = sx > 0 ? 'start' : 'end'; off = 7; }
      return { x: at.x + sx * off, y: at.y + sy * off, t, anchor };
    };
    let spot: LabelSpot;
    const tUser = labelT[key];
    if (tUser !== undefined) {
      spot = spotAt(tUser, false);
    } else {
      spot = spotAt(0.5, false);
      let best = covered(labelBox(spot));
      for (const flip of best ? [false, true] : []) {
        for (const t of SLIDE) {
          if (!flip && t === 0.5) continue;
          const c = spotAt(t, flip);
          const n = covered(labelBox(c));
          if (n < best) { best = n; spot = c; }
          if (!best) break;
        }
        if (!best) break;
      }
    }
    out.set(key, spot);
    placed.push(labelBox(spot));
  }
  return out;
}

/** Bounding box of the dots and edge bends. */
export function contentBounds(pos: Record<string, Pos>, geoms?: Map<string, EdgeGeom>) {
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  const add = (p: Pos) => {
    if (p.x < minX) minX = p.x; if (p.x > maxX) maxX = p.x;
    if (p.y < minY) minY = p.y; if (p.y > maxY) maxY = p.y;
  };
  for (const k in pos) add(pos[k]);
  geoms?.forEach((g) => g.bends.forEach(add));
  if (!Number.isFinite(minX)) return null;
  return { minX, minY, maxX, maxY };
}
