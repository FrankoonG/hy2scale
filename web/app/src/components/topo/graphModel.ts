import type { TopologyNode } from '@/api';

/*
 * Merged topology graph: one dot per node (merged by name), one edge per
 * directed dialer → dialee connection. Built from the /api/topology tree,
 * which already applies the nested gate and cycle rules on the backend
 * (docs/nested-rules.md); the walk below re-applies rule 1 (no repeated
 * node in a path, self id AND name always ancestors) so the graph never
 * shows more than the list.
 */

export interface Pos { x: number; y: number }

export interface GraphNode {
  key: string;
  name: string;
  isSelf: boolean;
  disabled: boolean;
  /** Unreachable on every path that reaches it (or disabled). */
  offline: boolean;
  nested: boolean;
  native: boolean;
  incompatible: boolean;
  /** Shortest hop count from self. */
  depth: number;
  degree: number;
  qpath: string;
  /** Cumulative latency from self in ms (0 unknown, -1 offline). */
  totalLatencyMs: number;
  /** Configured locally (self or a top-level outbound client). */
  editable: boolean;
  /** Direct peer with a live hole-punched connection: who initiated it. */
  punched?: string[];
}

export interface GraphEdge {
  key: string;
  from: string;             // dialer side (arrow source)
  to: string;               // dialee side
  directionKnown: boolean;
  disabled: boolean;
  /** Bytes/sec flowing from `to` toward `from`. */
  txRate: number;
  /** Bytes/sec flowing from `from` toward `to`. */
  rxRate: number;
  /** Configured bandwidth ceiling (bytes/sec), 0 = unknown. */
  configuredMaxRate: number;
  /** Latency of this hop alone (ms), taken from the observation on the
   *  lowest-latency path through the edge so it agrees with the shortest
   *  path's total. -1 = broken hop, 0 = unknown. */
  segmentLatencyMs: number;
}

export const R_SINGLE = 11;
export const R_HUB_OUTER = 15;
export const R_HUB_INNER = 7;

/** Concentric-circle marker: self, or a transit-like node (degree ≥ 3). */
export function isTransit(n: GraphNode | undefined): boolean {
  return !!n && (n.isSelf || n.degree >= 3);
}

export function nodeRadiusFor(n: GraphNode | undefined): number {
  return isTransit(n) ? R_HUB_OUTER : R_SINGLE;
}

export function fmtLatency(ms: number): string {
  if (ms < 0) return 'offline';
  if (ms === 0) return '—';
  return `${Math.round(ms)}ms`;
}

export function buildGraph(topo: TopologyNode[], selfId: string, selfName?: string) {
  const nodes = new Map<string, GraphNode>();
  const edges = new Map<string, GraphEdge>();
  const selfSet = new Set<string>([selfId, selfName || ''].filter(Boolean));
  const reach = new Map<string, { any: boolean; un: boolean }>();
  // Cumulative latency at the child for the observation each edge's
  // segment latency was taken from (lower = on a shorter path).
  const edgeBest = new Map<string, number>();

  const keyOf = (n: TopologyNode) => (n.is_self ? selfId : n.name);

  function addOrMergeNode(n: TopologyNode, depth: number, qpath: string, editable: boolean) {
    const k = keyOf(n);
    const unreachable = n.connected === false && !n.is_self;
    const r = reach.get(k) || { any: false, un: false };
    if (unreachable) r.un = true; else r.any = true;
    reach.set(k, r);
    const existing = nodes.get(k);
    if (!existing) {
      nodes.set(k, {
        key: k,
        name: n.is_self ? (selfName || n.name || 'self') : n.name,
        isSelf: !!n.is_self,
        disabled: !!n.disabled,
        offline: !!n.disabled || unreachable,
        nested: !!n.nested,
        native: !!n.native,
        incompatible: !!n.incompatible || !!n.conflict || !!n.unsupported,
        depth,
        degree: 0,
        qpath,
        totalLatencyMs: n.is_self ? 0 : (typeof n.latency_ms === 'number' ? n.latency_ms : 0),
        editable,
        punched: depth === 1 && n.punched?.length ? n.punched : undefined,
      });
      return;
    }
    // OR-merge positive states; offline only when EVERY path is unreachable.
    if (n.disabled) existing.disabled = true;
    if (n.nested) existing.nested = true;
    if (n.native) existing.native = true;
    if (n.incompatible || n.conflict || n.unsupported) existing.incompatible = true;
    if (depth === 1 && n.punched?.length) existing.punched = n.punched;
    existing.offline = existing.disabled || (r.un && !r.any);
    if (depth < existing.depth) {
      existing.depth = depth;
      existing.qpath = qpath;
      if (!n.is_self && typeof n.latency_ms === 'number' && n.latency_ms > 0) existing.totalLatencyMs = n.latency_ms;
      if (editable) existing.editable = true;
    }
  }

  function addEdge(a: string, b: string, n: TopologyNode, segLat: number) {
    if (a === b) return;
    let from = a, to = b, directionKnown = false, ekey: string;
    if (n.direction === 'outbound') { directionKnown = true; ekey = `${a}→${b}`; }
    else if (n.direction === 'inbound') { from = b; to = a; directionKnown = true; ekey = `${b}→${a}`; }
    else { const [lo, hi] = a < b ? [a, b] : [b, a]; ekey = `?${lo}|${hi}`; }
    const txRate = n.tx_rate || 0;
    const rxRate = n.rx_rate || 0;
    const maxRate = n.max_rate || 0;
    const cum = n.connected === false || !(n.latency_ms > 0) ? Infinity : n.latency_ms;
    const e = edges.get(ekey);
    if (!e) {
      edges.set(ekey, {
        key: ekey, from, to, directionKnown, disabled: !!n.disabled,
        txRate, rxRate, configuredMaxRate: maxRate, segmentLatencyMs: segLat,
      });
      edgeBest.set(ekey, cum);
      const na = nodes.get(a); if (na) na.degree++;
      const nb = nodes.get(b); if (nb) nb.degree++;
      return;
    }
    if (txRate > e.txRate) e.txRate = txRate;
    if (rxRate > e.rxRate) e.rxRate = rxRate;
    if (maxRate > e.configuredMaxRate) e.configuredMaxRate = maxRate;
    if (n.disabled) e.disabled = true;
    // Prefer the observation on the lowest-latency path through this edge.
    if (segLat > 0 && cum < (edgeBest.get(ekey) ?? Infinity)) {
      e.segmentLatencyMs = segLat;
      edgeBest.set(ekey, cum);
    } else if (e.segmentLatencyMs <= 0 && segLat > 0) {
      e.segmentLatencyMs = segLat;
    }
  }

  function segLatency(n: TopologyNode, parentCum: number): number {
    // `connected === false` marks this hop broken (sub-peers keep
    // latency_ms = 0 when offline, so it must be checked explicitly).
    if (n.connected === false) return -1;
    const c = n.latency_ms;
    if (c > 0 && parentCum >= 0) { const d = c - parentCum; return d > 0 ? d : c; }
    if (c > 0) return c;
    if (c === -1) return -1;
    return 0;
  }

  function walk(list: TopologyNode[], parentKey: string, parentCum: number, depth: number, parentQpath: string, editableRoot: boolean) {
    for (const n of list) {
      if (selfSet.has(n.name)) continue;
      const segs = parentQpath.split('/');
      if (segs.includes(n.name)) continue;
      const qpath = parentQpath + '/' + n.name;
      addOrMergeNode(n, depth, qpath, editableRoot && depth === 1);
      addEdge(parentKey, keyOf(n), n, segLatency(n, parentCum));
      if (n.children && n.children.length > 0) {
        walk(n.children, keyOf(n), n.latency_ms || 0, depth + 1, qpath, false);
      }
    }
  }

  // INBOUND peers are children of the self entry, OUTBOUND peers are
  // top-level siblings; both get a self ↔ peer edge.
  const selfEntry = topo.find((n) => n.is_self);
  const selfKey = selfEntry ? keyOf(selfEntry) : selfId;
  if (selfEntry) addOrMergeNode(selfEntry, 0, selfKey, true);
  for (const n of topo) {
    if (n.is_self) {
      if (n.children?.length) walk(n.children, selfKey, 0, 1, selfKey, false);
    } else {
      walk([n], selfKey, 0, 1, selfKey, true);
    }
  }
  return { nodes, edges };
}

