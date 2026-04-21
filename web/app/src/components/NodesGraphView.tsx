import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type MouseEvent, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { TopologyNode } from '@/api';
import { fmtRate } from '@/hooks/useFormat';
import { useExitPaths } from '@/hooks/useExitPaths';

interface Props {
  topology: TopologyNode[];
  /** Local node_id — used as the key for the self-node. */
  selfId: string;
  selfName?: string;
  onOpenRemote?: (qpath: string) => void;
  /** Current qpath selected from the list view (keyed path from self). When
   *  the user clicks a node in the graph, we notify the parent to update
   *  this — selection state is shared with the list so the card-header
   *  bulk action buttons respond the same way. */
  selectedQPath?: string | null;
  onSelectQPath?: (qpath: string | null) => void;
}

interface Pos { x: number; y: number }

interface GraphNode {
  key: string;
  name: string;
  isSelf: boolean;
  disabled: boolean;
  /** Unreachable (connection down OR latency report negative OR disabled).
   *  An offline direct-peer stays visible in the graph; its edge turns red
   *  and the flow animation pauses. */
  offline: boolean;
  nested: boolean;
  native: boolean;
  incompatible: boolean;
  depth: number;
  degree: number;
  qpath: string;
  /** Cumulative latency from self, in ms (as reported by the topology).
   *  0 when unknown, -1 when offline. */
  totalLatencyMs: number;
}

interface GraphEdge {
  key: string;
  from: string;             // dialer side (arrow source)
  to: string;               // dialee side
  directionKnown: boolean;
  disabled: boolean;
  /** Current tx+rx in bytes/sec (updated every topology poll). */
  currentRate: number;
  /** Segment latency in ms — between the two endpoints only, NOT cumulative
   *  from self. Derived as child.cumulative − parent.cumulative during the
   *  tree walk and stored for the first-visited occurrence of the edge. */
  segmentLatencyMs: number;
}

const R_SINGLE = 11;
const R_HUB_OUTER = 15;
const R_HUB_INNER = 7;
const LEVEL_GAP = 130;
const SIBLING_GAP = 85;
const NODE_PADDING = 8;           // extra px between two nodes' edges
const LS_POS_KEY = 'scale:topology-graph-positions';
/** 1000 Mbps ≡ 1 Gbps ≡ 125 000 000 bytes/sec. Used as the absolute ceiling
 *  for edge-thickness scaling so line width is comparable across deployments
 *  instead of being relative to whatever the local mesh happens to have
 *  observed. Rates above 1 Gbps clamp to maximum width. */
const REF_MBPS_1000 = 125_000_000;

/** Concentric-circle marker: self, or any node that acts as transit in the
 *  current mesh (degree >= 3). NOT gated on nested, because a single dot
 *  can represent multiple qualified paths, each with its own nested state
 *  — so "nested" isn't a clean per-dot property. Keep the heuristic. */
function isTransit(n: GraphNode | undefined): boolean {
  return !!n && (n.isSelf || n.degree >= 3);
}

function nodeRadiusFor(n: GraphNode | undefined): number {
  if (!n) return R_SINGLE;
  return isTransit(n) ? R_HUB_OUTER : R_SINGLE;
}

/**
 * Iteratively pushes `pos` outward from any node that overlaps it. Applied
 * both to user drop points and to newly-inserted node positions so circles
 * never stack on top of each other.
 */
/**
 * Walks the topology tree once and collects every qualified path that lands
 * on a node with the given display name (or self id/name when target is the
 * local node). Each returned path is an array of peer-name segments; the
 * first segment is always the self id (topology root key).
 */
function findPathsToName(
  topology: TopologyNode[],
  targetKey: string,
  selfId: string,
  selfName: string | undefined
): string[][] {
  const paths: string[][] = [];
  const selfSet = new Set<string>([selfId, selfName || ''].filter(Boolean));
  function keyOf(n: TopologyNode): string { return n.is_self ? selfId : n.name; }
  // Match the TreeTable key convention on NodesPage:
  //   - inbound child of self → "selfId/peerName" (segments start with selfId
  //     because the self entry is traversed first and adds its own key as
  //     the first segment)
  //   - outbound top-level peer → "peerName" alone (no self prefix)
  // Keeping this layout means the qpath emitted by the graph matches the
  // selection.toggle() keys that useSelection will accept, so clicking an
  // outbound node in the graph selects the same row the TreeTable would.
  function walk(list: TopologyNode[], segments: string[]) {
    for (const n of list) {
      if (segments.length > 0 && selfSet.has(n.name)) continue;
      if (segments.length > 0 && segments.includes(n.name)) continue;
      const nextSegs = segments.length === 0 ? [keyOf(n)] : [...segments, n.name];
      if (keyOf(n) === targetKey) paths.push(nextSegs);
      if (n.children && n.children.length > 0) walk(n.children, nextSegs);
    }
  }
  walk(topology, []);
  return paths;
}

/** Given a path of node keys, return the set of edge keys traversed. */
function edgeKeysOnPath(path: string[], edges: Map<string, GraphEdge>): Set<string> {
  const s = new Set<string>();
  for (let i = 0; i < path.length - 1; i++) {
    const a = path[i], b = path[i + 1];
    const candidates = [
      `${a}→${b}`,
      `${b}→${a}`,
      `?${a < b ? a : b}|${a < b ? b : a}`,
    ];
    for (const c of candidates) if (edges.has(c)) { s.add(c); break; }
  }
  return s;
}

