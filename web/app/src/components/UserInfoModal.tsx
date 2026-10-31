import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { QRCodeCanvas } from 'qrcode.react';
import {
  Modal, Button, Badge, Table, Tabs, TabPanel, Tooltip, DetailGrid, FieldLabel, FormStack, IconButton, IconUnplug,
  useToast, type Column, type DetailItem,
} from '@hy2scale/ui';
import type { UserConfig, Session } from '@/api';
import * as api from '@/api';
import { useNodeStore } from '@/store/node';
import { PROXY_REGISTRY, DETAIL_PROXIES, type ProxyContext } from '@/config/proxyRegistry';
import { fmtBytes, fmtDuration } from '@/hooks/useFormat';
import { isPast } from '@/utils/date';
import { copyToClipboard } from '@/utils/clipboard';
import { ExitViaCell } from '@/components/ExitViaCell';
import UsageMeter from '@/components/UsageMeter';

export type UserInfoTab = 'details' | 'proxies';
const TABS: UserInfoTab[] = ['details', 'proxies'];

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
}

/** A user: account state and live devices, and the share links per proxy. */
export default function UserInfoModal({ open, onClose, user, tab: initialTab, animateFrom, sessions = [], conflicts, onKick, onEdit }: Props) {
  const { t } = useTranslation();
  const [tab, setTab] = useState<UserInfoTab>(initialTab);
  // each opening starts on the tab it was opened for
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) setTab(initialTab);
  }
  if (!user) return null;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={user.username}
      animateFrom={animateFrom}
      wide
      footer={
        <>
          <span className="spacer" />
          <Button onClick={onClose}>{t('app.close')}</Button>
          {onEdit && (
            <Button
              variant="primary"
              data-testid="user-info-edit"
              onClick={(ev) => {
                const r = (ev.currentTarget as HTMLElement).getBoundingClientRect();
                onEdit(user, { x: r.left + r.width / 2, y: r.top + r.height / 2 });
              }}
            >{t('app.edit')}</Button>
          )}
        </>
      }
    >
      <FormStack gap={14}>
        <Tabs
          items={[
            { key: 'details', label: t('users.detailsTab') },
            { key: 'proxies', label: t('users.proxiesTab') },
          ]}
          activeKey={tab}
          onChange={(k) => setTab(k as UserInfoTab)}
        />
        <TabPanel activeKey={tab} keys={TABS}>
          {tab === 'details'
            ? <DetailsTab user={user} sessions={sessions} conflicts={conflicts} onKick={onKick} />
            : <ProxiesTab user={user} open={open} />}
        </TabPanel>
      </FormStack>
    </Modal>
  );
}

function DetailsTab({ user, sessions, conflicts, onKick }: {
  user: UserConfig; sessions: Session[]; conflicts?: Record<string, string[]>; onKick?: (s: Session) => void;
}) {
  const { t } = useTranslation();
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
    { key: 'proxy', title: t('devices.proxy'), sortValue: (s) => s.protocol, render: (s) => (s.protocol === 'hy2-node' ? t('devices.hy2Node') : s.protocol) },
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
  );
}

/** The user's share link per proxy, each with a QR code on hover. */
function ProxiesTab({ user, open }: { user: UserConfig; open: boolean }) {
  const { t } = useTranslation();
  const toast = useToast();
  const node = useNodeStore((s) => s.node);
  const { data: proxies = [] } = useQuery({ queryKey: ['proxies'], queryFn: api.getProxies, enabled: open });
  const { data: ssConfig } = useQuery({ queryKey: ['ss'], queryFn: api.getSS, enabled: open });

  const host = window.location.hostname || 'localhost';
  const serverPort = node?.server?.listen?.replace(/.*:/, '') || '5565';
  const ctx: ProxyContext = { serverPort, ssConfig, proxies };
  const links = DETAIL_PROXIES.map((def) => {
    const port = def.getPort(ctx);
    const pw = (def.authType === 'password' && user.proxy_passwords?.[def.key]) || user.password;
    return { label: def.label, url: def.buildUrl({ host, port, username: user.username, password: pw }, ctx) };
  });

  const handleCopy = (url: string) => {
    copyToClipboard(url).then((ok) => { if (ok) toast.success(t('app.copied')); });
  };

  return (
    <FormStack gap={12}>
      {links.map((link) => (
        <div key={link.label}>
          <FieldLabel>{link.label}</FieldLabel>
          <Tooltip content={
            <QRCodeCanvas value={link.url} size={220} bgColor="#ffffff" fgColor="#000000" level="M" includeMargin />
          }>
            <div className="hy-link-box" onClick={() => handleCopy(link.url)}>
              {link.url}
            </div>
          </Tooltip>
        </div>
      ))}
    </FormStack>
  );
}
