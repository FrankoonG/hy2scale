import { useState, useCallback, useEffect, useRef, type ReactNode } from 'react';
import { useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Card, Button, Badge, Tooltip, IconChevronRight, ExpandButton, IconLink,
  TreeTable, TreeCell, useToast, useConfirm, useSelection,
  type TreeNode, type TreeColumn,
} from '@hy2scale/ui';
import type { TopologyNode } from '@/api';
import * as api from '@/api';
import { useNodeStore } from '@/store/node';
import { SELF_KEY } from '@/utils/nodeKeys';
import { usePageView } from '@/store/pageView';
import { fmtRate } from '@/hooks/useFormat';
import NodeModal from '@/components/NodeModal';
import NodeImportModal from '@/components/NodeImportModal';
import EditSelfModal from '@/components/EditSelfModal';
import RemoteModal from '@/components/RemoteModal';
import NodeDetailModal from '@/components/NodeDetailModal';
import ResponsiveActions from '@/components/ResponsiveActions';
import { useImportExport } from '@/components/ImportExport';
import NodesGraphView from '@/components/NodesGraphView';
import { useDeselectOnBlankClick } from '@/hooks/useDeselectOnBlankClick';
import { versionAtLeast } from '@/utils/version';
import { latencyClass } from '@/utils/latency';
import { fmtPunchAddr, punchesFor } from '@/utils/punch';
import VersionBadge from '@/components/VersionBadge';
import { boldMarkup } from '@/utils/richText';
import { PublicIP } from '@/components/CountryFlag';

