import { useState, useCallback, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  StatsGrid, Card, Button, Badge, Toggle, Tooltip,
  TreeTable, TreeCell, useToast, useConfirm,
  type TreeNode, type TreeColumn,
} from '@hy2scale/ui';
import type { TopologyNode } from '@/api';
import * as api from '@/api';
import { useNodeStore } from '@/store/node';
import { fmtBytes, fmtRate } from '@/hooks/useFormat';
import { getBasePath } from '@/api/client';
import NodeModal from '@/components/NodeModal';
import EditSelfModal from '@/components/EditSelfModal';
import ImportExportButton from '@/components/ImportExportButton';

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

  const buildChildNode = (c: TopologyNode, parentPath: string): TreeNode<TopologyNode> => {
    const qp = `${parentPath}/${c.name}`;
    return {
      key: qp,
      data: c,
      expanded: true,
      className: `sub-row${syncingNodes.has(qp) ? ' syncing' : ''}${c.disabled ? ' disabled-row' : ''}`,
      children: c.children
        ? [...c.children].sort((a, b) => a.name.localeCompare(b.name)).map((cc) => buildChildNode(cc, qp))
        : undefined,
    };
  };

  const latencyCell = (n: TopologyNode, qualifiedPath?: string) => {
    if (qualifiedPath && syncingNodes.has(qualifiedPath)) {
      return <span className="latency latency-sync">{t('nodes.syncing')}</span>;
    }
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
      key: 'toggle', title: t('nodes.on') || 'ON', width: '40px',
      render: (n, meta) => {
        if (n.native) return null;
        if (n.is_self) {
          return <Toggle checked={!n.disabled} onChange={() => handleToggle(n, '__self__')} />;
        }
        if (meta.depth === 0 && n.direction === 'outbound') {
          return <Toggle checked={!n.disabled} onChange={() => handleToggle(n, meta.nodeKey)} />;
        }
        if (meta.depth === 0 && n.direction === 'inbound') return null;
        // Sub-rows: enable/disable toggle
        if (meta.depth > 0) {
          return <Toggle checked={!n.disabled} onChange={() => handleToggle(n, meta.nodeKey)} />;
        }
        return null;
      },
    },
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
        const nameEl = n.is_self ? (
          <span className="peer-name-cell" style={{ color: 'var(--primary)' }}>{n.name || node?.name || 'self'}</span>
        ) : (
          <a className="peer-link peer-name-cell" href={`${basePath}/remote/${n.name}/scale/`} target="_blank" rel="noopener">
            {n.name}
          </a>
        );

        const versionBadge = n.version && n.version !== node?.version ? (
          <span className={`version-badge${n.compat ? ' compat' : ''}`} style={{ marginLeft: 6 }}>
            v{n.version}
          </span>
        ) : null;

        const nativeBadge = n.native ? <Badge variant="muted">native</Badge> : null;

        const ipTooltip = n.ip_statuses && n.ip_statuses.length > 1 ? (
          <Tooltip content={n.ip_statuses.map((s) => `${s.addr}: ${s.status}`).join('\n')}>
            <Badge variant="muted">+{n.ip_statuses.length - 1}</Badge>
          </Tooltip>
        ) : null;

        return (
          <TreeCell meta={meta}>
            <span className="sub-name-wrap">
              {nameEl}
              {versionBadge}
              {nativeBadge && <> {nativeBadge}</>}
              {ipTooltip && <> {ipTooltip}</>}
              {n.addr && <span className="peer-addr-sub">{n.addr}</span>}
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
      key: 'nested', title: t('nodes.nested'), width: '70px', className: 'col-nested',
      render: (n, meta) => {
        if (n.is_self) return <Toggle checked disabled />;
        if (n.native) return <Toggle checked={false} disabled />;
        if (meta.depth === 0 && n.direction === 'inbound') return null;
        // All peers and sub-rows: interactive nested toggle
        return <Toggle checked={n.nested} onChange={() => handleNestedToggle(n, meta.nodeKey)} />;
      },
    },
    {
      key: 'actions', title: '', width: '150px', className: 'col-actions',
      render: (n, meta) => (
        <div className="act-group">
          {n.is_self ? (
            <button className="act-btn edit" onClick={(e) => openEditSelf(e as any)}>
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7"/><path d="M18.5 2.5a2.12 2.12 0 013 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
              {t('app.edit')}
            </button>
          ) : meta.depth === 0 && !n.native ? (
            <>
              <button className="act-btn edit" onClick={(e) => openEdit(n.name, e as any)}>
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7"/><path d="M18.5 2.5a2.12 2.12 0 013 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
              </button>
              <button className="act-btn danger" onClick={() => handleDelete(n.name)}>
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M3 6h18M19 6v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6m3 0V4a2 2 0 012-2h4a2 2 0 012 2v2"/></svg>
              </button>
            </>
          ) : null}
        </div>
      ),
    },
  ];

  const treeNodes = buildTreeNodes(topology);

  return (
    <div>
      <StatsGrid
        items={[
          {
            label: t('stats.totalTraffic'),
            value: stats ? (
              <><span className="stat-up">{fmtBytes(stats.tx_bytes)}</span> <span className="sep">/</span> <span className="stat-down">{fmtBytes(stats.rx_bytes)}</span></>
            ) : '—',
            sub: t('stats.uploadDownload'),
          },
          {
            label: t('stats.realtimeSpeed'),
            value: stats ? (
              <><span className="stat-up">{fmtRate(stats.tx_rate)}</span> <span className="sep">/</span> <span className="stat-down">{fmtRate(stats.rx_rate)}</span></>
            ) : '—',
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
        ]}
      />

      <Card
        title={t('nodes.title')}
        count={topology.length}
        actions={
          <div style={{ display: 'flex', gap: 8 }}>
            <ImportExportButton target="nodes" />
            <Button size="sm" variant="primary" onClick={openAdd}>{t('nodes.addNode')}</Button>
          </div>
        }
        noPadding
      >
        <TreeTable
          columns={columns}
          nodes={treeNodes}
          emptyText={t('nodes.noConnections')}
        />
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
