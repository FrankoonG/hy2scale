import { useState, useCallback, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Toggle, Badge, useToast, useConfirm } from '@hy2scale/ui';
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

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
      {/* Users */}
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
        {users.length === 0 ? (
          <div className="hy-empty" dangerouslySetInnerHTML={{ __html: t('users.noUsers') }} />
        ) : (
          <div className="hy-table-wrap">
            <table className="hy-table">
              <thead>
                <tr>
                  <th style={{ width: 50 }}>{t('users.on')}</th>
                  <th style={{ width: 120 }}>{t('users.username')}</th>
                  <th style={{ minWidth: 180 }}>{t('users.exitVia')}</th>
                  <th style={{ width: 130, textAlign: 'right' }}>{t('users.traffic')}</th>
                  <th style={{ width: 90, textAlign: 'right' }}>{t('users.expiry')}</th>
                  <th style={{ width: 150 }}></th>
                </tr>
              </thead>
              <tbody>
                {users.map((u) => {
                  const limitGB = u.traffic_limit > 0 ? (u.traffic_limit / 1073741824).toFixed(1) + ' GB' : '∞';
                  const usedGB = (u.traffic_used / 1073741824).toFixed(2) + ' GB';
                  const pct = u.traffic_limit > 0 ? Math.min(100, (u.traffic_used / u.traffic_limit * 100)) : 0;
                  const expired = isExpired(u.expiry_date);
                  return (
                    <tr key={u.id} className={!u.enabled ? 'disabled-row' : undefined}>
                      <td>
                        <Toggle checked={u.enabled} onChange={() => handleToggle(u)} />
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
                      <td className="col-actions">
                        <div className="act-group">
                          <button className="act-btn edit" onClick={(e) => openEdit(u.id, e)}>
                            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7"/><path d="M18.5 2.5a2.12 2.12 0 013 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
                          </button>
                          <button className="act-btn warn" onClick={() => handleReset(u)}>
                            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M1 4v6h6"/><path d="M3.51 15a9 9 0 105.64-12.36L1 10"/></svg>
                          </button>
                          <button className="act-btn danger" onClick={() => handleDelete(u)}>
                            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M3 6h18M19 6v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6m3 0V4a2 2 0 012-2h4a2 2 0 012 2v2"/></svg>
                          </button>
                        </div>
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
