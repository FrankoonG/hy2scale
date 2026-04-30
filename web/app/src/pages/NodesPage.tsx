import { useState, useCallback, useEffect, useRef, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  StatsGrid, Card, Button, Badge, Tooltip,
  TreeTable, TreeCell, IconTabs, useToast, useConfirm, useSelection,
  type TreeNode, type TreeColumn,
} from '@hy2scale/ui';
import type { TopologyNode } from '@/api';
import * as api from '@/api';
import { useNodeStore } from '@/store/node';
import { fmtBytes, fmtRate } from '@/hooks/useFormat';
import { useHistory } from '@/hooks/useHistory';
import { getBasePath } from '@/api/client';
import NodeModal from '@/components/NodeModal';
import NodeImportModal from '@/components/NodeImportModal';
import EditSelfModal from '@/components/EditSelfModal';
import ResponsiveActions from '@/components/ResponsiveActions';
import ImportExportButton from '@/components/ImportExportButton';
import NodesGraphView from '@/components/NodesGraphView';
import { useLongPress } from '@/hooks/useLongPress';
import { useDeselectOnBlankClick } from '@/hooks/useDeselectOnBlankClick';

export default function NodesPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const queryClient = useQueryClient();
  const { node, topology, setTopology, syncingNodes, setSyncing, clearSyncing } = useNodeStore();

  const [modalOpen, setModalOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [editSelfOpen, setEditSelfOpen] = useState(false);
  const [editingName, setEditingName] = useState<string | null>(null);
  const [clickPos, setClickPos] = useState<{ x: number; y: number } | undefined>();
  const lastPointer = useRef<{ x: number; y: number } | null>(null);
  // Default to graph view on first visit (no stored preference) since the
  // topology graph is the more informative presentation; list view remains
  // a one-click toggle away. localStorage remembers the user's last pick
  // explicitly — but the fallback when no preference exists is 'graph',
  // not 'list', so fresh installs don't land on the denser table.
  const [viewMode, setViewMode] = useState<'list' | 'graph'>(() => {
    try {
      const v = localStorage.getItem('scale:nodes-view');
      if (v === 'list' || v === 'graph') return v;
    } catch { /* ignore */ }
    return 'graph';
  });
  useEffect(() => {
    try { localStorage.setItem('scale:nodes-view', viewMode); } catch { /* ignore */ }
  }, [viewMode]);
  // The list-view scroll-to-selection effect is defined AFTER `selection`
  // is set up further down. JS hoists `function` declarations and `const`
  // bindings differently; this useEffect references `selection.*` so it
  // can't sit above the `useSelection(...)` call.

  // On short or narrow viewports the 4-stat row squeezes the Network
  // Topology card below half-screen — at narrow widths the cards wrap
  // to 2×2 and eat 300+ px of height before topology gets any. Measure
  // actual element heights and hide the stats row whenever showing it
  // would leave topology at <50 % of the viewport. Uses hysteresis
  // against viewport height rather than current topo height so the
  // decision doesn't thrash when toggling stats.
  const [showStats, setShowStats] = useState(true);
  const pageRef = useRef<HTMLDivElement | null>(null);
  const statsRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    const evaluate = () => {
      const vh = window.innerHeight;
      const pageEl = pageRef.current;
      const statsEl = statsRef.current;
      if (!pageEl) return;
      // Everything that competes for vertical space inside the page
      // area: topbar above + this page's top offset + the card gap.
      const pageTop = pageEl.getBoundingClientRect().top;
      // Measure the stats row height if it's currently rendered. When
      // it's hidden, estimate based on a fresh paint would be fragile —
      // so we only flip hidden→shown when the math works even with a
      // conservative overestimate of the stats height.
      const statsH = statsEl ? statsEl.getBoundingClientRect().height : 320;
      const STATS_GAP = 20; // gap between page children
      const CARD_HEADER = 60; // network topology card header + padding
      const nonTopo = pageTop + statsH + STATS_GAP + CARD_HEADER;
      const topoIfStats = vh - nonTopo;
      const shouldShow = topoIfStats >= 0.5 * vh;
      setShowStats((prev) => (prev === shouldShow ? prev : shouldShow));
    };
    // Run after layout settles; ResizeObserver on the stats row
    // catches the narrow-width re-wrap that changes its height.
    evaluate();
    const ro = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(evaluate) : null;
    if (ro && statsRef.current) ro.observe(statsRef.current);
    if (ro && pageRef.current) ro.observe(pageRef.current);
    window.addEventListener('resize', evaluate);
    return () => { window.removeEventListener('resize', evaluate); ro?.disconnect(); };
  }, [showStats]);

  // Poll topology every 2s
  useQuery({
    queryKey: ['topology'],
    queryFn: async () => {
      const topo = await api.getTopology();
      setTopology(topo);
      return topo;
    },
    refetchInterval: 2000,
  });

  // Poll stats every 2s
  const { data: stats, dataUpdatedAt: statsTick } = useQuery({
    queryKey: ['stats'],
    queryFn: () => api.getStats(),
    refetchInterval: 2000,
  });

  // Rolling history for stat-card sparklines (tech-feel background).
  // Sample on the poll tick so plateaued metrics still advance the line.
  const txBytesHist = useHistory(stats?.tx_bytes, statsTick);
  const rxBytesHist = useHistory(stats?.rx_bytes, statsTick);
  const txRateHist = useHistory(stats?.tx_rate, statsTick);
  const rxRateHist = useHistory(stats?.rx_rate, statsTick);
  const connsHist = useHistory(stats?.conns, statsTick);
  const exitClientsHist = useHistory(stats?.exit_clients, statsTick);

  // Short click on the Add Node button → manual form. The pointer position
  // for the modal animation comes from lastPointer (captured on mousedown
  // by useLongPress' handlers, since onClick's MouseEvent reflects the
  // release point — usually identical, but we want the press anchor).
  const addNodeShortClick = useCallback(() => {
    setClickPos(lastPointer.current || undefined);
    setEditingName(null);
    setModalOpen(true);
  }, []);

  // Long press → import-from-URL submenu.
  const addNodeLongPress = useCallback(() => {
    setClickPos(lastPointer.current || undefined);
    setImportOpen(true);
  }, []);

  const addNodeHandlers = useLongPress({
    delayMs: 600,
    onLongPress: addNodeLongPress,
    onShortClick: addNodeShortClick,
  });

  // The single source of truth for opening the edit modal is the
  // top-right Edit button (see `openEditFromBar`/`openEditSelfFromBar`).
  // Per-row edit buttons and the graph overlay edit button were retired
  // in favour of select-then-edit.
  // Anchor the modal animation at the
  // Edit button itself so the in-modal scale-in still feels purposeful.
  const editButtonAnchor = (): { x: number; y: number } | undefined => {
    const btn = document.querySelector('[data-testid="edit-selected-btn"]');
    if (!btn) return undefined;
    const r = btn.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  };
  const openEditFromBar = (key: string, n: TopologyNode) => {
    setClickPos(editButtonAnchor());
    // For root-level peers and native servers the key IS the name. For
    // nested children we still want to edit the configured root entry,
    // not a sub-peer (which has no editable config), so fall back to
    // the topology node's display name.
    const name = key.includes('/') ? n.name : key;
    setEditingName(name);
    setModalOpen(true);
  };
  const openEditSelfFromBar = () => {
    setClickPos(editButtonAnchor());
    setEditSelfOpen(true);
  };

  const handleToggle = useCallback(async (n: TopologyNode, qualifiedPath: string) => {
    const newDisabled = !n.disabled;
    setSyncing(qualifiedPath, !newDisabled);
    try {
      await api.disableClient(n.name, newDisabled);
      queryClient.invalidateQueries({ queryKey: ['topology'] });
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setTimeout(() => clearSyncing(qualifiedPath), 3000);
    }
  }, [setSyncing, clearSyncing, queryClient, toast]);

  const handleNestedToggle = useCallback(async (n: TopologyNode, qualifiedPath: string) => {
    const newNested = !n.nested;
    setSyncing(qualifiedPath, newNested);
    try {
      // Use qualifiedPath — backend needs full path for sub-peers
      // e.g. "eeff2f4a/0fb04e6f" maps to Peers["eeff2f4a/0fb04e6f"]
      // SetNested auto-strips self prefix for inbound peers
      await api.setNested(qualifiedPath, newNested);
      queryClient.invalidateQueries({ queryKey: ['topology'] });
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setTimeout(() => clearSyncing(qualifiedPath), 3000);
    }
  }, [setSyncing, clearSyncing, queryClient, toast]);

  const handleDelete = useCallback(async (name: string) => {
    const ok = await confirm({
      title: t('nodes.deleteTitle'),
      message: t('nodes.deleteConfirm', { name }),
      danger: true,
      confirmText: t('app.delete'),
      cancelText: t('app.cancel'),
    });
    if (!ok) return;
    try {
      await api.deleteClient(name);
      toast.success(t('nodes.deleted', { name }));
      queryClient.invalidateQueries({ queryKey: ['topology'] });
    } catch (e: any) {
      toast.error(String(e.message || e));
    }
  }, [confirm, t, queryClient, toast]);

  // Build tree nodes from topology
  const buildTreeNodes = (topo: TopologyNode[]): TreeNode<TopologyNode>[] => {
    const sorted = [...topo].sort((a, b) => {
      if (a.is_self) return -1;
      if (b.is_self) return 1;
      return a.name.localeCompare(b.name);
    });

    const selfId = node?.node_id || '';
    const selfName = node?.name || '';
    return sorted.map((n) => {
      const rootPath = n.is_self ? (node?.node_id || n.name) : n.name;
      // Rule 1 (including self identity): strip any first-level child whose
      // name matches our own node_id OR display name, plus the root itself.
      const safeChildren = n.children?.filter(
        (c) => c.name !== selfId && c.name !== selfName && c.name !== n.name
      );
      return {
        key: n.is_self ? '__self__' : n.name,
        data: n,
        expanded: true,
        className: n.is_self ? 'self-row' : syncingNodes.has(n.name) ? 'syncing' : n.disabled ? 'disabled-row' : undefined,
        children: safeChildren && safeChildren.length > 0
          ? [...safeChildren].sort((a, b) => a.name.localeCompare(b.name)).map((c) => buildChildNode(c, rootPath))
          : undefined,
      };
    });
  };

  const buildChildNode = (c: TopologyNode, parentPath: string): TreeNode<TopologyNode> => {
    const qp = `${parentPath}/${c.name}`;
    // Ancestors always include our local node_id AND display name so that
    // neither can ever re-appear deep in the tree, even if rootPath only
    // contained one of them (Rule 1 — self identity).
    const ancestors = new Set(parentPath.split('/'));
    if (node?.node_id) ancestors.add(node.node_id);
    if (node?.name) ancestors.add(node.name);
    const safeChildren = c.children?.filter((cc) => !ancestors.has(cc.name) && cc.name !== c.name);
    return {
      key: qp,
      data: c,
      expanded: true,
      className: `sub-row${syncingNodes.has(qp) ? ' syncing' : ''}${c.disabled ? ' disabled-row' : ''}`,
      // Don't expand children of a disabled node — they're effectively unreachable
      children: c.disabled || !safeChildren || safeChildren.length === 0
        ? undefined
        : [...safeChildren].sort((a, b) => a.name.localeCompare(b.name)).map((cc) => buildChildNode(cc, qp)),
    };
  };

  const latencyCell = (n: TopologyNode, qualifiedPath?: string) => {
    if (qualifiedPath && syncingNodes.has(qualifiedPath)) {
      return <span className="latency latency-sync">{t('nodes.syncing')}</span>;
    }
    if (n.incompatible) return <span className="latency latency-bad">{t('nodes.incompatible')}</span>;
    if (n.conflict) return <span className="latency latency-bad">{t('nodes.conflict')}</span>;
    if (n.unsupported) return <span className="latency latency-bad">{t('nodes.unsupported')}</span>;
    if (n.disabled) return <span className="latency latency-off">{t('nodes.offline')}</span>;
    if (n.is_self) return <span className="latency latency-good">∞</span>;
    // `connected === false` is the authoritative offline flag — set on
    // direct peers when the QUIC link is down, AND on nested sub-peers
    // that the parent reports as offline (post offline-propagation
    // change). Trips before the latency-based heuristics so the row
    // shows "offline" instead of an ambiguous em-dash.
    if (n.connected === false) return <span className="latency latency-off">{t('nodes.offline')}</span>;
    if (n.latency_ms === -1) return <span className="latency latency-off">{t('nodes.offline')}</span>;
    if (n.latency_ms === 0) return <span className="latency latency-na">—</span>;
    const cls = n.latency_ms < 80 ? 'latency-good' : n.latency_ms < 200 ? 'latency-med' : 'latency-bad';
    return <span className={`latency ${cls}`}>{n.latency_ms}ms</span>;
  };

  const dirBadge = (n: TopologyNode) => {
    if (n.is_self) return <Badge variant="blue">LOCAL</Badge>;
    if (n.direction === 'inbound') return <Badge variant="muted">IN</Badge>;
    if (n.direction === 'outbound') return <Badge variant="muted">OUT</Badge>;
    return null;
  };

  const columns: TreeColumn<TopologyNode>[] = [
    {
      key: 'status', title: t('nodes.status'), width: '75px', className: 'col-status',
      render: (n, meta) => {
        return latencyCell(n, meta.nodeKey);
      },
    },
    {
      key: 'dir', title: t('nodes.dir'), width: '55px', className: 'col-dir',
      render: (n) => dirBadge(n),
    },
    {
      key: 'node', title: t('nodes.node'), className: 'col-name',
      render: (n, meta) => {
        const basePath = getBasePath();
        // Already viewing a remote node through the local proxy — don't
        // offer to chain another /remote/ hop from here. Chained proxy
        // paths stack double-proxy overhead and make auth/routing hard to
        // reason about; the user should drive from their own local UI.
        const isRemoteView = !!(window as any).__PROXY__;
        const selfId = node?.node_id || '';
        const chain = selfId && meta.nodeKey.startsWith(selfId + '/')
          ? meta.nodeKey.slice(selfId.length + 1)
          : meta.nodeKey;
        const nameEl = n.is_self ? (
          <span className="peer-name-cell" style={{ color: 'var(--primary)' }}>{n.name || node?.name || 'self'}</span>
        ) : isRemoteView ? (
          <span className="peer-name-cell">{n.name}</span>
        ) : (
          <a className="peer-link peer-name-cell" href={`${basePath}/remote/${chain}/scale/`} target="_blank" rel="noopener">
            {n.name}
          </a>
        );

        const versionBadge = n.version && n.version !== node?.version ? (
          <span className={`version-badge${n.incompatible ? ' incompatible' : n.compat ? ' compat' : ''}`} style={{ marginLeft: 6 }}>
            v{n.version}{n.incompatible ? ' ✗' : ''}
          </span>
        ) : null;

        const nativeBadge = n.native ? <Badge variant="muted">native</Badge> : null;

        // Tree expand/collapse triangle: shown for non-self, non-native nodes
        // ▶ = nested false (collapsed), ▼ = nested true (expanded)
        // Clicking toggles nested state. Sits to the left of the whole stacked
        // (name + address) block, vertically centered via the tree-cell flex.
        // Self/native rows render a non-interactive disabled chevron (forced
        // down state) so the column stays visually consistent with other rows.
        const canExpand = !n.is_self && !n.native && !n.incompatible && !n.conflict;
        const chevronSvg = (rotated: boolean) => (
          <svg
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth={2.5}
            strokeLinecap="round"
            strokeLinejoin="round"
            style={{ transform: rotated ? 'rotate(90deg)' : 'rotate(0deg)' }}
          >
            <path d="M9 6l6 6-6 6" />
          </svg>
        );
        const triangleSlot = canExpand ? (
          <button
            type="button"
            className="hy-tree-toggle"
            onClick={(e) => { e.preventDefault(); e.stopPropagation(); handleNestedToggle(n, meta.nodeKey); }}
            aria-label={n.nested ? 'Collapse' : 'Expand'}
          >
            {chevronSvg(!!n.nested)}
          </button>
        ) : (
          <span className="hy-tree-toggle hy-tree-toggle-disabled" aria-hidden="true">
            {chevronSvg(true)}
          </span>
        );

        return (
          <TreeCell meta={meta}>
            {triangleSlot}
            <span className="sub-name-wrap">
              {nameEl}
              {versionBadge}
              {nativeBadge && <> {nativeBadge}</>}
              {n.via ? (
                <span className="peer-addr-sub">via {n.via}</span>
              ) : n.addr ? (
                <span className="peer-addr-sub">
                  {n.addr}
                  {n.ip_statuses && n.ip_statuses.length > 0 && (
                    <> <Tooltip content={
                      <div>
                        {n.ip_statuses.map((s, i) => {
                          const isGood = s.latency_ms && s.latency_ms > 0;
                          const isBad = s.latency_ms === -1;
                          const lat = isGood ? `${s.latency_ms}ms` : isBad ? 'timeout' : s.status;
                          const color = isGood ? 'var(--green)' : isBad ? 'var(--red)' : 'var(--text-muted)';
                          return (
                            <div key={i} style={{ fontSize: 12 }}>
                              {s.addr} — <span style={{ color, fontWeight: 600 }}>{lat}</span>
                            </div>
                          );
                        })}
                      </div>
                    }>
                      <Badge variant="muted">+{n.ip_statuses.length - 1}</Badge>
                    </Tooltip></>
                  )}
                </span>
              ) : null}
            </span>
          </TreeCell>
        );
      },
    },
    {
      key: 'traffic', title: t('nodes.traffic'), width: '160px', className: 'col-traffic',
      render: (n) => (
        <>
          <span className="stat-up">{fmtRate(n.tx_rate || 0)}</span>
          {' '}<span style={{ color: 'var(--text-muted)' }}>/</span>{' '}
          <span className="stat-down">{fmtRate(n.rx_rate || 0)}</span>
        </>
      ),
    },
    // Per-row edit column intentionally omitted — selecting a row and
    // pressing the top-right Edit button is the unified entry point.
  ];

  const treeNodes = buildTreeNodes(topology);

  // Collect selectable keys. Self is now selectable too — selecting the
  // self row is the unified way to reach EditSelfModal via the top-right
  // Edit button. Bulk actions still skip self where they don't apply
  // (no nested toggle, no enable/disable, no delete on self).
  const selectableKeys: string[] = [];
  const collectKeys = (nodes: TreeNode<TopologyNode>[]) => {
    for (const n of nodes) {
      selectableKeys.push(n.key);
      if (n.children) collectKeys(n.children);
    }
  };
  collectKeys(treeNodes);
  const selection = useSelection(selectableKeys);

  // Clicking anywhere outside the list region clears a single-row
  // selection (multi-select stays sticky). The boundary is the Card —
  // graph view's blank-canvas clear is owned by the graph itself, so we
  // mustn't double-trigger from inside it.
  const listScopeRef = useRef<HTMLDivElement | null>(null);
  useDeselectOnBlankClick(selection, listScopeRef);

  // Scroll-to-selection on graph→list view switch ONLY. Triggers when
  // the user has just flipped the view to list AND a single row is
  // selected (typically chosen on the graph just before). We don't
  // want this to fire on every in-list selection change — clicking a
  // row in list view should select it where the user clicked, not yank
  // the table to recenter on it. So `selection` is read inside the
  // effect but is NOT in the dependency list; the only trigger is the
  // viewMode transition. A ref tracks the previous render's viewMode
  // so we can distinguish a fresh graph→list flip from any later
  // re-render while in list view.
  const prevViewMode = useRef(viewMode);
  useEffect(() => {
    const wasGraph = prevViewMode.current === 'graph';
    prevViewMode.current = viewMode;
    if (!wasGraph || viewMode !== 'list') return;
    if (selection.count !== 1) return;
    const target = Array.from(selection.selected)[0];
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        const row = document.querySelector(`tr[data-row-key="${CSS.escape(target)}"]`);
        if (row) row.scrollIntoView({ behavior: 'smooth', block: 'center' });
      });
    });
    // selection intentionally omitted from deps — see comment above.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [viewMode]);

  const bulkToggleNodes = useCallback(async (disabled: boolean) => {
    try {
      const keys = [...selection.selected];
      await Promise.all(keys.map((key) => {
        if (key.includes('/')) {
          // Sub-row: use setPeerDisabled (qualified path) — keeps connection but blocks routing
          return api.setPeerDisabled(key, disabled);
        }
        // Root-level outbound: use disableClient (stops connection)
        return api.disableClient(key, disabled);
      }));
      toast.success(`${!disabled ? t('app.bulkEnable') : t('app.bulkDisable')}: ${keys.length}`);
      queryClient.invalidateQueries({ queryKey: ['topology'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [selection, queryClient, toast, t]);

  const bulkNested = useCallback(async (nested: boolean) => {
    try {
      await Promise.all([...selection.selected].map((key) => api.setNested(key, nested)));
      toast.success(`${nested ? t('nodes.bulkEnableNested') : t('nodes.bulkDisableNested')}: ${selection.count}`);
      queryClient.invalidateQueries({ queryKey: ['topology'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [selection, queryClient, toast, t]);

  const bulkDeleteNodes = useCallback(async () => {
    const ok = await confirm({
      title: t('app.bulkDelete'), message: t('nodes.deleteConfirm', { name: `${selection.count} nodes` }),
      danger: true, confirmText: t('app.delete'), cancelText: t('app.cancel'),
    });
    if (!ok) return;
    try {
      const rootKeys = [...selection.selected].filter((key) => !key.includes('/'));
      await Promise.all(rootKeys.map((name) => api.deleteClient(name)));
      toast.success(`${t('app.bulkDelete')}: ${selection.count}`);
      queryClient.invalidateQueries({ queryKey: ['topology'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [selection, confirm, queryClient, toast, t]);

  return (
    <div className="hy-page" ref={pageRef}>
      {showStats && (
      <div ref={statsRef}>
      <StatsGrid
        items={[
          {
            label: t('stats.totalTraffic'),
            value: stats ? (
              <><span className="stat-up">{fmtBytes(stats.tx_bytes)}</span> <span className="sep">/</span> <span className="stat-down">{fmtBytes(stats.rx_bytes)}</span></>
            ) : '—',
            sub: t('stats.uploadDownload'),
            history: { up: txBytesHist, down: rxBytesHist },
            formatPeak: (m) => 'max ' + fmtBytes(m),
          },
          {
            label: t('stats.realtimeSpeed'),
            value: stats ? (
              <><span className="stat-up">{fmtRate(stats.tx_rate)}</span> <span className="sep">/</span> <span className="stat-down">{fmtRate(stats.rx_rate)}</span></>
            ) : '—',
            sub: t('stats.uploadDownload'),
            history: { up: txRateHist, down: rxRateHist },
            formatPeak: (m) => 'max ' + fmtRate(m),
          },
          {
            label: t('stats.connections'),
            value: stats?.conns ?? '—',
            sub: t('stats.activeStreams'),
            history: connsHist,
            sparkColor: 'var(--primary)',
            formatPeak: (m) => 'max ' + Math.round(m),
          },
          {
            label: t('stats.exitClients'),
            value: stats?.exit_clients ?? '—',
            sub: t('stats.usingAsExit'),
            history: exitClientsHist,
            sparkColor: 'var(--primary)',
            formatPeak: (m) => 'max ' + Math.round(m),
          },
        ]}
      />
      </div>
      )}

      <Card
        fill={1}
        title={
          <>
            {t('nodes.title')}
            <IconTabs
              className="hy-card-title-tabs"
              items={[
                {
                  key: 'list',
                  tooltip: t('nodes.view.list'),
                  icon: (
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                      <line x1="8" y1="6" x2="21" y2="6" />
                      <line x1="8" y1="12" x2="21" y2="12" />
                      <line x1="8" y1="18" x2="21" y2="18" />
                      <circle cx="4" cy="6" r="1.2" fill="currentColor" />
                      <circle cx="4" cy="12" r="1.2" fill="currentColor" />
                      <circle cx="4" cy="18" r="1.2" fill="currentColor" />
                    </svg>
                  ),
                },
                {
                  key: 'graph',
                  tooltip: t('nodes.view.graph'),
                  icon: (
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                      <circle cx="6" cy="6" r="2.4" />
                      <circle cx="18" cy="6" r="2.4" />
                      <circle cx="12" cy="18" r="2.4" />
                      <line x1="7.7" y1="7.2" x2="10.7" y2="16.2" />
                      <line x1="16.3" y1="7.2" x2="13.3" y2="16.2" />
                    </svg>
                  ),
                },
              ]}
              activeKey={viewMode}
              onChange={(k) => setViewMode(k as 'list' | 'graph')}
            />
          </>
        }
        count={topology.length}
        actions={
          <ResponsiveActions
            selectedCount={selection.count}
            onClearSelection={selection.clear}
            selectedLabel={t('app.selected', { count: selection.count })}
          >
            {(() => {
              // Inspect selected nodes to decide which buttons to show.
              const sel = [...selection.selected];
              const findData = (key: string): TopologyNode | undefined => {
                const search = (nodes: TreeNode<TopologyNode>[]): TopologyNode | undefined => {
                  for (const n of nodes) {
                    if (n.key === key) return n.data;
                    if (n.children) { const r = search(n.children); if (r) return r; }
                  }
                  return undefined;
                };
                return search(treeNodes);
              };
              const items = sel.map(findData).filter(Boolean) as TopologyNode[];
              const onlySelf = sel.length === 1 && items.some((n) => n.is_self);
              // Self is preserved for the enable/disable affordance per UX
              // spec, but excluded from nested-toggle and bulk-delete (no
              // path-keyed Peers entry exists for self, and self can't be
              // removed from its own config).
              const peerItems = items.filter((n) => !n.is_self);
              const peerKeys = sel.filter((k) => k !== '__self__');
              const rootKeys = peerKeys.filter((k) => !k.includes('/'));
              const hasDisabled = items.some((n) => n.disabled);
              const hasEnabled = items.some((n) => !n.disabled);
              const hasNested = peerItems.some((n) => n.nested);
              const hasUnnested = peerItems.some((n) => !n.nested);
              const hasRoot = rootKeys.length > 0;
              return <>
                {hasDisabled && <Button size="sm" onClick={() => bulkToggleNodes(false)}>{t('app.bulkEnable')}</Button>}
                {hasEnabled && <Button size="sm" onClick={() => bulkToggleNodes(true)}>{t('app.bulkDisable')}</Button>}
                {!onlySelf && hasUnnested && <Button size="sm" onClick={() => bulkNested(true)}>{t('nodes.bulkEnableNested')}</Button>}
                {!onlySelf && hasNested && <Button size="sm" onClick={() => bulkNested(false)}>{t('nodes.bulkDisableNested')}</Button>}
                {hasRoot && <Button size="sm" variant="danger" onClick={bulkDeleteNodes}>{t('app.bulkDelete')}</Button>}
              </>;
            })()}
            <ImportExportButton target="nodes" />
            {(() => {
              // Edit is a single-select shortcut placed deliberately between
              // Import (group of catalog actions) and Add Node (the primary
              // creation action), so it reads as "edit the one I just picked"
              // and not as part of the multi-select bulk group on the left.
              // Hidden when more than one row is selected, when nothing is
              // selected, or when the single selection is a nested sub-peer
              // (whose config lives on the parent and isn't editable here).
              const sel = [...selection.selected];
              if (sel.length !== 1) return null;
              const key = sel[0];
              if (key === '__self__') {
                return (
                  <Button
                    size="sm"
                    variant="success"
                    data-testid="edit-selected-btn"
                    onClick={openEditSelfFromBar}
                  >{t('app.edit')}</Button>
                );
              }
              const selfId = node?.node_id || '';
              const stripped = selfId && key.startsWith(selfId + '/') ? key.slice(selfId.length + 1) : key;
              if (stripped.includes('/')) return null;
              const findData = (k: string): TopologyNode | undefined => {
                const search = (nodes: TreeNode<TopologyNode>[]): TopologyNode | undefined => {
                  for (const n of nodes) {
                    if (n.key === k) return n.data;
                    if (n.children) { const r = search(n.children); if (r) return r; }
                  }
                  return undefined;
                };
                return search(treeNodes);
              };
              const data = findData(key) || findData(stripped);
              if (!data) return null;
              // An inbound first-hop peer is one that dialed us — its
              // configuration lives on the initiator, not on us, so the
              // Edit modal would have nothing to load and any Save would
              // either fail or silently overwrite a non-existent client
              // entry on our side. Suppress the button entirely; bulk
              // enable/disable on the row still work for the cases (nested
              // toggle, etc) that ARE locally adjustable.
              if (data.direction === 'inbound') return null;
              return (
                <Button
                  size="sm"
                  variant="success"
                  data-testid="edit-selected-btn"
                  onClick={() => openEditFromBar(stripped, data)}
                >{t('app.edit')}</Button>
              );
            })()}
            <Button
              size="sm"
              variant="primary"
              title={t('nodes.addNodeHint')}
              data-testid="add-node-btn"
              onMouseDown={(e) => { lastPointer.current = { x: e.clientX, y: e.clientY }; addNodeHandlers.onMouseDown(e); }}
              onMouseUp={addNodeHandlers.onMouseUp}
              onMouseLeave={addNodeHandlers.onMouseLeave}
              onTouchStart={(e) => {
                const t0 = e.touches[0];
                if (t0) lastPointer.current = { x: t0.clientX, y: t0.clientY };
                addNodeHandlers.onTouchStart(e);
              }}
              onTouchEnd={addNodeHandlers.onTouchEnd}
              onTouchCancel={addNodeHandlers.onTouchCancel}
              onContextMenu={addNodeHandlers.onContextMenu}
              onClick={addNodeHandlers.onClick}
            >
              {t('nodes.addNode')}
            </Button>
          </ResponsiveActions>
        }
        noPadding
      >
        <div ref={listScopeRef} style={{ display: 'contents' }}>
        {viewMode === 'graph' ? (
          <NodesGraphView
            topology={topology}
            selfId={node?.node_id || ''}
            selfName={node?.name}
            selectedQPath={selection.count === 1 ? Array.from(selection.selected)[0] : null}
            onSelectQPath={(qpath) => {
              // Mirror list-view single-select semantics: clicking a node
              // replaces the current selection so the same bulk-action bar
              // responds in either view.
              selection.clear();
              if (qpath) selection.toggle(qpath);
            }}
            onOpenRemote={(qpath) => {
              const basePath = getBasePath();
              const selfId = node?.node_id || '';
              const chain = selfId && qpath.startsWith(selfId + '/') ? qpath.slice(selfId.length + 1) : qpath;
              window.open(`${basePath}/remote/${chain}/scale/`, '_blank', 'noopener');
            }}
          />
        ) : (
          <TreeTable
            columns={columns}
            nodes={treeNodes}
            emptyText={t('nodes.noConnections')}
            selection={selection}
            isSelectable={() => true}
          />
        )}
        </div>
      </Card>

      <NodeModal
        open={modalOpen}
        onClose={() => setModalOpen(false)}
        editingName={editingName}
        animateFrom={clickPos}
      />

      <NodeImportModal
        open={importOpen}
        onClose={() => setImportOpen(false)}
        animateFrom={clickPos}
      />

      <EditSelfModal
        open={editSelfOpen}
        onClose={() => setEditSelfOpen(false)}
        animateFrom={clickPos}
      />
    </div>
  );
}
