import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TopologyNode } from '@/api';
import { fmtRate } from '@/hooks/useFormat';

interface Props {
  topology: TopologyNode[];
  /** Local node_id — used as the key for the self-node. */
  selfId: string;
  selfName?: string;
  onOpenRemote?: (qpath: string) => void;
}

interface Pos { x: number; y: number }

interface GraphNode {
  key: string;
  name: string;
  isSelf: boolean;
  disabled: boolean;
  nested: boolean;
  native: boolean;
  incompatible: boolean;
  depth: number;
  degree: number;
  qpath: string;
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

function nodeRadiusFor(n: GraphNode | undefined): number {
  if (!n) return R_SINGLE;
  return (n.isSelf || n.degree >= 3) ? R_HUB_OUTER : R_SINGLE;
}

/**
 * Iteratively pushes `pos` outward from any node that overlaps it. Applied
 * both to user drop points and to newly-inserted node positions so circles
 * never stack on top of each other.
 */
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
        nested: !!n.nested,
        native: !!n.native,
        incompatible: !!n.incompatible || !!n.conflict,
        depth,
        degree: 0,
        qpath,
      });
    } else if (depth < existing.depth) {
      existing.depth = depth;
      existing.qpath = qpath;
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
  walk(topo, null, 0, 0, '');
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

function fmtLatency(ms: number): string {
  if (ms < 0) return 'offline';
  if (ms === 0) return '—';
  if (ms < 1000) return `${Math.round(ms)}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

export default function NodesGraphView({ topology, selfId, selfName, onOpenRemote }: Props) {
  const { t } = useTranslation();
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
    | { mode: 'pan'; startClient: Pos; startPan: Pos }
    | { mode: 'node'; key: string; startSvg: Pos; startNodePos: Pos }
    | null
  >(null);

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
      dragState.current = { mode: 'node', key, startSvg, startNodePos };
    } else {
      dragState.current = { mode: 'pan', startClient: { x: ev.clientX, y: ev.clientY }, startPan: pan };
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
        setPan({ x: s.startPan.x + (ev.clientX - s.startClient.x), y: s.startPan.y + (ev.clientY - s.startClient.y) });
      } else {
        const cur = clientToSvg(ev.clientX, ev.clientY);
        const nextPos = { x: s.startNodePos.x + (cur.x - s.startSvg.x), y: s.startNodePos.y + (cur.y - s.startSvg.y) };
        setOverrides((prev) => ({ ...prev, [s.key]: nextPos }));
      }
    }
    function onUp() {
      const s = dragState.current;
      dragState.current = null;
      if (s?.mode === 'node') {
        // Run the drop pipeline (snap → collision) and animate. Even when
        // snap is off, the bounce-off-overlap step still fires, so
        // releasing on top of another node always tidies itself up.
        setOverrides((prev) => {
          const pos = prev[s.key];
          if (pos) animateToFinal(s.key, pos);
          return prev;
        });
      }
    }
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    return () => { window.removeEventListener('mousemove', onMove); window.removeEventListener('mouseup', onUp); };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [clientToSvg]);

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
        setPan((p) => ({ x: cx - (cx - p.x) * scale, y: cy - (cy - p.y) * scale }));
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
        <defs>
          {/* Background grid — lives inside the transformed <g> so it pans
              and zooms as one piece with the nodes/edges. The stroke uses
              `currentColor` (inherited from the <g class="hy-topo-grid-host">
              below) so it flows through a CSS var and dark-mode extensions
              can transform it. */}
          <pattern id="hy-topo-grid" width="24" height="24" patternUnits="userSpaceOnUse">
            <path d="M 24 0 L 0 0 0 24" className="hy-topo-grid-line" />
          </pattern>
        </defs>
        <g transform={`translate(${pan.x},${pan.y}) scale(${zoom})`}>
          {/* Grid: huge rect filled by the pattern. */}
          <rect x={-5000} y={-5000} width={10000} height={10000} fill="url(#hy-topo-grid)" />

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
            const isHub = (k: string) => nodes.get(k)?.isSelf || (nodes.get(k)?.degree || 0) >= 3;
            const rFrom = isHub(e.from) ? R_HUB_OUTER : R_SINGLE;
            const rTo = isHub(e.to) ? R_HUB_OUTER : R_SINGLE;
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

            return (
              <g key={e.key} className={e.disabled ? 'hy-topo-edge disabled' : 'hy-topo-edge'}>
                {/* Base line — thickness encodes peak throughput on this edge. */}
                <line
                  x1={x1} y1={y1} x2={x2} y2={y2}
                  className="hy-topo-edge-base"
                  strokeWidth={width}
                  strokeDasharray={e.disabled ? '4 3' : undefined}
                />
                {/* Flow overlay — dashoffset is driven by the RAF loop
                    above; speed smoothly interpolates between the idle
                    baseline and a rate-scaled peak, so there's no
                    discontinuity when traffic starts or stops. */}
                {e.directionKnown && (
                  <line
                    x1={x1} y1={y1} x2={x2} y2={y2}
                    className="hy-topo-edge-flow"
                    ref={(el) => {
                      if (el) flowLineRefs.current.set(e.key, el);
                      else flowLineRefs.current.delete(e.key);
                    }}
                  />
                )}
                {/* Labels are always rendered horizontally (not rotated
                    with the edge) so they're readable regardless of angle. */}
                {showLatency && (
                  <text
                    x={mx} y={my - 5}
                    textAnchor="middle"
                    className={`hy-topo-edge-lat ${e.segmentLatencyMs < 0 ? 'bad' : e.segmentLatencyMs < 80 ? 'ok' : e.segmentLatencyMs < 200 ? 'mid' : 'bad'}`}
                  >
                    {fmtLatency(e.segmentLatencyMs)}
                  </text>
                )}
                {showRate && (
                  <text
                    x={mx} y={my + 11}
                    textAnchor="middle"
                    className="hy-topo-edge-rate"
                  >
                    {fmtRate(e.currentRate)}
                  </text>
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
            const isHub = n.isSelf || n.degree >= 3;
            const rOuter = isHub ? R_HUB_OUTER : R_SINGLE;
            const cls = [
              'hy-topo-dot',
              n.isSelf && 'self',
              n.disabled && 'disabled',
              n.nested && 'nested',
              n.incompatible && 'bad',
            ].filter(Boolean).join(' ');
            return (
              <g key={n.key} data-node-key={n.key} transform={`translate(${p.x},${p.y})`} className={cls} style={{ cursor: 'grab' }}>
                <circle r={rOuter} className="hy-topo-dot-outer" />
                {isHub && <circle r={R_HUB_INNER} className="hy-topo-dot-inner" />}
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
    </div>
  );
}
