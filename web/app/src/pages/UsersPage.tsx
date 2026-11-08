import { useCallback, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Card, Button, ExpandButton, Badge, Table, Tooltip, IconCircleAlert, useToast, useSelection, useModalState, type Column,
} from '@hy2scale/ui';
import type { UserConfig, Session, PasswordConflicts } from '@/api';
import * as api from '@/api';
import { GiB } from '@/hooks/useFormat';
import { useUserActions, isLeaving } from '@/hooks/useUserActions';
import { ListScope } from '@/hooks/useDeselectOnBlankClick';
import { isPast } from '@/utils/date';
import { ExitViaCell, exitViaSortValue } from '@/components/ExitViaCell';
import UserModal from '@/components/UserModal';
import UserInfoModal, { type UserInfoTab } from '@/components/UserInfoModal';
import ShareUserModal from '@/components/ShareUserModal';
import { useImportExport } from '@/components/ImportExport';
import ResponsiveActions from '@/components/ResponsiveActions';
import UsageMeter from '@/components/UsageMeter';
import { BulkButtons, EditSelectedButton, pickSelected } from '@/components/SelectionActions';
import { boldMarkup } from '@/utils/richText';

const gb = (digits: number) => (b: number) => (b / GiB).toFixed(digits) + ' GB';
const noSessions: Session[] = [];

