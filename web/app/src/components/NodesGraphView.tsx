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
  key: string;              // unique id across the mesh: self→node_id, others→name
  name: string;             // display label
  isSelf: boolean;
  latencyMs: number;
  disabled: boolean;
  nested: boolean;
  native: boolean;
  incompatible: boolean;
  depth: number;            // BFS depth from self
  degree: number;           // number of adjacent edges (for concentric style)
  qpath: string;            // first-reached qualified path (for /remote/ link)
}

interface GraphEdge {
  key: string;              // order-independent unique id
  from: string;             // dialer (source of arrow)
  to: string;               // dialee (arrow head)
  directionKnown: boolean;  // false → rendered as line without arrow (backward compat)
  disabled: boolean;
  currentRate: number;      // tx+rx observed this tick
}

const R_SINGLE = 11;
const R_HUB_OUTER = 15;
const R_HUB_INNER = 7;
const LEVEL_GAP = 130;
const SIBLING_GAP = 80;
const LS_POS_KEY = 'scale:topology-graph-positions';

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

/**
 * Build a deduped graph from the nested topology tree. A node that appears
 * via multiple parents (e.g. triangle `au-r1` reachable both via `au` and
 * via `au-r1-a`) collapses to a single vertex with multiple incident edges.
 *
 * Arrow orientation comes from the topoSubPeer's `direction`:
 *   inbound  → child dialed parent → arrow child→parent
 *   outbound → parent dialed child → arrow parent→child
 *   unknown  → fallback tree order parent→child, arrow omitted
 */
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
        latencyMs: n.latency_ms || 0,
        disabled: !!n.disabled,
        nested: !!n.nested,
        native: !!n.native,
        incompatible: !!n.incompatible || !!n.conflict,
        depth,
        degree: 0,
        qpath,
      });
    } else if (depth < existing.depth) {
      // Keep the shallowest depth so layout puts this node on its earliest
      // reachable layer. Also prefer the first-seen qpath on that layer.
      existing.depth = depth;
      existing.qpath = qpath;
    }
  }

  function addEdge(a: string, b: string, direction: string | undefined, disabled: boolean, rate: number) {
    if (a === b) return;
    const [lo, hi] = a < b ? [a, b] : [b, a];
    const ekey = `${lo}|${hi}`;
    let e = edges.get(ekey);
    if (!e) {
      let from = a, to = b, directionKnown = false;
      if (direction === 'outbound') {
        // `b` (child) is an outbound peer of `a` (parent) — a dialed b
        from = a; to = b; directionKnown = true;
      } else if (direction === 'inbound') {
        // `b` dialed `a`
        from = b; to = a; directionKnown = true;
      }
      e = { key: ekey, from, to, directionKnown, disabled, currentRate: rate };
      edges.set(ekey, e);
      const na = nodes.get(a); if (na) na.degree++;
      const nb = nodes.get(b); if (nb) nb.degree++;
    } else {
      // If a later visit carries direction, upgrade the edge.
      if (!e.directionKnown && direction) {
        if (direction === 'outbound') { e.from = a; e.to = b; e.directionKnown = true; }
        else if (direction === 'inbound') { e.from = b; e.to = a; e.directionKnown = true; }
      }
      // Merge rates (take the larger — two reports of the same edge)
      if (rate > e.currentRate) e.currentRate = rate;
      if (disabled) e.disabled = true;
    }
  }

  function walk(list: TopologyNode[], parentKey: string | null, depth: number, parentQpath: string) {
    list.forEach((n) => {
      // Rule 1 (including self-identity): never descend into an ancestor
      // or our own name/id. Matches list view's buildChildNode cycle guard.
      if (depth > 0) {
        if (selfSet.has(n.name)) return;
      }
      const segs = parentQpath ? parentQpath.split('/') : [];
      if (depth > 0 && segs.includes(n.name)) return;
      const qpath = parentQpath ? parentQpath + '/' + n.name : keyOf(n);
      addOrMergeNode(n, depth, qpath);
      if (parentKey) {
        const rate = (n.tx_rate || 0) + (n.rx_rate || 0);
        addEdge(parentKey, keyOf(n), n.direction, !!n.disabled, rate);
      }
      if (n.children && n.children.length > 0) {
        walk(n.children, keyOf(n), depth + 1, qpath);
      }
    });
  }
  walk(topo, null, 0, '');
  return { nodes, edges };
}