function resolveOverlap(
  key: string,
  pos: Pos,
  others: Record<string, Pos>,
  nodes: Map<string, GraphNode>
): Pos {
  let x = pos.x;
  let y = pos.y;
  const rSelf = nodeRadiusFor(nodes.get(key));
  for (let iter = 0; iter < 8; iter++) {
    let moved = false;
    for (const k in others) {
      if (k === key) continue;
      const other = others[k];
      const rOther = nodeRadiusFor(nodes.get(k));
      const minDist = rSelf + rOther + NODE_PADDING;
      let dx = x - other.x;
      let dy = y - other.y;
      let dist = Math.sqrt(dx * dx + dy * dy);
      if (dist < 0.001) {
        // Exactly coincident — pick a deterministic direction based on the
        // hash of the key so repeat renders land consistently.
        const ang = ((key.charCodeAt(0) * 37 + key.length * 11) % 360) * (Math.PI / 180);
        dx = Math.cos(ang);
        dy = Math.sin(ang);
        dist = 1;
      }
      if (dist < minDist) {
        const push = minDist - dist + 0.1;
        x += (dx / dist) * push;
        y += (dy / dist) * push;
        moved = true;
      }
    }
    if (!moved) break;
  }
  return { x, y };
}

function loadPositions(): Record<string, Pos> {
  try {
    const raw = localStorage.getItem(LS_POS_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === 'object') return parsed;
  } catch { /* ignore */ }
  return {};
}
function savePositions(p: Record<string, Pos>) {
  try { localStorage.setItem(LS_POS_KEY, JSON.stringify(p)); } catch { /* ignore */ }
}

function buildGraph(topo: TopologyNode[], selfId: string, selfName?: string) {
  const nodes = new Map<string, GraphNode>();
  const edges = new Map<string, GraphEdge>();
  const selfSet = new Set<string>([selfId, selfName || ''].filter(Boolean));

  function keyOf(n: TopologyNode): string {
    return n.is_self ? selfId : n.name;
  }

  function addOrMergeNode(n: TopologyNode, depth: number, qpath: string) {
    const k = keyOf(n);
    const existing = nodes.get(k);
    if (!existing) {
      nodes.set(k, {
        key: k,
        name: n.is_self ? (selfName || n.name || 'self') : n.name,
        isSelf: !!n.is_self,
        disabled: !!n.disabled,
        // Offline if explicitly disabled, latency reported as -1 (unreachable),
        // or the top-level `connected` flag is false. Root-peer offline keeps
        // its dot visible but edges to it turn red + marked ×.
        offline: !!n.disabled || (n.latency_ms < 0) || (n.connected === false && !n.is_self),
        nested: !!n.nested,
        native: !!n.native,
        incompatible: !!n.incompatible || !!n.conflict,
        depth,
        degree: 0,
        qpath,
        totalLatencyMs: n.is_self ? 0 : (typeof n.latency_ms === 'number' ? n.latency_ms : 0),
      });
    } else if (depth < existing.depth) {
      existing.depth = depth;
      existing.qpath = qpath;
      if (!n.is_self && typeof n.latency_ms === 'number' && n.latency_ms > 0) {
        existing.totalLatencyMs = n.latency_ms;
      }
    }
  }

  function addEdge(a: string, b: string, direction: string | undefined, disabled: boolean, rate: number, segmentLatencyMs: number) {
    if (a === b) return;
    // Directed keying: an edge represents a specific dialer→dialee TCP link.
    // - outbound: parent dialed child → key `a→b`
    // - inbound:  child dialed parent → key `b→a`
    // - unknown:  fall back to undirected sorted pair so it still dedupes
    //             across passes; keep directionKnown=false.
    let from = a, to = b, directionKnown = false, ekey: string;
    if (direction === 'outbound') { from = a; to = b; directionKnown = true; ekey = `${from}→${to}`; }
    else if (direction === 'inbound') { from = b; to = a; directionKnown = true; ekey = `${from}→${to}`; }
    else {
      const [lo, hi] = a < b ? [a, b] : [b, a];
      ekey = `?${lo}|${hi}`;
    }
    const existing = edges.get(ekey);
    if (!existing) {
      const e: GraphEdge = { key: ekey, from, to, directionKnown, disabled, currentRate: rate, segmentLatencyMs };
      edges.set(ekey, e);
      const na = nodes.get(a); if (na) na.degree++;
      const nb = nodes.get(b); if (nb) nb.degree++;
    } else {
      if (rate > existing.currentRate) existing.currentRate = rate;
      if (disabled) existing.disabled = true;
      // keep first-visited segment latency
    }
  }

  function walk(list: TopologyNode[], parentKey: string | null, parentCumulativeLat: number, depth: number, parentQpath: string) {
    list.forEach((n) => {
      if (depth > 0 && selfSet.has(n.name)) return;
      const segs = parentQpath ? parentQpath.split('/') : [];
      if (depth > 0 && segs.includes(n.name)) return;
      const qpath = parentQpath ? parentQpath + '/' + n.name : keyOf(n);
      addOrMergeNode(n, depth, qpath);
      if (parentKey) {
        const rate = (n.tx_rate || 0) + (n.rx_rate || 0);
        const childLat = n.latency_ms;
        // Segment latency: cumulative at child minus cumulative at parent.
        // When either is unknown (0 = not measured, -1 = offline) fall back
        // to the child's own value — best we can do with current data.
        let segLat = 0;
        if (childLat > 0 && parentCumulativeLat >= 0) {
          const diff = childLat - parentCumulativeLat;
          segLat = diff > 0 ? diff : childLat;
        } else if (childLat > 0) {
          segLat = childLat;
        } else if (childLat === -1) {
          segLat = -1;
        }
        addEdge(parentKey, keyOf(n), n.direction, !!n.disabled, rate, segLat);
      }
      if (n.children && n.children.length > 0) {
        walk(n.children, keyOf(n), n.latency_ms || 0, depth + 1, qpath);
      }
    });
  }

  // The /api/topology response puts INBOUND peers as children of the self
  // entry and OUTBOUND peers as sibling top-level entries. A naive walker
  // with parentKey=null at the top level would never add an edge from self
  // to any outbound peer, and they'd render as disconnected dots. Pull
  // selfKey out and use it as the parent for every non-self top-level node
  // so outbound and inbound peers both receive a self→peer edge.
  const selfEntry = topo.find((n) => n.is_self);
  const selfKey = selfEntry ? keyOf(selfEntry) : selfId;
  if (selfEntry) addOrMergeNode(selfEntry, 0, selfKey);
  topo.forEach((n) => {
    if (n.is_self) {
      if (n.children && n.children.length > 0) {
        walk(n.children, selfKey, 0, 1, selfKey);
      }
    } else {
      walk([n], selfKey, 0, 1, selfKey);
    }
  });
  return { nodes, edges };
}