export default function NodesPage() {
  const { t } = useTranslation();
  const ie = useImportExport('nodes');
  // Hole-punched routes this node works on (state per route address)
  const { data: punchStatus = [] } = useQuery({ queryKey: ['punchStatus'], queryFn: api.getPunchStatus, refetchInterval: 5000 });
  const fmtAddr = (a: string) => fmtPunchAddr(t, a);
  const punchBadge = (name: string) => {
    const sts = punchesFor(punchStatus, name);
    if (!sts.length) return null;
    const best = sts.find((p) => p.state === 'connected') || sts.find((p) => p.state === 'punching') || sts[0];
    const variant = best.state === 'connected' ? 'blue' : best.state === 'failed' ? 'red' : 'muted';
    const tip = sts.map((p) => `${fmtAddr(p.addr)}: ${t(`punch.state.${p.state}`)}${p.reason ? ` (${t(`punch.reason.${p.reason}`, { defaultValue: p.reason })})` : ''}${p.mapping ? ` → ${p.mapping}` : ''}`).join('\n');
    return <span title={tip} data-testid="punch-badge" data-state={best.state} data-reason={best.reason || ''}> <Badge variant={variant as any}>{t(`punch.state.${best.state}`)}</Badge></span>;
  };
  const toast = useToast();
  const confirm = useConfirm();
  const queryClient = useQueryClient();
  const { node, topology, setTopology, syncingNodes, setSyncing, clearSyncing } = useNodeStore();

  const [modalOpen, setModalOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [editSelfOpen, setEditSelfOpen] = useState(false);
  const [editingName, setEditingName] = useState<string | null>(null);
  // Add Node opened from the Punch button: the node and its mesh path.
  const [punchFor, setPunchFor] = useState<{ name: string; signal: string } | null>(null);
  const [clickPos, setClickPos] = useState<{ x: number; y: number } | undefined>();
  // Remote-connect modal: confirm + (auto / manual) login happens HERE on
  // the local page, then a fresh tab is opened with a token already in
  // sessionStorage so the remote SPA never has to render its own LoginPage.
  const [remoteConnect, setRemoteConnect] = useState<{ chain: string[]; label: string } | null>(null);
  // Node details dialog (row click in the list, overlay button in the graph).
  // The key outlives `open` so the dialog keeps its content while closing.
  const [detailKey, setDetailKey] = useState<string | null>(null);
  const [detailOpen, setDetailOpen] = useState(false);
  const openDetails = (key: string, anchor?: { x: number; y: number }) => {
    setClickPos(anchor);
    setDetailKey(key);
    setDetailOpen(true);
  };
  // List / topology view. The switch is the top bar title (MainLayout);
  // the store persists the pick.
  const [view] = usePageView('nodes');
  // The list-view scroll-to-selection effect is defined AFTER `selection`
  // is set up further down. JS hoists `function` declarations and `const`
  // bindings differently; this useEffect references `selection.*` so it
  // can't sit above the `useSelection(...)` call.

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

  // Add Node: the button opens the manual form; its secondary action
  // imports a hysteria2:// link. Both animate from the click.
  const at = (e: { clientX: number; clientY: number }) => ({ x: e.clientX, y: e.clientY });
  const openAddNode = (e: { clientX: number; clientY: number }) => {
    setClickPos(at(e));
    setEditingName(null);
    setPunchFor(null);
    setModalOpen(true);
  };
  const openImportLink = (e: { clientX: number; clientY: number }) => {
    setClickPos(at(e));
    setImportOpen(true);
  };

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
    setPunchFor(null);
    setModalOpen(true);
  };
  const openEditSelfFromBar = () => {
    setClickPos(editButtonAnchor());
    setEditSelfOpen(true);
  };

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
        key: n.is_self ? SELF_KEY : n.name,
        data: n,
        expanded: true,
        // This node stays on top whatever the sort.
        pinned: n.is_self,
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
    return <span className={`latency ${latencyClass(n.latency_ms)}`}>{n.latency_ms}ms</span>;
  };

  const dirBadge = (n: TopologyNode) => {
    if (n.is_self) return <Badge variant="blue">{t('nodes.dirLocal')}</Badge>;
    if (n.direction === 'inbound') return <Badge variant="muted">{t('nodes.dirIn')}</Badge>;
    if (n.direction === 'outbound') return <Badge variant="muted">{t('nodes.dirOut')}</Badge>;
    return null;
  };

  const columns: TreeColumn<TopologyNode>[] = [
    {
      key: 'status', title: t('nodes.status'), width: '75px', className: 'col-status',
      // Latency in ms; self first, unreachable / unmeasured rows last.
      sortValue: (n) => {
        if (n.is_self) return 0;
        if (n.incompatible || n.conflict || n.unsupported || n.disabled || n.connected === false) return null;
        return n.latency_ms > 0 ? n.latency_ms : null;
      },
      render: (n, meta) => {
        return latencyCell(n, meta.nodeKey);
      },
    },
    {
      key: 'dir', title: t('nodes.dir'), width: '55px', className: 'col-dir',
      sortValue: (n) => n.is_self ? 'local' : n.direction || null,
      render: (n) => dirBadge(n),
    },
    {
      key: 'node', title: t('nodes.node'), className: 'col-name',
      sortValue: (n) => n.name || (n.is_self ? node?.name : '') || null,
      render: (n, meta) => {
        // Name opens the edit dialog for rows configured here: this node
        // and its top-level outbound peers. Inbound and nested rows have
        // no local config — their name is plain text, and a click falls
        // through to the row (details dialog). Remote connect lives in the
        // details dialog.
        const editable = meta.depth === 0 && (n.is_self || n.direction !== 'inbound');
        const label = n.is_self ? (n.name || node?.name || 'self') : n.name;
        const nameEl = editable ? (
          <a
            className="peer-link peer-name-cell"
            href="#"
            style={n.is_self ? { color: 'var(--primary)' } : undefined}
            data-testid="node-name-edit"
            onClick={(e) => {
              e.preventDefault();
              const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
              setClickPos({ x: r.left + r.width / 2, y: r.top + r.height / 2 });
              if (n.is_self) {
                setEditSelfOpen(true);
              } else {
                setEditingName(n.name);
                setPunchFor(null);
                setModalOpen(true);
              }
            }}
          >
            {label}
          </a>
        ) : (
          <span className="peer-name-cell">{label}</span>
        );

        const lossMin = node?.lossless_min_version;
        const outdated = !!n.version && !!lossMin && !n.incompatible && !versionAtLeast(n.version, lossMin);
        const versionBadge = n.version && n.version !== node?.version ? (
          <VersionBadge peer version={n.version} compat={n.compat} incompatible={n.incompatible} outdated={outdated}
            className="version-after-name" title={outdated ? t('nodes.updateForLossless', { v: lossMin }) : undefined} />
        ) : null;

        const nativeBadge = n.native ? <Badge variant="muted">{t('nodes.native')}</Badge> : null;
        const invisibleBadge = n.invisible
          ? <span title={t('nodes.invisibleTitle')} data-testid="invisible-badge"><Badge variant="muted">{t('nodes.invisible')}</Badge></span>
          : null;
        const restrictedBadge = n.restricted
          ? <span title={t('nodes.restrictedTitle')} data-testid="restricted-badge"><Badge variant="muted" className="badge-restricted">{t('nodes.restricted')}</Badge></span>
          : null;

        // Tree expand/collapse triangle: shown for non-self, non-native nodes
        // ▶ = nested false (collapsed), ▼ = nested true (expanded)
        // Clicking toggles nested state. Sits to the left of the whole stacked
        // (name + address) block, vertically centered via the tree-cell flex.
        // Self/native rows render a non-interactive disabled chevron (forced
        // down state) so the column stays visually consistent with other rows.
        const canExpand = !n.is_self && !n.native && !n.incompatible && !n.conflict;
        const chevron = (rotated: boolean) => (
          <IconChevronRight size={18} strokeWidth={2.5} className={rotated ? 'rot-90' : undefined} />
        );
        const triangleSlot = canExpand ? (
          <button
            type="button"
            className="hy-tree-toggle"
            onClick={(e) => { e.preventDefault(); e.stopPropagation(); handleNestedToggle(n, meta.nodeKey); }}
            aria-label={n.nested ? t('app.collapse') : t('app.expand')}
          >
            {chevron(!!n.nested)}
          </button>
        ) : (
          <span className="hy-tree-toggle hy-tree-toggle-disabled" aria-hidden="true">
            {chevron(true)}
          </span>
        );

        return (
          <TreeCell meta={meta}>
            {triangleSlot}
            <span className="sub-name-wrap">
              {nameEl}
              {versionBadge}
              {nativeBadge && <> {nativeBadge}</>}
              {invisibleBadge && <> {invisibleBadge}</>}
              {restrictedBadge && <> {restrictedBadge}</>}
              {n.via ? (
                <span className="peer-addr-sub">{t('nodes.via', { name: n.via })}</span>
              ) : n.addr ? (
                <span className="peer-addr-sub">
                  {fmtAddr(n.addr)}
                  {punchBadge(n.name)}
                  {n.ip_statuses && n.ip_statuses.length > 0 && (
                    <> <Tooltip content={
                      <div>
                        {n.ip_statuses.map((s, i) => {
                          const isGood = s.latency_ms && s.latency_ms > 0;
                          const isBad = s.latency_ms === -1;
                          const lat = isGood ? `${s.latency_ms}ms` : isBad ? t('nodes.timeout') : s.status;
                          return (
                            <div key={i} className="cell-small">
                              {fmtAddr(s.addr)} — <span className={`addr-lat ${isGood ? 'good' : isBad ? 'bad' : ''}`}>{lat}</span>
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
      // Selected direct egress of the node (IPv4 line, IPv6 line).
      key: 'egress', title: t('nodes.egressIp'), className: 'col-egress-ip',
      sortValue: (n) => {
        const e = n.egress;
        return (e?.ipv4?.ok && e.ipv4.ip) || (e?.ipv6?.ok && e.ipv6.ip) || null;
      },
      render: (n) => {
        const e = n.egress;
        if (e == null) return <span className="iface-muted" data-testid="egress-unknown">{t('nodes.egressUnknown')}</span>;
        // Only the families the node provides; "—" when it provides neither.
        const line = (f: api.NodeEgressFamily | null, fam: string) => f == null ? null : (
          <div className="iface-line" data-fam={fam} key={fam}>
            {!f.ok ? <span className="iface-bad">{t('iface.unavailable')}</span> : f.ip ? <PublicIP ip={f.ip} /> : <span>—</span>}
            {n.egress_probed && f.ok && <span className="iface-tag faint" data-testid="egress-probed">{t('nodes.egressProbed')}</span>}
          </div>
        );
        const lines = [line(e.ipv4, 'ipv4'), line(e.ipv6, 'ipv6')].filter(Boolean);
        return (
          <div className="iface-lines" data-testid="egress-ip">
            {lines.length ? lines : <div className="iface-line"><span className="iface-muted">—</span></div>}
          </div>
        );
      },
    },
    {
      key: 'traffic', title: t('nodes.traffic'), width: '160px', className: 'col-traffic',
      sortValue: (n) => (n.tx_rate || 0) + (n.rx_rate || 0),
      render: (n) => (
        <>
          <span className="stat-up">{fmtRate(n.tx_rate || 0)}</span>
          {' '}<span className="text-muted">/</span>{' '}
          <span className="stat-down">{fmtRate(n.rx_rate || 0)}</span>
        </>
      ),
    },
    // Per-row edit column intentionally omitted — selecting a row and
    // pressing the top-right Edit button is the unified entry point.
  ];

  const treeNodes = buildTreeNodes(topology);
  // Row data by tree key (keys are unique across the tree).
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

  // Opened with { select: key } (the top bar node badge): select that row
  // alone once it exists.
  const location = useLocation();
  const pendingSelect = useRef<string | null>(null);
  useEffect(() => { pendingSelect.current = (location.state as { select?: string } | null)?.select ?? null; }, [location.key]);
  useEffect(() => {
    const k = pendingSelect.current;
    if (!k || !selectableKeys.includes(k)) return;
    pendingSelect.current = null;
    if (!(selection.count === 1 && selection.selected.has(k))) selection.selectOnly(k);
  });

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
  // view transition. A ref tracks the previous render's view so we can
  // distinguish a fresh topo→list flip from any later re-render while
  // in list view.
  const prevView = useRef(view);
  useEffect(() => {
    const wasTopo = prevView.current === 'topo';
    prevView.current = view;
    if (!wasTopo || view !== 'list') return;
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
  }, [view]);

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
      title: t('app.bulkDelete'), message: t('app.bulkDeleteConfirm', { count: selection.count }),
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

  // Card header actions in list view; floated at the top-right of the
  // graph in topology view.
  // `more`: the topology canvas's own secondary controls (⋯ menu).
  const actionsFor = (more?: ReactNode) => (
    <ResponsiveActions
      selectedCount={selection.count}
      onClearSelection={selection.clear}
      more={more || undefined}
    >
      {(() => {
        // Inspect selected nodes to decide which buttons to show.
        const sel = [...selection.selected];
        const items = sel.map(findData).filter(Boolean) as TopologyNode[];
        const onlySelf = sel.length === 1 && items.some((n) => n.is_self);
        // Self is preserved for the enable/disable affordance per UX
        // spec, but excluded from nested-toggle and bulk-delete (no
        // path-keyed Peers entry exists for self, and self can't be
        // removed from its own config).
        const peerItems = items.filter((n) => !n.is_self);
        const peerKeys = sel.filter((k) => k !== SELF_KEY);
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
        if (key === SELF_KEY) {
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
        if (stripped.includes('/')) {
          // A node reached through the mesh, not yet a direct peer: offer
          // a hole-punched entry signalled along this path.
          const target = stripped.split('/').pop() || '';
          const nd = findData(key) || findData(stripped);
          const selfNode = topology.find((t) => t.is_self);
          const direct = topology.some((t) => !t.is_self && t.name === target)
            || !!selfNode?.children?.some((c) => c.name === target);
          if (!target || direct || target === node?.name || nd?.native) return null;
          return (
            <Button
              size="sm"
              data-testid="punch-selected-btn"
              onClick={(e) => {
                const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
                setClickPos({ x: r.left + r.width / 2, y: r.top + r.height / 2 });
                setEditingName(null);
                setPunchFor({ name: target, signal: stripped });
                setModalOpen(true);
              }}
            >{t('nodes.punchNode')}</Button>
          );
        }
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
      <ExpandButton
        variant="primary"
        label={t('nodes.addNode')}
        testId="add-node-btn"
        moreLabel={t('app.moreActions')}
        onClick={openAddNode}
        actions={[
          { key: 'link', label: t('nodes.importTitle'), icon: IconLink, onClick: openImportLink, testId: 'add-node-link' },
          ...ie.actions,
        ]}
      />
    </ResponsiveActions>
  );
  const actionsEl = actionsFor();

  const graphEl = (
    <NodesGraphView
      full
      actions={actionsFor}
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
      // In proxy mode (already viewing a remote node's UI), no further
      // remote hop: undefined renders the overlay hops as plain text.
      onOpenRemote={(window as any).__PROXY__ ? undefined : (qpath, anchor) => {
        const selfId = node?.node_id || '';
        const chainStr = selfId && qpath.startsWith(selfId + '/') ? qpath.slice(selfId.length + 1) : qpath;
        const chain = chainStr.split('/').filter(Boolean);
        if (chain.length === 0) return;
        if (anchor) setClickPos(anchor);
        setRemoteConnect({ chain, label: chain.join(' / ') });
      }}
      onOpenDetails={openDetails}
    />
  );

  // Bounds "inside the list" for useDeselectOnBlankClick: the table body in
  // list view, the whole canvas (with its overlays) in topology view.
  const scope = (children: ReactNode) => (
    <div
      ref={listScopeRef}
      style={{ display: 'contents' }}
    >
      {children}
    </div>
  );

  return (
    <div className={view === 'topo' ? 'hy-topo-full' : 'hy-page'}>
      {view === 'topo' ? scope(graphEl) : (
        <Card
          fill={1}
          title={t('nodes.title')}
          count={topology.length}
          actions={actionsEl}
          noPadding
        >
          {scope(
            <TreeTable
              id="nodes"
              columns={columns}
              nodes={treeNodes}
              emptyText={boldMarkup(t('nodes.noConnections'))}
              selection={selection}
              isSelectable={() => true}
              paginate={false}
              onRowClick={(tn, _m, origin) => openDetails(tn.key, origin)}
            />,
          )}
        </Card>
      )}

      <NodeModal
        open={modalOpen}
        onClose={() => setModalOpen(false)}
        editingName={editingName}
        animateFrom={clickPos}
        punchFor={punchFor}
      />

      <NodeImportModal
        open={importOpen}
        onClose={() => setImportOpen(false)}
        animateFrom={clickPos}
      />
      {ie.modal}

      <EditSelfModal
        open={editSelfOpen}
        onClose={() => setEditSelfOpen(false)}
        animateFrom={clickPos}
      />

      <NodeDetailModal
        open={detailOpen}
        onClose={() => setDetailOpen(false)}
        qpath={detailKey}
        animateFrom={clickPos}
        renderStatus={latencyCell}
        // Already viewing a remote node through the local proxy: no
        // further remote hop from here.
        onRemoteConnect={(window as any).__PROXY__ ? undefined : (chain, anchor) => {
          setDetailOpen(false);
          if (anchor) setClickPos(anchor);
          setRemoteConnect({ chain, label: chain.join(' / ') });
        }}
      />

      <RemoteModal
        open={!!remoteConnect}
        onClose={() => setRemoteConnect(null)}
        chain={remoteConnect?.chain ?? []}
        targetLabel={remoteConnect?.label ?? ''}
        animateFrom={clickPos}
      />
    </div>
  );
}
