import {
  useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState,
  type MouseEvent as ReactMouseEvent, type PointerEvent as ReactPointerEvent, type ReactNode,
} from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import type { TopologyNode, GraphCanvas, GraphCanvasEdge } from '@/api';
import * as api from '@/api';
import { getBasePath } from '@/api/client';
import { fmtRate } from '@/hooks/useFormat';
import { useExitPaths } from '@/hooks/useExitPaths';
import { usePanZoom, type View } from '@/hooks/usePanZoom';
import {
  useConfirm, useMediaQuery, useToast, MenuCheck, readStored, writeStored,
  IconCopy, IconMaximize2, IconChevronLeft, IconChevronRight, IconScan, IconZoomIn, IconZoomOut,
} from '@hy2scale/ui';
import { copyToClipboard } from '@/utils/clipboard';
import { latencyTier, pathLatencyClass } from '@/utils/latency';
import {
  buildGraph, findPaths, fmtLatency, isTransit, nodeRadiusFor, R_HUB_INNER, R_HUB_OUTER, R_SINGLE,
  type GraphEdge, type GraphNode, type GraphPath, type Pos,
} from './topo/graphModel';
import {
  PRESET_IDS, presetLayout, graphSignature, routeEdges, placeLabels, pointAt, project, ghostPoints,
  toAbs, toRel, contentBounds, type PresetId, type Waypoint, type EdgeGeom,
} from './topo/layouts';
import { CanvasMenuButton, CanvasSubmenu, useNamePrompt, type CanvasMenuProps } from './topo/CanvasMenu';

// Graph model lives in ./topo/graphModel; re-exported for older imports.
export { buildGraph, findPathsToName } from './topo/graphModel';
export type { GraphNode, GraphEdge } from './topo/graphModel';

interface Props {
  topology: TopologyNode[];
  /** Local node_id — used as the key for the self-node. */
  selfId: string;
  selfName?: string;
  onOpenRemote?: (qpath: string, anchor?: { x: number; y: number }) => void;
  /** Details: `qpath` is the list-view row key ('__self__' for this node). */
  onOpenDetails?: (qpath: string, anchor?: { x: number; y: number }) => void;
  selectedQPath?: string | null;
  onSelectQPath?: (qpath: string | null) => void;
  /** Fill the parent (flex) container instead of the fixed card height. */
  full?: boolean;
  /** Page actions at the top-right. A function receives the canvas's own
   *  secondary controls for the ⋯ menu. */
  actions?: ReactNode | ((more: ReactNode) => ReactNode);
}

const GRID_STEP = 24;
const NODE_PADDING = 8;
const TOP_RESERVE = 48;
const FIT_PAD = 40;
const CLAMP_VISIBLE = 60;
const MAX_WAYPOINTS = 8;
const MORPH_MS = 360;
const REF_MBPS_1000 = 125_000_000;
const PATH_TOTAL_MS = 260;
const CHEV_HALF_LEN = 3;
const CHEV_HALF_WID = 2.4;
const CHEV_SPACING = 18;

type EdgeData = GraphCanvasEdge;
type Drag =
  | { kind: 'node'; id: number; key: string; start: Pos; startPos: Pos; moved: boolean; touch: boolean }
  | { kind: 'wp'; id: number; key: string; i: number; sx: number; sy: number; moved: boolean; inserted: boolean; timer: number }
  | { kind: 'label'; id: number; key: string; moved: boolean };

const ACTIVE_STORE = () => 'hy2.topo.canvas:' + getBasePath();
const SNAP_STORE = 'scale:topology-graph-snap';
const ALT_STORE = 'hy2.topo.altPaths';

const lsGet = readStored;
const lsSet = (k: string, v: string) => writeStored(k, v);
const newId = () => {
  const b = new Uint8Array(5);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
};

/** Pushes `pos` out of any rendered dot it overlaps. */
function resolveOverlap(key: string, pos: Pos, others: Record<string, Pos>, nodes: Map<string, GraphNode>): Pos {
  let { x, y } = pos;
  const rSelf = nodeRadiusFor(nodes.get(key));
  for (let iter = 0; iter < 8; iter++) {
    let moved = false;
    for (const k in others) {
      if (k === key || !nodes.has(k)) continue;
      const o = others[k];
      const minDist = rSelf + nodeRadiusFor(nodes.get(k)) + NODE_PADDING;
      let dx = x - o.x, dy = y - o.y;
      let dist = Math.hypot(dx, dy);
      if (dist < 0.001) {
        const ang = ((key.charCodeAt(0) * 37 + key.length * 11) % 360) * (Math.PI / 180);
        dx = Math.cos(ang); dy = Math.sin(ang); dist = 1;
      }
      if (dist < minDist) {
        const push = minDist - dist + 0.1;
        x += (dx / dist) * push; y += (dy / dist) * push;
        moved = true;
      }
    }
    if (!moved) break;
  }
  return { x, y };
}

/** Live list of custom canvases (SSE; first event is the snapshot). */
function useGraphCanvases() {
  const [canvases, setCanvases] = useState<GraphCanvas[] | null>(null);
  useEffect(() => {
    const token = sessionStorage.getItem('token:' + getBasePath()) || '';
    const es = new EventSource(getBasePath() + '/api/graph-canvases/stream?token=' + encodeURIComponent(token));
    es.onmessage = (ev) => {
      try {
        const data = JSON.parse(ev.data);
        if (data && Array.isArray(data.canvases)) setCanvases(data.canvases as GraphCanvas[]);
      } catch { /* ignore malformed */ }
    };
    return () => es.close();
  }, []);
  return { canvases, setCanvases };
}

const sameJSON = (a: unknown, b: unknown) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);

