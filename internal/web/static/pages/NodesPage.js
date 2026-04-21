import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { StatsGrid, Card, Button, Badge, Tooltip, TreeTable, TreeCell, useToast, useConfirm, useSelection, } from '@hy2scale/ui';
import * as api from '@/api';
import { useNodeStore } from '@/store/node';
import { fmtBytes, fmtRate } from '@/hooks/useFormat';
import { getBasePath } from '@/api/client';
import NodeModal from '@/components/NodeModal';
import EditSelfModal from '@/components/EditSelfModal';
import BulkActionBar from '@/components/BulkActionBar';
import ImportExportButton from '@/components/ImportExportButton';
export default function NodesPage() {
    const { t } = useTranslation();
    const toast = useToast();
    const confirm = useConfirm();
    const queryClient = useQueryClient();
    const { node, topology, setTopology, syncingNodes, setSyncing, clearSyncing } = useNodeStore();
    const [modalOpen, setModalOpen] = useState(false);
    const [editSelfOpen, setEditSelfOpen] = useState(false);
    const [editingName, setEditingName] = useState(null);
    const [clickPos, setClickPos] = useState();
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
    const { data: stats } = useQuery({
        queryKey: ['stats'],
        queryFn: () => api.getStats(),
        refetchInterval: 2000,
    });
    const openAdd = (e) => {
        setClickPos({ x: e.clientX, y: e.clientY });
        setEditingName(null);
        setModalOpen(true);
    };
    const openEdit = (name, e) => {
        setClickPos({ x: e.clientX, y: e.clientY });
        setEditingName(name);
        setModalOpen(true);
    };
    const openEditSelf = (e) => {
        setClickPos({ x: e.clientX, y: e.clientY });
        setEditSelfOpen(true);
    };
    const handleToggle = useCallback(async (n, qualifiedPath) => {
        const newDisabled = !n.disabled;
        setSyncing(qualifiedPath, !newDisabled);
        try {
            await api.disableClient(n.name, newDisabled);
            queryClient.invalidateQueries({ queryKey: ['topology'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
        finally {
            setTimeout(() => clearSyncing(qualifiedPath), 3000);
        }
    }, [setSyncing, clearSyncing, queryClient, toast]);
    const handleNestedToggle = useCallback(async (n, qualifiedPath) => {
        const newNested = !n.nested;
        setSyncing(qualifiedPath, newNested);
        try {
            // Use qualifiedPath — backend needs full path for sub-peers
            // e.g. "eeff2f4a/0fb04e6f" maps to Peers["eeff2f4a/0fb04e6f"]
            // SetNested auto-strips self prefix for inbound peers
            await api.setNested(qualifiedPath, newNested);
            queryClient.invalidateQueries({ queryKey: ['topology'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
        finally {
            setTimeout(() => clearSyncing(qualifiedPath), 3000);
        }
    }, [setSyncing, clearSyncing, queryClient, toast]);
    const handleDelete = useCallback(async (name) => {
        const ok = await confirm({
            title: t('nodes.deleteTitle'),
            message: t('nodes.deleteConfirm', { name }),
            danger: true,
            confirmText: t('app.delete'),
            cancelText: t('app.cancel'),
        });
        if (!ok)
            return;
        try {
            await api.deleteClient(name);
            toast.success(t('nodes.deleted', { name }));
            queryClient.invalidateQueries({ queryKey: ['topology'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [confirm, t, queryClient, toast]);
    // Build tree nodes from topology
    const buildTreeNodes = (topo) => {
        const sorted = [...topo].sort((a, b) => {
            if (a.is_self)
                return -1;
            if (b.is_self)
                return 1;
            return a.name.localeCompare(b.name);
        });
        return sorted.map((n) => {
            const rootPath = n.is_self ? (node?.node_id || n.name) : n.name;
            return {
                key: n.is_self ? '__self__' : n.name,
                data: n,
                expanded: true,
                className: n.is_self ? 'self-row' : syncingNodes.has(n.name) ? 'syncing' : n.disabled ? 'disabled-row' : undefined,
                children: n.children
                    ? [...n.children].sort((a, b) => a.name.localeCompare(b.name)).map((c) => buildChildNode(c, rootPath))
                    : undefined,
            };
        });
    };
    const buildChildNode = (c, parentPath) => {
        const qp = `${parentPath}/${c.name}`;
        return {
            key: qp,
            data: c,
            expanded: true,
            className: `sub-row${syncingNodes.has(qp) ? ' syncing' : ''}${c.disabled ? ' disabled-row' : ''}`,
            // Don't expand children of a disabled node — they're effectively unreachable
            children: c.disabled || !c.children
                ? undefined
                : [...c.children].sort((a, b) => a.name.localeCompare(b.name)).map((cc) => buildChildNode(cc, qp)),
        };
    };
    const latencyCell = (n, qualifiedPath) => {
        if (qualifiedPath && syncingNodes.has(qualifiedPath)) {
            return _jsx("span", { className: "latency latency-sync", children: t('nodes.syncing') });
        }
        if (n.disabled)
            return _jsx("span", { className: "latency latency-off", children: t('nodes.offline') });
        if (n.is_self)
            return _jsx("span", { className: "latency latency-good", children: "\u221E" });
        if (n.latency_ms === -1)
            return _jsx("span", { className: "latency latency-off", children: t('nodes.offline') });
        if (n.latency_ms === 0)
            return _jsx("span", { className: "latency latency-na", children: "\u2014" });
        const cls = n.latency_ms < 80 ? 'latency-good' : n.latency_ms < 200 ? 'latency-med' : 'latency-bad';
        return _jsxs("span", { className: `latency ${cls}`, children: [n.latency_ms, "ms"] });
    };
    const dirBadge = (n) => {
        if (n.is_self)
            return _jsx(Badge, { variant: "blue", children: "LOCAL" });
        if (n.direction === 'inbound')
            return _jsx(Badge, { variant: "muted", children: "IN" });
        if (n.direction === 'outbound')
            return _jsx(Badge, { variant: "muted", children: "OUT" });
        return null;
    };
    const columns = [
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
                const nameEl = n.is_self ? (_jsx("span", { className: "peer-name-cell", style: { color: 'var(--primary)' }, children: n.name || node?.name || 'self' })) : (_jsx("a", { className: "peer-link peer-name-cell", href: `${basePath}/remote/${n.name}/scale/`, target: "_blank", rel: "noopener", children: n.name }));
                const versionBadge = n.version && n.version !== node?.version ? (_jsxs("span", { className: `version-badge${n.compat ? ' compat' : ''}`, style: { marginLeft: 6 }, children: ["v", n.version] })) : null;
                const nativeBadge = n.native ? _jsx(Badge, { variant: "muted", children: "native" }) : null;
                // Tree expand/collapse triangle: shown for non-self, non-native nodes
                // ▶ = nested false (collapsed), ▼ = nested true (expanded)
                // Clicking toggles nested state. Sits to the left of the whole stacked
                // (name + address) block, vertically centered via the tree-cell flex.
                // Self/native rows render a non-interactive disabled chevron (forced
                // down state) so the column stays visually consistent with other rows.
                const canExpand = !n.is_self && !n.native;
                const chevronSvg = (rotated) => (_jsx("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: 2.5, strokeLinecap: "round", strokeLinejoin: "round", style: { transform: rotated ? 'rotate(90deg)' : 'rotate(0deg)' }, children: _jsx("path", { d: "M9 6l6 6-6 6" }) }));
                const triangleSlot = canExpand ? (_jsx("button", { type: "button", className: "hy-tree-toggle", onClick: (e) => { e.preventDefault(); e.stopPropagation(); handleNestedToggle(n, meta.nodeKey); }, "aria-label": n.nested ? 'Collapse' : 'Expand', children: chevronSvg(!!n.nested) })) : (_jsx("span", { className: "hy-tree-toggle hy-tree-toggle-disabled", "aria-hidden": "true", children: chevronSvg(true) }));
                return (_jsxs(TreeCell, { meta: meta, children: [triangleSlot, _jsxs("span", { className: "sub-name-wrap", children: [nameEl, versionBadge, nativeBadge && _jsxs(_Fragment, { children: [" ", nativeBadge] }), n.via ? (_jsxs("span", { className: "peer-addr-sub", children: ["via ", n.via] })) : n.addr ? (_jsxs("span", { className: "peer-addr-sub", children: [n.addr, n.ip_statuses && n.ip_statuses.length > 0 && (_jsxs(_Fragment, { children: [" ", _jsx(Tooltip, { content: n.ip_statuses.map((s) => {
                                                        const lat = s.latency_ms && s.latency_ms > 0 ? `${s.latency_ms}ms` : s.latency_ms === -1 ? 'timeout' : s.status;
                                                        return `${s.addr} — ${lat}`;
                                                    }).join('\n'), children: _jsxs(Badge, { variant: "muted", children: ["+", n.ip_statuses.length - 1] }) })] }))] })) : null] })] }));
            },
        },
        {
            key: 'traffic', title: t('nodes.traffic'), width: '160px', className: 'col-traffic',
            render: (n) => (_jsxs(_Fragment, { children: [_jsx("span", { className: "stat-up", children: fmtRate(n.tx_rate || 0) }), ' ', _jsx("span", { style: { color: 'var(--text-muted)' }, children: "/" }), ' ', _jsx("span", { className: "stat-down", children: fmtRate(n.rx_rate || 0) })] })),
        },
        {
            key: 'actions', title: '', width: '40px',
            render: (n, meta) => {
                if (n.is_self) {
                    return _jsx("button", { className: "hy-row-edit", onClick: (e) => openEditSelf(e), title: t('app.edit'), children: _jsxs("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: "2", children: [_jsx("path", { d: "M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7" }), _jsx("path", { d: "M18.5 2.5a2.12 2.12 0 013 3L12 15l-4 1 1-4 9.5-9.5z" })] }) });
                }
                if (meta.depth === 0 && !n.native) {
                    return _jsx("button", { className: "hy-row-edit", onClick: (e) => openEdit(n.name, e), title: t('app.edit'), children: _jsxs("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: "2", children: [_jsx("path", { d: "M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7" }), _jsx("path", { d: "M18.5 2.5a2.12 2.12 0 013 3L12 15l-4 1 1-4 9.5-9.5z" })] }) });
                }
                return null;
            },
        },
    ];
    const treeNodes = buildTreeNodes(topology);
    // Collect selectable keys (exclude self, native)
    const selectableKeys = [];
    const collectKeys = (nodes) => {
        for (const n of nodes) {
            if (!n.data.is_self && !n.data.native)
                selectableKeys.push(n.key);
            if (n.children)
                collectKeys(n.children);
        }
    };
    collectKeys(treeNodes);
    const selection = useSelection(selectableKeys);
    const bulkToggleNodes = useCallback(async (disabled) => {
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
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [selection, queryClient, toast, t]);
    const bulkNested = useCallback(async (nested) => {
        try {
            await Promise.all([...selection.selected].map((key) => api.setNested(key, nested)));
            toast.success(`${nested ? t('nodes.bulkEnableNested') : t('nodes.bulkDisableNested')}: ${selection.count}`);
            queryClient.invalidateQueries({ queryKey: ['topology'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [selection, queryClient, toast, t]);
    const bulkDeleteNodes = useCallback(async () => {
        const ok = await confirm({
            title: t('app.bulkDelete'), message: t('nodes.deleteConfirm', { name: `${selection.count} nodes` }),
            danger: true, confirmText: t('app.delete'), cancelText: t('app.cancel'),
        });
        if (!ok)
            return;
        try {
            const rootKeys = [...selection.selected].filter((key) => !key.includes('/'));
            await Promise.all(rootKeys.map((name) => api.deleteClient(name)));
            toast.success(`${t('app.bulkDelete')}: ${selection.count}`);
            queryClient.invalidateQueries({ queryKey: ['topology'] });
        }
        catch (e) {
            toast.error(String(e.message || e));
        }
    }, [selection, confirm, queryClient, toast, t]);
    return (_jsxs("div", { children: [_jsx(StatsGrid, { items: [
                    {
                        label: t('stats.totalTraffic'),
                        value: stats ? (_jsxs(_Fragment, { children: [_jsx("span", { className: "stat-up", children: fmtBytes(stats.tx_bytes) }), " ", _jsx("span", { className: "sep", children: "/" }), " ", _jsx("span", { className: "stat-down", children: fmtBytes(stats.rx_bytes) })] })) : '—',
                        sub: t('stats.uploadDownload'),
                    },
                    {
                        label: t('stats.realtimeSpeed'),
                        value: stats ? (_jsxs(_Fragment, { children: [_jsx("span", { className: "stat-up", children: fmtRate(stats.tx_rate) }), " ", _jsx("span", { className: "sep", children: "/" }), " ", _jsx("span", { className: "stat-down", children: fmtRate(stats.rx_rate) })] })) : '—',
                        sub: t('stats.uploadDownload'),
                    },
                    {
                        label: t('stats.connections'),
                        value: stats?.conns ?? '—',
                        sub: t('stats.activeStreams'),
                    },
                    {
                        label: t('stats.exitClients'),
                        value: stats?.exit_clients ?? '—',
                        sub: t('stats.usingAsExit'),
                    },
                ] }), _jsx(Card, { title: t('nodes.title'), count: topology.length, actions: _jsxs("div", { style: { display: 'flex', gap: 8, alignItems: 'center' }, children: [_jsx(BulkActionBar, { count: selection.count, onClear: selection.clear, children: (() => {
                                // Inspect selected nodes to decide which buttons to show
                                const sel = [...selection.selected];
                                const findData = (key) => {
                                    const search = (nodes) => {
                                        for (const n of nodes) {
                                            if (n.key === key)
                                                return n.data;
                                            if (n.children) {
                                                const r = search(n.children);
                                                if (r)
                                                    return r;
                                            }
                                        }
                                        return undefined;
                                    };
                                    return search(treeNodes);
                                };
                                const items = sel.map(findData).filter(Boolean);
                                const rootKeys = sel.filter((k) => !k.includes('/'));
                                // Enable/Disable applies to all nodes:
                                // - Root nodes: disableClient (stops QUIC connection)
                                // - Sub-rows: setPeerDisabled (keeps connection, blocks as exit hop)
                                const hasDisabled = items.some((n) => n.disabled);
                                const hasEnabled = items.some((n) => !n.disabled);
                                const hasNested = items.some((n) => n.nested);
                                const hasUnnested = items.some((n) => !n.nested);
                                const hasRoot = rootKeys.length > 0;
                                return _jsxs(_Fragment, { children: [hasDisabled && _jsx(Button, { size: "sm", onClick: () => bulkToggleNodes(false), children: t('app.bulkEnable') }), hasEnabled && _jsx(Button, { size: "sm", onClick: () => bulkToggleNodes(true), children: t('app.bulkDisable') }), hasUnnested && _jsx(Button, { size: "sm", onClick: () => bulkNested(true), children: t('nodes.bulkEnableNested') }), hasNested && _jsx(Button, { size: "sm", onClick: () => bulkNested(false), children: t('nodes.bulkDisableNested') }), hasRoot && _jsx(Button, { size: "sm", variant: "danger", onClick: bulkDeleteNodes, children: t('app.bulkDelete') })] });
                            })() }), _jsx(ImportExportButton, { target: "nodes" }), _jsx(Button, { size: "sm", variant: "primary", onClick: openAdd, children: t('nodes.addNode') })] }), noPadding: true, children: _jsx(TreeTable, { columns: columns, nodes: treeNodes, emptyText: t('nodes.noConnections'), selection: selection, isSelectable: (node) => !node.data.is_self && !node.data.native }) }), _jsx(NodeModal, { open: modalOpen, onClose: () => setModalOpen(false), editingName: editingName, animateFrom: clickPos }), _jsx(EditSelfModal, { open: editSelfOpen, onClose: () => setEditSelfOpen(false), animateFrom: clickPos })] }));
}
