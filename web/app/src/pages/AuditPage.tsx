import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import {
  Autocomplete, Badge, Button, Card, CheckSelect, DetailGrid, Input, Modal, SegmentedControl, Table, Toggle,
  useModalState, useStoredState, boolCodec, type Column, type DetailItem,
} from '@hy2scale/ui';
import { getUsers } from '@/api';
import {
  getAudit, getAuditStats, auditExportUrl, type AuditFilter, type AuditRecord, type AuditStats,
} from '@/api/storage';
import { GeoDetails, IPFlag } from '@/components/CountryFlag';
import { useGeo, hostOf, isIPLiteral } from '@/hooks/useGeo';
import { fmtBytes } from '@/hooks/useFormat';

/*
 * Audit: the flows this node handled, newest first. Filters run on the node;
 * pages are fetched with its cursor ("Load more").
 */

type Range = '1h' | '24h' | '7d' | '30d' | 'all';
const RANGE_MS: Record<Range, number> = { '1h': 3600e3, '24h': 86400e3, '7d': 7 * 86400e3, '30d': 30 * 86400e3, all: 0 };
const PAGE = 200;
const REFRESH_MS = 10_000;
const INBOUNDS = ['local', 'forward', 'socks5', 'http', 'ss', 'trojan', 'hy2', 'ikev2', 'l2tp', 'openvpn', 'wireguard', 'access', 'relay'];

/** Display names of application protocols (technical identifiers). */
const APP_NAMES: Record<string, string> = {
  tls: 'TLS', http: 'HTTP', http2: 'HTTP/2', quic: 'QUIC', dns: 'DNS', mdns: 'mDNS', ssh: 'SSH', rdp: 'RDP', vnc: 'VNC',
  smb: 'SMB', smtp: 'SMTP', ftp: 'FTP', pop3: 'POP3', imap: 'IMAP', mysql: 'MySQL', postgres: 'PostgreSQL', redis: 'Redis',
  mqtt: 'MQTT', xmpp: 'XMPP', socks5: 'SOCKS5', bittorrent: 'BitTorrent', stun: 'STUN', ntp: 'NTP', dtls: 'DTLS',
  wireguard: 'WireGuard', openvpn: 'OpenVPN', ike: 'IKE', ssdp: 'SSDP', sip: 'SIP', tcp: 'TCP', udp: 'UDP',
};
const APP_FILTER = ['tls', 'http', 'quic', 'dns', 'ssh', 'stun', 'wireguard', 'bittorrent', 'rdp', 'smtp', 'ntp', '-'];
const appName = (k: string) => APP_NAMES[k] ?? k.toUpperCase();

/** The ticked lists of the filters: an unticked value is left out. */
type Group = 'inbound' | 'exit' | 'proto' | 'app' | 'result';
const GROUPS: Group[] = ['inbound', 'exit', 'proto', 'app', 'result'];
const GROUP_VALUES: Record<Group, string[]> = {
  inbound: INBOUNDS,
  exit: ['direct', 'node', 'blocked'],
  proto: ['tcp', 'udp'],
  app: APP_FILTER,
  result: ['ok', 'error', 'refused', 'blocked'],
};
type Hidden = Record<Group, string[]>;
const NONE_HIDDEN: Hidden = { inbound: [], exit: [], proto: [], app: [], result: [] };

