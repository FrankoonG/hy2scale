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

// ---------------------------------------------------------------- layered

const LEVEL = 130;
const SIB = 92;
const ROW_STAGGER = 48;
const MAX_PER_ROW = 12;

function layeredTD(g: Graph): Record<string, Pos> {
  const ls = orderLayers(g, layersOf(g));
  const pos: Record<string, Pos> = {};
  let y = 0;
  for (const l of ls) {
    // Wide layers zig-zag over several staggered rows: the left-to-right
    // order is kept, same-row neighbours stay SIB apart, and edges from
    // the layer above pass between the dots of the front row.
    const rows = Math.ceil(l.length / MAX_PER_ROW);
    const step = SIB / rows;
    l.forEach((k, i) => {
      pos[k] = { x: (i - (l.length - 1) / 2) * step, y: y + (i % rows) * ROW_STAGGER };
    });
    y += LEVEL + (rows - 1) * ROW_STAGGER;
  }
  return pos;
}

const LEVEL_X = 200;
const SIB_Y = 56;
const COL_STAGGER = 84;
const MAX_PER_COL = 14;

function layeredLR(g: Graph): Record<string, Pos> {
  const ls = orderLayers(g, layersOf(g));
  const pos: Record<string, Pos> = {};
  let x = 0;
  for (const l of ls) {
    const cols = Math.ceil(l.length / MAX_PER_COL);
    const step = SIB_Y / cols;
    l.forEach((k, i) => {
      pos[k] = { x: x + (i % cols) * COL_STAGGER, y: (i - (l.length - 1) / 2) * step };
    });
    x += LEVEL_X + (cols - 1) * COL_STAGGER;
  }
  return pos;
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
  for (const n of g.nodes.values()) counts[n.depth] = (counts[n.depth] || 0) + 1;
  const R: number[] = [0];
  for (let d = 1; d < counts.length; d++) {
    R[d] = Math.max((R[d - 1] || 0) + RING, ((counts[d] || 0) * MIN_ARC) / (2 * Math.PI));
  }
  const weight = new Map<string, number>();
  const w = (k: string): number => {
    const ks = tree.kids.get(k) || [];
    const v = ks.length ? ks.reduce((a, c) => a + w(c), 0) : 1;
    weight.set(k, v);
    return v;
  };
  const allocate = () => {
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
  orderSiblings(g, root, tree, allocate);
  const ang = allocate();
  // Per ring, enforce a minimum angular gap (circular sweep) so compressed
  // subtrees never stack dots.
  const byDepth: string[][] = [];
  for (const k of Object.keys(ang)) if (k !== root) (byDepth[depthOf(k)] ||= []).push(k);
  byDepth.forEach((ring, d) => {
    if (!ring || ring.length < 2) return;
    const gap = MIN_ARC / R[d];
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
  const r = Math.max(170, (seq.length * MIN_ARC * 1.15) / (2 * Math.PI));
  const pos: Record<string, Pos> = { [root]: { x: 0, y: 0 } };
  seq.forEach((k, i) => {
    const a = -Math.PI / 2 + (2 * Math.PI * (i + 0.5)) / seq.length;
    pos[k] = { x: Math.cos(a) * r, y: Math.sin(a) * r };
  });
  return pos;
}

export function presetLayout(id: PresetId, g: Graph): Record<string, Pos> {
  if (g.nodes.size === 0) return {};
  switch (id) {
    case 'layered-lr': return layeredLR(g);
    case 'radial': return radial(g);
    case 'ring': return ring(g);
    default: return layeredTD(g);
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
  const obstacles = placedNodes.flatMap((n, ni) => {
    const p = pos[n.key], r = nodeRadiusFor(n);
    return [{ key: n.key, ni, p, r }, { key: n.key, ni, p: { x: p.x, y: p.y + r + 10 }, r: 7 }];
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
    const p = pos[e.from], q = pos[e.to];
    if (!p || !q) continue;
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

/**
 * Places each edge's latency / rate label: at the stored fraction when the
 * user dragged it, otherwise near the middle, slid along the curve past
 * other dots and already placed labels; offset to the side with more room.
 */
export function placeLabels(
  g: Graph, pos: Record<string, Pos>, geoms: Map<string, EdgeGeom>, labelT: Record<string, number | undefined>,
): Map<string, LabelSpot> {
  const out = new Map<string, LabelSpot>();
  const nodeList = [...g.nodes.values()].filter((n) => pos[n.key]).map((n) => ({ key: n.key, p: pos[n.key], r: nodeRadiusFor(n) }));
  const placed: Pos[] = [];
  // User-placed labels first so automatic ones avoid them.
  const keys = [...geoms.keys()].sort((a, b) => (labelT[a] !== undefined ? 0 : 1) - (labelT[b] !== undefined ? 0 : 1));
  for (const key of keys) {
    const geo = geoms.get(key)!;
    const e = g.edges.get(key);
    if (!e) continue;
    const blocked = (pt: Pos) => {
      for (const o of nodeList) {
        if (o.key === e.from || o.key === e.to) continue;
        if (Math.hypot(o.p.x - pt.x, o.p.y - pt.y) < o.r + 16) return true;
      }
      for (const pl of placed) if (Math.abs(pl.x - pt.x) < 50 && Math.abs(pl.y - pt.y) < 26) return true;
      return false;
    };
    let t = labelT[key];
    if (t === undefined) {
      t = 0.5;
      if (blocked(pointAt(geo, geo.len * 0.5))) {
        for (let step = 1; step <= 12; step++) {
          const c1 = 0.5 + step * 0.05, c2 = 0.5 - step * 0.05;
          if (c1 <= 0.85 && !blocked(pointAt(geo, geo.len * c1))) { t = c1; break; }
          if (c2 >= 0.15 && !blocked(pointAt(geo, geo.len * c2))) { t = c2; break; }
        }
      }
    }
    const at = pointAt(geo, geo.len * t);
    const nx = -at.ty, ny = at.tx;
    // Side: the outer side of a bend (keeps the two labels of a split pair
    // apart), otherwise the side farther from other dots.
    let side = 1;
    const bend = geo.bends.length === 1 && !geo.custom ? geo.bends[0] : null;
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
        for (const pl of placed) m = Math.min(m, Math.hypot(pl.x - cx, pl.y - cy) + 6);
        if (m > bestC) { bestC = m; side = s; }
      }
    }
    const sx = nx * side, sy = ny * side;
    let anchor: LabelSpot['anchor'] = 'middle';
    let off = 8 + Math.abs(sy) * 10;
    if (Math.abs(sx) > 0.75) { anchor = sx > 0 ? 'start' : 'end'; off = 7; }
    const spot = { x: at.x + sx * off, y: at.y + sy * off, t, anchor };
    out.set(key, spot);
    placed.push({ x: spot.x, y: spot.y });
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
