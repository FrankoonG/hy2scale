import { useTranslation } from 'react-i18next';
import {
  Modal, Button, Badge, Table, DetailGrid, FieldLabel, FormStack, IconButton, IconUnplug,
  type Column, type DetailItem,
} from '@hy2scale/ui';
import type { UserConfig, Session } from '@/api';
import { PROXY_REGISTRY } from '@/config/proxyRegistry';
import { fmtBytes, fmtDuration } from '@/hooks/useFormat';
import { isPast } from '@/utils/date';
import { ExitViaCell } from '@/components/ExitViaCell';
import UsageMeter from '@/components/UsageMeter';

interface Props {
  open: boolean;
  onClose: () => void;
  user: UserConfig | null;
  animateFrom?: { x: number; y: number };
  /** This user's live sessions (the page's polled query). */
  sessions?: Session[];
  /** proxy → other usernames sharing this user's password there. */
  conflicts?: Record<string, string[]>;
  onKick?: (s: Session) => void;
}

/** Row-click details: account state, then the user's live devices. */
export default function UserDetailModal({ open, onClose, user, animateFrom, sessions = [], conflicts, onKick }: Props) {
  const { t } = useTranslation();
  if (!user) return null;

  const denied = (user.proxy_disabled || []).map((k) => PROXY_REGISTRY.find((p) => p.key === k)?.label || k);
  const conflictEntries = Object.entries(conflicts || {});

  const fields: DetailItem[] = [
    [t('users.status'), user.enabled
      ? <Badge variant="green">{t('app.enabled')}</Badge>
      : <Badge variant="muted">{t('users.disabled')}</Badge>],
    [t('users.traffic'), <UsageMeter used={user.traffic_used} limit={user.traffic_limit} />],
    [t('users.expiry'), <span className={`cell-small${isPast(user.expiry_date) ? ' text-bad' : ''}`}>{user.expiry_date || '—'}</span>],
    [t('users.exitVia'), <ExitViaCell exitVia={user.exit_via} exitPaths={user.exit_paths} exitMode={user.exit_mode} />],
  ];
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
    { key: 'proxy', title: t('devices.proxy'), sortValue: (s) => s.protocol, render: (s) => s.protocol },
    { key: 'ip', title: t('devices.ip'), sortValue: (s) => s.remote_ip, render: (s) => <span className="mono">{s.remote_ip}</span> },
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
        <IconButton icon={IconUnplug} label={t('devices.kick')} variant="subtle" tone="danger" onClick={() => onKick?.(s)} />
      ),
    },
  ] as Column<Session>[]).map((c) => ({ ...c, hideable: false }));

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={user.username}
      animateFrom={animateFrom}
      wide
      footer={<Button onClick={onClose}>{t('app.close')}</Button>}
    >
      <FormStack gap={16}>
        <DetailGrid variant="plain" items={fields} />
        <div>
          <FieldLabel>{t('devices.title')} ({sessions.length})</FieldLabel>
          <Table
            id="users.detail.devices"
            columns={deviceColumns}
            data={sessions}
            rowKey={(s) => s.key}
            emptyText={t('devices.noDevices')}
            paginate={false}
          />
        </div>
      </FormStack>
    </Modal>
  );
}
