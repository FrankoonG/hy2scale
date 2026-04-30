import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { TopologyNode } from '@/api';
import * as api from '@/api';
import { getBasePath } from '@/api/client';
import { fmtRate } from '@/hooks/useFormat';
import { useExitPaths } from '@/hooks/useExitPaths';
import { useConfirm } from '@hy2scale/ui';

interface Props {
  topology: TopologyNode[];
  /** Local node_id — used as the key for the self-node. */
  selfId: string;
  selfName?: string;
  onOpenRemote?: (qpath: string) => void;
  /** Invoked with the bare node name (or '__self__' for the local node)
   *  when the user clicks the edit button in the selected-path overlay.
   *  The click coords let the parent anchor the edit-modal enter animation
   *  at the button position. */
  onEditNode?: (key: string, clickPos: { x: number; y: number }) => void;
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
  /** True when this node is configured locally (self or a direct/top-level
   *  client) and therefore editable via the same modal the list view uses.
   *  Nested descendants discovered via peer-introspection are read-only. */
  editable: boolean;
}

interface GraphEdge {
  key: string;
  from: string;             // dialer side (arrow source)
  to: string;               // dialee side
  directionKnown: boolean;
  disabled: boolean;
  /** Current tx+rx in bytes/sec (updated every topology poll). */
  currentRate: number;
  /** Configured bandwidth ceiling for this edge (bytes/sec). Set when
   *  EITHER endpoint reports `max_rate` from cfg.Clients (`MaxTx/MaxRx`).
   *  When non-zero, edgeWidth scales by this instead of the observed
   *  peak — so a known-bandwidth link draws at calibrated thickness on
   *  the very first paint. 0 means "unknown, fall back to peak". */
  configuredMaxRate: number;
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

function buildGraph(topo: TopologyNode[], selfId: string, selfName?: string) {
  const nodes = new Map<string, GraphNode>();
  const edges = new Map<string, GraphEdge>();
  const selfSet = new Set<string>([selfId, selfName || ''].filter(Boolean));

  function keyOf(n: TopologyNode): string {
    return n.is_self ? selfId : n.name;
  }

  function addOrMergeNode(n: TopologyNode, depth: number, qpath: string, editable: boolean) {
    const k = keyOf(n);
    const existing = nodes.get(k);
    if (!existing) {
      const thisUnreachable = n.connected === false && !n.is_self;
      const node: GraphNode & { _anyReachable?: boolean; _anyUnreachable?: boolean } = {
        key: k,
        name: n.is_self ? (selfName || n.name || 'self') : n.name,
        isSelf: !!n.is_self,
        disabled: !!n.disabled,
        // Offline only when (a) the user disabled it, or (b) ALL paths
        // observed for this node are unreachable. The single-occurrence
        // case is trivial; multi-occurrence is settled below in the
        // merge branch. Trust list-view's status-cache contract:
        // `connected` is the authoritative reachability flag.
        offline: !!n.disabled || thisUnreachable,
        nested: !!n.nested,
        native: !!n.native,
        incompatible: !!n.incompatible || !!n.conflict || !!n.unsupported,
        depth,
        degree: 0,
        qpath,
        totalLatencyMs: n.is_self ? 0 : (typeof n.latency_ms === 'number' ? n.latency_ms : 0),
        editable,
      };
      // Seed reachability tracking — see merge branch below for why.
      if (thisUnreachable) node._anyUnreachable = true;
      else node._anyReachable = true;
      nodes.set(k, node);
    } else {
      // Multi-occurrence merge per flag — needs different semantics
      // depending on what the flag means:
      //
      // OR (any occurrence's `true` wins): `disabled`, `nested`,
      // `native`, `incompatible`. These are positive states that we
      // want to surface as long as at least one source asserts them
      // (e.g. force_native on the direct-peer entry shouldn't be
      // erased when the same node appears as a deep child whose
      // cached copy didn't carry the override).
      //
      // AND (offline only when EVERY path is unreachable): `offline`.
      // For a mutual peer reachable both inbound and outbound, or for
      // a sub-peer reachable via multiple parent paths, having one
      // line in red and another in blue means the node IS still
      // reachable. The dot stays normal-coloured; only the dead lines
      // turn red. Tracked via `_anyReachable` so the FINAL state
      // (committed below the loop) is `disabled || !anyReachable`.
      if (n.disabled) existing.disabled = true;
      if (n.nested) existing.nested = true;
      if (n.native) existing.native = true;
      if (n.incompatible || n.conflict || n.unsupported) existing.incompatible = true;
      // Track reachability state — any one occurrence that's NOT
      // explicitly unreachable counts as reachable. We re-derive
      // `existing.offline` here so the answer reflects the strongest
      // available signal across all occurrences seen so far.
      const thisUnreachable = n.connected === false && !n.is_self;
      if (!thisUnreachable) (existing as any)._anyReachable = true;
      else (existing as any)._anyUnreachable = true;
      existing.offline = !!existing.disabled ||
        (!!(existing as any)._anyUnreachable && !(existing as any)._anyReachable);
      if (depth < existing.depth) {
        existing.depth = depth;
        existing.qpath = qpath;
        if (!n.is_self && typeof n.latency_ms === 'number' && n.latency_ms > 0) {
          existing.totalLatencyMs = n.latency_ms;
        }
        if (editable) existing.editable = true;
      }
    }
  }

  function addEdge(a: string, b: string, direction: string | undefined, disabled: boolean, rate: number, segmentLatencyMs: number, configuredMaxRate: number) {
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
      const e: GraphEdge = { key: ekey, from, to, directionKnown, disabled, currentRate: rate, configuredMaxRate, segmentLatencyMs };
      edges.set(ekey, e);
      const na = nodes.get(a); if (na) na.degree++;
      const nb = nodes.get(b); if (nb) nb.degree++;
    } else {
      if (rate > existing.currentRate) existing.currentRate = rate;
      if (configuredMaxRate > existing.configuredMaxRate) existing.configuredMaxRate = configuredMaxRate;
      if (disabled) existing.disabled = true;
      // keep first-visited segment latency
    }
  }

  function walk(list: TopologyNode[], parentKey: string | null, parentCumulativeLat: number, depth: number, parentQpath: string, editableRoot: boolean) {
    list.forEach((n) => {
      if (depth > 0 && selfSet.has(n.name)) return;
      const segs = parentQpath ? parentQpath.split('/') : [];
      if (depth > 0 && segs.includes(n.name)) return;
      const qpath = parentQpath ? parentQpath + '/' + n.name : keyOf(n);
      // Only the initial hop handed to walk() carries the editableRoot flag —
      // anything deeper is a discovered descendant and stays read-only.
      addOrMergeNode(n, depth, qpath, editableRoot && depth === 1);
      if (parentKey) {
        const rate = (n.tx_rate || 0) + (n.rx_rate || 0);
        const childLat = n.latency_ms;
        // Segment latency: cumulative at child minus cumulative at parent.
        // When either is unknown (0 = not measured, -1 = offline) fall back
        // to the child's own value — best we can do with current data.
        // `connected === false` means this specific hop is broken. The
        // backend only fills latency_ms = -1 on top-level direct peers; for
        // sub-peers it leaves latency_ms = 0 even when connected = false,
        // so we MUST gate on connected explicitly — otherwise the parent →
        // offline-child edge gets segLat = 0 and renders as a healthy 0 ms
        // hop, while every consumer of segmentLatencyMs (edge offline class,
        // path-info totals, × marker) silently treats it as reachable.
        let segLat = 0;
        if (n.connected === false) {
          segLat = -1;
        } else if (childLat > 0 && parentCumulativeLat >= 0) {
          const diff = childLat - parentCumulativeLat;
          segLat = diff > 0 ? diff : childLat;
        } else if (childLat > 0) {
          segLat = childLat;
        } else if (childLat === -1) {
          segLat = -1;
        }
        addEdge(parentKey, keyOf(n), n.direction, !!n.disabled, rate, segLat, n.max_rate || 0);
      }
      if (n.children && n.children.length > 0) {
        walk(n.children, keyOf(n), n.latency_ms || 0, depth + 1, qpath, false);
      }
    });
  }

