import { useState, useCallback, useRef, type MouseEvent } from 'react';
import { useDeselectOnBlankClick } from '@/hooks/useDeselectOnBlankClick';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Badge, AlertBadge, useToast, useConfirm, useSelection, isInteractiveDescendant } from '@hy2scale/ui';
import type { UserConfig, Session, PasswordConflicts } from '@/api';
import * as api from '@/api';
import { fmtBytes, fmtDuration } from '@/hooks/useFormat';
import { ExitViaCell } from '@/components/ExitViaCell';
import UserModal from '@/components/UserModal';
import UserDetailModal from '@/components/UserDetailModal';
import ImportExportButton from '@/components/ImportExportButton';
import ResponsiveActions from '@/components/ResponsiveActions';

export default function UsersPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const queryClient = useQueryClient();

  const [modalOpen, setModalOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [clickPos, setClickPos] = useState<{ x: number; y: number } | undefined>();
  const [detailUser, setDetailUser] = useState<UserConfig | null>(null);
  const [detailOpen, setDetailOpen] = useState(false);
  const [detailPos, setDetailPos] = useState<{ x: number; y: number } | undefined>();

  const { data: users = [] } = useQuery({ queryKey: ['users'], queryFn: api.getUsers, refetchInterval: 5000 });
  const { data: conflicts = {} } = useQuery<PasswordConflicts>({ queryKey: ['userConflicts'], queryFn: api.getUserConflicts, refetchInterval: 5000 });
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

  const openDetail = (u: UserConfig, e: MouseEvent) => {
    setDetailPos({ x: e.clientX, y: e.clientY });
    setDetailUser(u);
    setDetailOpen(true);
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
  const listScopeRef = useRef<HTMLDivElement | null>(null);
  useDeselectOnBlankClick(selection, listScopeRef);

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
    <div className="hy-page">
      {/* Users */}
      <Card
        fill={1}
        title={t('users.title')}
        count={users.length}
        actions={
          <ResponsiveActions
            selectedCount={selection.count}
            onClearSelection={selection.clear}
            selectedLabel={t('app.selected', { count: selection.count })}
          >
            {(() => {
              const sel = [...selection.selected];
              const items = sel.map((id) => users.find((u) => u.id === id)).filter(Boolean);
              const hasDisabled = items.some((u) => !u!.enabled);
              const hasEnabled = items.some((u) => u!.enabled);
              return <>
                {hasDisabled && <Button size="sm" onClick={() => bulkToggle(true)}>{t('app.bulkEnable')}</Button>}
                {hasEnabled && <Button size="sm" onClick={() => bulkToggle(false)}>{t('app.bulkDisable')}</Button>}
                {selection.count > 0 && <Button size="sm" onClick={bulkReset}>{t('users.bulkResetTraffic')}</Button>}
                {selection.count > 0 && <Button size="sm" variant="danger" onClick={bulkDelete}>{t('app.bulkDelete')}</Button>}
              </>;
            })()}
            <ImportExportButton target="users" />
            {(() => {
              const sel = [...selection.selected];
              if (sel.length !== 1) return null;
              const onEditClick = (e: MouseEvent) => openEdit(sel[0], e);
              return (
                <Button size="sm" variant="success" data-testid="edit-selected-btn" onClick={onEditClick}>
                  {t('app.edit')}
                </Button>
              );
            })()}
            <Button size="sm" variant="primary" onClick={openAdd}>{t('users.addUser')}</Button>
          </ResponsiveActions>
        }
        noPadding
      >
        <div ref={listScopeRef} style={{ display: 'contents' }}>
        {users.length === 0 ? (
          <div className="hy-empty" dangerouslySetInnerHTML={{ __html: t('users.noUsers') }} />
        ) : (
          <div className="hy-table-wrap">
            <table className="hy-table">
              <thead>
                <tr>
                  <th
                    className="col-check"
                    onClick={(e) => {
                      e.stopPropagation();
                      if ((e.target as HTMLElement).tagName !== 'INPUT') {
                        selection.toggleAll();
                      }
                    }}
                  >
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
                    <tr
                      key={u.id}
                      className={`${!u.enabled ? 'disabled-row ' : ''}${isSelected ? 'selected ' : ''}hy-row-clickable`}
                      onClick={(e) => {
                        // Mirrors the framework Table's row-body click:
                        // clicking blank cells exclusively selects this row.
                        // The username link, edit button, and checkbox are
                        // interactive descendants and bail out via the helper.
                        if (isInteractiveDescendant(e.target, e.currentTarget)) return;
                        selection.selectOnly(u.id);
                      }}
                    >
                      <td
                        className="col-check"
                        onClick={(e) => {
                          e.stopPropagation();
                          if ((e.target as HTMLElement).tagName !== 'INPUT') {
                            selection.toggle(u.id);
                          }
                        }}
                      >
                        <input type="checkbox" checked={isSelected} onChange={() => selection.toggle(u.id)} />
                      </td>
                      <td>
                        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                          {conflicts[u.username] && (
                            <AlertBadge tooltip={
                              <div style={{ fontSize: 12 }}>
                                <div style={{ fontWeight: 600, marginBottom: 4 }}>{t('users.conflictWarning')}</div>
                                {Object.entries(conflicts[u.username]).map(([proxy, others]) => (
                                  <div key={proxy}>{proxy}: {others.join(', ')}</div>
                                ))}
                              </div>
                            } />
                          )}
                          <a className="peer-link peer-name-cell" style={{ cursor: 'pointer' }} onClick={(e) => openDetail(u, e)}>{u.username}</a>
                        </span>
                      </td>
                      <td><ExitViaCell exitVia={u.exit_via} exitPaths={u.exit_paths} exitMode={u.exit_mode} /></td>
                      <td style={{ textAlign: 'right' }}>
                        <span style={{ fontSize: 12 }}>
                          <span style={{ color: pct > 90 ? 'var(--red)' : 'var(--primary)', fontWeight: 600 }}>{usedGB}</span>
                          <span className="sep">/</span>
                          <span style={{ color: 'var(--text-muted)' }}>{limitGB}</span>
                        </span>
                        {u.traffic_limit > 0 && (
                          <div style={{ background: 'var(--border-light)', height: 3, borderRadius: 2, marginTop: 3 }}>
                            <div style={{ background: pct > 90 ? 'var(--red)' : 'var(--primary)', height: '100%', width: `${pct}%`, borderRadius: 2 }} />
                          </div>
                        )}
                      </td>
                      <td style={{ textAlign: 'right' }}>
                        <span style={{ fontSize: 12, color: expired ? 'var(--red)' : undefined }}>{u.expiry_date || '—'}</span>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        </div>
      </Card>

      {/* Active Devices */}
      <Card fill={1} title={t('devices.title')} count={sessions.length} noPadding>
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
                    <td><b>{s.username}</b></td>
                    <td><span className="mono">{s.remote_ip}</span></td>
                    <td>{s.protocol}</td>
                    <td>{s.conn_count}</td>
                    <td>
                      <span className="mono" style={{ fontSize: 12 }}>
                        <span className="stat-up">↑{fmtBytes(s.tx_bytes)}</span>
                        <span className="sep">/</span>
                        <span className="stat-down">↓{fmtBytes(s.rx_bytes)}</span>
                      </span>
                    </td>
                    <td>{fmtDuration(s.duration)}</td>
                    <td>
                      <button className="hy-row-edit hy-row-kick" onClick={() => handleKick(s)} title={t('devices.kick')}>
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                          <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/>
                          <polyline points="16 17 21 12 16 7"/>
                          <line x1="21" y1="12" x2="9" y2="12"/>
                        </svg>
                      </button>
                    </td>
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

      <UserDetailModal
        open={detailOpen}
        onClose={() => setDetailOpen(false)}
        user={detailUser}
        animateFrom={detailPos}
      />
    </div>
  );
}
