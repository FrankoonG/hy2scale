import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Modal, Button, Badge, Table, type Column } from '@hy2scale/ui';
import type { UserConfig, Session } from '@/api';
import { PROXY_REGISTRY } from '@/config/proxyRegistry';
import { fmtBytes, fmtDuration } from '@/hooks/useFormat';
import { ExitViaCell } from '@/components/ExitViaCell';

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

const labelStyle = { fontSize: 11, fontWeight: 600, color: 'var(--text-muted)', textTransform: 'uppercase' as const, letterSpacing: '.5px', marginBottom: 4 };

function Field({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div style={{ minWidth: 0 }}>
      <div style={labelStyle}>{label}</div>
      <div style={{ fontSize: 13 }}>{children}</div>
    </div>
  );
}

function isExpired(date?: string) {
  if (!date) return false;
  const ts = Date.parse(date);
  return !Number.isNaN(ts) && ts < Date.now();
}

/** Row-click details: account state, then the user's live devices. */
export default function UserInfoModal({ open, onClose, user, animateFrom, sessions = [], conflicts, onKick }: Props) {
  const { t } = useTranslation();
  if (!user) return null;

  const denied = (user.proxy_disabled || []).map((k) => PROXY_REGISTRY.find((p) => p.key === k)?.label || k);
  const conflictEntries = Object.entries(conflicts || {});
  const pct = user.traffic_limit > 0 ? Math.min(100, user.traffic_used / user.traffic_limit * 100) : 0;
  const trafficColor = pct > 90 ? 'var(--red)' : 'var(--primary)';

  // A short fixed list: sortable, no column menu.
  const deviceColumns: Column<Session>[] = ([
    { key: 'proxy', title: t('devices.proxy'), sortValue: (s) => s.protocol, render: (s) => s.protocol },
    { key: 'ip', title: t('devices.ip'), sortValue: (s) => s.remote_ip, render: (s) => <span className="mono">{s.remote_ip}</span> },
    { key: 'conn', title: t('devices.conn'), sortValue: (s) => s.conn_count, render: (s) => s.conn_count },
    {
      key: 'traffic', title: t('devices.traffic'),
      sortValue: (s) => s.tx_bytes + s.rx_bytes,
      render: (s) => (
        <span className="mono" style={{ fontSize: 12 }}>
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
        <button className="hy-row-edit hy-row-kick danger" onClick={() => onKick?.(s)} title={t('devices.kick')}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/>
            <polyline points="16 17 21 12 16 7"/>
            <line x1="21" y1="12" x2="9" y2="12"/>
          </svg>
        </button>
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
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))', gap: '12px 20px' }}>
          <Field label={t('users.status')}>
            {user.enabled
              ? <Badge variant="green">{t('app.enabled')}</Badge>
              : <Badge variant="muted">{t('users.disabled')}</Badge>}
          </Field>
          <Field label={t('users.traffic')}>
            <span style={{ fontSize: 12 }}>
              <span style={{ color: trafficColor, fontWeight: 600 }}>{fmtBytes(user.traffic_used)}</span>
              <span className="sep">/</span>
              <span style={{ color: 'var(--text-muted)' }}>{user.traffic_limit > 0 ? fmtBytes(user.traffic_limit) : '∞'}</span>
            </span>
            {user.traffic_limit > 0 && (
              <div style={{ background: 'var(--border-light)', height: 3, borderRadius: 2, marginTop: 3 }}>
                <div style={{ background: trafficColor, height: '100%', width: `${pct}%`, borderRadius: 2 }} />
              </div>
            )}
          </Field>
          <Field label={t('users.expiry')}>
            <span style={{ fontSize: 12, color: isExpired(user.expiry_date) ? 'var(--red)' : undefined }}>{user.expiry_date || '—'}</span>
          </Field>
          <Field label={t('users.exitVia')}>
            <ExitViaCell exitVia={user.exit_via} exitPaths={user.exit_paths} exitMode={user.exit_mode} />
          </Field>
          {denied.length > 0 && (
            <Field label={t('users.deniedProxies')}>
              <span style={{ display: 'inline-flex', flexWrap: 'wrap', gap: 4 }}>
                {denied.map((d) => <Badge key={d} variant="red">{d}</Badge>)}
              </span>
            </Field>
          )}
          {conflictEntries.length > 0 && (
            <Field label={t('users.conflicts')}>
              <div style={{ fontSize: 12, color: 'var(--red)' }}>
                {conflictEntries.map(([proxy, others]) => (
                  <div key={proxy}>{proxy}: {others.join(', ')}</div>
                ))}
              </div>
            </Field>
          )}
        </div>

        <div>
          <div style={labelStyle}>{t('devices.title')} ({sessions.length})</div>
          <Table
            id="users.detail.devices"
            columns={deviceColumns}
            data={sessions}
            rowKey={(s) => s.key}
            emptyText={t('devices.noDevices')}
            paginate={false}
          />
        </div>
      </div>
    </Modal>
  );
}