function autoLayout(nodes: Map<string, GraphNode>): Record<string, Pos> {
  const byDepth: GraphNode[][] = [];
  nodes.forEach((n) => { (byDepth[n.depth] ||= []).push(n); });
  const positions: Record<string, Pos> = {};
  byDepth.forEach((row, depth) => {
    row.sort((a, b) => a.name.localeCompare(b.name));
    const width = (row.length - 1) * SIBLING_GAP;
    row.forEach((n, i) => {
      positions[n.key] = { x: -width / 2 + i * SIBLING_GAP, y: depth * LEVEL_GAP };
    });
  });
  return positions;
}

/**
 * Text-halo element that paints `stroke` with `!important` via ref, so
 * dark-mode extensions that forcibly override inline SVG stroke via an
 * injected CSS rule (e.g. Dark Reader's `--darkreader-inline-stroke`
 * pattern) lose out — the !important on the inline style wins by CSS
 * cascade rules regardless of the extension's own !important rules.
 * Not a dark-reader-specific hack: this is just standard CSS priority.
 */
function HaloText({ x, y, stroke, children }: { x: number; y: number; stroke: string; children: ReactNode }) {
  const ref = useRef<SVGTextElement | null>(null);
  useLayoutEffect(() => {
    if (ref.current) ref.current.style.setProperty('stroke', stroke, 'important');
  }, [stroke]);
  return (
    <text ref={ref} x={x} y={y} textAnchor="middle" className="hy-topo-edge-label-halo">{children}</text>
  );
}