/**
 * One route from self to a target, in two forms:
 * - `segs`: the list-view row key segments (inbound routes start with the
 *   self id; outbound top-level routes start at the peer);
 * - `keys`: graph node keys, always starting at self.
 */
export interface GraphPath {
  segs: string[];
  keys: string[];
  /** Per-hop latency (ms) along this route; -1 broken, 0 unknown. */
  hopLat: number[];
  /** Cumulative latency (ms) as measured along this route; -1 if broken. */
  total: number;
  offline: boolean;
}

/**
 * Every route from self to the node with graph key `targetKey`, applying
 * the same per-path rule-1 filter as the list view. Sorted by total
 * latency (reachable first), so index 0 is the shortest path.
 */
export function findPaths(topology: TopologyNode[], targetKey: string, selfId: string, selfName?: string): GraphPath[] {
  const out: GraphPath[] = [];
  const selfSet = new Set<string>([selfId, selfName || ''].filter(Boolean));
  const keyOf = (n: TopologyNode) => (n.is_self ? selfId : n.name);
  function walk(list: TopologyNode[], segs: string[], keys: string[], lats: number[], prevCum: number, broken: boolean) {
    for (const n of list) {
      if (segs.length > 0 && selfSet.has(n.name)) continue;
      if (segs.length > 0 && segs.includes(n.name)) continue;
      const k = keyOf(n);
      const nextSegs = segs.length === 0 ? [k] : [...segs, n.name];
      let nextKeys = keys, nextLats = lats, cum = prevCum, br = broken;
      if (!n.is_self) {
        const bad = n.connected === false || !!n.disabled || n.latency_ms === -1;
        const c = n.latency_ms > 0 ? n.latency_ms : 0;
        const hop = bad ? -1 : c > 0 && prevCum >= 0 && c - prevCum > 0 ? c - prevCum : c;
        nextKeys = [...keys, k];
        nextLats = [...lats, hop];
        cum = c;
        br = broken || bad;
      }
      if (k === targetKey) {
        out.push({
          segs: nextSegs,
          keys: nextKeys,
          hopLat: nextLats,
          total: br ? -1 : n.is_self ? 0 : cum,
          offline: br,
        });
      }
      if (n.children?.length) walk(n.children, nextSegs, nextKeys, nextLats, cum, br);
    }
  }
  walk(topology, [], [selfId], [], 0, false);
  const rank = (p: GraphPath) => (p.offline ? 2 : p.total > 0 ? 0 : 1);
  return out
    .map((p, i) => ({ p, i }))
    .sort((a, b) => rank(a.p) - rank(b.p) || (rank(a.p) === 0 ? a.p.total - b.p.total : 0) || a.p.keys.length - b.p.keys.length || a.i - b.i)
    .map((x) => x.p);
}

/** Back-compat helper: just the list-view key segments of every route. */
export function findPathsToName(topology: TopologyNode[], targetKey: string, selfId: string, selfName?: string): string[][] {
  return findPaths(topology, targetKey, selfId, selfName).map((p) => p.segs);
}