export default function UsersPage() {
  const { t } = useTranslation();
  const ie = useImportExport('users');
  const toast = useToast();
  const queryClient = useQueryClient();

  const edit = useModalState<string | null>();      // user id; null = new user
  // row click: details; username click: proxies (share links)
  const info = useModalState<{ user: UserConfig; tab: UserInfoTab }>();
  const share = useModalState<string>();             // user id

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
  // The dialog follows the live user row (traffic etc. keep polling); the
  // snapshot taken on open covers the close animation after a delete.
  const infoUser = (info.data && users.find((u) => u.id === info.data!.user.id)) || info.data?.user || null;

  const handleKick = useCallback(async (s: Session) => {
    try {
      await api.kickSession(s.key);
      toast.success(t('devices.kicked'));
      queryClient.invalidateQueries({ queryKey: ['sessions'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [queryClient, toast, t]);

  const selection = useSelection(users.map((u) => u.id));
  const selected = pickSelected(users, selection.selected, (u) => u.id);
  const actions = useUserActions();
  const leaving = isLeaving(selected);
  const one = selected.length === 1 ? selected[0] : null;

  const userColumns: Column<UserConfig>[] = [
    {
      key: 'username', title: t('users.username'), width: '120px',
      sortValue: (u) => u.username,
      render: (u) => (
        <span className="name-with-mark">
          {conflicts[u.username] && (
            <Tooltip content={
              <div className="cell-small">
                <div className="tip-title">{t('users.conflictWarning')}</div>
                {Object.entries(conflicts[u.username]).map(([proxy, others]) => (
                  <div key={proxy}>{proxy}: {others.join(', ')}</div>
                ))}
              </div>
            }>
              <IconCircleAlert size={14} className="alert-mark" />
            </Tooltip>
          )}
          <a className="peer-link peer-name-cell" onClick={(e) => { e.stopPropagation(); info.show({ user: u, tab: 'proxies' }, e); }}>{u.username}</a>
          {u.shared && (
            <Tooltip content={<div className="cell-small">{u.shared.members.map((m) => <div key={m.node_id || m.name}>{m.name}</div>)}</div>}>
              <span data-testid="user-shared-tag"><Badge variant="blue">{t('users.sharedTag', { count: u.shared.members.length })}</Badge></span>
            </Tooltip>
          )}
        </span>
      ),
    },
    {
      key: 'exit', title: t('users.exitVia'),
      sortValue: (u) => exitViaSortValue(u.exit_via, u.exit_paths),
      render: (u) => (
        <div className="cell-exit">
          <ExitViaCell exitVia={u.exit_via} exitPaths={u.exit_paths} exitMode={u.exit_mode} />
        </div>
      ),
    },
    {
      key: 'devices', title: t('users.devices'), width: '80px', className: 'col-right',
      sortValue: (u) => sessionsByUser.get(u.username)?.length ?? 0,
      render: (u) => {
        const list = sessionsByUser.get(u.username);
        if (!list || list.length === 0) return <span className="text-muted">—</span>;
        return (
          <Tooltip content={
            <div className="cell-small">
              {list.map((s) => <div key={s.key}>{s.protocol} <span className="mono">{s.remote_ip}</span></div>)}
            </div>
          }>
            <Badge variant="green">{list.length}</Badge>
          </Tooltip>
        );
      },
    },
    {
      key: 'traffic', title: t('users.traffic'), width: '130px', className: 'col-right',
      sortValue: (u) => u.traffic_used,
      render: (u) => <UsageMeter used={u.traffic_used} limit={u.traffic_limit} format={gb(2)} formatLimit={gb(1)} />,
    },
    {
      key: 'expiry', title: t('users.expiry'), width: '90px', className: 'col-right',
      sortValue: (u) => {
        if (!u.expiry_date) return null;
        const ts = Date.parse(u.expiry_date);
        return Number.isNaN(ts) ? null : ts;
      },
      render: (u) => <span className={`cell-small${isPast(u.expiry_date) ? ' text-bad' : ''}`}>{u.expiry_date || '—'}</span>,
    },
  ];

  return (
    <div className="hy-page">
      <Card
        fill={1}
        title={t('users.title')}
        count={users.length}
        actions={
          <ResponsiveActions selectedCount={selection.count} onClearSelection={selection.clear}>
            <BulkButtons selected={selected} isEnabled={(u) => u.enabled}
              onToggle={(enabled) => actions.toggle(selected, enabled)} onDelete={() => actions.remove(selected)}
              deleteLabel={leaving ? t('users.leave') : undefined}>
              <Button size="sm" onClick={() => actions.reset(selected)}>{t('users.bulkResetTraffic')}</Button>
              {one && <Button size="sm" data-testid="user-share" onClick={(e) => share.show(one.id, e)}>{t('users.share')}</Button>}
              {one?.shared && (
                <Button size="sm" variant="danger" data-testid="user-delete-everywhere" onClick={() => actions.deleteEverywhere(one)}>
                  {t('users.deleteEverywhere')}
                </Button>
              )}
            </BulkButtons>
            <EditSelectedButton selected={selected} onEdit={(u, e) => edit.show(u.id, e)} />
            <ExpandButton variant="primary" label={t('users.addUser')} moreLabel={t('app.moreActions')}
              onClick={(e) => edit.show(null, e)} actions={ie.actions} />
          </ResponsiveActions>
        }
        noPadding
      >
        <ListScope selection={selection}>
          <Table
            id="users"
            columns={userColumns}
            data={users}
            rowKey={(u) => u.id}
            rowClassName={(u) => !u.enabled ? 'disabled-row' : undefined}
            emptyText={boldMarkup(t('users.noUsers'))}
            selection={selection}
            onRowClick={(u, _i, origin) => info.show({ user: u, tab: 'details' }, origin)}
          />
        </ListScope>
      </Card>

      <UserModal
        {...edit.modalProps}
        editing={edit.data ? users.find((u) => u.id === edit.data) || null : null}
      />

      {ie.modal}

      <ShareUserModal
        {...share.modalProps}
        user={share.data ? users.find((u) => u.id === share.data) || null : null}
      />

      <UserInfoModal
        {...info.modalProps}
        user={infoUser}
        tab={info.data?.tab ?? 'details'}
        sessions={(infoUser && sessionsByUser.get(infoUser.username)) || noSessions}
        conflicts={infoUser ? conflicts[infoUser.username] : undefined}
        onKick={handleKick}
        onEdit={(u, from) => { info.close(); edit.show(u.id, from); }}
        onShare={(u, from) => share.show(u.id, from)}
      />
    </div>
  );
}
