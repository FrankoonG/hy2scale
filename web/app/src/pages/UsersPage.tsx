import { useState, useCallback, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Card, Button, Table, Toggle, Badge, CopyButton, useToast, useConfirm, type Column,
} from '@hy2scale/ui';
import type { UserConfig, Session } from '@/api';
import * as api from '@/api';
import { fmtBytes } from '@/hooks/useFormat';
import { ExitViaCell } from '@/components/ExitViaCell';
import UserModal from '@/components/UserModal';
import ImportExportButton from '@/components/ImportExportButton';

export default function UsersPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const queryClient = useQueryClient();

  // Both users and devices cards are always visible (no tabs)
  const [modalOpen, setModalOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [clickPos, setClickPos] = useState<{ x: number; y: number } | undefined>();

  const { data: users = [] } = useQuery({ queryKey: ['users'], queryFn: api.getUsers, refetchInterval: 5000 });
  const { data: sessions = [] } = useQuery({ queryKey: ['sessions'], queryFn: api.getSessions, refetchInterval: 3000 });

  const openAdd = (e: MouseEvent) => {
    setClickPos({ x: e.clientX, y: e.clientY });
    setEditingId(null);
    setModalOpen(true);
  };

  const openEdit = (id: string, e: MouseEvent) => {
    setClickPos({ x: e.clientX, y: e.clientY });
    setEditingId(id);
    setModalOpen(true);
  };

  const handleToggle = useCallback(async (u: UserConfig) => {
    try {
      await api.toggleUser(u.id, !u.enabled);
      queryClient.invalidateQueries({ queryKey: ['users'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [queryClient, toast]);

  const handleReset = useCallback(async (u: UserConfig) => {
    const ok = await confirm({
      title: t('users.resetTitle'),
      message: t('users.resetConfirm'),
      confirmText: t('users.reset'),
      cancelText: t('app.cancel'),
    });
    if (!ok) return;
    try {
      await api.resetTraffic(u.id);
      toast.success(t('users.trafficReset'));
      queryClient.invalidateQueries({ queryKey: ['users'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [confirm, t, queryClient, toast]);

  const handleDelete = useCallback(async (u: UserConfig) => {
    const ok = await confirm({
      title: t('users.deleteTitle'),
      message: t('users.deleteConfirm', { name: u.username }),
      danger: true, confirmText: t('app.delete'), cancelText: t('app.cancel'),
    });
    if (!ok) return;
    try {
      await api.deleteUser(u.id);
      toast.success(t('users.deleted', { name: u.username }));
      queryClient.invalidateQueries({ queryKey: ['users'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [confirm, t, queryClient, toast]);

  const handleKick = useCallback(async (s: Session) => {
    try {
      await api.kickSession(s.key);
      toast.success(t('devices.kicked'));
      queryClient.invalidateQueries({ queryKey: ['sessions'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [queryClient, toast, t]);

  const userColumns: Column<UserConfig>[] = [
    { key: 'on', title: t('users.on'), width: '40px', render: (u) => <Toggle checked={u.enabled} onChange={() => handleToggle(u)} size="sm" /> },
    { key: 'username', title: t('users.username'), render: (u) => <strong>{u.username}</strong> },
    {
      key: 'password', title: t('users.password'), render: (u) => (
        <div style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
          <span className="mono" style={{ fontSize: 12 }}>{'••••••••'}</span>
          <CopyButton text={u.password} />
        </div>
      ),
    },
    { key: 'exit', title: t('users.exitVia'), render: (u) => <ExitViaCell exitVia={u.exit_via} exitPaths={u.exit_paths} exitMode={u.exit_mode} /> },
    {
      key: 'traffic', title: t('users.traffic'), render: (u) => {
        const limit = u.traffic_limit > 0 ? fmtBytes(u.traffic_limit) : '∞';
        return <span className="mono">{fmtBytes(u.traffic_used)} / {limit}</span>;
      },
    },
    { key: 'expiry', title: t('users.expiry'), render: (u) => u.expiry_date || '—' },
    {
      key: 'actions', title: '', width: '120px', render: (u) => (
        <div className="actions">
          <button className="hy-icon-btn" onClick={(e) => openEdit(u.id, e as any)} title={t('app.edit')}>✎</button>
          <button className="hy-icon-btn" onClick={() => handleReset(u)} title={t('users.reset')}>↺</button>
          <button className="hy-icon-btn danger" onClick={() => handleDelete(u)} title={t('app.delete')}>✕</button>
        </div>
      ),
    },
  ];

  const sessionColumns: Column<Session>[] = [
    { key: 'user', title: t('devices.user'), render: (s) => <strong>{s.user}</strong> },
    { key: 'ip', title: t('devices.ip'), render: (s) => <span className="mono">{s.ip}</span> },
    { key: 'proxy', title: t('devices.proxy'), render: (s) => s.proxy },
    { key: 'conn', title: t('devices.conn'), render: (s) => s.conns },
    { key: 'traffic', title: t('devices.traffic'), render: (s) => <span className="mono">{fmtBytes(s.tx_bytes + s.rx_bytes)}</span> },
    { key: 'duration', title: t('devices.duration'), render: (s) => s.duration },
    {
      key: 'kick', title: '', width: '60px',
      render: (s) => <Button size="sm" variant="danger" onClick={() => handleKick(s)}>{t('devices.kick')}</Button>,
    },
  ];

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
      <Card
        title={t('users.title')}
        count={users.length}
        actions={
          <div style={{ display: 'flex', gap: 8 }}>
            <ImportExportButton target="users" />
            <Button size="sm" variant="primary" onClick={openAdd}>{t('users.addUser')}</Button>
          </div>
        }
        noPadding
      >
        <Table
          columns={userColumns}
          data={users}
          rowKey={(u) => u.id}
          emptyText={t('users.noUsers')}
        />
      </Card>

      <Card title={t('devices.title')} count={sessions.length} noPadding>
        <Table
          columns={sessionColumns}
          data={sessions}
          rowKey={(s) => s.key}
          emptyText={t('devices.noDevices')}
        />
      </Card>

      <UserModal
        open={modalOpen}
        onClose={() => setModalOpen(false)}
        editingId={editingId}
        animateFrom={clickPos}
      />
    </div>
  );
}