const pad = (n: number) => String(n).padStart(2, '0');
function fmtTime(ms: number): string {
  const d = new Date(ms);
  const time = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  return d.toDateString() === new Date().toDateString() ? time : `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${time}`;
}
function fmtDur(ms: number): string {
  if (ms < 1000) return `${ms} ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(s < 10 ? 1 : 0)} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${Math.round(s % 60)}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

/** Debounced copy of a value. */
function useDebounced<T>(v: T, ms: number): T {
  const [d, setD] = useState(v);
  useEffect(() => { const id = setTimeout(() => setD(v), ms); return () => clearTimeout(id); }, [v, ms]);
  return d;
}

export default function AuditPage() {
  const { t } = useTranslation();
  const [range, setRange] = useStoredState<Range>('hy-audit-range', '24h');
  const [auto, setAuto] = useStoredState('hy-audit-auto', false, boolCodec);
  const [q, setQ] = useState('');
  const [src, setSrc] = useState('');
  const [dst, setDst] = useState('');
  const [hidden, setHidden] = useState<Hidden>(NONE_HIDDEN);
  // one debounce per field: an object made each render never settles and
  // reloaded the page in a loop
  const dq = useDebounced(q, 400);
  const dsrc = useDebounced(src, 400);
  const ddst = useDebounced(dst, 400);

  const [records, setRecords] = useState<AuditRecord[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [truncated, setTruncated] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [stats, setStats] = useState<AuditStats | null>(null);
  const [statsLoading, setStatsLoading] = useState(false);
  // a newer request cancels the one before (the node stops that scan too)
  const listCtl = useRef<AbortController | null>(null);
  const statsCtl = useRef<AbortController | null>(null);
  useEffect(() => () => { listCtl.current?.abort(); statsCtl.current?.abort(); }, []);

  const filter = useMemo<AuditFilter>(() => ({
    from: RANGE_MS[range] ? Date.now() - RANGE_MS[range] : undefined,
    q: dq.trim(), src: dsrc.trim(), dst: ddst.trim(),
    not_inbound: hidden.inbound.join(','), not_exit: hidden.exit.join(','), not_proto: hidden.proto.join(','),
    not_app: hidden.app.join(','), not_result: hidden.result.join(','),
  }), [range, dq, dsrc, ddst, hidden]);

  const load = useCallback(async (more: boolean) => {
    listCtl.current?.abort();
    const c = new AbortController();
    listCtl.current = c;
    setLoading(true);
    setError('');
    try {
      const f = more ? filter : { ...filter, from: RANGE_MS[range] ? Date.now() - RANGE_MS[range] : undefined };
      const page = await getAudit(f, PAGE, more ? cursor : undefined, c.signal);
      if (c.signal.aborted) return;
      setRecords((prev) => (more ? prev.concat(page.records) : page.records));
      setCursor(page.next_cursor || undefined);
      setTruncated(!!page.truncated);
    } catch (e: any) {
      if (!c.signal.aborted) setError(String(e?.message || e));
    } finally {
      if (listCtl.current === c) setLoading(false);
    }
  }, [filter, cursor, range]);

  // the summary (whole range, no filters) loads on its own
  const loadStats = useCallback(async () => {
    statsCtl.current?.abort();
    const c = new AbortController();
    statsCtl.current = c;
    setStatsLoading(true);
    try {
      const st = await getAuditStats({ from: RANGE_MS[range] ? Date.now() - RANGE_MS[range] : undefined }, c.signal);
      if (!c.signal.aborted) setStats(st);
    } catch {
      if (!c.signal.aborted) setStats(null);
    } finally {
      if (statsCtl.current === c) setStatsLoading(false);
    }
  }, [range]);

  const refresh = useCallback(() => { load(false); loadStats(); }, [load, loadStats]);
  useEffect(() => { load(false); }, [filter]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { loadStats(); }, [loadStats]);
  useEffect(() => {
    if (!auto) return;
    const id = setInterval(refresh, REFRESH_MS);
    return () => clearInterval(id);
  }, [auto, refresh]);

  const detail = useModalState<AuditRecord>();

  const columns: Column<AuditRecord>[] = [
    { key: 'time', title: t('audit.time'), render: (r) => <span className="mono nowrap">{fmtTime(r.start)}</span>, sortValue: (r) => r.start, hideable: false },
    { key: 'dur', title: t('audit.duration'), render: (r) => <span className="nowrap">{fmtDur(Math.max(0, r.end - r.start))}{r.partial ? ' …' : ''}</span>, sortValue: (r) => r.end - r.start },
    { key: 'proto', title: t('audit.proto'), render: (r) => <ProtoCell r={r} />, sortValue: (r) => r.app || r.proto },
    { key: 'inbound', title: t('audit.inbound'), render: (r) => <Badge variant="muted">{t(`audit.in.${r.inbound}`, { defaultValue: r.inbound })}</Badge> },
    { key: 'src', title: t('audit.source'), render: (r) => <Source r={r} />, sortValue: (r) => r.user || r.src || '' },
    { key: 'dst', title: t('audit.destination'), render: (r) => <Dest r={r} />, sortValue: (r) => r.dst_host || r.dst || '' },
    { key: 'policy', title: t('audit.policy'), render: (r) => <PolicyCell r={r} />, sortValue: (r) => r.rule || '' },
    { key: 'link', title: t('audit.link'), render: (r) => <Link r={r} />, defaultHidden: true },
    { key: 'exit', title: t('audit.exit'), render: (r) => <ExitCell r={r} /> },
    { key: 'egress', title: t('audit.egress'), render: (r) => <Egress r={r} />, defaultHidden: true },
    { key: 'up', title: '↑', menuTitle: t('audit.up'), render: (r) => <span className="nowrap num">{fmtBytes(r.up || 0)}</span>, sortValue: (r) => r.up || 0, className: 'num' },
    { key: 'down', title: '↓', menuTitle: t('audit.down'), render: (r) => <span className="nowrap num">{fmtBytes(r.down || 0)}</span>, sortValue: (r) => r.down || 0, className: 'num' },
    { key: 'result', title: t('audit.result'), render: (r) => <ResultBadge r={r} /> },
  ];

  // source suggestions: the users, and the biggest sources of the range
  const { data: users } = useQuery({ queryKey: ['users'], queryFn: getUsers, staleTime: 60_000 });
  const srcOptions = useMemo(() => Array.from(new Set([
    ...(users || []).map((u) => u.username),
    ...(stats?.top_users || []).map((x) => x.key),
    ...(stats?.top_sources || []).map((x) => x.key),
  ].filter(Boolean))), [users, stats]);
  const groupLabel = (g: Group, v: string) => {
    switch (g) {
      case 'inbound': return t(`audit.in.${v}`);
      case 'exit': return t(`audit.exitKind.${v}`);
      case 'proto': return v.toUpperCase();
      case 'app': return v === '-' ? t('audit.appUnknown') : appName(v);
      default: return t(`audit.res.${v}`);
    }
  };

  return (
    <div className="hy-page audit-page" data-testid="audit-page">
      <AuditSummary stats={stats} loading={statsLoading} />

      <Card>
        <div className="audit-bar">
          <SegmentedControl<Range>
            items={(['1h', '24h', '7d', '30d', 'all'] as Range[]).map((k) => ({ key: k, label: t(`audit.range.${k}`) }))}
            value={range}
            onChange={setRange}
          />
          <Input className="audit-search" value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('audit.search')} data-testid="audit-q" />
          <span className="spacer" />
          <Toggle size="sm" className="audit-auto" checked={auto} onChange={(e) => setAuto(e.target.checked)} label={t('audit.autoRefresh')} />
          <Button onClick={refresh} loading={loading && !records.length} data-testid="audit-refresh">{t('audit.refresh')}</Button>
          <Button onClick={() => { window.location.href = auditExportUrl(filter, 'csv'); }} data-testid="audit-export">{t('audit.exportCsv')}</Button>
        </div>
        <div className="audit-filters">
          <Autocomplete options={srcOptions} value={src} onChange={setSrc} placeholder={t('audit.sourceFilter')} testId="audit-src" className="audit-src" />
          <Input value={dst} onChange={(e) => setDst(e.target.value)} placeholder={t('audit.destination')} data-testid="audit-dst" />
          {GROUPS.map((g) => (
            <CheckSelect key={g} testId={`audit-group-${g}`} label={t(`audit.group.${g}`)}
              options={GROUP_VALUES[g].map((v) => ({ value: v, label: groupLabel(g, v) }))}
              value={GROUP_VALUES[g].filter((v) => !hidden[g].includes(v))}
              onChange={(on) => setHidden({ ...hidden, [g]: GROUP_VALUES[g].filter((v) => !on.includes(v)) })} />
          ))}
        </div>
      </Card>

      <Card noPadding title={t('audit.flows')} count={records.length}>
        {error && <div className="audit-note error">{error}</div>}
        <Table
          id="audit"
          columns={columns}
          data={records}
          rowKey={(r) => r.id}
          paginate={false}
          emptyText={loading ? t('audit.loading') : t('audit.empty')}
          onRowClick={(r, _i, origin) => detail.show(r, origin)}
        />
        {(cursor || truncated) && (
          <div className="audit-more">
            {truncated && <span className="muted">{t('audit.truncated')}</span>}
            {cursor && <Button onClick={() => load(true)} loading={loading} data-testid="audit-more">{t('audit.loadMore')}</Button>}
          </div>
        )}
      </Card>

      <AuditDetail {...detail.modalProps} record={detail.data} />
    </div>
  );
}

function AuditSummary({ stats, loading }: { stats: AuditStats | null; loading: boolean }) {
  const { t } = useTranslation();
  if (!stats) {
    return loading ? <Card><div className="audit-summary-wait muted" data-testid="audit-summary-loading">{t('audit.loading')}</div></Card> : null;
  }
  const top = (title: string, list?: { key: string; bytes: number; ip?: string }[], addr = false, name?: (k: string) => string) => (
    <div className="audit-top">
      <div className="audit-top-title">{title}</div>
      {(list || []).slice(0, 5).map((x) => (
        <div className="audit-top-row" key={x.key}>
          <span className="audit-top-key">{(x.ip || addr) && <IPFlag addr={x.ip || x.key} />}{x.key ? (name ? name(x.key) : x.key) : '—'}</span>
          <span className="muted num">{fmtBytes(x.bytes)}</span>
        </div>
      ))}
      {!list?.length && <div className="muted">—</div>}
    </div>
  );
  return (
    <Card>
      <div className="audit-summary" data-testid="audit-summary">
        <div className="audit-totals">
          <div><div className="audit-top-title">{t('audit.flows')}</div><div className="audit-big">{stats.flows.toLocaleString()}</div></div>
          <div><div className="audit-top-title">{t('audit.up')}</div><div className="audit-big">{fmtBytes(stats.up)}</div></div>
          <div><div className="audit-top-title">{t('audit.down')}</div><div className="audit-big">{fmtBytes(stats.down)}</div></div>
        </div>
        {top(t('audit.topUsers'), stats.top_users)}
        {top(t('audit.topSources'), stats.top_sources, true)}
        {top(t('audit.topHosts'), stats.top_destinations)}
        {top(t('audit.topApps'), stats.top_apps, false, appName)}
      </div>
      {(stats.truncated || stats.hourly) && (
        <div className="audit-summary-note muted" data-testid="audit-summary-note">
          {stats.truncated ? t('audit.statsTruncated', { n: (stats.scanned ?? 0).toLocaleString() }) : t('audit.statsHourly')}
        </div>
      )}
    </Card>
  );
}

/** Transport, and the application protocol when it was told. */
function ProtoCell({ r }: { r: AuditRecord }) {
  if (!r.app) return <span className="mono">{r.proto.toUpperCase()}</span>;
  return <span className="nowrap"><b>{appName(r.app)}</b> <span className="mono muted">{r.proto.toUpperCase()}</span></span>;
}

/** Who: the user (when any), the client address, and where it came in. */
function Source({ r }: { r: AuditRecord }) {
  const { t } = useTranslation();
  const via = [r.src_node && t('audit.viaNode', { node: r.src_node }), r.in_iface].filter(Boolean).join(' · ');
  // a VPN client: where it connects from, then its tunnel address
  const main = r.src_peer || r.src;
  return (
    <span className="audit-addr">
      {r.user && <b className="nowrap">{r.user}</b>}
      {main && <span className={r.user ? 'mono muted nowrap' : 'mono nowrap'}><IPFlag addr={main} />{main}</span>}
      {r.src_peer && r.src && <span className="mono muted nowrap" data-testid="audit-tunnel">{t('audit.tunnelAddr', { addr: hostOf(r.src) })}</span>}
      {via && <span className="muted nowrap">{via}</span>}
      {!r.user && !r.src && !via && <span className="muted">—</span>}
    </span>
  );
}

/** Inbound interface and source MAC (forwarded traffic). */
function Link({ r }: { r: AuditRecord }) {
  if (!r.in_iface && !r.src_mac) return <span className="muted">—</span>;
  return <span className="mono nowrap">{[r.in_iface, r.src_mac].filter(Boolean).join(' · ')}</span>;
}

const TERM_TONE: Record<string, 'red' | 'blue' | 'muted' | 'green'> = { deny: 'red', exit: 'blue', iface: 'green', default: 'muted' };

/** The policy outcome: its terminal and the rules that led there. */
function PolicyCell({ r }: { r: AuditRecord }) {
  const { t } = useTranslation();
  if (!r.terminal && !r.rule) return <span className="muted">—</span>;
  const why = r.reason ? t(`audit.reason.${r.reason}`, { defaultValue: r.reason }) : '';
  return (
    <span className="audit-addr" title={why || undefined}>
      {r.terminal && <span><Badge variant={TERM_TONE[r.terminal] ?? 'muted'}>{t(`policy.termKind.${r.terminal}`, { defaultValue: r.terminal })}</Badge></span>}
      {r.rule ? <span className="muted nowrap">{r.rule}</span> : why && <span className="muted nowrap">{why}</span>}
    </span>
  );
}
function Dest({ r }: { r: AuditRecord }) {
  const host = r.dst_host || (r.dst ? hostOf(r.dst) : '');
  const ip = r.dst_ip && r.dst_ip !== host ? r.dst_ip : '';
  const port = r.dst ? r.dst.slice(r.dst.lastIndexOf(':') + 1) : '';
  return (
    <span className="audit-addr">
      <span className="nowrap">{isIPLiteral(host) && <IPFlag addr={host} />}<b>{host}</b>{port && <span className="muted">:{port}</span>}</span>
      {ip && <span className="mono muted nowrap"><IPFlag addr={ip} />{ip}</span>}
    </span>
  );
}

function ExitCell({ r }: { r: AuditRecord }) {
  const { t } = useTranslation();
  if (r.exit === 'node' && r.exit_path) {
    return (
      <span className="audit-path nowrap">
        {r.exit_path.split('/').map((h, i) => <span key={i} className="audit-hop">{h}</span>)}
        {r.exit_mode && <span className="muted"> · {t(`audit.mode.${r.exit_mode}`, { defaultValue: r.exit_mode })}</span>}
      </span>
    );
  }
  return <span className="nowrap">{t(`audit.exitKind.${r.exit || 'direct'}`)}</span>;
}

function Egress({ r }: { r: AuditRecord }) {
  if (!r.egress_iface && !r.egress_src) return <span className="muted">—</span>;
  return <span className="mono nowrap">{[r.egress_iface, r.egress_src].filter(Boolean).join(' · ')}</span>;
}

function ResultBadge({ r }: { r: AuditRecord }) {
  const { t } = useTranslation();
  const res = r.result || 'ok';
  const v = res === 'ok' ? 'green' : res === 'blocked' ? 'orange' : 'red';
  return <span title={r.error || undefined}><Badge variant={v}>{t(`audit.res.${res}`)}</Badge></span>;
}

function AuditDetail({ open, onClose, animateFrom, record: r }: { open: boolean; onClose: () => void; animateFrom?: { x: number; y: number }; record?: AuditRecord }) {
  const { t } = useTranslation();
  if (!r) return null;
  const dash = <span className="muted">—</span>;
  const items: DetailItem[] = [
    [t('audit.start'), <span className="mono">{fmtTime(r.start)}</span>],
    [t('audit.end'), <span className="mono">{fmtTime(r.end)}{r.partial ? ` (${t('audit.ongoing')})` : ''}</span>],
    [t('audit.duration'), fmtDur(Math.max(0, r.end - r.start))],
    [t('audit.proto'), r.app ? `${appName(r.app)} · ${r.proto.toUpperCase()}` : r.proto.toUpperCase()],
    [t('audit.inbound'), t(`audit.in.${r.inbound}`, { defaultValue: r.inbound })],
    [t('audit.user'), r.user || dash],
    [t('audit.sourceAddr'), r.src ? <span className="mono">{r.src}</span> : dash],
    [t('audit.peerAddr'), r.src_peer ? <span className="mono">{r.src_peer}</span> : dash],
    [t('audit.fromNode'), r.src_node || dash],
    [t('audit.inIface'), r.in_iface || dash],
    [t('audit.srcMac'), r.src_mac ? <span className="mono">{r.src_mac}</span> : dash],
    [t('audit.requested'), r.dst ? <span className="mono">{r.dst}</span> : dash],
    [t('audit.host'), r.dst_host || dash],
    [t('audit.dialled'), r.dst_ip ? <span className="mono">{r.dst_ip}</span> : dash],
    [t('audit.policy'), <PolicyCell r={r} />],
    [t('audit.reasonTitle'), r.reason ? t(`audit.reason.${r.reason}`, { defaultValue: r.reason }) : dash],
    [t('audit.exit'), <ExitCell r={r} />],
    [t('audit.egress'), <Egress r={r} />],
    [t('audit.up'), fmtBytes(r.up || 0)],
    [t('audit.down'), fmtBytes(r.down || 0)],
    [t('audit.result'), <><ResultBadge r={r} />{r.error && <span className="muted"> {r.error}</span>}</>],
  ];
  return (
    <Modal open={open} onClose={onClose} animateFrom={animateFrom} wide title={t('audit.flow')}
      footer={<><span className="spacer" /><Button onClick={onClose}>{t('app.close')}</Button></>}>
      <div data-testid="audit-detail">
        <DetailGrid items={items} variant="plain" columns={3} />
        <div className="audit-geo">
          <GeoBlock title={t('audit.source')} addr={r.src_peer || r.src} />
          <GeoBlock title={t('audit.destination')} addr={r.dst_ip || r.dst_host} />
        </div>
      </div>
    </Modal>
  );
}

/** Location of an address in the flow details; nothing without geo data. */
function GeoBlock({ title, addr }: { title: string; addr?: string }) {
  const { i18n } = useTranslation();
  const ip = hostOf(addr);
  const geo = useGeo(ip && isIPLiteral(ip) ? ip : undefined, i18n.language);
  if (!geo || (!geo.country && !geo.org)) return null;
  return (
    <div className="audit-geo-block">
      <div className="audit-top-title">{title} · <span className="mono">{ip}</span></div>
      <GeoDetails geo={geo} />
    </div>
  );
}