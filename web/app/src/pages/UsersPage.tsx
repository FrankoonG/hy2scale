import { useState, useCallback, useRef, useMemo, type MouseEvent } from 'react';
import { useDeselectOnBlankClick } from '@/hooks/useDeselectOnBlankClick';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Badge, AlertBadge, Table, Tooltip, useToast, useConfirm, useSelection, type Column } from '@hy2scale/ui';
import type { UserConfig, Session, PasswordConflicts } from '@/api';
import * as api from '@/api';
import { ExitViaCell, exitViaSortValue } from '@/components/ExitViaCell';
import UserModal from '@/components/UserModal';
import UserDetailModal from '@/components/UserDetailModal';
import UserInfoModal from '@/components/UserInfoModal';
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
  const [detailId, setDetailId] = useState<string | null>(null);
  const [detailSnap, setDetailSnap] = useState<UserConfig | null>(null);
  const [detailOpen, setDetailOpen] = useState(false);
  const [detailPos, setDetailPos] = useState<{ x: number; y: number } | undefined>();
  const [preview, setPreview] = useState<UserConfig | null>(null);
  const [previewOpen, setPreviewOpen] = useState(false);
  const [previewPos, setPreviewPos] = useState<{ x: number; y: number } | undefined>();

  const { data: users = [] } = useQuery({ queryKey: ['users'], queryFn: api.getUsers, refetchInterval: 5000 });
  const { data: conflicts = {} } = useQuery<PasswordConflicts>({ queryKey: ['userConflicts'], queryFn: api.getUserConflicts, refetchInterval: 5000 });
  const { data: sessions = [] } = useQuery({ queryKey: ['sessions'], queryFn: api.getSessions, refetchInterval: 3000 });

  const sessionsByUser = useMemo(() => {
    const m = new Map<string, Session[]>();
    for (const s of sessions) {
      const list = m.get(s.username);
      if (list) list.push(s); else m.set(s.username, [s]);
    }
    return m;
  }, [sessions]);
  const noSessions: Session[] = [];
  // The dialog follows the live user row (traffic etc. keep polling); the
  // snapshot covers the close animation after a delete.
  const detailUser = (detailId && users.find((u) => u.id === detailId)) || detailSnap;

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

  const openDetailAt = (u: UserConfig, pos?: { x: number; y: number }) => {
    setDetailPos(pos);
    setDetailId(u.id);
    setDetailSnap(u);
    setDetailOpen(true);
  };
  // Username click: the quick preview (links / QR codes).
  const openPreview = (u: UserConfig, e: MouseEvent) => {
    setPreviewPos({ x: e.clientX, y: e.clientY });
    setPreview(u);
    setPreviewOpen(true);
  };
  // Where the last click in the user list landed (modal animation origin).
  const userClickPos = useRef<{ x: number; y: number } | undefined>(undefined);

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

  const userColumns: Column<UserConfig>[] = [
    {
      key: 'username', title: t('users.username'), width: '120px',
      sortValue: (u) => u.username,
      render: (u) => (
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
          <a className="peer-link peer-name-cell" style={{ cursor: 'pointer' }} onClick={(e) => openPreview(u, e)}>{u.username}</a>
        </span>
      ),
    },
    {
      key: 'exit', title: t('users.exitVia'),
      sortValue: (u) => exitViaSortValue(u.exit_via, u.exit_paths),
      render: (u) => (
        <div style={{ minWidth: 180 }}>
          <ExitViaCell exitVia={u.exit_via} exitPaths={u.exit_paths} exitMode={u.exit_mode} />
        </div>
      ),
    },
    {
      key: 'devices', title: t('users.devices'), width: '80px', className: 'col-right',
      sortValue: (u) => sessionsByUser.get(u.username)?.length ?? 0,
      render: (u) => {
        const list = sessionsByUser.get(u.username);
        if (!list || list.length === 0) {
          return <div style={{ textAlign: 'right', color: 'var(--text-muted)' }}>—</div>;
        }
        return (
          <div style={{ textAlign: 'right' }}>
            <Tooltip content={
              <div style={{ fontSize: 12 }}>
                {list.map((s) => (
                  <div key={s.key}>{s.protocol} <span className="mono">{s.remote_ip}</span></div>
                ))}
              </div>
            }>
              <Badge variant="green">{list.length}</Badge>
            </Tooltip>
          </div>
        );
      },
    },
    {
      key: 'traffic', title: t('users.traffic'), width: '130px', className: 'col-right',
      sortValue: (u) => u.traffic_used,
      render: (u) => {
        const limitGB = u.traffic_limit > 0 ? (u.traffic_limit / 1073741824).toFixed(1) + ' GB' : '∞';
        const usedGB = (u.traffic_used / 1073741824).toFixed(2) + ' GB';
        const pct = u.traffic_limit > 0 ? Math.min(100, (u.traffic_used / u.traffic_limit * 100)) : 0;
        return (
          <div style={{ textAlign: 'right' }}>
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
          </div>
        );
      },
    },
    {
      key: 'expiry', title: t('users.expiry'), width: '90px', className: 'col-right',
      sortValue: (u) => {
        if (!u.expiry_date) return null;
        const ts = Date.parse(u.expiry_date);
        return Number.isNaN(ts) ? null : ts;
      },
      render: (u) => (
        <div style={{ textAlign: 'right' }}>
          <span style={{ fontSize: 12, color: isExpired(u.expiry_date) ? 'var(--red)' : undefined }}>{u.expiry_date || '—'}</span>
        </div>
      ),
    },
  ];

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
        <div
          ref={listScopeRef}
          style={{ display: 'contents' }}
          onClickCapture={(e) => { userClickPos.current = { x: e.clientX, y: e.clientY }; }}
        >
          <Table
            id="users"
            columns={userColumns}
            data={users}
            rowKey={(u) => u.id}
            rowClassName={(u) => !u.enabled ? 'disabled-row' : undefined}
            emptyText={t('users.noUsers')}
            selection={selection}
            onRowClick={(u) => openDetailAt(u, userClickPos.current)}
          />
        </div>
      </Card>

      <UserModal
        open={modalOpen}
        onClose={() => setModalOpen(false)}
        editing={editingId ? users.find((u) => u.id === editingId) || null : null}
        animateFrom={clickPos}
      />

      <UserDetailModal
        open={previewOpen}
        onClose={() => setPreviewOpen(false)}
        user={preview}
        animateFrom={previewPos}
      />

      <UserInfoModal
        open={detailOpen}
        onClose={() => setDetailOpen(false)}
        user={detailUser}
        animateFrom={detailPos}
        sessions={(detailUser && sessionsByUser.get(detailUser.username)) || noSessions}
        conflicts={detailUser ? conflicts[detailUser.username] : undefined}
        onKick={handleKick}
      />
    </div>
  );
}
