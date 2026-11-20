import { useState, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';
import { useStoredFeatures } from './FeatureGate';
import type { AuditFrom } from '@/pages/AuditPage';
import {
  Modal, Button, Badge, Table, Tabs, TabPanel, DetailGrid, FieldLabel, FormStack, Hint, IconButton, IconUnplug,
  useToast, useConfirm, type Column, type DetailItem,
} from '@hy2scale/ui';
import type { UserConfig, Session } from '@/api';
import * as api from '@/api';
import type { SharedMember, SharedMemberState } from '@/api/types';
import { proxyLabel } from '@/config/proxyRegistry';
import { getUserLinks, type ProxyLink } from '@/api/links';
import { fmtBytes, fmtDuration } from '@/hooks/useFormat';
import { isPast } from '@/utils/date';
import { copyToClipboard } from '@/utils/clipboard';
import { ExitViaCell } from '@/components/ExitViaCell';
import UsageMeter from '@/components/UsageMeter';
import { useUserActions, isLeaving } from '@/hooks/useUserActions';
import { fmtRateLimit } from './RateInput';
import { userKind, usePolicySets, rowSets, KIND_LABEL } from './users/userKind';
import { LinkBox } from './users/LinkBox';
import UserSubscriptionTab from './users/UserSubscriptionTab';
import { resetText, resetUnit, extendedExpiry, extendByText } from './users/trafficReset';

export type UserInfoTab = 'details' | 'proxies' | 'subscription';
const TABS: UserInfoTab[] = ['details', 'proxies', 'subscription'];

interface Props {
  open: boolean;
  onClose: () => void;
  user: UserConfig | null;
  /** The tab shown when the dialog opens. */
  tab: UserInfoTab;
  animateFrom?: { x: number; y: number };
  /** This user's live sessions (the page's polled query). */
  sessions?: Session[];
  /** proxy → other usernames sharing this user's password there. */
  conflicts?: Record<string, string[]>;
  onKick?: (s: Session) => void;
  /** Opens the edit dialog of this user (from the Edit button). */
  onEdit?: (u: UserConfig, from: { x: number; y: number }) => void;
  /** Opens the share dialog of this user (from the Share button). */
  onShare?: (u: UserConfig, from: { x: number; y: number }) => void;
}

/** Centre of the clicked button: where the next dialog grows from. */
const centreOf = (ev: MouseEvent) => {
  const r = (ev.currentTarget as HTMLElement).getBoundingClientRect();
  return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
};

/** A user: account state and live devices, the share links per proxy, and the subscription. */
export default function UserInfoModal({ open, onClose, user, tab: initialTab, animateFrom, sessions = [], conflicts, onKick, onEdit, onShare }: Props) {
  const { t } = useTranslation();
  const actions = useUserActions();
  const [tab, setTab] = useState<UserInfoTab>(initialTab);
  // each opening starts on the tab it was opened for
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) setTab(initialTab);
  }
  if (!user) return null;
  // the tab shows a subscription that is on (node access users have none)
  const hasSub = userKind(user) !== 'access' && !!user.sub?.token;
  const shown = tab === 'subscription' && !hasSub ? 'details' : tab;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={user.username}
      animateFrom={animateFrom}
      wide
      footer={
        <>
          <div className="modal-footer-group">
            <Button data-testid="user-info-toggle" onClick={() => actions.toggle([user], !user.enabled)}>
              {user.enabled ? t('app.bulkDisable') : t('app.bulkEnable')}
            </Button>
            {onShare && (
              <Button data-testid="user-info-share" onClick={(ev) => onShare(user, centreOf(ev))}>{t('users.share')}</Button>
            )}
            {user.shared && (
              <Button variant="danger" data-testid="user-info-delete-everywhere"
                onClick={async () => { if (await actions.deleteEverywhere(user)) onClose(); }}>
                {t('users.deleteEverywhere')}
              </Button>
            )}
            <Button variant="danger" data-testid="user-info-delete"
              onClick={async () => { if (await actions.remove([user])) onClose(); }}>
              {isLeaving([user]) ? t('users.leave') : t('app.bulkDelete')}
            </Button>
          </div>
          <span className="spacer" />
          <Button onClick={onClose}>{t('app.close')}</Button>
          {onEdit && (
            <Button variant="primary" data-testid="user-info-edit" onClick={(ev) => onEdit(user, centreOf(ev))}>
              {t('app.edit')}
            </Button>
          )}
        </>
      }
    >
      <FormStack gap={14}>
        <Tabs
          items={[
            { key: 'details', label: t('users.detailsTab') },
            { key: 'proxies', label: t('users.proxiesTab') },
            ...(hasSub ? [{ key: 'subscription', label: t('users.subscriptionTab') }] : []),
          ]}
          activeKey={shown}
          onChange={(k) => setTab(k as UserInfoTab)}
        />
        <TabPanel activeKey={shown} keys={TABS}>
          {shown === 'details' ? <DetailsTab user={user} sessions={sessions} conflicts={conflicts} onKick={onKick} />
            : shown === 'proxies' ? <ProxiesTab user={user} open={open} />
              : <UserSubscriptionTab userId={user.id} />}
        </TabPanel>
      </FormStack>
    </Modal>
  );
}