function fmtLatency(ms: number): string {
  if (ms < 0) return 'offline';
  if (ms === 0) return '—';
  if (ms < 1000) return `${Math.round(ms)}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

export default function NodesGraphView({ topology, selfId, selfName, onOpenRemote, selectedQPath, onSelectQPath }: Props) {
  const { t } = useTranslation();
  const { isReachableAt } = useExitPaths();
  const svgRef = useRef<SVGSVGElement | null>(null);

  const { nodes, edges } = useMemo(() => buildGraph(topology, selfId, selfName), [topology, selfId, selfName]);

  // Per-edge running max — used to normalise flow speed.
  const maxByEdgeRef = useRef<Map<string, number>>(new Map());
  useMemo(() => {
    edges.forEach((e) => {
      const prev = maxByEdgeRef.current.get(e.key) || 0;
      if (e.currentRate > prev) maxByEdgeRef.current.set(e.key, e.currentRate);
    });
  }, [edges]);

  const auto = useMemo(() => autoLayout(nodes), [nodes]);
  // `overrides` is the authoritative snapshot of every node's position.
  // It's persisted to localStorage so that offline→online cycles restore
  // the same spot, AND new nodes hot-created mid-session get remembered
  // from the moment they first appear. It's updated in three places:
  //   1. Below — when auto-layout produces positions for nodes that
  //      weren't in storage yet (new hot-created / newly-seen inbound).
  //   2. During drag-move — the dragged node's position tracks the cursor.
  //   3. On drag release — snap + collision-resolved final position.
  const [overrides, setOverrides] = useState<Record<string, Pos>>(() => loadPositions());
  const overridesRef = useRef(overrides);
  useEffect(() => { overridesRef.current = overrides; }, [overrides]);
  const nodesRef = useRef(nodes);
  useEffect(() => { nodesRef.current = nodes; }, [nodes]);

  // When the topology adds a new node that has no persisted position, use
  // the auto-layout slot as its initial spot and nudge it out of the way
  // of any existing node that happens to be sitting there. Immediately
  // persist so next render treats it as user-placed and survives reload.
  useEffect(() => {
    const cur = overridesRef.current;
    let changed = false;
    const next: Record<string, Pos> = { ...cur };
    for (const key in auto) {
      if (!(key in cur)) {
        const initial = auto[key];
        const existing: Record<string, Pos> = {};
        for (const k in next) if (k !== key) existing[k] = next[k];
        next[key] = resolveOverlap(key, initial, existing, nodes);
        changed = true;
      }
    }
    if (changed) {
      setOverrides(next);
      savePositions(next);
    }
  }, [auto, nodes]);

  const positions = useMemo(() => {
    // Position comes from overrides if known, otherwise the freshly computed
    // auto-layout slot (covers the render between a topology change and the
    // effect above persisting new entries).
    const m: Record<string, Pos> = {};
    for (const key in auto) {
      m[key] = overrides[key] || auto[key];
    }
    return m;
  }, [auto, overrides]);

  const [pan, setPan] = useState<Pos>({ x: 400, y: 80 });
  const [zoom, setZoom] = useState(1);

  // Reads the actually-painted background colour of the surrounding card.
  // Dark-mode extensions (Dark Reader, Chrome Auto Dark Mode, Midnight
  // Lizard, …) transform the card's background via their normal pipeline;
  // by reading the *rendered* colour we can paint the text halo in
  // whatever the current surface happens to be, so the halo always
  // matches the bg and reads as a "cut-out" around labels. No extension-
  // specific API is used — we just sample `getComputedStyle`.
  const [surfaceColor, setSurfaceColor] = useState('#ffffff');
  useEffect(() => {
    const read = () => {
      const svg = svgRef.current;
      if (!svg) return;
      // Walk up until we find an element with a non-transparent bg.
      let el: HTMLElement | null = svg.closest('.hy-card-body') || svg.closest('.hy-card');
      let found: string | null = null;
      while (el) {
        const bg = getComputedStyle(el).backgroundColor;
        if (bg && bg !== 'rgba(0, 0, 0, 0)' && bg !== 'transparent') { found = bg; break; }
        el = el.parentElement;
      }
      if (found) setSurfaceColor((prev) => (prev === found ? prev : (found as string)));
    };
    read();
    const id = window.setInterval(read, 1200);
    const obs = new MutationObserver(read);
    obs.observe(document.documentElement, {
      attributes: true,
      subtree: true,
      attributeFilter: ['class', 'style', 'data-darkreader-scheme', 'data-darkreader-mode'],
    });
    return () => { window.clearInterval(id); obs.disconnect(); };
  }, []);

  // Clamp pan so the positioned nodes' bounding box never leaves the viewport
  // entirely. Keeps at least CLAMP_VISIBLE px of the bbox on-screen on every
  // side — prevents the user from panning so far that the entire graph
  // disappears with no anchor to scroll back to.
  const CLAMP_VISIBLE = 60;
  const positionsRef = useRef(positions);
  useEffect(() => { positionsRef.current = positions; }, [positions]);
  const clampPan = useCallback((p: Pos, z: number): Pos => {
    const svg = svgRef.current;
    if (!svg) return p;
    const r = svg.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) return p;
    const pts = Object.values(positionsRef.current);
    if (pts.length === 0) return p;
    const minX = Math.min(...pts.map((pt) => pt.x)) - R_HUB_OUTER;
    const maxX = Math.max(...pts.map((pt) => pt.x)) + R_HUB_OUTER;
    const minY = Math.min(...pts.map((pt) => pt.y)) - R_HUB_OUTER;
    const maxY = Math.max(...pts.map((pt) => pt.y)) + R_HUB_OUTER;
    // bbox right edge in screen space must be >= CLAMP_VISIBLE
    let x = p.x;
    let y = p.y;
    const rightMin = CLAMP_VISIBLE - maxX * z;
    const leftMax = r.width - CLAMP_VISIBLE - minX * z;
    if (x < rightMin) x = rightMin;
    if (x > leftMax) x = leftMax;
    const bottomMin = CLAMP_VISIBLE - maxY * z;
    const topMax = r.height - CLAMP_VISIBLE - minY * z;
    if (y < bottomMin) y = bottomMin;
    if (y > topMax) y = topMax;
    return { x, y };
  }, []);

  // Snap-to-grid: when on, dragging a node rounds its position to the
  // nearest grid intersection (grid spacing matches the visual pattern).
  const [snap, setSnap] = useState<boolean>(() => {
    try { return localStorage.getItem('scale:topology-graph-snap') === '1'; } catch { return false; }
  });
  useEffect(() => {
    try { localStorage.setItem('scale:topology-graph-snap', snap ? '1' : '0'); } catch { /* ignore */ }
  }, [snap]);
  const GRID_STEP = 24;
  const snapRef = useRef(snap);
  useEffect(() => { snapRef.current = snap; }, [snap]);

  const dragState = useRef<
    | { mode: 'pan'; startClient: Pos; startPan: Pos; moved: boolean }
    | { mode: 'node'; key: string; startSvg: Pos; startNodePos: Pos; moved: boolean }
    | null
  >(null);

  // Selection is stored as a single currentQPath string. The node key and
  // pathIdx are derived from it so topology refreshes — which can change the
  // order of equivalent paths — never cause the highlighted route to drift:
  // we look up the same qpath text in each fresh paths list.
  const [currentQPath, setCurrentQPath] = useState<string | null>(selectedQPath ?? null);
  useEffect(() => {
    if ((selectedQPath ?? null) !== currentQPath) {
      setCurrentQPath(selectedQPath ?? null);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedQPath]);

  const selectedKey = useMemo(() => {
    if (!currentQPath) return null;
    const segs = currentQPath.split('/');
    return segs[segs.length - 1];
  }, [currentQPath]);
  const selectedPaths = useMemo(() => {
    if (!selectedKey) return [] as string[][];
    return findPathsToName(topology, selectedKey, selfId, selfName);
  }, [selectedKey, topology, selfId, selfName]);
  const pathIdx = useMemo(() => {
    if (!currentQPath || selectedPaths.length === 0) return 0;
    const i = selectedPaths.findIndex((p) => p.join('/') === currentQPath);
    return i >= 0 ? i : 0;
  }, [currentQPath, selectedPaths]);
  const rawActivePath = selectedPaths[pathIdx] || null;
  // findPathsToName preserves the TreeTable key convention — inbound
  // paths are prefixed with selfId while outbound top-level peers arrive
  // as a single [peerName]. For EDGE ANIMATION we always need self as
  // the origin so the radiate-from-self draw animation works, so prepend
  // selfId when it isn't already there. For the OVERLAY DISPLAY we keep
  // the raw form: outbound lookups render as just the peer name (no
  // implicit self-prefix) because, as seen by the local node, an
  // outbound connection is a direct one-hop link rather than a traversal
  // through self.
  const activePath = useMemo(() => {
    if (!rawActivePath) return null;
    if (rawActivePath[0] === selfId) return rawActivePath;
    return [selfId, ...rawActivePath];
  }, [rawActivePath, selfId]);
  const displayPath = rawActivePath;
  const activePathOrder = useMemo(() => {
    const m = new Map<string, number>();
    if (!activePath) return m;
    for (let i = 0; i < activePath.length - 1; i++) {
      const a = activePath[i], b = activePath[i + 1];
      const candidates = [
        `${a}→${b}`,
        `${b}→${a}`,
        `?${a < b ? a : b}|${a < b ? b : a}`,
      ];
      for (const c of candidates) if (edges.has(c)) { m.set(c, i); break; }
    }
    return m;
  }, [activePath, edges]);

  // Sequentially "draw" the path after a selection change — each edge
  // fades in one step at a time. On deselect the counter drops to 0
  // instantly so all edges un-highlight simultaneously.
  const [pathProgress, setPathProgress] = useState(0);
  useEffect(() => {
    if (!activePath) { setPathProgress(0); return; }
    const total = Math.max(0, activePath.length - 1);
    if (total === 0) { setPathProgress(0); return; }
    setPathProgress(1);
    let step = 1;
    const id = window.setInterval(() => {
      step += 1;
      setPathProgress(step);
      if (step >= total) window.clearInterval(id);
    }, 280);
    return () => window.clearInterval(id);
    // Lock on the qpath text so a topology refresh that reshuffles the paths
    // array but keeps this route intact doesn't re-trigger the animation.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activePath ? activePath.join('/') : null]);

  const handleNodeClick = useCallback((key: string) => {
    const paths = findPathsToName(topology, key, selfId, selfName);
    if (paths.length === 0) return;
    if (selectedKey === key) {
      // Same node → cycle paths (wrap around; deselect happens only when
      // the user clicks the empty canvas).
      const next = (pathIdx + 1) % paths.length;
      const q = paths[next].join('/');
      setCurrentQPath(q);
      onSelectQPath?.(q);
    } else {
      const q = paths[0].join('/');
      setCurrentQPath(q);
      onSelectQPath?.(q);
    }
  }, [topology, selfId, selfName, selectedKey, pathIdx, onSelectQPath]);

  const handleBlankClick = useCallback(() => {
    if (!currentQPath) return;
    setCurrentQPath(null);
    onSelectQPath?.(null);
  }, [currentQPath, onSelectQPath]);

  const clientToSvg = useCallback(
    (clientX: number, clientY: number): Pos => {
      const svg = svgRef.current;
      if (!svg) return { x: 0, y: 0 };
      const r = svg.getBoundingClientRect();
      return { x: (clientX - r.left - pan.x) / zoom, y: (clientY - r.top - pan.y) / zoom };
    },
    [pan.x, pan.y, zoom]
  );

  const onMouseDown = useCallback((ev: MouseEvent) => {
    const target = ev.target as SVGElement;
    const nodeEl = target.closest('[data-node-key]') as SVGElement | null;
    if (nodeEl) {
      const key = nodeEl.getAttribute('data-node-key') || '';
      const startSvg = clientToSvg(ev.clientX, ev.clientY);
      const startNodePos = positions[key] || { x: 0, y: 0 };
      dragState.current = { mode: 'node', key, startSvg, startNodePos, moved: false };
    } else {
      dragState.current = { mode: 'pan', startClient: { x: ev.clientX, y: ev.clientY }, startPan: pan, moved: false };
    }
    ev.preventDefault();
  }, [clientToSvg, positions, pan]);

  // Eased drop handler: runs snap-to-grid (optional) and collision-bounce
  // in sequence, then animates the node from its release point to the
  // final resting position over ~260 ms with a cubic ease-out. The same
  // animation handles both "click-to-grid" magnetism and the "bounce off
  // a neighbour" push, so there's one smooth motion regardless of which
  // adjustments fired.
  function animateToFinal(key: string, from: Pos) {
    let to: Pos = { ...from };
    if (snapRef.current) {
      to = {
        x: Math.round(to.x / GRID_STEP) * GRID_STEP,
        y: Math.round(to.y / GRID_STEP) * GRID_STEP,
      };
    }
    // Resolve against every other node (snap may have landed us on a
    // neighbour; bounce outward until we're clear).
    const others: Record<string, Pos> = {};
    const cur = overridesRef.current;
    for (const k in cur) if (k !== key) others[k] = cur[k];
    to = resolveOverlap(key, to, others, nodesRef.current);

    if (Math.abs(to.x - from.x) < 0.5 && Math.abs(to.y - from.y) < 0.5) {
      setOverrides((prev) => { savePositions(prev); return prev; });
      return;
    }
    const start = performance.now();
    const duration = 260;
    const step = (now: number) => {
      const tt = Math.min(1, (now - start) / duration);
      const k = 1 - Math.pow(1 - tt, 3);
      const nx = from.x + (to.x - from.x) * k;
      const ny = from.y + (to.y - from.y) * k;
      setOverrides((prev) => ({ ...prev, [key]: { x: nx, y: ny } }));
      if (tt < 1) {
        requestAnimationFrame(step);
      } else {
        setOverrides((prev) => ({ ...prev, [key]: to }));
        setOverrides((prev) => { savePositions(prev); return prev; });
      }
    };
    requestAnimationFrame(step);
  }

  useEffect(() => {
    function onMove(ev: globalThis.MouseEvent) {
      const s = dragState.current;
      if (!s) return;
      if (s.mode === 'pan') {
        const dx = ev.clientX - s.startClient.x;
        const dy = ev.clientY - s.startClient.y;
        if (!s.moved && Math.abs(dx) + Math.abs(dy) > 4) s.moved = true;
        setPan(clampPan({ x: s.startPan.x + dx, y: s.startPan.y + dy }, zoom));
      } else {
        const cur = clientToSvg(ev.clientX, ev.clientY);
        const dx = cur.x - s.startSvg.x;
        const dy = cur.y - s.startSvg.y;
        // Treat as drag only when the cursor has moved past a small
        // threshold (in SVG coords), otherwise the up-event is a click.
        if (!s.moved && Math.abs(dx) + Math.abs(dy) > 3 / zoom) s.moved = true;
        if (s.moved) {
          const nextPos = { x: s.startNodePos.x + dx, y: s.startNodePos.y + dy };
          setOverrides((prev) => ({ ...prev, [s.key]: nextPos }));
        }
      }
    }
    function onUp() {
      const s = dragState.current;
      dragState.current = null;
      if (s?.mode === 'node') {
        if (!s.moved) {
          handleNodeClick(s.key);
        } else {
          // Drag-drop pipeline: snap → collision-bounce → animate.
          setOverrides((prev) => {
            const pos = prev[s.key];
            if (pos) animateToFinal(s.key, pos);
            return prev;
          });
        }
      } else if (s?.mode === 'pan' && !s.moved) {
        // Click on empty canvas → deselect current selection.
        handleBlankClick();
      }
    }
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    return () => { window.removeEventListener('mousemove', onMove); window.removeEventListener('mouseup', onUp); };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [clientToSvg, handleNodeClick, handleBlankClick, clampPan, zoom]);

  useEffect(() => {
    const svg = svgRef.current;
    if (!svg) return;
    const handler = (ev: WheelEvent) => {
      ev.preventDefault();
      const r = svg.getBoundingClientRect();
      const cx = ev.clientX - r.left;
      const cy = ev.clientY - r.top;
      const factor = ev.deltaY < 0 ? 1.12 : 1 / 1.12;
      setZoom((z) => {
        const nz = Math.max(0.3, Math.min(3, z * factor));
        const scale = nz / z;
        setPan((p) => clampPan({ x: cx - (cx - p.x) * scale, y: cy - (cy - p.y) * scale }, nz));
        return nz;
      });
    };
    svg.addEventListener('wheel', handler, { passive: false });
    return () => svg.removeEventListener('wheel', handler);
  }, []);

  const fitView = useCallback(() => {
    const svg = svgRef.current;
    if (!svg) return;
    const r = svg.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) return;
    const pts = Object.values(positions);
    if (pts.length === 0) return;
    const minX = Math.min(...pts.map((p) => p.x)) - R_HUB_OUTER - 40;
    const maxX = Math.max(...pts.map((p) => p.x)) + R_HUB_OUTER + 40;
    const minY = Math.min(...pts.map((p) => p.y)) - R_HUB_OUTER - 40;
    const maxY = Math.max(...pts.map((p) => p.y)) + R_HUB_OUTER + 40;
    const z = Math.min(r.width / (maxX - minX), r.height / (maxY - minY), 1.2);
    setPan({ x: r.width / 2 - ((minX + maxX) / 2) * z, y: r.height / 2 - ((minY + maxY) / 2) * z });
    setZoom(z);
  }, [positions]);

  const didFit = useRef(false);
  useLayoutEffect(() => {
    if (didFit.current) return;
    if (Object.keys(positions).length === 0) return;
    const svg = svgRef.current;
    if (!svg) return;
    const r = svg.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) return;
    didFit.current = true;
    fitView();
  }, [positions, fitView]);

  const resetLayout = () => { setOverrides({}); savePositions({}); };

  // Edge stroke width — scaled against an ABSOLUTE 0–1000 Mbps reference
  // so a line's thickness means the same thing regardless of what else
  // the mesh has seen. Values above 1 Gbps saturate at the max width.
  function edgeWidth(e: GraphEdge): number {
    const peak = maxByEdgeRef.current.get(e.key) || 0;
    const t = Math.min(1, peak / REF_MBPS_1000);
    return 1.5 + t * 5.5; // 1.5 .. 7 px
  }

  // Unified RAF-driven flow: a single animation mechanism expresses both
  // direction AND real-time speed. Each edge's dashoffset advances by
  // `speed * dt` each frame; `speed` is smoothed toward a rate-derived
  // target so transitions between idle and active are gradual, not
  // jump-cuts. An idle baseline speed keeps direction always readable.
  const flowLineRefs = useRef<Map<string, SVGLineElement>>(new Map());
  const flowStateRef = useRef<Map<string, { offset: number; speed: number }>>(new Map());
  const edgesRef = useRef(edges);
  useEffect(() => { edgesRef.current = edges; }, [edges]);

  useEffect(() => {
    let raf = 0;
    let last = performance.now();
    const BASE_SPEED = 24;   // px/sec at idle
    const MAX_SPEED = 180;   // px/sec when current reaches the global max
    const LERP = 0.08;        // per-frame smoothing toward target speed
    const loop = (now: number) => {
      const dt = Math.min(0.1, (now - last) / 1000);
      last = now;
      const curEdges = edgesRef.current;
      const maxs = maxByEdgeRef.current;
      // Denominator for the flow-speed percentage is max(globalPeak, REF).
      // - globalPeak: largest per-edge peak ever observed across the mesh.
      //   Once heavy traffic has been seen, it anchors 100 % for everyone.
      // - REF: a floor reference (10 MB/s). Before any heavy traffic has
      //   been seen, a single edge at 100 KB/s reads as ~1 % and only
      //   nudges above baseline — avoids the "first-burst instantly
      //   saturates" pathology of using the observed-peak-only denominator.
      const REF_MAX = 10 * 1024 * 1024;
      let globalPeak = 0;
      maxs.forEach((v) => { if (v > globalPeak) globalPeak = v; });
      const denom = Math.max(globalPeak, REF_MAX);
      curEdges.forEach((e) => {
        if (!e.directionKnown) return;
        const t = Math.min(1, e.currentRate / denom);
        const target = BASE_SPEED + t * (MAX_SPEED - BASE_SPEED);
        const state = flowStateRef.current.get(e.key) || { offset: 0, speed: BASE_SPEED };
        state.speed += (target - state.speed) * LERP;
        state.offset -= state.speed * dt;
        flowStateRef.current.set(e.key, state);
        const el = flowLineRefs.current.get(e.key);
        if (el) el.style.strokeDashoffset = String(state.offset);
      });
      raf = requestAnimationFrame(loop);
    };
    raf = requestAnimationFrame(loop);
    return () => cancelAnimationFrame(raf);
  }, []);

  // Pixel length of an edge in screen space (factors in zoom). Used to decide
  // whether the rate/latency labels fit inside the edge span.
  function edgeScreenLen(p: Pos, q: Pos): number {
    const dx = q.x - p.x;
    const dy = q.y - p.y;
    return Math.sqrt(dx * dx + dy * dy) * zoom;
  }

  return (
    <div className="hy-topo-graph">
      <div className="hy-topo-graph-toolbar">
        <button type="button" className="hy-topo-btn" onClick={fitView} title={t('nodes.graph.resetView')}>
          {t('nodes.graph.resetView')}
        </button>
        <button type="button" className="hy-topo-btn" onClick={resetLayout} title={t('nodes.graph.resetLayout')}>
          {t('nodes.graph.resetLayout')}
        </button>
        <span className="hy-topo-graph-hint">{t('nodes.graph.hint')}</span>
      </div>
      <div className="hy-topo-graph-toolbar-right">
        <label className="hy-topo-snap">
          <input type="checkbox" checked={snap} onChange={(e) => setSnap(e.target.checked)} />
          <span>{t('nodes.graph.snap')}</span>
        </label>
      </div>
      <svg ref={svgRef} className="hy-topo-graph-svg" onMouseDown={onMouseDown}>
        <g transform={`translate(${pan.x},${pan.y}) scale(${zoom})`}>
          {/* Background grid — rendered as real <line> elements in the main
              transformed group (not via <pattern>/<defs>). Dark-mode
              extensions reliably transform strokes on main-tree elements
              but tend to skip pattern contents, so keeping the grid in
              the direct DOM path makes it invertable. Range covers a wide
              box around the nodes; lines are step=24 SVG units. */}
          {(() => {
            const pts = Object.values(positions);
            const minX = pts.length ? Math.min(...pts.map((p) => p.x)) - 300 : -400;
            const maxX = pts.length ? Math.max(...pts.map((p) => p.x)) + 300 : 400;
            const minY = pts.length ? Math.min(...pts.map((p) => p.y)) - 300 : -400;
            const maxY = pts.length ? Math.max(...pts.map((p) => p.y)) + 300 : 400;
            const step = 24;
            const x0 = Math.floor(minX / step) * step;
            const x1 = Math.ceil(maxX / step) * step;
            const y0 = Math.floor(minY / step) * step;
            const y1 = Math.ceil(maxY / step) * step;
            const lines: JSX.Element[] = [];
            for (let x = x0; x <= x1; x += step) {
              lines.push(<line key={`v${x}`} x1={x} y1={y0} x2={x} y2={y1} className="hy-topo-grid-line" />);
            }
            for (let y = y0; y <= y1; y += step) {
              lines.push(<line key={`h${y}`} x1={x0} y1={y} x2={x1} y2={y} className="hy-topo-grid-line" />);
            }
            return lines;
          })()}

          {/* Edges */}
          {Array.from(edges.values()).map((e) => {
            const p = positions[e.from];
            const q = positions[e.to];
            if (!p || !q) return null;
            const dx = q.x - p.x;
            const dy = q.y - p.y;
            const d = Math.sqrt(dx * dx + dy * dy) || 1;
            const ux = dx / d;
            const uy = dy / d;
            // If an opposite-direction edge exists between the same pair,
            // offset both edges perpendicular to the line so they render as
            // two parallel rails (flow in opposite directions). The offset
            // direction is derived from the edge's own orientation, so each
            // side naturally ends up on opposite rails.
            const hasOpposite = edges.has(`${e.to}→${e.from}`);
            const offX = hasOpposite ? uy * 5 : 0;
            const offY = hasOpposite ? -ux * 5 : 0;
            const rFrom = isTransit(nodes.get(e.from)) ? R_HUB_OUTER : R_SINGLE;
            const rTo = isTransit(nodes.get(e.to)) ? R_HUB_OUTER : R_SINGLE;
            const x1 = p.x + ux * rFrom + offX;
            const y1 = p.y + uy * rFrom + offY;
            const x2 = q.x - ux * rTo + offX;
            const y2 = q.y - uy * rTo + offY;
            const mx = (x1 + x2) / 2;
            const my = (y1 + y2) / 2;

            const screenLen = edgeScreenLen(p, q);
            const showLatency = e.segmentLatencyMs !== 0 && screenLen > 60;
            // Rate label is always shown (even for 0 B/s) once the edge is
            // long enough on screen to fit the label text.
            const showRate = screenLen > 110;
            const width = edgeWidth(e);

            // Path highlight animation: each edge on the active path has
            // its own index 0..n-1. `pathProgress` is a counter that ticks
            // up once per ~160 ms after a selection change, so the route
            // "draws" sequentially from self to the selected node.
            const edgeIdx = activePathOrder.get(e.key);
            const onPath = edgeIdx !== undefined && edgeIdx < pathProgress;
            // An edge is offline/down when the edge itself is disabled OR
            // either endpoint reports offline. Such edges turn red, lose
            // the flow-dot animation, and carry an × marker at their
            // midpoint so the broken hop is obvious at a glance.
            const fromOff = nodes.get(e.from)?.offline;
            const toOff = nodes.get(e.to)?.offline;
            const offline = e.disabled || !!fromOff || !!toOff;
            const cls = [
              'hy-topo-edge',
              offline && 'offline',
              onPath && !offline && 'on-path',
            ].filter(Boolean).join(' ');
            // Draw-direction: for the path-highlight overlay we want the
            // stroke to appear as if being "painted" from the self-side
            // endpoint toward the farther endpoint, regardless of which
            // side of this edge is the dialer. activePath is ordered
            // self → target, so activePath[edgeIdx] is the nearer-self
            // node on this edge — if that's e.to, we flip the overlay's
            // coords so its (x1,y1) ends up at the self-side end.
            const nearerSelfKey = onPath && edgeIdx !== undefined && activePath ? activePath[edgeIdx] : null;
            const flipForDraw = nearerSelfKey === e.to;
            const dx1 = flipForDraw ? x2 : x1;
            const dy1 = flipForDraw ? y2 : y1;
            const dx2 = flipForDraw ? x1 : x2;
            const dy2 = flipForDraw ? y1 : y2;
            const drawLen = Math.hypot(dx2 - dx1, dy2 - dy1);
            return (
              <g key={e.key} className={cls}>
                {/* Base line — thickness encodes peak throughput on this edge. */}
                <line
                  x1={x1} y1={y1} x2={x2} y2={y2}
                  className="hy-topo-edge-base"
                  strokeWidth={width}
                  strokeDasharray={offline ? '4 3' : undefined}
                />
                {/* Path draw overlay — appears only when this edge becomes
                    on-path, oriented self-side → target-side so the stroke-
                    dashoffset keyframe visually "paints" the blue from the
                    self end outward. The pathProgress timer mounts edges
                    one at a time in sequence from self, so the cumulative
                    effect radiates outward to the selected node. A fresh
                    key per selection change forces animation restart. */}
                {onPath && !offline && (
                  <line
                    key={activePath ? activePath.join('/') + ':' + edgeIdx : 'draw'}
                    x1={dx1} y1={dy1} x2={dx2} y2={dy2}
                    className="hy-topo-edge-draw"
                    style={{ ['--hy-path-len' as any]: drawLen }}
                  />
                )}
                {/* Flow overlay — dashoffset is driven by the RAF loop
                    above; speed smoothly interpolates between the idle
                    baseline and a rate-scaled peak. Offline edges have
                    no flow: the × marker speaks for their state. */}
                {e.directionKnown && !offline && (
                  <line
                    x1={x1} y1={y1} x2={x2} y2={y2}
                    className="hy-topo-edge-flow"
                    ref={(el) => {
                      if (el) flowLineRefs.current.set(e.key, el);
                      else flowLineRefs.current.delete(e.key);
                    }}
                  />
                )}
                {/* Offline × marker — small red cross at the midpoint,
                    constant size independent of edge thickness. */}
                {offline && (
                  <g transform={`translate(${mx},${my})`} className="hy-topo-edge-x">
                    <path d="M -4 -4 L 4 4 M -4 4 L 4 -4" />
                  </g>
                )}
                {/* Labels — halo+fill pair of <text> nodes. The halo's
                    stroke is set via the <HaloText> helper below, which
                    applies the sampled surface colour with `!important`
                    so dark-mode extensions' CSS overrides can't win. */}
                {showLatency && (() => {
                  const latClass = e.segmentLatencyMs < 0 ? 'lat-na' : e.segmentLatencyMs < 80 ? 'lat-ok' : e.segmentLatencyMs < 200 ? 'lat-mid' : 'lat-bad';
                  const text = fmtLatency(e.segmentLatencyMs);
                  return (
                    <g>
                      <HaloText x={mx} y={my - 5} stroke={surfaceColor}>{text}</HaloText>
                      <text x={mx} y={my - 5} textAnchor="middle" className={`hy-topo-edge-label ${latClass}`}>{text}</text>
                    </g>
                  );
                })()}
                {showRate && (
                  <g>
                    <HaloText x={mx} y={my + 11} stroke={surfaceColor}>{fmtRate(e.currentRate)}</HaloText>
                    <text x={mx} y={my + 11} textAnchor="middle" className="hy-topo-edge-label rate">{fmtRate(e.currentRate)}</text>
                  </g>
                )}
              </g>
            );
          })}

          {/* Nodes — hubs (self or degree ≥ 3) render as concentric rings
              (pale outer + inner dot). Leaves render as a single pale disc
              WITHOUT the inner dot — same outer colour as hubs so the
              palette stays coherent. The inner dot is a "hub marker",
              reserved for transit-like nodes and self. Self's inner is
              primary blue; other hubs use the dark navy. */}
          {Array.from(nodes.values()).map((n) => {
            const p = positions[n.key];
            if (!p) return null;
            const transit = isTransit(n);
            const rOuter = transit ? R_HUB_OUTER : R_SINGLE;
            const isSelected = selectedKey === n.key;
            // Intermediate nodes along the path no longer get a stroke —
            // only the selected node itself is outlined. Path is
            // communicated purely by the edge-highlight animation.
            const cls = [
              'hy-topo-dot',
              n.isSelf && 'self',
              n.disabled && 'disabled',
              n.nested && 'nested',
              n.incompatible && 'bad',
              isSelected && 'selected',
            ].filter(Boolean).join(' ');
            return (
              <g key={n.key} data-node-key={n.key} transform={`translate(${p.x},${p.y})`} className={cls} style={{ cursor: 'grab' }}>
                <circle r={rOuter} className="hy-topo-dot-outer" />
                {transit && <circle r={R_HUB_INNER} className="hy-topo-dot-inner" />}
                <text y={rOuter + 14} textAnchor="middle" className="hy-topo-dot-name">
                  {n.name}
                </text>
                {!n.isSelf && onOpenRemote && (
                  <g transform={`translate(${rOuter + 4},${-rOuter})`} className="hy-topo-dot-open" onClick={(ev) => { ev.stopPropagation(); onOpenRemote(n.qpath); }}>
                    <rect width={14} height={14} rx={3} fill="transparent" />
                    <svg x={0} y={0} width={14} height={14} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2">
                      <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" />
                      <polyline points="15 3 21 3 21 9" />
                      <line x1="10" y1="14" x2="21" y2="3" />
                    </svg>
                  </g>
                )}
              </g>
            );
          })}
        </g>
      </svg>
      {displayPath && displayPath.length > 0 && (() => {
        const selNode = nodes.get(displayPath[displayPath.length - 1]);
        const totalLat = selNode?.totalLatencyMs ?? 0;
        const offline = !!selNode?.offline || totalLat < 0;
        const hops = displayPath.map((k) => nodes.get(k)?.name || k);
        const latClass = offline ? 'lat-bad' : totalLat <= 0 ? 'lat-na' : totalLat < 80 ? 'lat-ok' : totalLat < 200 ? 'lat-mid' : 'lat-bad';
        return (
          <div className="hy-topo-graph-pathinfo" role="status" aria-live="polite">
            <span className="hy-topo-pathinfo-label">{t('nodes.graph.selectedPath')}</span>
            <span className="hy-topo-pathinfo-chain">
              {hops.map((hop, i) => {
                const qp = hops.slice(0, i + 1).join('/');
                const reach = i === 0 || isReachableAt(qp);
                return (
                  <span key={i}>
                    {i > 0 && <span className="hy-topo-pathinfo-sep">/</span>}
                    <span className="hy-topo-pathinfo-hop" style={{ color: reach ? 'var(--green)' : 'var(--red)' }}>{hop}</span>
                  </span>
                );
              })}
            </span>
            <span className={`hy-topo-pathinfo-lat ${latClass}`}>
              {offline ? t('nodes.graph.unreachable') : fmtLatency(totalLat)}
            </span>
          </div>
        );
      })()}
    </div>
  );
}
