import { useState, useCallback, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Badge, useToast, useConfirm, useSelection } from '@hy2scale/ui';
import type { UserConfig, Session } from '@/api';
import * as api from '@/api';
import { fmtBytes } from '@/hooks/useFormat';
import { ExitViaCell } from '@/components/ExitViaCell';
import UserModal from '@/components/UserModal';
import ImportExportButton from '@/components/ImportExportButton';
import BulkActionBar from '@/components/BulkActionBar';

export default function UsersPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const queryClient = useQueryClient();

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

  const isExpired = (date?: string) => {
    if (!date) return false;
    try { return new Date(date) < new Date(); } catch { return false; }
  };

  const selection = useSelection(users.map((u) => u.id));

  const bulkToggle = useCallback(async (enabled: boolean) => {
    try {
      await Promise.all([...selection.selected].map((id) => api.toggleUser(id, enabled)));
      toast.success(`${enabled ? t('app.bulkEnable') : t('app.bulkDisable')}: ${selection.count}`);
      queryClient.invalidateQueries({ queryKey: ['users'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [selection, queryClient, toast, t]);

  const bulkReset = useCallback(async () => {
    const ok = await confirm({
      title: t('users.resetTitle'), message: t('users.resetConfirm'),
      confirmText: t('users.reset'), cancelText: t('app.cancel'),
    });
    if (!ok) return;
    try {
      await Promise.all([...selection.selected].map((id) => api.resetTraffic(id)));
      toast.success(`${t('users.bulkResetTraffic')}: ${selection.count}`);
      queryClient.invalidateQueries({ queryKey: ['users'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [selection, confirm, queryClient, toast, t]);

  const bulkDelete = useCallback(async () => {
    const ok = await confirm({
      title: t('app.bulkDelete'), message: t('users.deleteConfirm', { name: `${selection.count} users` }),
      danger: true, confirmText: t('app.delete'), cancelText: t('app.cancel'),
    });
    if (!ok) return;
    try {
      await Promise.all([...selection.selected].map((id) => api.deleteUser(id)));
      toast.success(`${t('app.bulkDelete')}: ${selection.count}`);
      queryClient.invalidateQueries({ queryKey: ['users'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [selection, confirm, queryClient, toast, t]);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
      {/* Users */}
      <Card
        title={t('users.title')}
        count={users.length}
        actions={
          <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <BulkActionBar count={selection.count} onClear={selection.clear}>
              {(() => {
                const sel = [...selection.selected];
                const items = sel.map((id) => users.find((u) => u.id === id)).filter(Boolean);
                const hasDisabled = items.some((u) => !u!.enabled);
                const hasEnabled = items.some((u) => u!.enabled);
                return <>
                  {hasDisabled && <Button size="sm" onClick={() => bulkToggle(true)}>{t('app.bulkEnable')}</Button>}
                  {hasEnabled && <Button size="sm" onClick={() => bulkToggle(false)}>{t('app.bulkDisable')}</Button>}
                  <Button size="sm" onClick={bulkReset}>{t('users.bulkResetTraffic')}</Button>
                  <Button size="sm" variant="danger" onClick={bulkDelete}>{t('app.bulkDelete')}</Button>
                </>;
              })()}
            </BulkActionBar>
            <ImportExportButton target="users" />
            <Button size="sm" variant="primary" onClick={openAdd}>{t('users.addUser')}</Button>
          </div>
        }
        noPadding
      >
        {users.length === 0 ? (
          <div className="hy-empty" dangerouslySetInnerHTML={{ __html: t('users.noUsers') }} />
        ) : (
          <div className="hy-table-wrap">
            <table className="hy-table">
              <thead>
                <tr>
                  <th className="col-check">
                    <input
                      type="checkbox"
                      checked={selection.isAllSelected}
                      ref={(el) => { if (el) el.indeterminate = selection.isSomeSelected; }}
                      onChange={selection.toggleAll}
                    />
                  </th>
                  <th style={{ width: 120 }}>{t('users.username')}</th>
                  <th style={{ minWidth: 180 }}>{t('users.exitVia')}</th>
                  <th style={{ width: 130, textAlign: 'right' }}>{t('users.traffic')}</th>
                  <th style={{ width: 90, textAlign: 'right' }}>{t('users.expiry')}</th>
                  <th style={{ width: 40 }}></th>
                </tr>
              </thead>
              <tbody>
                {users.map((u) => {
                  const limitGB = u.traffic_limit > 0 ? (u.traffic_limit / 1073741824).toFixed(1) + ' GB' : '∞';
                  const usedGB = (u.traffic_used / 1073741824).toFixed(2) + ' GB';
                  const pct = u.traffic_limit > 0 ? Math.min(100, (u.traffic_used / u.traffic_limit * 100)) : 0;
                  const expired = isExpired(u.expiry_date);
                  const isSelected = selection.selected.has(u.id);
                  return (
                    <tr key={u.id} className={`${!u.enabled ? 'disabled-row' : ''}${isSelected ? ' selected' : ''}`}>
                      <td className="col-check">
                        <input type="checkbox" checked={isSelected} onChange={() => selection.toggle(u.id)} />
                      </td>
                      <td><b>{u.username}</b></td>
                      <td><ExitViaCell exitVia={u.exit_via} exitPaths={u.exit_paths} exitMode={u.exit_mode} /></td>
                      <td style={{ textAlign: 'right' }}>
                        <span style={{ fontSize: 12 }}>{usedGB} / {limitGB}</span>
                        {u.traffic_limit > 0 && (
                          <div style={{ background: 'var(--border-light)', height: 3, borderRadius: 2, marginTop: 3 }}>
                            <div style={{ background: pct > 90 ? 'var(--red)' : 'var(--primary)', height: '100%', width: `${pct}%`, borderRadius: 2 }} />
                          </div>
                        )}
                      </td>
                      <td style={{ textAlign: 'right' }}>
                        <span style={{ fontSize: 12, color: expired ? 'var(--red)' : undefined }}>{u.expiry_date || '—'}</span>
                      </td>
                      <td>
                        <button className="hy-row-edit" onClick={(e) => openEdit(u.id, e)} title={t('app.edit')}>
                          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7"/><path d="M18.5 2.5a2.12 2.12 0 013 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
                        </button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {/* Active Devices */}
      <Card title={t('devices.title')} count={sessions.length} noPadding>
        {sessions.length === 0 ? (
          <div className="hy-empty">{t('devices.noDevices')}</div>
        ) : (
          <div className="hy-table-wrap">
            <table className="hy-table">
              <thead>
                <tr>
                  <th>{t('devices.user')}</th>
                  <th>{t('devices.ip')}</th>
                  <th>{t('devices.proxy')}</th>
                  <th>{t('devices.conn')}</th>
                  <th>{t('devices.traffic')}</th>
                  <th>{t('devices.duration')}</th>
                  <th style={{ width: 60 }}></th>
                </tr>
              </thead>
              <tbody>
                {sessions.map((s) => (
                  <tr key={s.key}>
                    <td><b>{s.user}</b></td>
                    <td><span className="mono">{s.ip}</span></td>
                    <td>{s.proxy}</td>
                    <td>{s.conns}</td>
                    <td><span className="mono">{fmtBytes(s.tx_bytes + s.rx_bytes)}</span></td>
                    <td>{s.duration}</td>
                    <td><Button size="sm" variant="danger" onClick={() => handleKick(s)}>{t('devices.kick')}</Button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
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