function DetailsTab({ user, sessions, conflicts, onKick }: {
  user: UserConfig; sessions: Session[]; conflicts?: Record<string, string[]>; onKick?: (s: Session) => void;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();
  const actions = useUserActions();
  const sets = usePolicySets();
  const kind = userKind(user);
  const navigate = useNavigate();
  const stored = useStoredFeatures();
  const [extending, setExtending] = useState(false);
  const confirm = useConfirm();
  const extend = async () => {
    const unit = resetUnit(user.traffic_reset);
    const ok = await confirm({
      title: t('users.extend'),
      message: t('users.extendConfirm', { by: extendByText(t, unit, user.traffic_reset_every), from: user.expiry_date,
        to: extendedExpiry(user.expiry_date || '', unit, user.traffic_reset_every) }),
      confirmText: t('users.extend'), cancelText: t('app.cancel'),
    });
    if (!ok) return;
    setExtending(true);
    try {
      const r = await api.extendUser(user.id);
      toast.success(t('users.extended', { date: r.expiry_date }));
      queryClient.invalidateQueries({ queryKey: ['users'] });
    } catch (e: any) {
      toast.error(String(e?.message || e));
    } finally { setExtending(false); }
  };
  const extendBtn = user.expiry_date ? (
    <Button size="sm" variant="ghost" className="inline-btn" loading={extending} onClick={extend} data-testid="user-extend">
      {t('users.extend')}
    </Button>
  ) : null;
  const hasReset = !!resetUnit(user.traffic_reset);
  const denied = kind === 'access' ? [] : (user.proxy_disabled || []).map(proxyLabel);
  const conflictEntries = Object.entries(conflicts || {});

  const fields: DetailItem[] = [
    [t('users.status'), user.enabled
      ? <Badge variant="green">{t('app.enabled')}</Badge>
      : <Badge variant="muted">{t('users.disabled')}</Badge>],
    [t('users.traffic'), (
      <div className="inline-line">
        <div className="inline-line-main"><UsageMeter used={user.traffic_used} limit={user.traffic_limit} /></div>
        <Button size="sm" variant="ghost" className="inline-btn" onClick={() => actions.reset([user])} data-testid="user-reset-traffic">
          {t('users.reset')}
        </Button>
      </div>
    )],
    [t('users.rateLimit'), (
      <span className="cell-small" data-testid="user-rate-info">
        {user.rate_up || user.rate_down
          ? `↑ ${user.rate_up ? fmtRateLimit(user.rate_up) : '∞'} · ↓ ${user.rate_down ? fmtRateLimit(user.rate_down) : '∞'}`
          : t('users.rateUnlimited')}
      </span>
    )],
    [t('users.expiry'), (
      <span className="inline-line">
        <span className={`cell-small${isPast(user.expiry_date) ? ' text-bad' : ''}`}>{user.expiry_date || '—'}</span>
        {/* no reset period: extending (by a month) sits with the expiry */}
        {!hasReset && extendBtn}
      </span>
    )],
    [t('users.exitKind'), <span data-testid="user-kind-info">{t(KIND_LABEL[kind])}</span>],
  ];
  if (kind === 'group') {
    fields.push([t('users.exitVia'), <ExitViaCell exitVia={user.exit_via} exitPaths={user.exit_paths} exitMode={user.exit_mode} />]);
  } else if (kind === 'policy') {
    fields.push([t('users.passwordRows'), (
      <div className="cell-small" data-testid="user-rows-info">
        {rowSets(user, sets).map((r, i) => (
          <div key={r.id}>#{i + 1} {r.name || <span className="text-muted">{t('users.rowDisabled')}</span>}</div>
        ))}
      </div>
    )]);
  } else {
    fields.push([t('users.accessLevel'), user.node_access_mode === 'full' ? t('users.accessFull') : t('users.accessRestricted')]);
  }
  if (hasReset) {
    const next = user.traffic_reset_next ? new Date(user.traffic_reset_next) : null;
    fields.splice(2, 0, [t('users.trafficReset'), (
      <span className="inline-line">
        <span className="cell-small" data-testid="user-reset-info">
          {resetText(t, resetUnit(user.traffic_reset), user.traffic_reset_every)}
          {next && !isNaN(next.getTime()) && <> · {t('users.reset.next', { time: next.toLocaleString() })}</>}
        </span>
        {extendBtn}
      </span>
    )]);
  }
  if (denied.length > 0) {
    fields.push([t('users.deniedProxies'), (
      <span className="badge-list">{denied.map((d) => <Badge key={d} variant="red">{d}</Badge>)}</span>
    )]);
  }
  if (conflictEntries.length > 0) {
    fields.push([t('users.conflicts'), (
      <div className="cell-small text-bad">
        {conflictEntries.map(([proxy, others]) => <div key={proxy}>{proxy}: {others.join(', ')}</div>)}
      </div>
    )]);
  }

  // A short fixed list: sortable, no column menu.
  const deviceColumns: Column<Session>[] = ([
    { key: 'proxy', title: t('devices.proxy'), sortValue: (s) => s.protocol, render: (s) => (s.protocol === 'hy2-node' ? t('devices.hy2Node') : s.protocol) },
    { key: 'ip', title: t('devices.source'), sortValue: (s) => deviceSource(s), render: (s) => <span className="mono">{deviceSource(s)}</span> },
    {
      key: 'tunnel', title: t('devices.tunnelIp'), sortValue: (s) => (s.peer ? s.remote_ip : ''),
      render: (s) => (s.peer ? <span className="mono">{s.remote_ip}</span> : <span className="muted">—</span>),
    },
    { key: 'conn', title: t('devices.conn'), sortValue: (s) => s.conn_count, render: (s) => s.conn_count },
    {
      key: 'traffic', title: t('devices.traffic'),
      sortValue: (s) => s.tx_bytes + s.rx_bytes,
      render: (s) => (
        <span className="mono">
          <span className="stat-up">↑{fmtBytes(s.tx_bytes)}</span>
          <span className="sep">/</span>
          <span className="stat-down">↓{fmtBytes(s.rx_bytes)}</span>
        </span>
      ),
    },
    { key: 'duration', title: t('devices.duration'), sortValue: (s) => s.duration, render: (s) => fmtDuration(s.duration) },
    {
      key: 'actions', title: '', width: '48px',
      render: (s) => (
        <IconButton icon={IconUnplug} label={t('devices.kick')} variant="subtle" tone="danger"
          onClick={(e) => { e.stopPropagation(); onKick?.(s); }} />
      ),
    },
  ] as Column<Session>[]).map((c) => ({ ...c, hideable: false }));

  return (
    <FormStack gap={16}>
      <DetailGrid variant="plain" items={fields} />
      {user.shared && <SharedMembers members={user.shared.members} />}
      <div>
        <FieldLabel>{t('devices.title')} ({sessions.length})</FieldLabel>
        <Table
          id="users.detail.devices"
          columns={deviceColumns}
          data={sessions}
          rowKey={(s) => s.key}
          onRowClick={stored.audit ? (s) => navigate('/audit', { state: {
            user: s.username, src: deviceSource(s), inbound: s.protocol === 'hy2-node' ? 'access' : s.protocol,
          } satisfies AuditFrom }) : undefined}
          emptyText={t('devices.noDevices')}
          paginate={false}
        />
      </div>
    </FormStack>
  );
}

/** Where a device connects from: a VPN client's public address, else its own. */
function deviceSource(s: Session): string {
  if (!s.peer) return s.remote_ip;
  const m = /^\[(.+)\]:\d+$/.exec(s.peer) || /^([^:]+):\d+$/.exec(s.peer);
  return m ? m[1] : s.peer;
}

const STATE_BADGE: Record<SharedMemberState, 'green' | 'muted' | 'red' | 'orange'> = {
  synced: 'green', pending: 'muted', error: 'red', unreachable: 'orange',
};

/** The nodes holding a shared user: sync state and traffic counted on each. */
function SharedMembers({ members }: { members: SharedMember[] }) {
  const { t } = useTranslation();
  const columns: Column<SharedMember>[] = ([
    {
      key: 'name', title: t('users.memberNode'), sortValue: (m) => m.name,
      render: (m) => (
        <span className="name-with-mark">
          {m.name}
          {m.self && <Badge variant="blue">{t('users.thisNode')}</Badge>}
        </span>
      ),
    },
    {
      key: 'state', title: t('users.memberSync'), sortValue: (m) => m.state,
      render: (m) => (m.self
        ? <span className="text-muted">—</span>
        : <Badge variant={STATE_BADGE[m.state] || 'muted'}>{t(`users.memberState.${m.state}`, { defaultValue: m.state })}</Badge>),
    },
    { key: 'used', title: t('users.memberUsed'), sortValue: (m) => m.used, render: (m) => <span className="mono">{fmtBytes(m.used)}</span> },
    {
      key: 'error', title: t('users.memberError'),
      render: (m) => (m.error && (m.state === 'error' || m.state === 'unreachable')
        ? <span className="cell-small text-bad">{m.error}</span>
        : <span className="text-muted">—</span>),
    },
  ] as Column<SharedMember>[]).map((c) => ({ ...c, hideable: false }));
  return (
    <div data-testid="shared-members">
      <FieldLabel>{t('users.members')} ({members.length})</FieldLabel>
      <Table
        id="users.detail.members"
        columns={columns}
        data={members}
        rowKey={(m) => m.node_id || m.name}
        paginate={false}
      />
    </div>
  );
}

/**
 * The user's share links per proxy (public host, then LAN), each with a QR
 * code on hover; a policy-kind user's grouped by password row.
 */
function ProxiesTab({ user, open }: { user: UserConfig; open: boolean }) {
  const { t } = useTranslation();
  const toast = useToast();
  const { data: all = [] } = useQuery({ queryKey: ['userLinks', user.id], queryFn: () => getUserLinks(user.id, window.location.hostname), enabled: open });
  // the links come row by row: a new group where the label changes
  const groups: { label?: string; links: ProxyLink[] }[] = [];
  for (const l of all) {
    const last = groups[groups.length - 1];
    if (last && last.label === l.label) last.links.push(l);
    else groups.push({ label: l.label, links: [l] });
  }

  const handleCopy = (url: string) => {
    copyToClipboard(url).then((ok) => { if (ok) toast.success(t('app.copied')); });
  };
  const caption = (l: ProxyLink) => proxyLabel(l.proto) + (l.lan ? ` · ${t('users.linkLAN')} ${l.host}` : '');

  return (
    <FormStack gap={12}>
      {all.length === 0 && <Hint>{t('users.noLinks')}</Hint>}
      {groups.map((g, gi) => {
        const links = g.links.map((l) => (
          <div key={l.url}>
            <FieldLabel>{caption(l)}</FieldLabel>
            <LinkBox url={l.url} onCopy={handleCopy} />
          </div>
        ));
        return g.label
          ? (
            <div key={gi} className="user-link-group" data-testid="user-link-group">
              <div className="user-link-group-title">{g.label}</div>
              {links}
            </div>
          )
          : links;
      })}
    </FormStack>
  );
}