export default function NodesGraphView({ topology, selfId, selfName, onOpenRemote, onOpenDetails, selectedQPath, onSelectQPath, full, actions }: Props) {
  const { t } = useTranslation();
  const { isReachableAt } = useExitPaths();
  const confirm = useConfirm();
  const toast = useToast();
  const narrow = useMediaQuery('(max-width: 640px)');
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const fileRef = useRef<HTMLInputElement | null>(null);
  const [namePrompt, askName] = useNamePrompt();

  const { nodes, edges } = useMemo(() => buildGraph(topology, selfId, selfName), [topology, selfId, selfName]);
  const graph = useMemo(() => ({ nodes, edges }), [nodes, edges]);
  const sig = useMemo(() => graphSignature(graph), [graph]);
  const graphRef = useRef(graph);
  graphRef.current = graph;
  const nodesRef = useRef(nodes);
  nodesRef.current = nodes;
  const edgesRef = useRef(edges);
  edgesRef.current = edges;

  // ---------------------------------------------------------------- edge peaks
  // Per-edge running max: sizes the stroke and normalises the flow speed.
  const maxByEdgeRef = useRef<Map<string, number>>((() => {
    try {
      const raw = localStorage.getItem('hy2.edgeMax');
      return raw ? new Map(Object.entries(JSON.parse(raw) as Record<string, number>)) : new Map();
    } catch { return new Map(); }
  })());
  const edgeMaxSaveTimer = useRef<number | null>(null);
  useMemo(() => {
    let dirty = false;
    edges.forEach((e) => {
      const peak = Math.max(e.txRate, e.rxRate);
      if (peak > (maxByEdgeRef.current.get(e.key) || 0)) { maxByEdgeRef.current.set(e.key, peak); dirty = true; }
    });
    if (dirty) {
      if (edgeMaxSaveTimer.current) window.clearTimeout(edgeMaxSaveTimer.current);
      edgeMaxSaveTimer.current = window.setTimeout(() => {
        const obj: Record<string, number> = {};
        maxByEdgeRef.current.forEach((v, k) => { obj[k] = v; });
        lsSet('hy2.edgeMax', JSON.stringify(obj));
      }, 1000);
    }
  }, [edges]);

  // ---------------------------------------------------------------- canvases
  const { canvases, setCanvases } = useGraphCanvases();
  const [listTimeout, setListTimeout] = useState(false);
  useEffect(() => {
    if (canvases) return;
    const id = window.setTimeout(() => setListTimeout(true), 800);
    return () => window.clearTimeout(id);
  }, [canvases]);
  const list = canvases ?? (listTimeout ? [] : null);
  const [storedKey, setStoredKey] = useState<string | null>(() => lsGet(ACTIVE_STORE()));
  const activeKey = useMemo<string | null>(() => {
    if (storedKey?.startsWith('p:') && PRESET_IDS.includes(storedKey.slice(2) as PresetId)) return storedKey;
    if (!list) return null;
    if (storedKey?.startsWith('c:') && list.some((c) => c.id === storedKey.slice(2))) return storedKey;
    if (list.some((c) => c.id === 'default')) return 'c:default';
    return 'p:layered';
  }, [storedKey, list]);
  const ready = activeKey !== null;
  const isCustom = !!activeKey?.startsWith('c:');
  const presetId: PresetId = activeKey?.startsWith('p:') ? (activeKey.slice(2) as PresetId) : 'layered';
  const activeCanvas = useMemo(() => (isCustom && list ? list.find((c) => c.id === activeKey!.slice(2)) || null : null), [isCustom, list, activeKey]);
  const activeCanvasRef = useRef(activeCanvas);
  activeCanvasRef.current = activeCanvas;
  const canvasLabel = useCallback((c: GraphCanvas) => c.name || (c.id === 'default' ? t('nodes.canvas.defaultName') : c.id), [t]);
  const presetLabel = useCallback((id: PresetId) => t(`nodes.canvas.preset.${id}`), [t]);
  const activeLabel = activeCanvas ? canvasLabel(activeCanvas) : presetLabel(presetId);

  const selectCanvas = useCallback((key: string) => {
    lsSet(ACTIVE_STORE(), key);
    setStoredKey(key);
  }, []);

  // Preset layouts depend only on the graph's shape (signature), not on the
  // rates/latencies refreshed by every topology poll.
  const layoutCache = useRef(new Map<string, Record<string, Pos>>());
  const layoutFor = useCallback((id: PresetId): Record<string, Pos> => {
    const k = id + '#' + sig;
    let v = layoutCache.current.get(k);
    if (!v) {
      if (layoutCache.current.size > 16) layoutCache.current.clear();
      v = presetLayout(id, graphRef.current);
      layoutCache.current.set(k, v);
    }
    return v;
  }, [sig]);
  const presetPos = useMemo(() => layoutFor(presetId), [layoutFor, presetId]);
  const fallbackPos = useMemo(() => layoutFor('layered'), [layoutFor]);

  /** Positions of a custom canvas: stored ones, plus automatic slots for
   *  nodes it doesn't know yet (local only, never saved by themselves). */
  const fillCustom = useCallback((stored: Record<string, Pos>): Record<string, Pos> => {
    const out: Record<string, Pos> = {};
    for (const k in stored) {
      const p = stored[k];
      if (k && p && Number.isFinite(p.x) && Number.isFinite(p.y)) out[k] = { x: p.x, y: p.y };
    }
    for (const k of nodesRef.current.keys()) {
      if (out[k]) continue;
      const others: Record<string, Pos> = { ...out };
      out[k] = resolveOverlap(k, fallbackPos[k] || { x: 0, y: 0 }, others, nodesRef.current);
    }
    return out;
  }, [fallbackPos]);

  // ---------------------------------------------------------------- custom draft
  // `overrides` holds every node position of the active custom canvas
  // (stored + auto-filled + in-flight drags); `edgeDraft` its edge data.
  const [overrides, setOverrides] = useState<Record<string, Pos>>({});
  const overridesRef = useRef(overrides);
  overridesRef.current = overrides;
  const [edgeDraft, setEdgeDraft] = useState<Record<string, EdgeData>>({});
  const edgeDraftRef = useRef(edgeDraft);
  edgeDraftRef.current = edgeDraft;
  const busyNodes = useRef(new Set<string>());
  const busyEdges = useRef(new Set<string>());
  const pendingNodes = useRef(new Map<string, { x: number; y: number; at: number }>());
  const pendingEdges = useRef(new Map<string, { json: string; at: number }>());
  const animRef = useRef(new Map<string, { from: Pos; to: Pos; start: number }>());
  const loadedCanvasRef = useRef<string | null>(null);
  const [loadTick, setLoadTick] = useState(0);

  // Remote moves (another session) slide over 300 ms.
  useEffect(() => {
    let raf = 0;
    const loop = (now: number) => {
      if (animRef.current.size > 0) {
        setOverrides((prev) => {
          const next = { ...prev };
          for (const [key, a] of animRef.current) {
            const p = Math.min(1, (now - a.start) / 300);
            const k = 1 - Math.pow(1 - p, 3);
            next[key] = { x: a.from.x + (a.to.x - a.from.x) * k, y: a.from.y + (a.to.y - a.from.y) * k };
            if (p >= 1) animRef.current.delete(key);
          }
          return next;
        });
      }
      raf = requestAnimationFrame(loop);
    };
    raf = requestAnimationFrame(loop);
    return () => cancelAnimationFrame(raf);
  }, []);

  // Load / reconcile the active custom canvas with the server snapshot.
  useLayoutEffect(() => {
    if (!isCustom || !activeCanvas) { loadedCanvasRef.current = null; return; }
    const remoteNodes = activeCanvas.nodes || {};
    const remoteEdges = activeCanvas.edges || {};
    if (loadedCanvasRef.current !== activeCanvas.id) {
      // Switched to this canvas: take it as is.
      loadedCanvasRef.current = activeCanvas.id;
      animRef.current.clear();
      pendingNodes.current.clear();
      pendingEdges.current.clear();
      setOverrides(fillCustom(remoteNodes));
      setEdgeDraft({ ...remoteEdges });
      setLoadTick((x) => x + 1);
      return;
    }
    const now = performance.now();
    for (const [k, v] of pendingNodes.current) if (now - v.at > 3000) pendingNodes.current.delete(k);
    for (const [k, v] of pendingEdges.current) if (now - v.at > 3000) pendingEdges.current.delete(k);
    const cur = overridesRef.current;
    const base: Record<string, Pos> = {};
    for (const k in remoteNodes) {
      const r = remoteNodes[k];
      if (!k || !r || !Number.isFinite(r.x) || !Number.isFinite(r.y)) continue;
      if (busyNodes.current.has(k)) { if (cur[k]) base[k] = cur[k]; continue; }
      const pend = pendingNodes.current.get(k);
      if (pend && (Math.abs(pend.x - r.x) > 0.5 || Math.abs(pend.y - r.y) > 0.5)) { if (cur[k]) base[k] = cur[k]; continue; }
      if (pend) pendingNodes.current.delete(k);
      base[k] = { x: r.x, y: r.y };
    }
    for (const k of busyNodes.current) if (cur[k] && !(k in base)) base[k] = cur[k];
    const filled = (() => {
      const out = { ...base };
      for (const k of nodesRef.current.keys()) {
        if (out[k]) continue;
        out[k] = cur[k] && !(k in remoteNodes) ? cur[k] : resolveOverlap(k, fallbackPos[k] || { x: 0, y: 0 }, { ...out }, nodesRef.current);
      }
      return out;
    })();
    setOverrides((prev) => {
      const next = { ...prev };
      let changed = false;
      for (const k in filled) {
        const target = filled[k];
        const local = prev[k];
        if (!local) { next[k] = target; changed = true; continue; }
        if (Math.abs(local.x - target.x) < 0.5 && Math.abs(local.y - target.y) < 0.5) continue;
        if (busyNodes.current.has(k)) continue;
        animRef.current.set(k, { from: local, to: target, start: now });
      }
      for (const k in prev) {
        if (busyNodes.current.has(k) || k in filled) continue;
        delete next[k]; changed = true; animRef.current.delete(k);
      }
      return changed ? next : prev;
    });
    setEdgeDraft((prev) => {
      const next: Record<string, EdgeData> = {};
      for (const k in remoteEdges) {
        const pend = pendingEdges.current.get(k);
        if (busyEdges.current.has(k) || (pend && pend.json !== JSON.stringify(remoteEdges[k]))) { if (prev[k]) next[k] = prev[k]; continue; }
        if (pend) pendingEdges.current.delete(k);
        next[k] = remoteEdges[k];
      }
      for (const k in prev) {
        if (k in next) continue;
        const pend = pendingEdges.current.get(k);
        if (busyEdges.current.has(k) || (pend && pend.json !== 'null')) next[k] = prev[k];
      }
      return sameJSON(prev, next) ? prev : next;
    });
  }, [isCustom, activeCanvas, fallbackPos, fillCustom, nodes]);

  const saveCanvas = useCallback((nodesMap: Record<string, Pos>, edgeMap: Record<string, EdgeData>) => {
    const c = activeCanvasRef.current;
    if (!c) return;
    const now = performance.now();
    const cleanNodes: Record<string, Pos> = {};
    for (const k in nodesMap) {
      const p = nodesMap[k];
      if (!k || !p || !Number.isFinite(p.x) || !Number.isFinite(p.y)) continue;
      cleanNodes[k] = { x: Math.round(p.x * 10) / 10, y: Math.round(p.y * 10) / 10 };
      pendingNodes.current.set(k, { ...cleanNodes[k], at: now });
    }
    // Custom data of a connection that no longer exists is dropped.
    const cleanEdges: Record<string, EdgeData> = {};
    const prevEdges = c.edges || {};
    for (const k in edgeMap) {
      if (!edgesRef.current.has(k)) continue;
      const e = edgeMap[k];
      const wps = (e.waypoints || []).filter((w) => Number.isFinite(w.u) && Number.isFinite(w.v)).slice(0, MAX_WAYPOINTS)
        .map((w) => ({ u: Math.round(w.u * 1e4) / 1e4, v: Math.round(w.v * 1e4) / 1e4 }));
      const lt = typeof e.label_t === 'number' && Number.isFinite(e.label_t) ? Math.max(0, Math.min(1, Math.round(e.label_t * 1e4) / 1e4)) : undefined;
      if (!wps.length && lt === undefined) continue;
      cleanEdges[k] = { ...(wps.length ? { waypoints: wps } : {}), ...(lt !== undefined ? { label_t: lt } : {}) };
    }
    for (const k of new Set([...Object.keys(prevEdges), ...Object.keys(cleanEdges)])) {
      pendingEdges.current.set(k, { json: JSON.stringify(cleanEdges[k] ?? null), at: now });
    }
    const next: GraphCanvas = { ...c, nodes: cleanNodes, edges: cleanEdges };
    setCanvases((l) => (l ? l.map((x) => (x.id === c.id ? next : x)) : l));
    api.putGraphCanvas(next).catch((err: any) => toast.error(`${t('nodes.canvas.saveFailed')}: ${String(err?.message || err)}`));
  }, [setCanvases, toast, t]);

  // ---------------------------------------------------------------- positions
  const targetPos = useMemo<Record<string, Pos>>(() => {
    if (!isCustom) return presetPos;
    const m: Record<string, Pos> = {};
    for (const k of nodesRef.current.keys()) m[k] = overrides[k] || fallbackPos[k] || { x: 0, y: 0 };
    return m;
    // The node set is part of `sig`; topology polls don't move anything.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isCustom, presetPos, overrides, sig, fallbackPos]);

  // Canvas switch: dots glide from where they were to the new layout.
  const [morph, setMorph] = useState<{ from: Record<string, Pos>; p: number } | null>(null);
  const shownRef = useRef<Record<string, Pos>>({});
  const positions = useMemo<Record<string, Pos>>(() => {
    if (!morph) return targetPos;
    const e = 1 - Math.pow(1 - morph.p, 3);
    const m: Record<string, Pos> = {};
    for (const k in targetPos) {
      const a = morph.from[k], b = targetPos[k];
      m[k] = a ? { x: a.x + (b.x - a.x) * e, y: a.y + (b.y - a.y) * e } : b;
    }
    return m;
  }, [morph, targetPos]);
  const positionsRef = useRef(positions);
  positionsRef.current = positions;
  useEffect(() => { shownRef.current = positions; }, [positions]);

  // ---------------------------------------------------------------- routing
  const waypointsMap = useMemo(() => {
    if (!isCustom) return undefined;
    const m: Record<string, Waypoint[] | undefined> = {};
    for (const k in edgeDraft) m[k] = edgeDraft[k].waypoints;
    return m;
  }, [isCustom, edgeDraft]);
  // Routing and label placement depend on the graph's shape only, so they
  // are keyed on `sig`, not on every topology poll.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const geoms = useMemo(() => routeEdges(graphRef.current, positions, { waypoints: waypointsMap, avoid: true }), [sig, positions, waypointsMap]);
  const geomsRef = useRef(geoms);
  geomsRef.current = geoms;
  const labelTMap = useMemo(() => {
    const m: Record<string, number | undefined> = {};
    if (isCustom) for (const k in edgeDraft) if (typeof edgeDraft[k].label_t === 'number') m[k] = edgeDraft[k].label_t;
    return m;
  }, [isCustom, edgeDraft]);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const labelSpots = useMemo(() => placeLabels(graphRef.current, positions, geoms, labelTMap), [sig, positions, geoms, labelTMap]);

  // ---------------------------------------------------------------- view
  const userMoved = useRef(false);
  const clampView = useCallback((v: View): View => {
    const el = wrapRef.current;
    if (!el) return v;
    const r = el.getBoundingClientRect();
    const b = contentBounds(positionsRef.current);
    if (!b || r.width <= 0 || r.height <= 0) return v;
    const minX = b.minX - R_HUB_OUTER, maxX = b.maxX + R_HUB_OUTER, minY = b.minY - R_HUB_OUTER, maxY = b.maxY + R_HUB_OUTER;
    const x = Math.min(r.width - CLAMP_VISIBLE - minX * v.k, Math.max(CLAMP_VISIBLE - maxX * v.k, v.x));
    const y = Math.min(r.height - CLAMP_VISIBLE - minY * v.k, Math.max(CLAMP_VISIBLE - maxY * v.k, v.y));
    return { ...v, x, y };
  }, []);

  const dragRef = useRef<Drag | null>(null);
  const [dragging, setDragging] = useState(false);
  const claimRef = useRef<(e: PointerEvent) => boolean>(() => false);
  const cancelDragRef = useRef<() => void>(() => {});
  const { view, viewRef, setView, animateTo, zoomBy, consumeClick, clampK } = usePanZoom({
    ref: wrapRef,
    minK: 0.2,
    maxK: 3,
    initial: { x: 400, y: 80, k: 1 },
    isOverlay: (el) => !!el.closest('[data-overlay]'),
    claim: (e) => claimRef.current(e),
    onClaimCancel: () => cancelDragRef.current(),
    clamp: clampView,
    onUserMove: () => { userMoved.current = true; },
  });

  const [size, setSize] = useState({ w: 0, h: 0 });
  useLayoutEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const measure = () => setSize((s) => (s.w === el.clientWidth && s.h === el.clientHeight ? s : { w: el.clientWidth, h: el.clientHeight }));
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const fitFor = useCallback((pos: Record<string, Pos>, bends: Pos[] = []): View | null => {
    if (!size.w || !size.h) return null;
    const b = contentBounds(pos);
    if (!b) return null;
    for (const p of bends) { b.minX = Math.min(b.minX, p.x); b.maxX = Math.max(b.maxX, p.x); b.minY = Math.min(b.minY, p.y); b.maxY = Math.max(b.maxY, p.y); }
    const minX = b.minX - R_HUB_OUTER - FIT_PAD, maxX = b.maxX + R_HUB_OUTER + FIT_PAD;
    const minY = b.minY - R_HUB_OUTER - FIT_PAD / 2, maxY = b.maxY + R_HUB_OUTER + FIT_PAD;
    const availH = Math.max(40, size.h - TOP_RESERVE);
    const k = clampK(Math.min(size.w / (maxX - minX), availH / (maxY - minY), 1.2));
    return { k, x: size.w / 2 - ((minX + maxX) / 2) * k, y: TOP_RESERVE + availH / 2 - ((minY + maxY) / 2) * k };
  }, [size.w, size.h, clampK]);
  const bendsOf = (gs: Map<string, EdgeGeom>) => { const out: Pos[] = []; gs.forEach((g) => out.push(...g.bends)); return out; };

  // Stay fitted until the user pans, zooms or drags.
  useLayoutEffect(() => {
    if (!ready || userMoved.current || morph) return;
    const v = fitFor(targetPos, bendsOf(geomsRef.current));
    if (v) setView(v);
    // targetPos is read on purpose only when the shape or size changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ready, sig, size.w, size.h, fitFor, setView, morph, loadTick]);

  const fitView = () => {
    userMoved.current = false;
    const v = fitFor(positionsRef.current, bendsOf(geomsRef.current));
    if (v) animateTo(v);
  };

  // Canvas switch → morph + animated fit to the new layout.
  const prevKeyRef = useRef<string | null>(null);
  useLayoutEffect(() => {
    if (!activeKey) return;
    const prev = prevKeyRef.current;
    prevKeyRef.current = activeKey;
    if (!prev || prev === activeKey) return;
    const from = shownRef.current;
    let target: Record<string, Pos>;
    let bends: Pos[] = [];
    if (activeKey.startsWith('p:')) {
      target = layoutFor(activeKey.slice(2) as PresetId);
      bends = bendsOf(routeEdges(graphRef.current, target, { avoid: true }));
    } else {
      const c = (list || []).find((x) => x.id === activeKey.slice(2));
      target = fillCustom(c?.nodes || {});
      for (const [k, e] of Object.entries(c?.edges || {})) {
        const ge = edgesRef.current.get(k);
        if (!ge || !target[ge.from] || !target[ge.to]) continue;
        for (const w of e.waypoints || []) bends.push(toAbs(target[ge.from], target[ge.to], w));
      }
    }
    userMoved.current = false;
    if (Object.keys(from).length) {
      const t0 = performance.now();
      setMorph({ from, p: 0 });
      const step = (now: number) => {
        const p = Math.min(1, (now - t0) / MORPH_MS);
        setMorph(p < 1 ? { from, p } : null);
        if (p < 1) requestAnimationFrame(step);
      };
      requestAnimationFrame(step);
    }
    const v = fitFor(target, bends);
    if (v) animateTo(v, MORPH_MS);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeKey]);

  const clientToWorld = useCallback((cx: number, cy: number): Pos => {
    const el = wrapRef.current;
    if (!el) return { x: 0, y: 0 };
    const r = el.getBoundingClientRect();
    const v = viewRef.current;
    return { x: (cx - r.left - v.x) / v.k, y: (cy - r.top - v.y) / v.k };
  }, [viewRef]);

  // ---------------------------------------------------------------- selection
  const toLocalQ = useCallback((q: string | null | undefined) => (q === '__self__' ? selfId : q ?? null), [selfId]);
  const [currentQPath, setCurrentQPath] = useState<string | null>(toLocalQ(selectedQPath));
  const prevSelectedPropRef = useRef<string | null>(selectedQPath ?? null);
  useEffect(() => {
    const next = selectedQPath ?? null;
    if (next !== prevSelectedPropRef.current) {
      prevSelectedPropRef.current = next;
      setCurrentQPath(toLocalQ(next));
    }
  }, [selectedQPath, toLocalQ]);

  const selectedKey = useMemo(() => {
    if (!currentQPath) return null;
    const segs = currentQPath.split('/');
    return segs[segs.length - 1];
  }, [currentQPath]);
  const selectedPaths = useMemo<GraphPath[]>(() => (selectedKey ? findPaths(topology, selectedKey, selfId, selfName) : []), [selectedKey, topology, selfId, selfName]);
  const pathIdx = useMemo(() => {
    if (!currentQPath || selectedPaths.length === 0) return 0;
    const i = selectedPaths.findIndex((p) => p.segs.join('/') === currentQPath);
    return i >= 0 ? i : 0;
  }, [currentQPath, selectedPaths]);
  const curPath = selectedPaths[pathIdx] || null;

  /** Direction-aware edge for hop i of a route (mutual peers: the first hop
   *  follows whether the route is the inbound or the outbound one). */
  const edgeKeyFor = useCallback((p: GraphPath, i: number): string | null => {
    const a = p.keys[i], b = p.keys[i + 1];
    const firstInbound = p.segs[0] === selfId;
    const cands = i === 0 && firstInbound
      ? [`${b}→${a}`, `${a}→${b}`, `?${a < b ? a : b}|${a < b ? b : a}`]
      : [`${a}→${b}`, `${b}→${a}`, `?${a < b ? a : b}|${a < b ? b : a}`];
    for (const c of cands) if (edges.has(c)) return c;
    return null;
  }, [edges, selfId]);

  const activePathOrder = useMemo(() => {
    const m = new Map<string, number>();
    if (!curPath) return m;
    for (let i = 0; i < curPath.keys.length - 1; i++) { const k = edgeKeyFor(curPath, i); if (k) m.set(k, i); }
    return m;
  }, [curPath, edgeKeyFor]);

  const [showAlt, setShowAlt] = useState(() => lsGet(ALT_STORE) === '1');
  const altEdges = useMemo(() => {
    const s = new Set<string>();
    if (!showAlt || selectedPaths.length < 2) return s;
    selectedPaths.forEach((p, j) => {
      if (j === pathIdx) return;
      for (let i = 0; i < p.keys.length - 1; i++) { const k = edgeKeyFor(p, i); if (k && !activePathOrder.has(k)) s.add(k); }
    });
    return s;
  }, [showAlt, selectedPaths, pathIdx, edgeKeyFor, activePathOrder]);

  // One continuous sweep from self to the target at constant speed.
  const pathEdgeSchedule = useMemo(() => {
    const map = new Map<string, { delayMs: number; durationMs: number }>();
    if (!curPath) return map;
    const segs: Array<{ key: string; len: number }> = [];
    for (let i = 0; i < curPath.keys.length - 1; i++) {
      const key = edgeKeyFor(curPath, i);
      const g = key ? geoms.get(key) : undefined;
      if (key && g) segs.push({ key, len: g.len });
    }
    const total = segs.reduce((a, s) => a + s.len, 0) || 1;
    let cum = 0;
    for (const s of segs) {
      map.set(s.key, { delayMs: (cum / total) * PATH_TOTAL_MS, durationMs: Math.max(60, (s.len / total) * PATH_TOTAL_MS) });
      cum += s.len;
    }
    return map;
  }, [curPath, edgeKeyFor, geoms]);

  const selectPath = useCallback((key: string, p: GraphPath) => {
    const q = p.segs.join('/');
    setCurrentQPath(q);
    onSelectQPath?.(key === selfId ? '__self__' : q);
  }, [onSelectQPath, selfId]);

  const handleNodeClick = useCallback((key: string) => {
    const ps = findPaths(topology, key, selfId, selfName);
    if (ps.length === 0) return;
    if (selectedKey === key) {
      selectPath(key, ps[(pathIdx + 1) % ps.length]);
      return;
    }
    // Clicking deeper into the selected route keeps extending it.
    let chosen = ps[0];
    if (currentQPath) {
      const prevSegs = currentQPath.split('/');
      const ext = ps.find((p) => p.segs.length > prevSegs.length && prevSegs.every((s, i) => p.segs[i] === s));
      if (ext) chosen = ext;
    }
    selectPath(key, chosen);
  }, [topology, selfId, selfName, selectedKey, pathIdx, currentQPath, selectPath]);

  const [hoverEdge, setHoverEdge] = useState<string | null>(null);
  const [selectedEdge, setSelectedEdge] = useState<string | null>(null);
  const hoverTimer = useRef(0);
  const enterEdge = (key: string) => (e: ReactPointerEvent) => {
    if (e.pointerType !== 'mouse') return;
    window.clearTimeout(hoverTimer.current);
    setHoverEdge(key);
  };
  const leaveEdge = () => {
    window.clearTimeout(hoverTimer.current);
    hoverTimer.current = window.setTimeout(() => setHoverEdge(null), 140);
  };
  useEffect(() => { if (!isCustom) { setHoverEdge(null); setSelectedEdge(null); } }, [isCustom]);

  const detailKeyFor = useCallback((key: string) => {
    if (key === selfId) return '__self__';
    if (selectedKey === key && curPath) return curPath.segs.join('/');
    const ps = findPaths(topology, key, selfId, selfName);
    return ps[0] ? ps[0].segs.join('/') : key;
  }, [selfId, selectedKey, curPath, topology, selfName]);

  // ---------------------------------------------------------------- editing (custom canvases)
  const [snap, setSnap] = useState(() => lsGet(SNAP_STORE) === '1');
  const snapRef = useRef(snap);
  snapRef.current = snap;

  // Edge edits update the ref synchronously (a save may follow at once).
  const putEdge = (key: string, fn: (cur: EdgeData) => EdgeData) => {
    const prev = edgeDraftRef.current;
    const next = { ...prev, [key]: fn(prev[key] || {}) };
    edgeDraftRef.current = next;
    setEdgeDraft(next);
  };
  const setWaypoint = (key: string, i: number, w: Waypoint | null, insert = false) => {
    putEdge(key, (cur) => {
      const wps = [...(cur.waypoints || [])];
      if (w === null) wps.splice(i, 1);
      else if (insert) wps.splice(i, 0, w);
      else wps[i] = w;
      return { ...cur, waypoints: wps };
    });
  };
  const deleteWaypoint = (key: string, i: number) => {
    setWaypoint(key, i, null);
    saveCanvas(overridesRef.current, edgeDraftRef.current);
  };
  const resetLabel = (key: string) => {
    putEdge(key, (cur) => { const c = { ...cur }; delete c.label_t; return c; });
    saveCanvas(overridesRef.current, edgeDraftRef.current);
  };

  const animateToFinal = useCallback((key: string, from: Pos) => {
    let to: Pos = { ...from };
    if (snapRef.current) to = { x: Math.round(to.x / GRID_STEP) * GRID_STEP, y: Math.round(to.y / GRID_STEP) * GRID_STEP };
    const others: Record<string, Pos> = {};
    for (const k in overridesRef.current) if (k !== key) others[k] = overridesRef.current[k];
    to = resolveOverlap(key, to, others, nodesRef.current);
    const finish = () => {
      const next = { ...overridesRef.current, [key]: to };
      overridesRef.current = next;
      setOverrides(next);
      saveCanvas(next, edgeDraftRef.current);
      busyNodes.current.delete(key);
    };
    if (Math.abs(to.x - from.x) < 0.5 && Math.abs(to.y - from.y) < 0.5) { finish(); return; }
    const start = performance.now();
    const step = (now: number) => {
      const p = Math.min(1, (now - start) / 240);
      const k = 1 - Math.pow(1 - p, 3);
      if (p < 1) {
        setOverrides((prev) => ({ ...prev, [key]: { x: from.x + (to.x - from.x) * k, y: from.y + (to.y - from.y) * k } }));
        requestAnimationFrame(step);
      } else finish();
    };
    requestAnimationFrame(step);
  }, [saveCanvas]);

  const nodeClickSuppress = useRef(false);
  claimRef.current = (e: PointerEvent) => {
    if (!isCustom || !activeCanvas) return false;
    const target = e.target as Element;
    const touch = e.pointerType !== 'mouse';
    const wpEl = target.closest('[data-wp]');
    if (wpEl) {
      const key = wpEl.getAttribute('data-edge') || '';
      const i = Number(wpEl.getAttribute('data-wp'));
      busyEdges.current.add(key);
      const d: Drag = { kind: 'wp', id: e.pointerId, key, i, sx: e.clientX, sy: e.clientY, moved: false, inserted: false, timer: 0 };
      if (touch) {
        // Long-press a handle to delete it.
        d.timer = window.setTimeout(() => {
          if (dragRef.current === d && !d.moved) {
            dragRef.current = null;
            setDragging(false);
            busyEdges.current.delete(key);
            deleteWaypoint(key, i);
          }
        }, 550);
      }
      dragRef.current = d;
      setDragging(true);
      setSelectedEdge(key);
      e.preventDefault();
      return true;
    }
    const ghEl = target.closest('[data-ghost]');
    if (ghEl) {
      const key = ghEl.getAttribute('data-edge') || '';
      const i = Number(ghEl.getAttribute('data-ghost'));
      const ge = edgesRef.current.get(key);
      const p = ge && positionsRef.current[ge.from], q = ge && positionsRef.current[ge.to];
      if (!ge || !p || !q) return false;
      if ((edgeDraftRef.current[key]?.waypoints?.length || 0) >= MAX_WAYPOINTS) return false;
      const at = clientToWorld(e.clientX, e.clientY);
      busyEdges.current.add(key);
      setWaypoint(key, i, toRel(p, q, at), true);
      dragRef.current = { kind: 'wp', id: e.pointerId, key, i, sx: e.clientX, sy: e.clientY, moved: false, inserted: true, timer: 0 };
      setDragging(true);
      setSelectedEdge(key);
      e.preventDefault();
      return true;
    }
    const lbEl = target.closest('[data-label-key]');
    if (lbEl) {
      const key = lbEl.getAttribute('data-label-key') || '';
      busyEdges.current.add(key);
      dragRef.current = { kind: 'label', id: e.pointerId, key, moved: false };
      setDragging(true);
      e.preventDefault();
      return true;
    }
    const ndEl = target.closest('[data-node-key]');
    if (ndEl) {
      const key = ndEl.getAttribute('data-node-key') || '';
      const startPos = positionsRef.current[key];
      if (!startPos) return false;
      busyNodes.current.add(key);
      dragRef.current = { kind: 'node', id: e.pointerId, key, start: clientToWorld(e.clientX, e.clientY), startPos, moved: false, touch };
      e.preventDefault();
      return true;
    }
    return false;
  };
  cancelDragRef.current = () => {
    const d = dragRef.current;
    dragRef.current = null;
    setDragging(false);
    if (!d) return;
    if (d.kind === 'node') {
      setOverrides((prev) => ({ ...prev, [d.key]: d.startPos }));
      busyNodes.current.delete(d.key);
    } else {
      if (d.kind === 'wp') window.clearTimeout(d.timer);
      busyEdges.current.delete(d.key);
    }
  };

  useEffect(() => {
    const onMove = (ev: PointerEvent) => {
      const d = dragRef.current;
      if (!d || d.id !== ev.pointerId) return;
      const at = clientToWorld(ev.clientX, ev.clientY);
      if (d.kind === 'node') {
        const dx = at.x - d.start.x, dy = at.y - d.start.y;
        if (!d.moved) {
          if (Math.hypot(dx, dy) * viewRef.current.k < (d.touch ? 8 : 3)) return;
          d.moved = true;
          userMoved.current = true;
          setDragging(true);
        }
        setOverrides((prev) => ({ ...prev, [d.key]: { x: d.startPos.x + dx, y: d.startPos.y + dy } }));
        return;
      }
      if (d.kind === 'wp') {
        if (!d.moved && Math.hypot(ev.clientX - d.sx, ev.clientY - d.sy) < 4) return;
        if (!d.moved) { d.moved = true; window.clearTimeout(d.timer); userMoved.current = true; }
        const ge = edgesRef.current.get(d.key);
        const p = ge && positionsRef.current[ge.from], q = ge && positionsRef.current[ge.to];
        if (p && q) setWaypoint(d.key, d.i, toRel(p, q, at));
        return;
      }
      const g = geomsRef.current.get(d.key);
      if (!g) return;
      d.moved = true;
      const tt = Math.max(0.05, Math.min(0.95, project(g, at) / g.len));
      putEdge(d.key, (cur) => ({ ...cur, label_t: tt }));
    };
    const onUp = (ev: PointerEvent) => {
      const d = dragRef.current;
      if (!d || d.id !== ev.pointerId) return;
      dragRef.current = null;
      setDragging(false);
      if (d.kind === 'node') {
        if (d.moved) {
          nodeClickSuppress.current = true;
          window.setTimeout(() => { nodeClickSuppress.current = false; }, 0);
          animateToFinal(d.key, overridesRef.current[d.key] || d.startPos);
        } else busyNodes.current.delete(d.key);
        return;
      }
      if (d.kind === 'wp') window.clearTimeout(d.timer);
      busyEdges.current.delete(d.key);
      if (d.moved || (d.kind === 'wp' && d.inserted)) saveCanvas(overridesRef.current, edgeDraftRef.current);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    window.addEventListener('pointercancel', onUp);
    return () => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onUp);
    };
    // setWaypoint only touches state setters and refs.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [clientToWorld, animateToFinal, saveCanvas, viewRef]);

  // Portalled dialogs bubble React events through this tree: only clicks
  // that really land on the canvas count.
  const onCanvas = (e: ReactMouseEvent) => !!wrapRef.current?.contains(e.target as Node) && !(e.target as Element).closest('[data-overlay]');
  const onCanvasClick = (e: ReactMouseEvent) => {
    if (consumeClick() || nodeClickSuppress.current || !onCanvas(e)) return;
    const target = e.target as Element;
    if (target.closest('[data-wp],[data-ghost]')) return;
    const lb = target.closest('[data-label-key]');
    if (lb) { setSelectedEdge(lb.getAttribute('data-label-key')); return; }
    const nd = target.closest('[data-node-key]');
    if (nd) {
      if (e.detail > 1) return;
      handleNodeClick(nd.getAttribute('data-node-key') || '');
      return;
    }
    const ed = target.closest('[data-edge-key]');
    if (ed) { setSelectedEdge(ed.getAttribute('data-edge-key')); return; }
    setSelectedEdge(null);
    if (currentQPath) { setCurrentQPath(null); onSelectQPath?.(null); }
  };
  const onCanvasDoubleClick = (e: ReactMouseEvent) => {
    if (!onCanvas(e)) return;
    const target = e.target as Element;
    const wp = target.closest('[data-wp]');
    if (wp && isCustom) { deleteWaypoint(wp.getAttribute('data-edge') || '', Number(wp.getAttribute('data-wp'))); return; }
    const lb = target.closest('[data-label-key]');
    if (lb && isCustom) { resetLabel(lb.getAttribute('data-label-key') || ''); return; }
    const nd = target.closest('[data-node-key]');
    if (nd && onOpenDetails) {
      const key = nd.getAttribute('data-node-key') || '';
      const r = nd.getBoundingClientRect();
      onOpenDetails(detailKeyFor(key), { x: r.left + r.width / 2, y: r.top + r.height / 2 });
    }
  };

  // ---------------------------------------------------------------- canvas actions
  /** The current picture as canvas data (positions + bends relative to each
   *  edge's chord), for copy and export. */
  const snapshotActive = useCallback(() => {
    const pos = targetPos;
    const nodesOut: Record<string, Pos> = {};
    for (const k in pos) nodesOut[k] = { x: Math.round(pos[k].x * 10) / 10, y: Math.round(pos[k].y * 10) / 10 };
    if (isCustom) for (const k in overridesRef.current) if (!nodesOut[k]) nodesOut[k] = overridesRef.current[k];
    const route = routeEdges(graphRef.current, pos, { waypoints: waypointsMap, avoid: true });
    const edgesOut: Record<string, EdgeData> = {};
    route.forEach((g, k) => {
      const ge = edgesRef.current.get(k);
      if (!ge) return;
      const d: EdgeData = {};
      if (g.bends.length) d.waypoints = g.bends.slice(0, MAX_WAYPOINTS).map((b) => toRel(pos[ge.from], pos[ge.to], b));
      if (isCustom && typeof edgeDraftRef.current[k]?.label_t === 'number') d.label_t = edgeDraftRef.current[k].label_t;
      if (d.waypoints || d.label_t !== undefined) edgesOut[k] = d;
    });
    return { nodes: nodesOut, edges: edgesOut };
  }, [targetPos, isCustom, waypointsMap]);

  const createCanvas = useCallback(async (name: string, data: { nodes: Record<string, Pos>; edges: Record<string, EdgeData> }) => {
    const c: GraphCanvas = { id: newId(), name, nodes: data.nodes, edges: data.edges };
    try {
      await api.putGraphCanvas(c);
      setCanvases((l) => (l && !l.some((x) => x.id === c.id) ? [...l, c] : l));
      selectCanvas('c:' + c.id);
    } catch (err: any) {
      toast.error(`${t('nodes.canvas.saveFailed')}: ${String(err?.message || err)}`);
    }
  }, [setCanvases, selectCanvas, toast, t]);

  const onCopy = async () => {
    const data = snapshotActive();
    const name = await askName(t('nodes.canvas.copyTitle'), t('nodes.canvas.newName', { name: activeLabel }));
    if (name) await createCanvas(name, data);
  };
  const onExport = () => {
    const data = snapshotActive();
    const blob = new Blob([JSON.stringify({ type: 'hy2scale-canvas', version: 1, name: activeLabel, ...data }, null, 2)], { type: 'application/json' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `topology-${activeLabel.replace(/[^\p{L}\p{N}._-]+/gu, '_') || 'canvas'}.json`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    window.setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  };
  const onImportFile = async (file: File) => {
    try {
      const obj = JSON.parse(await file.text());
      if (!obj || typeof obj !== 'object' || typeof obj.nodes !== 'object' || obj.nodes === null) throw new Error('format');
      const nodesIn: Record<string, Pos> = {};
      for (const [k, p] of Object.entries(obj.nodes as Record<string, any>)) {
        if (!k || !p || !Number.isFinite(p.x) || !Number.isFinite(p.y)) throw new Error('node');
        nodesIn[k] = { x: Number(p.x), y: Number(p.y) };
      }
      const edgesIn: Record<string, EdgeData> = {};
      for (const [k, e] of Object.entries((obj.edges || {}) as Record<string, any>)) {
        if (!k || !e || typeof e !== 'object') throw new Error('edge');
        const d: EdgeData = {};
        if (e.waypoints !== undefined) {
          if (!Array.isArray(e.waypoints) || e.waypoints.length > MAX_WAYPOINTS) throw new Error('waypoints');
          d.waypoints = e.waypoints.map((w: any) => {
            if (!w || !Number.isFinite(w.u) || !Number.isFinite(w.v)) throw new Error('waypoint');
            return { u: Number(w.u), v: Number(w.v) };
          });
        }
        if (e.label_t !== undefined) {
          if (!Number.isFinite(e.label_t) || e.label_t < 0 || e.label_t > 1) throw new Error('label');
          d.label_t = Number(e.label_t);
        }
        edgesIn[k] = d;
      }
      const base = typeof obj.name === 'string' && obj.name.trim() ? obj.name.trim() : file.name.replace(/\.json$/i, '');
      await createCanvas([...base].slice(0, 64).join('') || t('nodes.canvas.defaultName'), { nodes: nodesIn, edges: edgesIn });
    } catch {
      toast.error(t('nodes.canvas.importFailed'));
    }
  };
  const onRename = async () => {
    const c = activeCanvasRef.current;
    if (!c) return;
    const name = await askName(t('nodes.canvas.renameTitle'), canvasLabel(c));
    if (!name || name === c.name) return;
    const next = { ...c, name };
    setCanvases((l) => (l ? l.map((x) => (x.id === c.id ? next : x)) : l));
    api.putGraphCanvas(next).catch((err: any) => toast.error(`${t('nodes.canvas.saveFailed')}: ${String(err?.message || err)}`));
  };
  const onReset = async () => {
    const ok = await confirm({
      title: t('nodes.canvas.reset'),
      message: t('nodes.canvas.resetConfirm'),
      confirmText: t('app.reset'),
      cancelText: t('app.cancel'),
      danger: true,
    });
    if (!ok) return;
    animRef.current.clear();
    const filled = fillCustom({});
    overridesRef.current = filled;
    edgeDraftRef.current = {};
    setOverrides(filled);
    setEdgeDraft({});
    saveCanvas({}, {});
  };
  const onDelete = async () => {
    const c = activeCanvasRef.current;
    if (!c) return;
    const ok = await confirm({
      title: t('nodes.canvas.delete'),
      message: t('nodes.canvas.deleteConfirm', { name: canvasLabel(c) }),
      confirmText: t('app.delete'),
      cancelText: t('app.cancel'),
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteGraphCanvas(c.id);
      setCanvases((l) => (l ? l.filter((x) => x.id !== c.id) : l));
      selectCanvas('p:layered');
    } catch (err: any) {
      toast.error(String(err?.message || err));
    }
  };

  const menuProps: CanvasMenuProps = {
    presets: PRESET_IDS.map((id) => ({ key: 'p:' + id, label: presetLabel(id) })),
    customs: (list || []).map((c) => ({ key: 'c:' + c.id, label: canvasLabel(c) })),
    activeKey: activeKey || '',
    activeLabel,
    isCustom,
    onSelect: selectCanvas,
    onCopy,
    onImport: () => fileRef.current?.click(),
    onExport,
    onRename,
    onReset,
    onDelete,
  };

  // ---------------------------------------------------------------- flow chevrons
  const flowPathRefs = useRef(new Map<string, SVGPathElement>());
  const flowStateRef = useRef(new Map<string, { phase: number; speed: number }>());
  const flowRefCbs = useRef(new Map<string, (el: SVGPathElement | null) => void>());
  const getFlowRefCb = useCallback((key: string) => {
    let cb = flowRefCbs.current.get(key);
    if (!cb) {
      cb = (el: SVGPathElement | null) => { if (el) flowPathRefs.current.set(key, el); else flowPathRefs.current.delete(key); };
      flowRefCbs.current.set(key, cb);
    }
    return cb;
  }, []);
  useEffect(() => {
    let raf = 0;
    let last = performance.now();
    const BASE_SPEED = 24, MAX_SPEED = 180, IDLE_SPEED = 12, LERP = 0.08;
    const loop = (now: number) => {
      const dt = Math.min(0.1, (now - last) / 1000);
      last = now;
      let globalPeak = 0;
      maxByEdgeRef.current.forEach((v) => { if (v > globalPeak) globalPeak = v; });
      const denom = Math.max(globalPeak, 10 * 1024 * 1024);
      edgesRef.current.forEach((e) => {
        if (!e.directionKnown) return;
        const peak = Math.max(e.txRate, e.rxRate);
        const target = peak > 0 ? BASE_SPEED + Math.min(1, peak / denom) * (MAX_SPEED - BASE_SPEED) : IDLE_SPEED;
        const st = flowStateRef.current.get(e.key) || { phase: 0, speed: IDLE_SPEED };
        st.speed += (target - st.speed) * LERP;
        st.phase = ((st.phase + st.speed * dt) % CHEV_SPACING + CHEV_SPACING) % CHEV_SPACING;
        flowStateRef.current.set(e.key, st);
        const el = flowPathRefs.current.get(e.key);
        const g = geomsRef.current.get(e.key);
        if (!el || !g) return;
        let d = '';
        for (let s = st.phase; s <= g.len - CHEV_HALF_LEN; s += CHEV_SPACING) {
          if (s < CHEV_HALF_LEN) continue;
          const at = pointAt(g, s);
          const bx = at.x - at.tx * CHEV_HALF_LEN, by = at.y - at.ty * CHEV_HALF_LEN;
          const nx = -at.ty, ny = at.tx;
          d += `M${(bx + nx * CHEV_HALF_WID).toFixed(1)} ${(by + ny * CHEV_HALF_WID).toFixed(1)}L${at.x.toFixed(1)} ${at.y.toFixed(1)}L${(bx - nx * CHEV_HALF_WID).toFixed(1)} ${(by - ny * CHEV_HALF_WID).toFixed(1)}`;
        }
        el.setAttribute('d', d);
      });
      raf = requestAnimationFrame(loop);
    };
    raf = requestAnimationFrame(loop);
    return () => cancelAnimationFrame(raf);
  }, []);

  // Dark Reader rewrites SVG stroke colours as text colours, which turns a
  // background-coloured halo into a light box; under it, labels go
  // without a halo (they already sit beside their line).
  const [darkReader, setDarkReader] = useState(false);
  useEffect(() => {
    const html = document.documentElement;
    const check = () => setDarkReader(html.hasAttribute('data-darkreader-mode') || html.hasAttribute('data-darkreader-scheme'));
    check();
    const mo = new MutationObserver(check);
    mo.observe(html, { attributes: true, attributeFilter: ['data-darkreader-mode', 'data-darkreader-scheme'] });
    return () => mo.disconnect();
  }, []);

  function edgeWidth(e: GraphEdge): number {
    const ref = e.configuredMaxRate > 0 ? e.configuredMaxRate : maxByEdgeRef.current.get(e.key) || 0;
    return 1.5 + Math.min(1, ref / REF_MBPS_1000) * 5.5;
  }

  // ---------------------------------------------------------------- render
  const k = view.k;
  const handleKeys = new Set<string>();
  if (isCustom) {
    if (hoverEdge) handleKeys.add(hoverEdge);
    if (selectedEdge) handleKeys.add(selectedEdge);
    const d = dragRef.current;
    if (d && d.kind !== 'node') handleKeys.add(d.key);
  }

  const grid = (() => {
    if (!isCustom || !snap) return null;
    const vw = size.w || 1000, vh = size.h || 600;
    const pad = 2 * GRID_STEP;
    const x0 = Math.floor((-view.x / k - pad) / GRID_STEP) * GRID_STEP;
    const x1 = Math.ceil(((vw - view.x) / k + pad) / GRID_STEP) * GRID_STEP;
    const y0 = Math.floor((-view.y / k - pad) / GRID_STEP) * GRID_STEP;
    const y1 = Math.ceil(((vh - view.y) / k + pad) / GRID_STEP) * GRID_STEP;
    const lines: JSX.Element[] = [];
    const xc = Math.min(400, (x1 - x0) / GRID_STEP + 1), yc = Math.min(400, (y1 - y0) / GRID_STEP + 1);
    for (let i = 0; i < xc; i++) { const x = x0 + i * GRID_STEP; lines.push(<line key={`v${x}`} x1={x} y1={y0} x2={x} y2={y1} className="hy-topo-grid-line" />); }
    for (let i = 0; i < yc; i++) { const y = y0 + i * GRID_STEP; lines.push(<line key={`h${y}`} x1={x0} y1={y} x2={x1} y2={y} className="hy-topo-grid-line" />); }
    return lines;
  })();

  const edgeEls: ReactNode[] = [];
  const labelEls: ReactNode[] = [];
  const handleEls: ReactNode[] = [];
  edges.forEach((e) => {
    const g = geoms.get(e.key);
    if (!g) return;
    const fromN = nodes.get(e.from), toN = nodes.get(e.to);
    const offline = e.disabled || !!fromN?.offline || !!toN?.offline || e.segmentLatencyMs < 0;
    const idx = activePathOrder.get(e.key);
    const onPath = idx !== undefined;
    const sched = pathEdgeSchedule.get(e.key);
    const touchesNative = !!fromN?.native || !!toN?.native;
    const flip = onPath && curPath ? curPath.keys[idx!] === e.to : false;
    const mid = pointAt(g, g.len / 2);
    const peer = fromN?.isSelf ? toN : toN?.isSelf ? fromN : undefined;
    edgeEls.push(
      <g key={e.key} className={clsx('hy-topo-edge', offline && 'offline', onPath && !offline && 'on-path', touchesNative && 'to-native', g.custom && 'custom', altEdges.has(e.key) && 'alt')}>
        <path d={g.d} className="hy-topo-edge-base" strokeWidth={edgeWidth(e)} strokeDasharray={offline ? '4 3' : undefined} />
        {altEdges.has(e.key) && <path d={g.d} className="hy-topo-edge-alt" />}
        {onPath && (
          <path
            key={(curPath ? curPath.segs.join('/') : '') + ':' + idx}
            d={flip ? g.dRev : g.d}
            pathLength={1}
            className={clsx('hy-topo-edge-draw', offline && 'offline')}
            style={{
              ['--hy-path-len' as any]: 1,
              ['--hy-path-draw-ms' as any]: `${sched?.durationMs ?? PATH_TOTAL_MS}ms`,
              ['--hy-path-draw-delay' as any]: `${sched?.delayMs ?? 0}ms`,
            }}
          />
        )}
        {e.directionKnown && !offline && <path className="hy-topo-edge-flow" ref={getFlowRefCb(e.key)} />}
        {!offline && peer?.punched && (
          <g transform={`translate(${mid.x},${mid.y})`} className="hy-topo-edge-punch" data-punched={peer.name}>
            <title>{t('punch.ringTitle', { names: peer.punched.join(', ') })}</title>
            <circle r={4} />
          </g>
        )}
        {offline && (
          <g transform={`translate(${mid.x},${mid.y})`} className="hy-topo-edge-x"><path d="M -4 -4 L 4 4 M -4 4 L 4 -4" /></g>
        )}
        {isCustom && (
          <path
            d={g.d}
            className="hy-topo-edge-hit"
            data-edge-key={e.key}
            onPointerEnter={enterEdge(e.key)}
            onPointerLeave={leaveEdge}
          />
        )}
      </g>,
    );

    // Labels: segment latency above, dominant rate (with direction arrow)
    // below; hidden on broken hops and on lines too short to hold them.
    const spot = labelSpots.get(e.key);
    const screenLen = g.len * k;
    const showLatency = !offline && e.segmentLatencyMs > 0 && screenLen > 60;
    const showRate = !offline && screenLen > 110;
    if (spot && (showLatency || showRate)) {
      const latCls = ({ good: 'lat-ok', med: 'lat-mid', bad: 'lat-bad' } as const)[latencyTier(e.segmentLatencyMs)];
      const tx = e.txRate || 0, rx = e.rxRate || 0, value = Math.max(tx, rx);
      const arrow = value > 0 ? (tx >= rx ? '↓' : '↑') : '';
      const rateChildren = arrow
        ? <><tspan className={tx >= rx ? 'rate-arrow-down' : 'rate-arrow-up'}>{arrow}</tspan>{fmtRate(value)}</>
        : <>{fmtRate(value)}</>;
      const lines: Array<{ y: number; cls: string; body: ReactNode }> = [];
      if (showLatency) lines.push({ y: showRate ? -5 : 3, cls: latCls, body: fmtLatency(e.segmentLatencyMs) });
      if (showRate) lines.push({ y: showLatency ? 11 : 3, cls: 'rate', body: rateChildren });
      labelEls.push(
        <g
          key={e.key}
          className={clsx('hy-topo-edge-label-group', isCustom && 'editable', typeof labelTMap[e.key] === 'number' && 'placed')}
          data-label-key={isCustom ? e.key : undefined}
          style={{ transform: `translate(${spot.x}px, ${spot.y}px)` }}
        >
          {isCustom && <rect className="hy-topo-edge-label-hit" x={spot.anchor === 'start' ? -2 : spot.anchor === 'end' ? -58 : -30} y={showLatency && showRate ? -15 : -7} width={60} height={showLatency && showRate ? 30 : 16} />}
          {lines.map((l, i) => (
            <g key={i}>
              {!darkReader && <text x={0} y={l.y} textAnchor={spot.anchor} className="hy-topo-edge-label-halo">{l.body}</text>}
              <text x={0} y={l.y} textAnchor={spot.anchor} className={`hy-topo-edge-label ${l.cls}`}>{l.body}</text>
            </g>
          ))}
        </g>,
      );
    }

    if (handleKeys.has(e.key)) {
      const wps = edgeDraft[e.key]?.waypoints || [];
      const p = positions[e.from], q = positions[e.to];
      const items: ReactNode[] = [];
      if (wps.length < MAX_WAYPOINTS) {
        for (const gh of ghostPoints(g)) {
          items.push(<circle key={`g${gh.index}`} cx={gh.at.x} cy={gh.at.y} r={5 / k} className="hy-topo-ghost" data-ghost={gh.index} data-edge={e.key} />);
        }
      }
      if (p && q) {
        wps.forEach((w, i) => {
          const at = toAbs(p, q, w);
          items.push(<circle key={`w${i}`} cx={at.x} cy={at.y} r={6 / k} className="hy-topo-handle" data-wp={i} data-edge={e.key} />);
        });
      }
      handleEls.push(
        <g key={e.key} className="hy-topo-handles" style={{ strokeWidth: 2 / k }} onPointerEnter={enterEdge(e.key)} onPointerLeave={leaveEdge}>{items}</g>,
      );
    }
  });

  const nodeEls = Array.from(nodes.values()).map((n) => {
    const p = positions[n.key];
    if (!p) return null;
    const transit = isTransit(n);
    const rOuter = transit ? R_HUB_OUTER : R_SINGLE;
    return (
      <g
        key={n.key}
        data-node-key={n.key}
        transform={`translate(${p.x},${p.y})`}
        className={clsx(
          'hy-topo-dot', n.isSelf && 'self', n.disabled && 'disabled', n.offline && !n.disabled && 'offline',
          n.nested && 'nested', n.native && 'native', n.incompatible && 'bad', selectedKey === n.key && 'selected',
        )}
      >
        <circle r={rOuter} className="hy-topo-dot-outer" />
        {transit && <circle r={R_HUB_INNER} className="hy-topo-dot-inner" />}
        <text y={rOuter + 14} textAnchor="middle" className="hy-topo-dot-name">{n.name}</text>
      </g>
    );
  });

  // ---------------------------------------------------------------- path panel
  const pathPanel = (() => {
    if (!curPath) return null;
    const p = curPath;
    const selNode = nodes.get(p.keys[p.keys.length - 1]);
    const hops = p.segs.map((key) => ({ key, name: nodes.get(key)?.name || key, native: !!nodes.get(key)?.native }));
    const offline = !!selNode?.offline || p.offline;
    const total = p.total;
    const latClass = (ms: number, off: boolean) => (ms <= 0 && !off ? 'lat-na' : pathLatencyClass(ms, off));
    const copyPath = hops.filter((h) => h.key !== selfId).map((h) => h.name).join('/');
    const detailKey = p.segs.length === 1 && p.segs[0] === selfId ? '__self__' : p.segs.join('/');
    const n = selectedPaths.length;
    const step = (d: number) => selectPath(p.keys[p.keys.length - 1], selectedPaths[(pathIdx + d + n) % n]);
    const nameOf = (key: string) => nodes.get(key)?.name || key;
    return (
      <div className="hy-topo-graph-pathinfo" role="status" aria-live="polite" data-overlay="" data-testid="path-panel">
        <div className="hy-topo-pathinfo-row">
          <span className="hy-topo-pathinfo-chain">
            {hops.map((hop, i) => {
              const qpForReach = hops.slice(0, i + 1).map((h) => h.name).join('/');
              const qpForOpen = hops.slice(0, i + 1).map((h) => h.key).join('/');
              const isSelfHop = hop.key === selfId;
              const reach = isSelfHop || isReachableAt(qpForReach);
              const color = reach ? 'var(--green)' : 'var(--red)';
              const hopEl = isSelfHop || hop.native || !onOpenRemote || !reach ? (
                <span className="hy-topo-pathinfo-hop" style={{ color }}>{hop.name}</span>
              ) : (
                <a
                  href="#"
                  className="hy-topo-pathinfo-hop hy-topo-pathinfo-hop-link"
                  style={{ color }}
                  title={t('nodes.openRemote')}
                  onClick={(ev) => {
                    ev.preventDefault();
                    ev.stopPropagation();
                    const r = (ev.currentTarget as HTMLElement).getBoundingClientRect();
                    onOpenRemote!(qpForOpen, { x: r.left + r.width / 2, y: r.top + r.height / 2 });
                  }}
                >{hop.name}</a>
              );
              return <span key={i}>{i > 0 && <span className="hy-topo-pathinfo-sep">/</span>}{hopEl}</span>;
            })}
          </span>
          <span className={`hy-topo-pathinfo-lat ${latClass(total, offline)}`}>{offline ? t('nodes.offline') : fmtLatency(total)}</span>
          {copyPath && (
            <button
              type="button"
              className="hy-topo-pathinfo-copy"
              aria-label={t('nodes.graph.copyPath')}
              title={t('nodes.graph.copyPath')}
              onClick={async () => {
                if (await copyToClipboard(copyPath)) toast.success(t('app.copied'));
                else toast.error(t('nodes.exportFailed'));
              }}
            >
              <IconCopy size={12} />
            </button>
          )}
          {onOpenDetails && (
            <button
              type="button"
              className="hy-topo-pathinfo-toggle"
              aria-label={t('nodes.detail.open')}
              title={t('nodes.detail.open')}
              data-testid="path-panel-details"
              onClick={(ev) => {
                const r = (ev.currentTarget as HTMLElement).getBoundingClientRect();
                onOpenDetails(detailKey, { x: r.left + r.width / 2, y: r.top + r.height / 2 });
              }}
            >
              <IconMaximize2 size={12} />
            </button>
          )}
        </div>
        {n > 1 && (
          <>
            <div className="hy-topo-pathinfo-nav">
              <button type="button" aria-label={t('nodes.graph.prevPath')} title={t('nodes.graph.prevPath')} onClick={() => step(-1)} data-testid="path-prev">
                <IconChevronLeft size={12} />
              </button>
              <span className="hy-topo-pathinfo-count" data-testid="path-count">{t('nodes.graph.pathCount', { i: pathIdx + 1, n })}</span>
              <button type="button" aria-label={t('nodes.graph.nextPath')} title={t('nodes.graph.nextPath')} onClick={() => step(1)} data-testid="path-next">
                <IconChevronRight size={12} />
              </button>
              <label className="hy-topo-pathinfo-alt">
                <input type="checkbox" checked={showAlt} onChange={(ev) => { setShowAlt(ev.target.checked); lsSet(ALT_STORE, ev.target.checked ? '1' : '0'); }} data-testid="path-show-alt" />
                <span>{t('nodes.graph.showOtherPaths')}</span>
              </label>
            </div>
            <div className="hy-topo-pathinfo-list" role="listbox">
              {selectedPaths.map((sp, j) => (
                <button
                  key={sp.segs.join('/')}
                  type="button"
                  role="option"
                  aria-selected={j === pathIdx}
                  className={clsx('hy-topo-pathinfo-item', j === pathIdx && 'on')}
                  onClick={() => selectPath(sp.keys[sp.keys.length - 1], sp)}
                  data-testid={`path-item-${j}`}
                >
                  <span className="hy-topo-pathinfo-item-chain">{sp.segs.map(nameOf).join(' / ')}</span>
                  <span className={`hy-topo-pathinfo-item-lat ${latClass(sp.total, sp.offline)}`}>{sp.offline ? t('nodes.offline') : fmtLatency(sp.total)}</span>
                </button>
              ))}
            </div>
          </>
        )}
      </div>
    );
  })();

  // ---------------------------------------------------------------- chrome
  const snapBox = isCustom ? (
    <MenuCheck checked={snap} onChange={(on) => { setSnap(on); lsSet(SNAP_STORE, on ? '1' : '0'); }} testId="canvas-snap">
      {t('nodes.graph.snap')}
    </MenuCheck>
  ) : null;
  // ResponsiveActions closes its ⋯ menu on Escape.
  const closeMore = () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
  const more: ReactNode = narrow || snapBox ? (
    <>
      {narrow && <CanvasSubmenu {...menuProps} onDone={closeMore} />}
      {snapBox}
    </>
  ) : null;
  const actionsEl = typeof actions === 'function' ? actions(more) : actions;

  return (
    <div
      ref={wrapRef}
      className={clsx('hy-topo-graph', full && 'is-full', isCustom ? 'is-custom' : 'is-preset', (dragging || !!morph) && 'is-busy', darkReader && 'is-dr')}
      data-testid="nodes-topo"
      data-canvas={activeKey || ''}
      onClick={onCanvasClick}
      onDoubleClick={onCanvasDoubleClick}
    >
      <svg className="hy-topo-graph-svg" aria-hidden="true">
        <g transform={`translate(${view.x},${view.y}) scale(${k})`} style={{ opacity: ready ? 1 : 0, transition: 'opacity 180ms ease-out' }}>
          {grid}
          <g className="hy-topo-edges">{edgeEls}</g>
          <g className="hy-topo-labels">{labelEls}</g>
          <g className="hy-topo-nodes">{nodeEls}</g>
          <g className="hy-topo-handle-layer">{handleEls}</g>
        </g>
      </svg>

      <div className="hy-topo-graph-topbar" data-overlay="">
        <div className="iface-topo-tools">
          <button type="button" className="iface-topo-tool" onClick={fitView} title={t('nodes.graph.fit')} data-testid="topo-fit">
            <IconScan size={14} />
          </button>
          <button type="button" className="iface-topo-tool" onClick={() => zoomBy(1 / 1.2)} title={t('nodes.graph.zoomOut')} data-testid="topo-zoom-out">
            <IconZoomOut size={14} />
          </button>
          <button type="button" className="iface-topo-tool" onClick={() => zoomBy(1.2)} title={t('nodes.graph.zoomIn')} data-testid="topo-zoom-in">
            <IconZoomIn size={14} />
          </button>
          {!narrow && <CanvasMenuButton {...menuProps} />}
        </div>
        {actionsEl && <div className="hy-topo-graph-actions" data-testid="topo-actions">{actionsEl}</div>}
      </div>

      {pathPanel}
      <input
        ref={fileRef}
        type="file"
        accept=".json,application/json"
        hidden
        onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ''; if (f) onImportFile(f); }}
      />
      {namePrompt}
    </div>
  );
}