function autoLayout(nodes: Map<string, GraphNode>): Record<string, Pos> {
  const byDepth: GraphNode[][] = [];
  nodes.forEach((n) => {
    (byDepth[n.depth] ||= []).push(n);
  });
  const positions: Record<string, Pos> = {};
  byDepth.forEach((row, depth) => {
    // Sort each row deterministically so reloads keep the same initial layout
    row.sort((a, b) => a.name.localeCompare(b.name));
    const width = (row.length - 1) * SIBLING_GAP;
    row.forEach((n, i) => {
      positions[n.key] = { x: -width / 2 + i * SIBLING_GAP, y: depth * LEVEL_GAP };
    });
  });
  return positions;
}

export default function NodesGraphView({ topology, selfId, selfName, onOpenRemote }: Props) {
  const { t } = useTranslation();
  const svgRef = useRef<SVGSVGElement | null>(null);

  const { nodes, edges } = useMemo(() => buildGraph(topology, selfId, selfName), [topology, selfId, selfName]);

  // Per-edge running max-throughput (client-side). Resets on reload.
  const maxByEdgeRef = useRef<Map<string, number>>(new Map());
  useMemo(() => {
    edges.forEach((e) => {
      const prev = maxByEdgeRef.current.get(e.key) || 0;
      if (e.currentRate > prev) maxByEdgeRef.current.set(e.key, e.currentRate);
    });
  }, [edges]);

  // Layout — auto per-depth tier, with per-node user-drag overrides.
  const auto = useMemo(() => autoLayout(nodes), [nodes]);
  const [overrides, setOverrides] = useState<Record<string, Pos>>(() => loadPositions());
  const positions = useMemo(() => {
    const m: Record<string, Pos> = { ...auto };
    for (const k in overrides) if (k in auto) m[k] = overrides[k];
    return m;
  }, [auto, overrides]);

  const [pan, setPan] = useState<Pos>({ x: 400, y: 80 });
  const [zoom, setZoom] = useState(1);

  // Drag state
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
      return {
        x: (clientX - r.left - pan.x) / zoom,
        y: (clientY - r.top - pan.y) / zoom,
      };
    },
    [pan.x, pan.y, zoom]
  );

  const onMouseDown = useCallback(
    (ev: MouseEvent) => {
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
    },
    [clientToSvg, positions, pan]
  );

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
      if (dragState.current?.mode === 'node') {
        setOverrides((prev) => { savePositions(prev); return prev; });
      }
      dragState.current = null;
    }
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    return () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };
  }, [clientToSvg]);

  // Wheel attached natively so we can preventDefault (React's wheel is passive)
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

  const resetLayout = () => {
    setOverrides({});
    savePositions({});
  };

  // Throughput → stroke width
  const globalMaxRate = useMemo(() => {
    let m = 0;
    maxByEdgeRef.current.forEach((v) => { if (v > m) m = v; });
    return m;
  }, [edges]);

  function edgeWidth(e: GraphEdge): number {
    const peak = maxByEdgeRef.current.get(e.key) || 0;
    if (globalMaxRate <= 0) return 1.2;
    const t = peak / globalMaxRate;
    return 1 + t * 4.5; // 1 .. 5.5 px
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
      <svg ref={svgRef} className="hy-topo-graph-svg" onMouseDown={onMouseDown}>
        <defs>
          <marker id="hy-topo-arrow" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="5" markerHeight="5" orient="auto-start-reverse">
            <path d="M 0 0 L 10 5 L 0 10 z" fill="#4D6EA3" />
          </marker>
        </defs>
        <g transform={`translate(${pan.x},${pan.y}) scale(${zoom})`}>
          {/* Edges first so they sit below nodes. Shorten endpoints by node
              radius so arrowheads don't overlap the circles. */}
          {Array.from(edges.values()).map((e) => {
            const p = positions[e.from];
            const q = positions[e.to];
            if (!p || !q) return null;
            const dx = q.x - p.x;
            const dy = q.y - p.y;
            const d = Math.sqrt(dx * dx + dy * dy) || 1;
            const ux = dx / d;
            const uy = dy / d;
            const rFrom = (nodes.get(e.from)?.degree || 1) >= 3 || nodes.get(e.from)?.isSelf ? R_HUB_OUTER : R_SINGLE;
            const rTo = (nodes.get(e.to)?.degree || 1) >= 3 || nodes.get(e.to)?.isSelf ? R_HUB_OUTER : R_SINGLE;
            const x1 = p.x + ux * rFrom;
            const y1 = p.y + uy * rFrom;
            const x2 = q.x - ux * (rTo + (e.directionKnown ? 2 : 0));
            const y2 = q.y - uy * (rTo + (e.directionKnown ? 2 : 0));
            const width = edgeWidth(e);
            const peak = maxByEdgeRef.current.get(e.key) || 0;
            return (
              <g key={e.key} className={e.disabled ? 'hy-topo-edge disabled' : 'hy-topo-edge'}>
                <line
                  x1={x1} y1={y1} x2={x2} y2={y2}
                  stroke="#4D6EA3"
                  strokeOpacity={e.disabled ? 0.3 : 0.55}
                  strokeWidth={width}
                  strokeDasharray={e.disabled ? '4 3' : undefined}
                  markerEnd={e.directionKnown ? 'url(#hy-topo-arrow)' : undefined}
                />
                {peak > 0 && (
                  <text
                    x={(x1 + x2) / 2} y={(y1 + y2) / 2 - 4}
                    textAnchor="middle"
                    className="hy-topo-edge-rate"
                  >
                    {fmtRate(peak)}
                  </text>
                )}
              </g>
            );
          })}
          {/* Nodes as dots (logo-inspired: hub-like = concentric, leaves = single circle) */}
          {Array.from(nodes.values()).map((n) => {
            const p = positions[n.key];
            if (!p) return null;
            const isHub = n.isSelf || n.degree >= 3;
            const cls = [
              'hy-topo-dot',
              n.isSelf && 'self',
              n.disabled && 'disabled',
              n.nested && 'nested',
              n.incompatible && 'bad',
            ].filter(Boolean).join(' ');
            return (
              <g key={n.key} data-node-key={n.key} transform={`translate(${p.x},${p.y})`} className={cls} style={{ cursor: 'grab' }}>
                {isHub ? (
                  <>
                    <circle r={R_HUB_OUTER} className="hy-topo-dot-outer" />
                    <circle r={R_HUB_INNER} className="hy-topo-dot-inner" />
                  </>
                ) : (
                  <circle r={R_SINGLE} className="hy-topo-dot-single" />
                )}
                <text y={(isHub ? R_HUB_OUTER : R_SINGLE) + 14} textAnchor="middle" className="hy-topo-dot-name">
                  {n.name}
                </text>
                <text y={(isHub ? R_HUB_OUTER : R_SINGLE) + 27} textAnchor="middle" className={`hy-topo-dot-meta lat-${
                  n.isSelf ? 'self' : n.latencyMs === -1 ? 'bad' : n.latencyMs === 0 ? 'na' : n.latencyMs < 80 ? 'ok' : n.latencyMs < 200 ? 'mid' : 'bad'
                }`}>
                  {n.isSelf ? 'self' : n.latencyMs === -1 ? t('nodes.offline') : n.latencyMs === 0 ? '—' : `${n.latencyMs}ms`}
                </text>
                {!n.isSelf && onOpenRemote && (
                  <g transform={`translate(${(isHub ? R_HUB_OUTER : R_SINGLE) + 4},${-(isHub ? R_HUB_OUTER : R_SINGLE)})`} className="hy-topo-dot-open" onClick={(ev) => { ev.stopPropagation(); onOpenRemote(n.qpath); }}>
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
