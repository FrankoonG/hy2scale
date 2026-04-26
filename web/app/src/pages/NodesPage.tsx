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
import EditSelfModal from '@/components/EditSelfModal';
import ResponsiveActions from '@/components/ResponsiveActions';
import ImportExportButton from '@/components/ImportExportButton';
import NodesGraphView from '@/components/NodesGraphView';

export default function NodesPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const queryClient = useQueryClient();
  const { node, topology, setTopology, syncingNodes, setSyncing, clearSyncing } = useNodeStore();

  const [modalOpen, setModalOpen] = useState(false);
  const [editSelfOpen, setEditSelfOpen] = useState(false);
  const [editingName, setEditingName] = useState<string | null>(null);
  const [clickPos, setClickPos] = useState<{ x: number; y: number } | undefined>();
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

  const openAdd = (e: MouseEvent) => {
    setClickPos({ x: e.clientX, y: e.clientY });
    setEditingName(null);
    setModalOpen(true);
  };

  const openEdit = (name: string, e: MouseEvent) => {
    setClickPos({ x: e.clientX, y: e.clientY });
    setEditingName(name);
    setModalOpen(true);
  };

  const openEditSelf = (e: MouseEvent) => {
    setClickPos({ x: e.clientX, y: e.clientY });
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
    {
      key: 'actions', title: '', width: '40px',
      render: (n, meta) => {
        if (n.is_self) {
          return <button className="hy-row-edit" onClick={(e) => openEditSelf(e as any)} title={t('app.edit')}><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7"/><path d="M18.5 2.5a2.12 2.12 0 013 3L12 15l-4 1 1-4 9.5-9.5z"/></svg></button>;
        }
        if (meta.depth === 0) {
          // Root-level outbound clients (including native hy2 servers) are
          // user-configured entries, so they get the edit button. Depth>0
          // nested peers are discovered, not configured, and stay read-only.
          return <button className="hy-row-edit" onClick={(e) => openEdit(n.name, e as any)} title={t('app.edit')}><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7"/><path d="M18.5 2.5a2.12 2.12 0 013 3L12 15l-4 1 1-4 9.5-9.5z"/></svg></button>;
        }
        return null;
      },
    },
  ];

  const treeNodes = buildTreeNodes(topology);

  // Collect selectable keys. Exclude only the local self row; native hy2
  // servers ARE user-configured outbound clients and deserve the checkbox.
  const selectableKeys: string[] = [];
  const collectKeys = (nodes: TreeNode<TopologyNode>[]) => {
    for (const n of nodes) {
      if (!n.data.is_self) selectableKeys.push(n.key);
      if (n.children) collectKeys(n.children);
    }
  };
  collectKeys(treeNodes);
  const selection = useSelection(selectableKeys);

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
              // Inspect selected nodes to decide which bulk buttons to show.
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
              const rootKeys = sel.filter((k) => !k.includes('/'));
              const hasDisabled = items.some((n) => n.disabled);
              const hasEnabled = items.some((n) => !n.disabled);
              const hasNested = items.some((n) => n.nested);
              const hasUnnested = items.some((n) => !n.nested);
              const hasRoot = rootKeys.length > 0;
              return <>
                {hasDisabled && <Button size="sm" onClick={() => bulkToggleNodes(false)}>{t('app.bulkEnable')}</Button>}
                {hasEnabled && <Button size="sm" onClick={() => bulkToggleNodes(true)}>{t('app.bulkDisable')}</Button>}
                {hasUnnested && <Button size="sm" onClick={() => bulkNested(true)}>{t('nodes.bulkEnableNested')}</Button>}
                {hasNested && <Button size="sm" onClick={() => bulkNested(false)}>{t('nodes.bulkDisableNested')}</Button>}
                {hasRoot && <Button size="sm" variant="danger" onClick={bulkDeleteNodes}>{t('app.bulkDelete')}</Button>}
              </>;
            })()}
            <ImportExportButton target="nodes" />
            <Button size="sm" variant="primary" onClick={openAdd}>{t('nodes.addNode')}</Button>
          </ResponsiveActions>
        }
        noPadding
      >
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
            onEditNode={(key, clickPos) => {
              // Graph emits '__self__' for the local node, bare peer name
              // otherwise — same convention the TreeTable row uses. Coords
              // come from the overlay's edit button so the modal animation
              // anchors at the button, not at screen center.
              const ev = { clientX: clickPos.x, clientY: clickPos.y } as MouseEvent;
              if (key === '__self__') openEditSelf(ev);
              else openEdit(key, ev);
            }}
          />
        ) : (
          <TreeTable
            columns={columns}
            nodes={treeNodes}
            emptyText={t('nodes.noConnections')}
            selection={selection}
            isSelectable={(node) => !node.data.is_self}
          />
        )}
      </Card>

      <NodeModal
        open={modalOpen}
        onClose={() => setModalOpen(false)}
        editingName={editingName}
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