  // The /api/topology response puts INBOUND peers as children of the self
  // entry and OUTBOUND peers as sibling top-level entries. A naive walker
  // with parentKey=null at the top level would never add an edge from self
  // to any outbound peer, and they'd render as disconnected dots. Pull
  // selfKey out and use it as the parent for every non-self top-level node
  // so outbound and inbound peers both receive a self→peer edge.
  //
  // Editability mirrors the list view's openEdit gating: self AND top-level
  // outbound entries (the ones user-configured via POST /api/clients or the
  // config file) are editable; inbound peers that appear as children of
  // self come from remote dials and the deeper descendants are discovered
  // via nested peer lists, both read-only.
  const selfEntry = topo.find((n) => n.is_self);
  const selfKey = selfEntry ? keyOf(selfEntry) : selfId;
  if (selfEntry) addOrMergeNode(selfEntry, 0, selfKey, true);
  topo.forEach((n) => {
    if (n.is_self) {
      if (n.children && n.children.length > 0) {
        walk(n.children, selfKey, 0, 1, selfKey, false);
      }
    } else {
      walk([n], selfKey, 0, 1, selfKey, true);
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
  // Always render in milliseconds for visual consistency across the
  // graph — the previous "auto switch to seconds above 1000 ms" branch
  // made the dot-center label lose alignment with neighbouring labels
  // and made cumulative-vs-segment comparisons confusing.
  return `${Math.round(ms)}ms`;
}

export default function NodesGraphView({ topology, selfId, selfName, onOpenRemote, onEditNode, selectedQPath, onSelectQPath }: Props) {
  const { t } = useTranslation();
  const { isReachableAt } = useExitPaths();
  const svgRef = useRef<SVGSVGElement | null>(null);

  const { nodes, edges } = useMemo(() => buildGraph(topology, selfId, selfName), [topology, selfId, selfName]);

  // Per-edge running max — used to normalise flow speed AND to size the
  // edge stroke. Persist to localStorage so the line-thickness signal
  // doesn't disappear after every page refresh: previously a refresh
  // wiped the in-memory map, all edges drew at the minimum width, and
  // it took toggling a route off+on to repopulate the peak.
  const maxByEdgeRef = useRef<Map<string, number>>((() => {
    try {
      const raw = localStorage.getItem('hy2.edgeMax');
      if (!raw) return new Map();
      const obj = JSON.parse(raw) as Record<string, number>;
      return new Map(Object.entries(obj));
    } catch {
      return new Map();
    }
  })());
  const edgeMaxSaveTimer = useRef<number | null>(null);
  useMemo(() => {
    let dirty = false;
    edges.forEach((e) => {
      const prev = maxByEdgeRef.current.get(e.key) || 0;
      if (e.currentRate > prev) {
        maxByEdgeRef.current.set(e.key, e.currentRate);
        dirty = true;
      }
    });
    if (dirty) {
      if (edgeMaxSaveTimer.current) window.clearTimeout(edgeMaxSaveTimer.current);
      edgeMaxSaveTimer.current = window.setTimeout(() => {
        try {
          const obj: Record<string, number> = {};
          maxByEdgeRef.current.forEach((v, k) => { obj[k] = v; });
          localStorage.setItem('hy2.edgeMax', JSON.stringify(obj));
        } catch { /* quota / disabled storage — ignore */ }
      }, 1000);
    }
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
  // Overrides start empty. The SSE layout stream is the single source of
  // truth; no localStorage warm cache is used because it caused stale
  // positions to render briefly on reload, which then slid to the real
  // server state and looked like a reset.
  const [overrides, setOverrides] = useState<Record<string, Pos>>({});
  const overridesRef = useRef(overrides);
  useEffect(() => { overridesRef.current = overrides; }, [overrides]);
  const nodesRef = useRef(nodes);
  useEffect(() => { nodesRef.current = nodes; }, [nodes]);

  // Track whether the user is currently mid-drag on a specific node so
  // cross-tab layout polls don't clobber the node being dragged. Keyed
  // on node key so a remote update to any OTHER node can still apply
  // live.
  // Keys currently "busy" — either being dragged right now, or still
  // mid post-drop animation (which takes ~260 ms and ends with the PUT).
  // Reconcile skips any key in this set, so a remote SSE update can't
  // yank a dot back toward a stale server value while we're still in
  // the process of committing the user's drop.
  const activeDragKeysRef = useRef<Set<string>>(new Set());

  // Server-pushed layout via SSE — concurrent sessions on other browsers
  // see edits within ~one network RTT instead of waiting for a poll. The
  // stream endpoint sends the current snapshot on connect and every
  // subsequent SetGraphLayout, so we don't need a separate GET on mount.
  const [remoteLayout, setRemoteLayout] = useState<Record<string, Pos> | null>(null);
  const remoteLayoutReadyRef = useRef(false);
  // Tracks whether we've already applied one remote snapshot for this
  // component lifetime. The FIRST snapshot should teleport — it's just
  // catching up to the authoritative server state, and any visible slide
  // looks like the graph is being reset on page load. Subsequent changes
  // (another session dragging a dot) still animate.
  const firstSnapshotAppliedRef = useRef(false);
  // Mirrored as state so the auto-fit effect below can wait for it. We
  // can't fit on auto-layout positions and then have the SSE swap in
  // custom positions a few ms later — the pan/zoom would be locked on
  // the auto bbox, leaving the custom layout visibly off-center. The
  // gate also drives an opacity fade so the auto→custom flash is
  // hidden during the brief window between mount and first snapshot.
  const [snapshotApplied, setSnapshotApplied] = useState(false);
  useEffect(() => {
    const token = sessionStorage.getItem('token:' + getBasePath()) || '';
    const url = getBasePath() + '/api/graph-layout/stream?token=' + encodeURIComponent(token);
    const es = new EventSource(url);
    es.onmessage = (ev) => {
      try {
        const data = JSON.parse(ev.data);
        if (data && data.positions) {
          remoteLayoutReadyRef.current = true;
          setRemoteLayout(data.positions as Record<string, Pos>);
        }
      } catch { /* ignore malformed */ }
    };
    // EventSource auto-reconnects on error; no manual retry needed.
    return () => es.close();
  }, []);
  // Pending-write tracker — records positions we just PUT so the remote
  // snapshot that echoes our own write doesn't get treated as a "remote
  // edit" needing an animation; we already have the value locally.
  // Entries auto-expire after 3 s in case the server diverges.
  const pendingWriteRef = useRef<Map<string, { x: number; y: number; at: number }>>(new Map());
  // Animations in flight — each entry interpolates a node's on-screen
  // position from the point it was at when the remote update arrived to
  // the remote target, over 300 ms with a cubic ease-out. Visible effect:
  // when another session moves a dot, it slides to its new spot instead
  // of teleporting.
  const animRef = useRef<Map<string, { from: Pos; to: Pos; start: number }>>(new Map());
  const ANIM_MS = 300;

  // Fire-and-forget PUT. Records every key in the payload as a pending
  // write so the SSE echo of our own update (and any in-flight remote
  // snapshot that predated our PUT) doesn't rebound the UI back to the
  // server's old state.
  const saveLayoutToServer = useCallback((positions: Record<string, Pos>) => {
    // Filter out empty-string / invalid entries so a stale UI state
    // (e.g. selfId hadn't loaded yet when a node was added to the map)
    // never corrupts the server-persisted config.
    const clean: Record<string, Pos> = {};
    for (const k in positions) {
      if (!k) continue;
      const p = positions[k];
      if (!p || typeof p.x !== 'number' || typeof p.y !== 'number' || Number.isNaN(p.x) || Number.isNaN(p.y)) continue;
      clean[k] = p;
    }
    const now = performance.now();
    for (const k in clean) {
      pendingWriteRef.current.set(k, { x: clean[k].x, y: clean[k].y, at: now });
    }
    api.setGraphLayout(clean).catch(() => { /* best-effort; next save retries */ });
  }, []);

  useEffect(() => {
    let raf = 0;
    const loop = (now: number) => {
      if (animRef.current.size > 0) {
        setOverrides((prev) => {
          const next: Record<string, Pos> = { ...prev };
          let changed = false;
          for (const [key, anim] of animRef.current) {
            const t = Math.min(1, (now - anim.start) / ANIM_MS);
            const k = 1 - Math.pow(1 - t, 3);
            next[key] = {
              x: anim.from.x + (anim.to.x - anim.from.x) * k,
              y: anim.from.y + (anim.to.y - anim.from.y) * k,
            };
            changed = true;
            if (t >= 1) animRef.current.delete(key);
          }
          return changed ? next : prev;
        });
      }
      raf = requestAnimationFrame(loop);
    };
    raf = requestAnimationFrame(loop);
    return () => cancelAnimationFrame(raf);
  }, []);

  // Unified reconcile step. Runs whenever the topology (auto layout) or
  // the server-pushed layout changes.
  //   1. For every key the server has: if it differs from the local
  //      rendered position AND it isn't a stale echo of our own recent
  //      write, enqueue an animation from current → remote.
  //   2. Drop any local keys the server no longer has (handles "reset
  //      layout" propagating from another session).
  //   3. For any node still missing (new hot-created / newly-seen
  //      inbound), fall back to its auto-layout slot and push the
  //      augmented map back to the server so the new node's position
  //      is durably remembered across sessions.
  // The node currently under drag is left alone until pointerup so a
  // remote update can't snap the cursor mid-gesture.
  useEffect(() => {
    if (!remoteLayoutReadyRef.current) return;
    const remote = remoteLayout || {};
    const cur = overridesRef.current;
    const nowTs = performance.now();
    // Expire pending writes older than 3 s.
    for (const [k, v] of pendingWriteRef.current) {
      if (nowTs - v.at > 3000) pendingWriteRef.current.delete(k);
    }
    const dragKeys = activeDragKeysRef.current;
    // Compute authoritative base: remote values, except drag-locked node
    // (preserve local) and keys still awaiting our own PUT echo.
    const base: Record<string, Pos> = {};
    for (const k in remote) {
      // Defensive: ignore bogus entries (bad data from a prior buggy
      // save — e.g. empty-string key with null coords).
      if (!k) continue;
      const r = remote[k];
      if (!r || typeof r.x !== 'number' || typeof r.y !== 'number') continue;
      if (dragKeys.has(k)) {
        if (cur[k]) base[k] = cur[k];
        continue;
      }
      const pending = pendingWriteRef.current.get(k);
      if (pending && (Math.abs(pending.x - r.x) > 0.5 || Math.abs(pending.y - r.y) > 0.5)) {
        // Our PUT hasn't arrived back on the stream yet — keep local so
        // the dot doesn't rebound to whatever the server had before.
        if (cur[k]) base[k] = cur[k];
        continue;
      }
      if (pending) pendingWriteRef.current.delete(k);
      base[k] = { x: r.x, y: r.y };
    }
    for (const dk of dragKeys) {
      if (cur[dk] && !(dk in base)) base[dk] = cur[dk];
    }
    // Auto-fill missing nodes PURELY LOCALLY — do NOT save these back to
    // the server. Server state is strictly user-authored: drag release
    // and layout reset are the only paths that write. If we auto-saved
    // here, an empty server snapshot would cause every viewer to save
    // their own auto-layout (clobbering any custom positions that get
    // persisted later), and the first-to-save would freeze auto-layout
    // as the canonical state.
    for (const key in auto) {
      if (!key) continue; // skip empty-string artefacts
      if (!(key in base)) {
        const initial = auto[key];
        const existing: Record<string, Pos> = {};
        for (const k in base) if (k !== key) existing[k] = base[k];
        base[key] = resolveOverlap(key, initial, existing, nodes);
      }
    }
    const firstApply = !firstSnapshotAppliedRef.current;
    firstSnapshotAppliedRef.current = true;
    if (firstApply) setSnapshotApplied(true);
    // Diff against current render positions.
    setOverrides((prev) => {
      const next: Record<string, Pos> = { ...prev };
      let changed = false;
      for (const k in base) {
        const target = base[k];
        const local = prev[k];
        if (!local || firstApply) {
          // Brand-new node OR first-time catch-up to server authority —
          // appear directly at target, no animation. Animating on first
          // load would look like the graph is resetting before settling.
          next[k] = target;
          changed = true;
          animRef.current.delete(k);
          continue;
        }
        if (Math.abs(local.x - target.x) < 0.5 && Math.abs(local.y - target.y) < 0.5) continue;
        if (dragKeys.has(k)) continue;
        // Enqueue an animation. Leave the current override in place so
        // the RAF loop interpolates smoothly; it will update next[k]
        // over the 300 ms window.
        animRef.current.set(k, { from: local, to: target, start: nowTs });
      }
      // Drop any local keys that remote and auto both no longer have.
      for (const k in prev) {
        if (dragKeys.has(k)) continue;
        if (!(k in base)) { delete next[k]; changed = true; animRef.current.delete(k); }
      }
      return changed ? next : prev;
    });
  }, [auto, nodes, remoteLayout]);

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

  // Track the SVG's on-screen size so grid rendering can extend to
  // whatever portion of SVG space is currently visible under the active
  // pan/zoom. Without this the grid would only cover the nodes' bounding
  // box plus a fixed padding, leaving blank margins when the user pans
  // far from the nodes or zooms out.
  const [svgSize, setSvgSize] = useState<{ w: number; h: number }>({ w: 0, h: 0 });
  useEffect(() => {
    const svg = svgRef.current;
    if (!svg) return;
    const ro = new ResizeObserver(() => {
      const r = svg.getBoundingClientRect();
      setSvgSize((prev) => (prev.w === r.width && prev.h === r.height ? prev : { w: r.width, h: r.height }));
    });
    ro.observe(svg);
    const r = svg.getBoundingClientRect();
    setSvgSize({ w: r.width, h: r.height });
    return () => ro.disconnect();
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

  // Active pointers — keyed by pointerId. A second pointer going down
  // upgrades any in-progress single-pointer drag into a pinch-to-zoom
  // gesture; when either finger lifts the gesture ends and the remaining
  // pointer (if any) does NOT resume the earlier pan/drag (would feel
  // jarring on mobile), it simply becomes the seed for the next gesture.
  const pointersRef = useRef<Map<number, Pos>>(new Map());
  const pinchRef = useRef<{ startDist: number; startZoom: number; startPan: Pos; anchor: Pos } | null>(null);

  // Selection is stored as a single currentQPath string. The node key and
  // pathIdx are derived from it so topology refreshes — which can change the
  // order of equivalent paths — never cause the highlighted route to drift:
  // we look up the same qpath text in each fresh paths list.
  const [currentQPath, setCurrentQPath] = useState<string | null>(selectedQPath ?? null);
  // Track the last prop value so we only resync when the prop itself
  // actually changes — this lets the graph keep a self-only selection
  // active without the effect immediately clearing it back to null on
  // a same-value parent re-render.
  const prevSelectedPropRef = useRef<string | null>(selectedQPath ?? null);
  useEffect(() => {
    const next = selectedQPath ?? null;
    if (next !== prevSelectedPropRef.current) {
      prevSelectedPropRef.current = next;
      setCurrentQPath(next);
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
  // Direction-aware per-hop edge-key picker. Used by BOTH
  // `activePathOrder` (which edges are on-path) and `pathEdgeSchedule`
  // (when each on-path edge starts its draw). Both must agree on the
  // exact edge key for every hop, otherwise the schedule's
  // {delay, duration} attaches to the wrong key, the actually-on-path
  // edge falls back to default `delayMs: 0`, and the per-hop sequential
  // sweep collapses into a simultaneous all-at-once flash.
  const pickEdgeKey = useCallback((a: string, b: string, hopIndex: number): string | null => {
    // For mutual peers (both inbound + outbound edges exist), the FIRST
    // hop's direction depends on whether the user picked the inbound or
    // outbound qpath. rawActivePath starts with selfId for inbound
    // (peer dialed self → prefer `peer→self`); otherwise it's outbound
    // (self dialed peer → prefer `self→peer`).
    const firstHopInbound = !!rawActivePath && rawActivePath.length > 0 && rawActivePath[0] === selfId;
    const candidates = (hopIndex === 0 && firstHopInbound)
      ? [`${b}→${a}`, `${a}→${b}`, `?${a < b ? a : b}|${a < b ? b : a}`]
      : [`${a}→${b}`, `${b}→${a}`, `?${a < b ? a : b}|${a < b ? b : a}`];
    for (const c of candidates) if (edges.has(c)) return c;
    return null;
  }, [rawActivePath, selfId, edges]);

  const activePathOrder = useMemo(() => {
    const m = new Map<string, number>();
    if (!activePath) return m;
    for (let i = 0; i < activePath.length - 1; i++) {
      const key = pickEdgeKey(activePath[i], activePath[i + 1], i);
      if (key) m.set(key, i);
    }
    return m;
  }, [activePath, pickEdgeKey]);

  // Continuous sweep animation for the selected-path highlight. The
  // whole path is treated as one stroke being drawn at constant speed
  // from self to target — total duration PATH_TOTAL_MS regardless of
  // hop count. Per edge, we compute an animation-delay and duration
  // proportional to that edge's length within the path total length,
  // so a short first hop and a long second hop feel like one wave
  // moving at the same pixels-per-second rate. All edges on the path
  // are marked `.on-path` immediately at selection time; each edge's
  // draw overlay plays its CSS animation with the computed timing.
  const PATH_TOTAL_MS = 260;
  const pathEdgeSchedule = useMemo(() => {
    const map = new Map<string, { delayMs: number; durationMs: number }>();
    if (!activePath || activePath.length < 2) return map;
    const segs: Array<{ key: string; len: number }> = [];
    for (let i = 0; i < activePath.length - 1; i++) {
      const a = activePath[i], b = activePath[i + 1];
      const p = positions[a], q = positions[b];
      if (!p || !q) continue;
      // Use the SAME direction-aware picker as activePathOrder so the
      // schedule attaches to the same edge key the renderer marks
      // on-path. Mismatch → on-path edge gets default `delayMs: 0`
      // and the per-hop sequential draw collapses to all-at-once.
      const key = pickEdgeKey(a, b, i);
      if (!key) continue;
      segs.push({ key, len: Math.hypot(q.x - p.x, q.y - p.y) });
    }
    const totalLen = segs.reduce((acc, s) => acc + s.len, 0) || 1;
    let cum = 0;
    for (const s of segs) {
      const delayMs = (cum / totalLen) * PATH_TOTAL_MS;
      const durationMs = Math.max(60, (s.len / totalLen) * PATH_TOTAL_MS);
      map.set(s.key, { delayMs, durationMs });
      cum += s.len;
    }
    return map;
  }, [activePath, pickEdgeKey, positions]);

  const handleNodeClick = useCallback((key: string) => {
    const paths = findPathsToName(topology, key, selfId, selfName);
    if (paths.length === 0) return;
    // Forwarded selection key for the parent (NodesPage). Self uses the
    // sentinel '__self__' so it lines up with the TreeTable's row keys
    // and the top-right Edit button can route to EditSelfModal.
    const isSelf = key === selfId;
    const selKey = isSelf ? '__self__' : null;
    if (selectedKey === key) {
      // Same node → cycle paths (wrap around; deselect happens only when
      // the user clicks the empty canvas).
      const next = (pathIdx + 1) % paths.length;
      const q = paths[next].join('/');
      setCurrentQPath(q);
      onSelectQPath?.(isSelf ? selKey : q);
    } else {
      // If the previously-selected path's chain is a strict prefix of any
      // available route to the new target, prefer that route — clicking
      // deeper into the same subtree shouldn't snap back to a root-rooted
      // alternate path.
      let chosen = paths[0];
      if (currentQPath) {
        const prevSegs = currentQPath.split('/');
        const extending = paths.find(p =>
          p.length > prevSegs.length &&
          prevSegs.every((seg, i) => p[i] === seg)
        );
        if (extending) chosen = extending;
      }
      const q = chosen.join('/');
      setCurrentQPath(q);
      onSelectQPath?.(isSelf ? selKey : q);
    }
  }, [topology, selfId, selfName, selectedKey, pathIdx, currentQPath, onSelectQPath]);

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

  // Pointer events unify mouse, touch, and pen input — one code path
  // handles desktop dragging, touchscreen taps/drags, and stylus input
  // without forking per device. setPointerCapture ensures the SVG keeps
  // receiving pointermove/pointerup even if the finger slides outside
  // the element, so touch drags never lose tracking mid-gesture.
  const onPointerDown = useCallback((ev: ReactPointerEvent<SVGSVGElement>) => {
    // Secondary/middle buttons on mouse should not start a drag; a touch
    // contact has button=-1 on pointerdown which we do want to handle.
    if (ev.pointerType === 'mouse' && ev.button !== 0) return;
    pointersRef.current.set(ev.pointerId, { x: ev.clientX, y: ev.clientY });

    // Second finger down → start pinch gesture. Cancel any in-progress
    // single-pointer drag (node move / pan) cleanly: a node that was
    // being moved snaps back without finalising, since the gesture
    // meaningful to the user is now "zoom", not "drag".
    if (pointersRef.current.size === 2) {
      const pts = Array.from(pointersRef.current.values());
      const dx = pts[0].x - pts[1].x;
      const dy = pts[0].y - pts[1].y;
      const dist = Math.max(1, Math.hypot(dx, dy));
      const anchor = { x: (pts[0].x + pts[1].x) / 2, y: (pts[0].y + pts[1].y) / 2 };
      pinchRef.current = { startDist: dist, startZoom: zoom, startPan: pan, anchor };
      dragState.current = null;
      try { (ev.currentTarget as SVGSVGElement).setPointerCapture(ev.pointerId); } catch { /* ignore */ }
      ev.preventDefault();
      return;
    }

    const target = ev.target as SVGElement;
    const nodeEl = target.closest('[data-node-key]') as SVGElement | null;
    if (nodeEl) {
      const key = nodeEl.getAttribute('data-node-key') || '';
      const startSvg = clientToSvg(ev.clientX, ev.clientY);
      const startNodePos = positions[key] || { x: 0, y: 0 };
      dragState.current = { mode: 'node', key, startSvg, startNodePos, moved: false };
      // Lock this key against remote-layout clobbers until both the
      // gesture ends AND the post-drop animation + save finishes. A
      // previous incarnation released the lock on pointerup, which
      // left a ~260 ms window where a remote echo could yank the dot
      // back toward the stale server value (the "rebound on fast drag"
      // the user reported).
      activeDragKeysRef.current.add(key);
    } else {
      dragState.current = { mode: 'pan', startClient: { x: ev.clientX, y: ev.clientY }, startPan: pan, moved: false };
    }
    try { (ev.currentTarget as SVGSVGElement).setPointerCapture(ev.pointerId); } catch { /* ignore */ }
    ev.preventDefault();
  }, [clientToSvg, positions, pan, zoom]);

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
      setOverrides((prev) => { saveLayoutToServer(prev); return prev; });
      activeDragKeysRef.current.delete(key);
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
        setOverrides((prev) => { saveLayoutToServer(prev); return prev; });
        // Release the lock AFTER saveLayoutToServer has recorded the
        // pending-write entry. Any SSE echo that arrives from this
        // point on is matched against pendingWriteRef, so no rebound.
        activeDragKeysRef.current.delete(key);
      }
    };
    requestAnimationFrame(step);
  }

  useEffect(() => {
    function onMove(ev: PointerEvent) {
      // Keep tracked pointer positions fresh — pinch math reads them.
      if (pointersRef.current.has(ev.pointerId)) {
        pointersRef.current.set(ev.pointerId, { x: ev.clientX, y: ev.clientY });
      }

      // Pinch gesture active: compute current finger-spread distance,
      // scale zoom by the ratio to startDist, and move pan so the
      // anchor (midpoint between the two initial touch points) stays
      // fixed in SVG space as the zoom level changes.
      const pinch = pinchRef.current;
      if (pinch && pointersRef.current.size >= 2) {
        const pts = Array.from(pointersRef.current.values()).slice(0, 2);
        const dx = pts[0].x - pts[1].x;
        const dy = pts[0].y - pts[1].y;
        const dist = Math.max(1, Math.hypot(dx, dy));
        const ratio = dist / pinch.startDist;
        const nz = Math.max(0.3, Math.min(3, pinch.startZoom * ratio));
        const scale = nz / pinch.startZoom;
        // Anchor-preserving pan: translate so the original midpoint in
        // screen space still maps to the same SVG point under new zoom.
        const svg = svgRef.current;
        if (!svg) return;
        const r = svg.getBoundingClientRect();
        const ax = pinch.anchor.x - r.left;
        const ay = pinch.anchor.y - r.top;
        const nextPan = clampPan({
          x: ax - (ax - pinch.startPan.x) * scale,
          y: ay - (ay - pinch.startPan.y) * scale,
        }, nz);
        setZoom(nz);
        setPan(nextPan);
        return;
      }

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
        // Slightly larger threshold for touch to tolerate fingertip jitter.
        const slop = ev.pointerType === 'touch' ? 8 : 3;
        if (!s.moved && Math.abs(dx) + Math.abs(dy) > slop / zoom) s.moved = true;
        if (s.moved) {
          const nextPos = { x: s.startNodePos.x + dx, y: s.startNodePos.y + dy };
          setOverrides((prev) => ({ ...prev, [s.key]: nextPos }));
        }
      }
    }
    function onUp(ev: PointerEvent) {
      pointersRef.current.delete(ev.pointerId);
      // End pinch as soon as either finger lifts. Any remaining pointer
      // is treated as a fresh potential gesture seed — it won't resume
      // the earlier pan/drag since dragState was cleared on pinch start.
      if (pinchRef.current) {
        if (pointersRef.current.size < 2) pinchRef.current = null;
        activeDragKeysRef.current.clear();
        return;
      }
      const s = dragState.current;
      dragState.current = null;
      if (s?.mode === 'node') {
        if (!s.moved) {
          // Simple click — release lock immediately.
          activeDragKeysRef.current.delete(s.key);
          handleNodeClick(s.key);
        } else {
          // Drag-drop pipeline: snap → collision-bounce → animate. Keep
          // the lock on s.key set; animateToFinal releases it at the
          // end of its animation, after saveLayoutToServer records the
          // pending-write entry.
          setOverrides((prev) => {
            const pos = prev[s.key];
            if (pos) animateToFinal(s.key, pos);
            else activeDragKeysRef.current.delete(s.key);
            return prev;
          });
        }
      } else if (s?.mode === 'pan' && !s.moved) {
        // Click on empty canvas → deselect current selection.
        handleBlankClick();
      }
    }
    // Listen on window for pointer events so a touch/mouse drag that
    // leaves the SVG keeps tracking. pointercancel also clears state —
    // browsers fire it when a touch is pre-empted by a system gesture.
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    window.addEventListener('pointercancel', onUp);
    return () => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onUp);
    };
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

  // Fallback for when the SSE stream never delivers (server down, blocked
  // by a proxy that strips event-stream, layout endpoint unreachable):
  // after 800 ms with no snapshot, accept the auto-layout positions and
  // fit on those. Without this the graph would stay invisible (opacity
  // 0) indefinitely. 800 ms is well past the typical localhost SSE round
  // trip (sub-50 ms) but short enough that a real outage doesn't hang
  // the UI for noticeable time.
  useEffect(() => {
    if (snapshotApplied) return;
    const id = window.setTimeout(() => setSnapshotApplied(true), 800);
    return () => window.clearTimeout(id);
  }, [snapshotApplied]);

  // Edge-label CSS transition (transform .3s) makes labels glide when a
  // dragged dot moves their edge. On first paint that's the wrong
  // behaviour: labels sit at auto-layout midpoints, then snapshot apply
  // moves the dots to custom positions, and the CSS transition kicks in
  // to slide labels from auto-midpoints to custom-midpoints over 300 ms
  // — visible even after the opacity fade completes (~180 ms). Suppress
  // the transition until two rAFs after snapshot apply: one for React
  // to commit the new transform, one for the browser to paint it. After
  // that, drag-driven label glides work normally.
  const [labelsAnim, setLabelsAnim] = useState(false);
  useEffect(() => {
    if (!snapshotApplied) return;
    let r2 = 0;
    const r1 = requestAnimationFrame(() => {
      r2 = requestAnimationFrame(() => setLabelsAnim(true));
    });
    return () => { cancelAnimationFrame(r1); cancelAnimationFrame(r2); };
  }, [snapshotApplied]);

  // didFit is set after the auto-fit-on-mount stabilises. Once true,
  // neither the topology-poll position updates (every ~2 s) nor any
  // later SVG resize will steamroll a user-set pan/zoom. Manual Reset
  // view clears it so the next size-settle cycle re-fits.
  const didFit = useRef(false);
  const fitView = useCallback(() => {
    const svg = svgRef.current;
    if (!svg) return;
    const r = svg.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) return;
    const pts = Object.values(positions);
    if (pts.length === 0) return;
    // Manual Reset view (or any explicit fit) re-engages auto-fit on
    // the next stable-size cycle.
    didFit.current = false;
    const minX = Math.min(...pts.map((p) => p.x)) - R_HUB_OUTER - 40;
    const maxX = Math.max(...pts.map((p) => p.x)) + R_HUB_OUTER + 40;
    const minY = Math.min(...pts.map((p) => p.y)) - R_HUB_OUTER - 40;
    const maxY = Math.max(...pts.map((p) => p.y)) + R_HUB_OUTER + 40;
    const z = Math.min(r.width / (maxX - minX), r.height / (maxY - minY), 1.2);
    setPan({ x: r.width / 2 - ((minX + maxX) / 2) * z, y: r.height / 2 - ((minY + maxY) / 2) * z });
    setZoom(z);
  }, [positions]);

  // Auto-fit-on-mount with size-stability detection. Two failure modes
  // we have to defend against:
  //   1. list→graph view flip: parent Card mid-relayout, SVG briefly
  //      reports a smaller height than it'll settle at. Naive one-shot
  //      fit lands on the wrong dimensions.
  //   2. Topology poll (~2 s refetch): `positions` changes identity,
  //      this effect re-runs. Naive re-fit yanks the user's view.
  // Strategy: keep refitting through size changes until we measure the
  // SAME size in two consecutive attempts, then mark didFit and stop.
  // Subsequent runs of this effect bail on the didFit check, so polling
  // updates don't trigger fits.
  useLayoutEffect(() => {
    if (didFit.current) return;
    if (Object.keys(positions).length === 0) return;
    // Wait for the first remote snapshot (or the 800 ms fallback) before
    // committing to a fit. Fitting against auto-layout positions and
    // then having SSE swap in custom positions afterward leaves the
    // user's saved layout visibly off-center, because didFit locks the
    // pan/zoom on the auto bbox.
    if (!snapshotApplied) return;
    const svg = svgRef.current;
    if (!svg) return;
    let prevW = 0, prevH = 0;
    const tryFit = () => {
      if (didFit.current) return;
      const r = svg.getBoundingClientRect();
      if (r.width <= 0 || r.height <= 0) return;
      fitView();
      // Stable when this measurement matches the previous one.
      if (Math.abs(r.width - prevW) < 0.5 && Math.abs(r.height - prevH) < 0.5) {
        didFit.current = true;
      }
      prevW = r.width;
      prevH = r.height;
    };
    tryFit();
    // Cover async size changes during initial mount (parent reflow,
    // motion animations, font loading). Stops once didFit is set.
    const ro = new ResizeObserver(() => tryFit());
    ro.observe(svg);
    return () => ro.disconnect();
  }, [positions, fitView, snapshotApplied]);

  const confirm = useConfirm();
  const resetLayout = useCallback(async () => {
    const ok = await confirm({
      title: t('nodes.graph.resetLayout'),
      message: t('nodes.graph.resetLayoutConfirm'),
      confirmText: t('app.reset'),
      cancelText: t('app.cancel'),
      danger: true,
    });
    if (!ok) return;
    setOverrides({});
    saveLayoutToServer({});
  }, [confirm, t, saveLayoutToServer]);

  // Edge stroke width — scaled against an ABSOLUTE 0–1000 Mbps reference
  // so a line's thickness means the same thing regardless of what else
  // the mesh has seen. Values above 1 Gbps saturate at the max width.
  //
  // Reference picks the BEST available signal:
  //   1. `configuredMaxRate` from cfg.Clients (`MaxTx`/`MaxRx`) — if the
  //      operator declared the link's bandwidth, use it directly so the
  //      thickness is right on the very first paint, with no traffic
  //      needed. Cross-nested: the parent peer reports its own configured
  //      bandwidth for each sub-peer in its `max_rate` field, so a
  //      grand-child link can size correctly here too.
  //   2. otherwise the running observed peak from `maxByEdgeRef`.
  function edgeWidth(e: GraphEdge): number {
    const observedPeak = maxByEdgeRef.current.get(e.key) || 0;
    const ref = e.configuredMaxRate > 0 ? e.configuredMaxRate : observedPeak;
    const t = Math.min(1, ref / REF_MBPS_1000);
    return 1.5 + t * 5.5; // 1.5 .. 7 px
  }

  // Unified RAF-driven flow: a single animation mechanism expresses both
  // direction AND real-time speed. Each edge's flow-phase advances by
  // `speed * dt` each frame; `speed` is smoothed toward a rate-derived
  // target so transitions between idle and active are gradual, not
  // jump-cuts. An idle baseline speed keeps direction always readable.
  //
  // Rendered as a series of small chevron glyphs (">") along the edge
  // pointing in the flow direction, so even a static screenshot shows
  // where traffic is headed. Chevrons are drawn as a single <path>
  // per edge (cheap) and each frame rewrites that path's `d` to slide
  // the glyphs along the edge.
  const flowPathRefs = useRef<Map<string, SVGPathElement>>(new Map());
  const flowStateRef = useRef<Map<string, { phase: number; speed: number }>>(new Map());
  const edgesRef = useRef(edges);
  useEffect(() => { edgesRef.current = edges; }, [edges]);
  // Stable ref-callback per edge key — new inline closures would be
  // torn down + re-attached on every render, briefly removing the
  // path element from the map and freezing the chevron animation
  // whenever anything else in the graph re-rendered (selection, drag,
  // rate change). Memoising the callbacks means React only calls them
  // at true mount / unmount.
  const flowRefCbs = useRef<Map<string, (el: SVGPathElement | null) => void>>(new Map());
  const getFlowRefCb = useCallback((key: string) => {
    let cb = flowRefCbs.current.get(key);
    if (!cb) {
      cb = (el: SVGPathElement | null) => {
        if (el) flowPathRefs.current.set(key, el);
        else flowPathRefs.current.delete(key);
      };
      flowRefCbs.current.set(key, cb);
    }
    return cb;
  }, []);

  // Chevron sizing in SVG units. Kept tight so adding the arrowhead
  // doesn't visually thicken the edge — a 4-unit-long tip on a
  // stroke-width:1.5 line reads as an obvious arrow but doesn't
  // dominate the line itself.
  const CHEV_HALF_LEN = 3;     // along-edge distance from arm to tip
  const CHEV_HALF_WID = 2.4;   // perpendicular half-width
  const CHEV_SPACING = 18;     // chevrons are this far apart along the edge

  useEffect(() => {
    let raf = 0;
    let last = performance.now();
    const BASE_SPEED = 24;
    const MAX_SPEED = 180;
    // Idle (no traffic) chevron pace is HALF of BASE_SPEED so the
    // visual gap between "the link is alive but quiet" and "any
    // amount of traffic, even a few KB/s" is obvious. The instant
    // a non-zero rate is observed, target jumps to BASE_SPEED and
    // scales up from there; the lerp below smooths the transition
    // so the change reads as an acceleration rather than a jump-cut.
    const IDLE_SPEED = BASE_SPEED / 2;
    const LERP = 0.08;
    const loop = (now: number) => {
      const dt = Math.min(0.1, (now - last) / 1000);
      last = now;
      const curEdges = edgesRef.current;
      const maxs = maxByEdgeRef.current;
      const REF_MAX = 10 * 1024 * 1024;
      let globalPeak = 0;
      maxs.forEach((v) => { if (v > globalPeak) globalPeak = v; });
      const denom = Math.max(globalPeak, REF_MAX);
      const curPositions = positionsRef.current;
      const curNodes = nodesRef.current;
      curEdges.forEach((e) => {
        if (!e.directionKnown) return;
        const t = Math.min(1, e.currentRate / denom);
        const target = e.currentRate > 0
          ? BASE_SPEED + t * (MAX_SPEED - BASE_SPEED)
          : IDLE_SPEED;
        const state = flowStateRef.current.get(e.key) || { phase: 0, speed: IDLE_SPEED };
        state.speed += (target - state.speed) * LERP;
        // phase advances in the same direction as the edge (start → end).
        state.phase = ((state.phase + state.speed * dt) % CHEV_SPACING + CHEV_SPACING) % CHEV_SPACING;
        flowStateRef.current.set(e.key, state);
        const el = flowPathRefs.current.get(e.key);
        if (!el) return;
        // Compute geometry on the fly so the chevrons stay accurate as
        // the user drags nodes or pans the view; no stale cache to get
        // out of sync with the React render.
        const p = curPositions[e.from];
        const q = curPositions[e.to];
        if (!p || !q) return;
        // Shrink the chevron-path start/end by the node radius so
        // chevrons don't start inside a dot or cover its outline.
        const rFrom = isTransit(curNodes.get(e.from)) ? R_HUB_OUTER : R_SINGLE;
        const rTo = isTransit(curNodes.get(e.to)) ? R_HUB_OUTER : R_SINGLE;
        const dxAll = q.x - p.x;
        const dyAll = q.y - p.y;
        const fullLen = Math.hypot(dxAll, dyAll) || 1;
        const ux = dxAll / fullLen;
        const uy = dyAll / fullLen;
        const nx = -uy;
        const ny = ux;
        const hasOpposite = edgesRef.current.has(`${e.to}→${e.from}`);
        const offX = hasOpposite ? uy * 5 : 0;
        const offY = hasOpposite ? -ux * 5 : 0;
        const x1 = p.x + ux * rFrom + offX;
        const y1 = p.y + uy * rFrom + offY;
        const x2 = q.x - ux * rTo + offX;
        const y2 = q.y - uy * rTo + offY;
        const len = Math.hypot(x2 - x1, y2 - y1) || 1;
        const count = Math.ceil(len / CHEV_SPACING) + 1;
        let d = '';
        for (let i = 0; i < count; i++) {
          const s = i * CHEV_SPACING + state.phase;
          if (s < CHEV_HALF_LEN || s > len - CHEV_HALF_LEN) continue;
          const tipX = x1 + ux * s;
          const tipY = y1 + uy * s;
          // Arms trail behind the tip along -u direction, splayed by ±n.
          const backX = tipX - ux * CHEV_HALF_LEN;
          const backY = tipY - uy * CHEV_HALF_LEN;
          const a1x = backX + nx * CHEV_HALF_WID;
          const a1y = backY + ny * CHEV_HALF_WID;
          const a2x = backX - nx * CHEV_HALF_WID;
          const a2y = backY - ny * CHEV_HALF_WID;
          d += `M${a1x.toFixed(1)} ${a1y.toFixed(1)}L${tipX.toFixed(1)} ${tipY.toFixed(1)}L${a2x.toFixed(1)} ${a2y.toFixed(1)}`;
        }
        el.setAttribute('d', d);
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

  // Adaptive label placement.
  //
  // The previous midpoint-plus-perpendicular scheme couldn't handle the
  // common case where an edge passes *through* the area occupied by a
  // non-endpoint dot (e.g. the line from jp to 446f1f2b running past the
  // au dot). A fixed 14-unit perpendicular nudge doesn't clear a 30-unit
  // tall dot + label text, and nothing moved the label off the conflict
  // zone *along* the edge.
  //
  // New algorithm:
  //   1. For every non-endpoint node whose perpendicular distance to the
  //      line is within a label's reach, mark an interval of the edge
  //      parameter t ∈ [0,1] that the label must avoid. The interval
  //      width scales with the node's radius so larger dots claim a
  //      larger keep-out zone.
  //   2. Also mark the zones claimed by other edges' already-chosen
  //      labels so two crossing-edge labels don't overlap each other.
  //   3. Walk t values near 0.5 (midpoint) looking for a sample that
  //      isn't in any keep-out zone. If found, use it. If the midpoint
  //      itself was already clear, prefer it (avoids shuffling labels
  //      when nothing is blocking).
  //   4. Stack a per-edge side choice: for the chosen t, pick the
  //      perpendicular side (above/below the line) farther from any
  //      remaining nearby dots. Keeps labels off the line itself so the
  //      halo cut-out still reads cleanly.
  //
  // Returns absolute SVG coordinates for each edge's label anchor point.
  // Memoised against edges / positions so it doesn't recompute per frame.
  const edgeLabelAnchors = useMemo(() => {
    const map = new Map<string, { x: number; y: number; nx: number; ny: number; side: number }>();
    const nodeList = Array.from(nodes.values());
    const edgeList = Array.from(edges.values());
    // Precompute per-edge geometry.
    const geom = edgeList.map((e) => {
      const p = positions[e.from];
      const q = positions[e.to];
      if (!p || !q) return null;
      const dx = q.x - p.x;
      const dy = q.y - p.y;
      const len = Math.hypot(dx, dy) || 1;
      return { e, p, q, dx, dy, len, ux: dx / len, uy: dy / len, nx: -dy / len, ny: dx / len };
    });
    // Track already-placed label anchors so later edges avoid them too.
    const placed: Array<{ x: number; y: number }> = [];
    const PERP_REACH = 16;  // perpendicular distance at which a node still collides with a label
    const LABEL_PERP = 8;   // how far off the line the label sits
    const T_LO = 0.18, T_HI = 0.82;

    for (const g of geom) {
      if (!g) continue;
      const { e, p, len, ux, uy, nx, ny } = g;
      // Build keep-out intervals on t.
      const forbidden: Array<[number, number]> = [];
      for (const n of nodeList) {
        if (n.key === e.from || n.key === e.to) continue;
        const np = positions[n.key];
        if (!np) continue;
        const vx = np.x - p.x, vy = np.y - p.y;
        const tNode = (vx * ux + vy * uy) / len;
        if (tNode < -0.1 || tNode > 1.1) continue;
        const perpDist = Math.abs(vx * nx + vy * ny);
        const rNode = isTransit(n) ? R_HUB_OUTER : R_SINGLE;
        if (perpDist > rNode + PERP_REACH) continue;
        // How far along t we need to clear the node horizontally. The
        // keep-out width shrinks as perpendicular distance grows (a dot
        // slightly off-axis needs less along-axis clearance).
        const clearDist = rNode + 14 - Math.max(0, perpDist - rNode) * 0.5;
        const clearT = Math.max(0.02, clearDist / len);
        forbidden.push([tNode - clearT, tNode + clearT]);
      }
      // Also mark previously-placed labels' positions along this edge.
      for (const pl of placed) {
        const vx = pl.x - p.x, vy = pl.y - p.y;
        const tPl = (vx * ux + vy * uy) / len;
        if (tPl < -0.1 || tPl > 1.1) continue;
        const perpDist = Math.abs(vx * nx + vy * ny);
        if (perpDist > 18) continue;
        const clearT = 16 / len;
        forbidden.push([tPl - clearT, tPl + clearT]);
      }
      // Pick t: start at midpoint, then step outward until open.
      const isBlocked = (t: number) => {
        for (const [a, b] of forbidden) if (t >= a && t <= b) return true;
        return false;
      };
      let pickT = 0.5;
      if (isBlocked(pickT)) {
        let found = false;
        for (let step = 1; step <= 12 && !found; step++) {
          const delta = step * 0.05;
          for (const cand of [0.5 + delta, 0.5 - delta]) {
            if (cand < T_LO || cand > T_HI) continue;
            if (!isBlocked(cand)) { pickT = cand; found = true; break; }
          }
        }
        // If every sample is blocked, keep midpoint as a last resort.
      }
      const ax = p.x + (g.dx) * pickT;
      const ay = p.y + (g.dy) * pickT;
      // Choose perpendicular side that's farther from any remaining
      // nearby node. Default to +perp if no obstacle.
      let side = 1;
      let bestClearance = -Infinity;
      for (const cand of [1, -1]) {
        const cx = ax + nx * LABEL_PERP * cand;
        const cy = ay + ny * LABEL_PERP * cand;
        let minDist = Infinity;
        for (const n of nodeList) {
          if (n.key === e.from || n.key === e.to) continue;
          const np = positions[n.key];
          if (!np) continue;
          const dd = Math.hypot(np.x - cx, np.y - cy);
          if (dd < minDist) minDist = dd;
        }
        if (minDist > bestClearance) { bestClearance = minDist; side = cand; }
      }
      const finalX = ax + nx * LABEL_PERP * side;
      const finalY = ay + ny * LABEL_PERP * side;
      map.set(e.key, { x: finalX, y: finalY, nx, ny, side });
      placed.push({ x: finalX, y: finalY });
    }
    return map;
  }, [edges, nodes, positions]);

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
      <svg ref={svgRef} className="hy-topo-graph-svg" onPointerDown={onPointerDown}>
        {/*
          Opacity gate: hides the brief auto-layout render before the
          first SSE snapshot arrives (or the 800 ms fallback fires).
          Combined with the snapshotApplied gate on the auto-fit effect,
          this means the user only ever sees the correctly-fitted custom
          layout — no flash of auto-layout in the wrong viewport.
        */}
        <g
          transform={`translate(${pan.x},${pan.y}) scale(${zoom})`}
          style={{ opacity: snapshotApplied ? 1 : 0, transition: 'opacity 180ms ease-out' }}
        >
          {/* Background grid — rendered as real <line> elements in the main
              transformed group (not via <pattern>/<defs>). Dark-mode
              extensions reliably transform strokes on main-tree elements
              but tend to skip pattern contents, so keeping the grid in
              the direct DOM path makes it invertable. Range covers a wide
              box around the nodes; lines are step=24 SVG units. */}
          {(() => {
            // Render the grid across the entire visible viewport in SVG
            // space. Convert screen-space viewport corners back to SVG
            // coords via the inverse of the transform(translate(pan) scale(z))
            // applied by the parent <g>, then snap to the grid step so the
            // lines align on the integer grid regardless of pan offset.
            const step = 24;
            const vw = svgSize.w || 1000;
            const vh = svgSize.h || 600;
            const z = zoom || 1;
            const pad = 2 * step;
            const svMinX = -pan.x / z - pad;
            const svMaxX = (vw - pan.x) / z + pad;
            const svMinY = -pan.y / z - pad;
            const svMaxY = (vh - pan.y) / z + pad;
            const x0 = Math.floor(svMinX / step) * step;
            const x1 = Math.ceil(svMaxX / step) * step;
            const y0 = Math.floor(svMinY / step) * step;
            const y1 = Math.ceil(svMaxY / step) * step;
            // Safety cap: at extreme zoom-outs the span could balloon.
            // Hard-limit the line count so we never iterate unboundedly.
            const MAX_LINES = 400;
            const xCount = Math.min(MAX_LINES, Math.ceil((x1 - x0) / step) + 1);
            const yCount = Math.min(MAX_LINES, Math.ceil((y1 - y0) / step) + 1);
            const lines: JSX.Element[] = [];
            for (let i = 0; i < xCount; i++) {
              const x = x0 + i * step;
              lines.push(<line key={`v${x}`} x1={x} y1={y0} x2={x} y2={y1} className="hy-topo-grid-line" />);
            }
            for (let i = 0; i < yCount; i++) {
              const y = y0 + i * step;
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
            // Suppress in-line edge labels on unreachable links — both
            // latency and rate are meaningless on a connection that
            // isn't flowing, and crowding them onto a red dashed edge
            // adds visual noise without information.
            // segmentLatencyMs < 0 is its own broken-edge signal: in mesh
            // topologies a peer reachable via one path can be unreachable
            // via another, so node.offline (which dedupes by name across
            // every walk-encounter) is too coarse for per-edge state.
            const fromOffEarly = nodes.get(e.from)?.offline;
            const toOffEarly = nodes.get(e.to)?.offline;
            const edgeReachable = !e.disabled && !fromOffEarly && !toOffEarly && e.segmentLatencyMs >= 0;
            const showLatency = edgeReachable && e.segmentLatencyMs !== 0 && screenLen > 60;
            const showRate = edgeReachable && screenLen > 110;
            const width = edgeWidth(e);

            // Path highlight animation: all edges on the active path go
            // `on-path` immediately, and each draw overlay plays its CSS
            // animation with a delay + duration computed from the edge's
            // proportion of the total path length. That produces one
            // continuous wave sweeping from self to target at a constant
            // speed (total duration PATH_TOTAL_MS no matter the hop
            // count), instead of discrete per-hop flashes.
            const edgeIdx = activePathOrder.get(e.key);
            const onPath = edgeIdx !== undefined;
            const sched = pathEdgeSchedule.get(e.key);
            // An edge is offline/down when the edge itself is disabled OR
            // either endpoint reports offline OR the edge's own segment
            // latency is < 0 (the per-hop broken-link signal — needed for
            // mesh topologies where a single peer name appears in multiple
            // paths and the node-level offline flag dedupes wrong).
            // Such edges turn red, lose the flow-dot animation, and carry
            // an × marker at their midpoint so the broken hop is obvious.
            const fromOff = nodes.get(e.from)?.offline;
            const toOff = nodes.get(e.to)?.offline;
            const offline = e.disabled || !!fromOff || !!toOff || e.segmentLatencyMs < 0;
            // An edge "touches native" when EITHER endpoint is a NATIVE
            // node (raw hy2 server, not a hy2scale peer). The path-
            // highlight overlay re-tints to yellow on these edges so
            // selecting a route through a native upstream/downstream
            // visually matches the dot styling.
            const touchesNative = !!nodes.get(e.from)?.native || !!nodes.get(e.to)?.native;
            const cls = [
              'hy-topo-edge',
              offline && 'offline',
              onPath && !offline && 'on-path',
              touchesNative && 'to-native',
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
                {onPath && (
                  <line
                    key={activePath ? activePath.join('/') + ':' + edgeIdx : 'draw'}
                    x1={dx1} y1={dy1} x2={dx2} y2={dy2}
                    className={`hy-topo-edge-draw${offline ? ' offline' : ''}`}
                    style={{
                      ['--hy-path-len' as any]: drawLen,
                      ['--hy-path-draw-ms' as any]: `${sched?.durationMs ?? PATH_TOTAL_MS}ms`,
                      ['--hy-path-draw-delay' as any]: `${sched?.delayMs ?? 0}ms`,
                    }}
                  />
                )}
                {/* Flow overlay — a path of small chevrons (>) pointing
                    in the flow direction, slid along the edge by the
                    RAF loop above. The chevron shape makes direction
                    readable even in a still screenshot. Offline edges
                    have no flow; the × marker speaks for their state. */}
                {e.directionKnown && !offline && (
                  <path
                    className="hy-topo-edge-flow"
                    ref={getFlowRefCb(e.key)}
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
                    so dark-mode extensions' CSS overrides can't win.
                    The edgeLabelAnchors memo slides the anchor along the
                    edge (past non-endpoint dots the line passes through)
                    and offsets it perpendicular to the line on the side
                    with the most clearance. Within that anchor, the two
                    labels stack vertically in screen space as before:
                    latency slightly above, rate slightly below. */}
                {(() => {
                  const anchor = edgeLabelAnchors.get(e.key);
                  const lx = anchor?.x ?? mx;
                  const ly = anchor?.y ?? my;
                  // Position the label pair via a CSS translate on the
                  // wrapping <g> rather than x/y attributes on the text
                  // elements. SVG presentation attributes don't animate
                  // through CSS transitions; `transform` on an <g> does,
                  // so when edgeLabelAnchors picks a new anchor (e.g.
                  // after a dot is dragged nearby) the labels glide to
                  // the new spot instead of teleporting.
                  return (
                    <g
                      className="hy-topo-edge-label-group"
                      style={{
                        transform: `translate(${lx}px, ${ly}px)`,
                        // Disable the .3s glide on the very first paint so
                        // labels appear at their custom-layout midpoints
                        // directly instead of sliding in from auto-layout
                        // midpoints over the next 300 ms.
                        ...(labelsAnim ? null : { transition: 'none' }),
                      }}
                    >
                      {showLatency && (() => {
                        const latClass = e.segmentLatencyMs < 0 ? 'lat-na' : e.segmentLatencyMs < 80 ? 'lat-ok' : e.segmentLatencyMs < 200 ? 'lat-mid' : 'lat-bad';
                        const text = fmtLatency(e.segmentLatencyMs);
                        return (
                          <g>
                            <HaloText x={0} y={-5} stroke={surfaceColor}>{text}</HaloText>
                            <text x={0} y={-5} textAnchor="middle" className={`hy-topo-edge-label ${latClass}`}>{text}</text>
                          </g>
                        );
                      })()}
                      {showRate && (
                        <g>
                          <HaloText x={0} y={11} stroke={surfaceColor}>{fmtRate(e.currentRate)}</HaloText>
                          <text x={0} y={11} textAnchor="middle" className="hy-topo-edge-label rate">{fmtRate(e.currentRate)}</text>
                        </g>
                      )}
                    </g>
                  );
                })()}
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
              // Offline (couldn't connect) but not user-disabled →
              // light-red dot. Applies regardless of `native` so an
              // offline native upstream reads as broken just like any
              // other offline peer; `disabled` already fades opacity
              // and is the user's intentional state, so it's exempt.
              // CSS source order ensures `.offline` rules override the
              // `.native` palette when both classes are present.
              n.offline && !n.disabled && 'offline',
              n.nested && 'nested',
              n.native && 'native',
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
              </g>
            );
          })}
        </g>
      </svg>
      {displayPath && displayPath.length > 0 && (() => {
        const selNode = nodes.get(displayPath[displayPath.length - 1]);
        // Sum segment latencies along the SELECTED path, not whatever value
        // was last written into the per-node map. The per-node
        // totalLatencyMs is overwritten on every visit during topology
        // traversal, so it represents "the last path that happened to be
        // walked", not the path the user is currently viewing — which made
        // the displayed sum identical for every selection of the same
        // final node.
        const fullPath = displayPath[0] === selfId ? displayPath : [selfId, ...displayPath];
        let totalLat = 0;
        let anyOffline = false;
        for (let i = 1; i < fullPath.length; i++) {
          const a = fullPath[i - 1], b = fullPath[i];
          const e = edges.get(`${a}→${b}`) ?? edges.get(`${b}→${a}`)
            ?? edges.get(`?${a < b ? a : b}|${a < b ? b : a}`);
          const seg = e?.segmentLatencyMs ?? -1;
          if (seg < 0) { anyOffline = true; break; }
          totalLat += seg;
        }
        if (anyOffline) totalLat = -1;
        const offline = !!selNode?.offline || totalLat < 0;
        // Both display name and the underlying graph key per hop — the
        // key is what the qualifiedPath is built from (and what `selfId`
        // is compared against to identify the self hop), while the name
        // is what we actually render.
        const hops = displayPath.map((k) => ({ key: k, name: nodes.get(k)?.name || k }));
        const latClass = offline ? 'lat-bad' : totalLat <= 0 ? 'lat-na' : totalLat < 80 ? 'lat-ok' : totalLat < 200 ? 'lat-mid' : 'lat-bad';
        // Edit button retired from the path-info overlay; selecting a node
        // and pressing the top-right Edit button is now the unified entry
        // point for both list and graph views. Per-hop remote-open is now
        // handled inline by clicking the hop itself.
        return (
          <div className="hy-topo-graph-pathinfo" role="status" aria-live="polite">
            <span className="hy-topo-pathinfo-label">{t('nodes.graph.selectedPath')}</span>
            <span className="hy-topo-pathinfo-chain">
              {hops.map((hop, i) => {
                // Reach check is name-keyed (matches the topology store's
                // connectedPeers/disabledPaths sets); the remote-open URL
                // is key-keyed so NodesPage's onOpenRemote can strip the
                // self-id prefix correctly. Display names and node_ids
                // diverge for the local node (e.g. id=`ea1b2adb`,
                // name=`sg-home`), so the two paths must be distinct.
                const qpForReach = hops.slice(0, i + 1).map((h) => h.name).join('/');
                const qpForOpen = hops.slice(0, i + 1).map((h) => h.key).join('/');
                const reach = i === 0 || isReachableAt(qpForReach);
                const isSelfHop = hop.key === selfId;
                const color = reach ? 'var(--green)' : 'var(--red)';
                // Self hop: plain colored text — opening "remote into self"
                // would render the same UI we're already in, so the link
                // is suppressed there. Every other hop is a link.
                const hopEl = isSelfHop || !onOpenRemote ? (
                  <span className="hy-topo-pathinfo-hop" style={{ color }}>{hop.name}</span>
                ) : (
                  <a
                    href="#"
                    className="hy-topo-pathinfo-hop hy-topo-pathinfo-hop-link"
                    style={{ color }}
                    title={t('nodes.openRemote') || 'Open remote'}
                    onClick={(e) => {
                      e.preventDefault();
                      e.stopPropagation();
                      onOpenRemote!(qpForOpen);
                    }}
                  >{hop.name}</a>
                );
                return (
                  <span key={i}>
                    {i > 0 && <span className="hy-topo-pathinfo-sep">/</span>}
                    {hopEl}
                  </span>
                );
              })}
            </span>
            <span className={`hy-topo-pathinfo-lat ${latClass}`}>
              {/*
               * Use the same status word the list view's status column uses:
               * `nodes.offline` for both `connected=false` and explicitly
               * `disabled=true`. The previous generic `nodes.graph.unreachable`
               * literal made graph and list disagree on what to call the
               * same condition.
               */}
              {offline ? t('nodes.offline') : fmtLatency(totalLat)}
            </span>
          </div>
        );
      })()}
    </div>
  );
}
