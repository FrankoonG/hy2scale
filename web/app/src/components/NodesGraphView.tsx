import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TopologyNode } from '@/api';

interface Props {
  topology: TopologyNode[];
  /** Local node_id — used to build qualified paths (matches list-view keys). */
  selfId: string;
  selfName?: string;
  /** Called when user clicks on a node's name (remote view link, etc.). */
  onOpenRemote?: (qpath: string) => void;
  /** Called when user clicks the edit icon on a node row. */
  onEdit?: (qpath: string, name: string, ev: MouseEvent) => void;
}

interface FlatNode {
  qpath: string;
  name: string;
  depth: number;
  node: TopologyNode;
  parentQpath: string | null;
}

interface Pos { x: number; y: number }

const NODE_W = 150;
const NODE_H = 56;
const LEVEL_GAP = 120;
const SIBLING_GAP = 30;
const LS_POS_KEY = 'scale:topology-graph-positions';

/**
 * Flatten the nested topology tree into a list carrying each node's
 * qualified path ("self/au/au-r1-a") and parent qpath — the same key
 * scheme the list view uses, so a node keeps a stable identity no
 * matter how many times it appears via different parents (triangle etc.).
 */
function flatten(topo: TopologyNode[], selfId: string, selfName?: string): FlatNode[] {
  const out: FlatNode[] = [];
  const selfSet = new Set<string>([selfId, selfName || '']);

  function walk(nodes: TopologyNode[], depth: number, parentQpath: string | null, parentSegments: string[]) {
    nodes.forEach((n) => {
      const rootSeg = n.is_self ? selfId : n.name;
      const segs = parentSegments.length === 0 ? [rootSeg] : [...parentSegments, n.name];
      const qpath = segs.join('/');
      // Ancestor-cycle guard — matches list view's buildChildNode rule:
      // a descendant whose name matches any ancestor OR our self id/name is skipped.
      if (depth > 0) {
        const ancestors = new Set(parentSegments);
        selfSet.forEach((s) => s && ancestors.add(s));
        if (ancestors.has(n.name)) return;
      }
      out.push({ qpath, name: n.name, depth, node: n, parentQpath });
      if (n.children && n.children.length > 0) {
        walk(n.children, depth + 1, qpath, segs);
      }
    });
  }
  walk(topo, 0, null, []);
  return out;
}

/**
 * Simple tiered layout: group by depth, center each level horizontally.
 * Called only for nodes that don't have a user-dragged override in LS.
 */
function autoLayout(flat: FlatNode[]): Record<string, Pos> {
  const byDepth: FlatNode[][] = [];
  flat.forEach((n) => {
    (byDepth[n.depth] ||= []).push(n);
  });
  const positions: Record<string, Pos> = {};
  byDepth.forEach((row, depth) => {
    const rowWidth = row.length * NODE_W + (row.length - 1) * SIBLING_GAP;
    const startX = -rowWidth / 2 + NODE_W / 2;
    row.forEach((n, i) => {
      positions[n.qpath] = { x: startX + i * (NODE_W + SIBLING_GAP), y: depth * LEVEL_GAP };
    });
  });
  return positions;
}

function loadPositions(): Record<string, Pos> {
  try {
    const raw = localStorage.getItem(LS_POS_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === 'object') return parsed;
  } catch {
    /* ignore */
  }
  return {};
}

function savePositions(p: Record<string, Pos>) {
  try {
    localStorage.setItem(LS_POS_KEY, JSON.stringify(p));
  } catch {
    /* ignore */
  }
}

export default function NodesGraphView({ topology, selfId, selfName, onOpenRemote, onEdit }: Props) {
  const { t } = useTranslation();
  const svgRef = useRef<SVGSVGElement | null>(null);

  const flat = useMemo(() => flatten(topology, selfId, selfName), [topology, selfId, selfName]);
  const auto = useMemo(() => autoLayout(flat), [flat]);

  // Merge auto-layout with user-dragged overrides. Stored overrides persist
  // across topology changes — nodes that have been manually placed stay put.
  const [overrides, setOverrides] = useState<Record<string, Pos>>(() => loadPositions());
  const positions = useMemo(() => {
    const m: Record<string, Pos> = { ...auto };
    for (const k in overrides) {
      if (k in auto) m[k] = overrides[k]; // only apply overrides for nodes still in topology
    }
    return m;
  }, [auto, overrides]);

  // Pan & zoom state
  const [pan, setPan] = useState<Pos>({ x: 400, y: 80 });
  const [zoom, setZoom] = useState(1);

  // Drag state — either panning the canvas or a specific node
  const dragState = useRef<
    | { mode: 'pan'; startClient: Pos; startPan: Pos }
    | { mode: 'node'; qpath: string; startSvg: Pos; startNodePos: Pos }
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
      const nodeEl = target.closest('[data-node-qpath]') as SVGElement | null;
      if (nodeEl) {
        const qpath = nodeEl.getAttribute('data-node-qpath') || '';
        const startSvg = clientToSvg(ev.clientX, ev.clientY);
        const startNodePos = positions[qpath] || { x: 0, y: 0 };
        dragState.current = { mode: 'node', qpath, startSvg, startNodePos };
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
        setOverrides((prev) => ({ ...prev, [s.qpath]: nextPos }));
      }
    }
    function onUp() {
      if (dragState.current?.mode === 'node') {
        // Persist overrides on drag-end — avoids thrashing LS every mousemove.
        setOverrides((prev) => {
          savePositions(prev);
          return prev;
        });
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

  // Wheel handler attached manually so we can use `{passive: false}` —
  // React's synthetic wheel listener is passive and swallows preventDefault.
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
        const newZoom = Math.max(0.3, Math.min(3, z * factor));
        const scale = newZoom / z;
        setPan((p) => ({ x: cx - (cx - p.x) * scale, y: cy - (cy - p.y) * scale }));
        return newZoom;
      });
    };
    svg.addEventListener('wheel', handler, { passive: false });
    return () => svg.removeEventListener('wheel', handler);
  }, []);

  // Auto-fit: compute pan/zoom that makes all nodes visible with some padding.
  const fitView = useCallback(() => {
    const svg = svgRef.current;
    if (!svg) return;
    const r = svg.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) return;
    const xs = Object.values(positions).map((p) => p.x);
    const ys = Object.values(positions).map((p) => p.y);
    if (xs.length === 0) return;
    const minX = Math.min(...xs) - NODE_W / 2 - 20;
    const maxX = Math.max(...xs) + NODE_W / 2 + 20;
    const minY = Math.min(...ys) - NODE_H / 2 - 20;
    const maxY = Math.max(...ys) + NODE_H / 2 + 20;
    const w = maxX - minX;
    const h = maxY - minY;
    const z = Math.min(r.width / w, r.height / h, 1);
    const panX = r.width / 2 - ((minX + maxX) / 2) * z;
    const panY = r.height / 2 - ((minY + maxY) / 2) * z;
    setPan({ x: panX, y: panY });
    setZoom(z);
  }, [positions]);

  // Fit once after the graph mounts and has real dimensions.
  const didInitialFit = useRef(false);
  useLayoutEffect(() => {
    if (didInitialFit.current) return;
    if (Object.keys(positions).length === 0) return;
    const svg = svgRef.current;
    if (!svg) return;
    const r = svg.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) return;
    didInitialFit.current = true;
    fitView();
  }, [positions, fitView]);

  const resetView = fitView;

  const resetLayout = () => {
    setOverrides({});
    savePositions({});
  };

  // Edges: parent → child, draw Bezier from parent-bottom to child-top.
  const edges = useMemo(() => {
    const out: { from: Pos; to: Pos; id: string; disabled: boolean }[] = [];
    flat.forEach((n) => {
      if (!n.parentQpath) return;
      const p = positions[n.parentQpath];
      const c = positions[n.qpath];
      if (!p || !c) return;
      out.push({
        from: { x: p.x, y: p.y + NODE_H / 2 },
        to: { x: c.x, y: c.y - NODE_H / 2 },
        id: `${n.parentQpath}->${n.qpath}`,
        disabled: !!n.node.disabled,
      });
    });
    return out;
  }, [flat, positions]);

  return (
    <div className="hy-topo-graph">
      <div className="hy-topo-graph-toolbar">
        <button type="button" className="hy-topo-btn" onClick={resetView} title={t('nodes.graph.resetView')}>
          {t('nodes.graph.resetView')}
        </button>
        <button type="button" className="hy-topo-btn" onClick={resetLayout} title={t('nodes.graph.resetLayout')}>
          {t('nodes.graph.resetLayout')}
        </button>
        <span className="hy-topo-graph-hint">{t('nodes.graph.hint')}</span>
      </div>
      <svg
        ref={svgRef}
        className="hy-topo-graph-svg"
        onMouseDown={onMouseDown}
      >
        <defs>
          <marker id="hy-topo-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
            <path d="M 0 0 L 10 5 L 0 10 z" fill="var(--border)" />
          </marker>
        </defs>
        <g transform={`translate(${pan.x},${pan.y}) scale(${zoom})`}>
          {/* Edges behind nodes */}
          {edges.map((e) => {
            const midY = (e.from.y + e.to.y) / 2;
            const d = `M ${e.from.x} ${e.from.y} C ${e.from.x} ${midY}, ${e.to.x} ${midY}, ${e.to.x} ${e.to.y}`;
            return (
              <path
                key={e.id}
                d={d}
                fill="none"
                stroke={e.disabled ? 'var(--border-light)' : 'var(--border)'}
                strokeWidth={1.5}
                strokeDasharray={e.disabled ? '4 3' : undefined}
                markerEnd="url(#hy-topo-arrow)"
              />
            );
          })}
          {/* Nodes */}
          {flat.map((n) => {
            const p = positions[n.qpath];
            if (!p) return null;
            const x = p.x - NODE_W / 2;
            const y = p.y - NODE_H / 2;
            const lat = n.node.latency_ms;
            const latClass = n.node.is_self ? 'ok' : lat === -1 ? 'bad' : lat === 0 ? 'na' : lat < 80 ? 'ok' : lat < 200 ? 'mid' : 'bad';
            const cls = [
              'hy-topo-node',
              n.node.is_self && 'self',
              n.node.disabled && 'disabled',
              n.node.nested && 'nested',
              n.node.incompatible && 'bad',
              n.node.conflict && 'bad',
            ]
              .filter(Boolean)
              .join(' ');
            return (
              <g
                key={n.qpath}
                data-node-qpath={n.qpath}
                transform={`translate(${x},${y})`}
                className={cls}
                style={{ cursor: 'grab' }}
              >
                <rect width={NODE_W} height={NODE_H} rx={8} className="hy-topo-node-box" />
                <text x={12} y={22} className="hy-topo-node-name">{n.name}</text>
                <text x={12} y={42} className={`hy-topo-node-meta lat-${latClass}`}>
                  {n.node.is_self
                    ? 'self'
                    : lat === -1
                    ? t('nodes.offline')
                    : lat === 0
                    ? '—'
                    : `${lat}ms`}
                </text>
                {(n.node.tx_rate > 0 || n.node.rx_rate > 0) && (
                  <text x={NODE_W - 12} y={42} textAnchor="end" className="hy-topo-node-rate">
                    ↑{fmtRateShort(n.node.tx_rate)} ↓{fmtRateShort(n.node.rx_rate)}
                  </text>
                )}
                {!n.node.is_self && onOpenRemote && (
                  <foreignObject x={NODE_W - 26} y={4} width={22} height={22} style={{ cursor: 'pointer' }}>
                    <button
                      type="button"
                      className="hy-topo-node-open"
                      title={t('app.open')}
                      onClick={(ev) => {
                        ev.stopPropagation();
                        onOpenRemote(n.qpath);
                      }}
                    >
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                        <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" />
                        <polyline points="15 3 21 3 21 9" />
                        <line x1="10" y1="14" x2="21" y2="3" />
                      </svg>
                    </button>
                  </foreignObject>
                )}
              </g>
            );
          })}
        </g>
      </svg>
    </div>
  );
}

function fmtRateShort(b: number): string {
  if (!b || b < 1024) return '0';
  if (b < 1048576) return (b / 1024).toFixed(0) + 'K';
  return (b / 1048576).toFixed(1) + 'M';
}
