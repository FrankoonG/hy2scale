import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Badge, Button, Card, DetailGrid, Input, Modal, Select, SegmentedControl, Table, Toggle,
  useModalState, useStoredState, boolCodec, type Column, type DetailItem,
} from '@hy2scale/ui';
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
const INBOUNDS = ['local', 'forward', 'socks5', 'http', 'ss', 'hy2', 'ikev2', 'l2tp', 'openvpn', 'wireguard', 'access', 'relay'];

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
  const [user, setUser] = useState('');
  const [src, setSrc] = useState('');
  const [dst, setDst] = useState('');
  const [inbound, setInbound] = useState('');
  const [exit, setExit] = useState('');
  const [proto, setProto] = useState('');
  const [result, setResult] = useState('');
  const text = useDebounced({ q, user, src, dst }, 400);

  const [records, setRecords] = useState<AuditRecord[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [truncated, setTruncated] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [stats, setStats] = useState<AuditStats | null>(null);
  const gen = useRef(0);

  const filter = useMemo<AuditFilter>(() => {
    const now = Date.now();
    return {
      from: RANGE_MS[range] ? now - RANGE_MS[range] : undefined,
      q: text.q.trim(), user: text.user.trim(), src: text.src.trim(), dst: text.dst.trim(),
      inbound, exit, proto, result,
    };
    // `tick` re-anchors the time window on refresh
  }, [range, text, inbound, exit, proto, result]); // eslint-disable-line react-hooks/exhaustive-deps

  const load = useCallback(async (more: boolean) => {
    const g = more ? gen.current : ++gen.current;
    setLoading(true);
    setError('');
    try {
      const f = more ? filter : { ...filter, from: RANGE_MS[range] ? Date.now() - RANGE_MS[range] : undefined };
      const [page, st] = await Promise.all([
        getAudit(f, PAGE, more ? cursor : undefined),
        more ? Promise.resolve(null) : getAuditStats({ from: f.from, to: f.to }).catch(() => null),
      ]);
      if (g !== gen.current) return;
      setRecords((prev) => (more ? prev.concat(page.records) : page.records));
      setCursor(page.next_cursor || undefined);
      setTruncated(!!page.truncated);
      if (!more) setStats(st);
    } catch (e: any) {
      if (g === gen.current) setError(String(e?.message || e));
    } finally {
      if (g === gen.current) setLoading(false);
    }
  }, [filter, cursor, range]);

  useEffect(() => { load(false); }, [filter]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!auto) return;
    const id = setInterval(() => load(false), REFRESH_MS);
    return () => clearInterval(id);
  }, [auto, load]);

  const detail = useModalState<AuditRecord>();

  const columns: Column<AuditRecord>[] = [
    { key: 'time', title: t('audit.time'), render: (r) => <span className="mono nowrap">{fmtTime(r.start)}</span>, sortValue: (r) => r.start, hideable: false },
    { key: 'dur', title: t('audit.duration'), render: (r) => <span className="nowrap">{fmtDur(Math.max(0, r.end - r.start))}{r.partial ? ' …' : ''}</span>, sortValue: (r) => r.end - r.start },
    { key: 'proto', title: t('audit.proto'), render: (r) => <span className="mono">{r.proto.toUpperCase()}</span> },
    { key: 'inbound', title: t('audit.inbound'), render: (r) => <Badge variant="muted">{t(`audit.in.${r.inbound}`, { defaultValue: r.inbound })}</Badge> },
    { key: 'user', title: t('audit.user'), render: (r) => r.user || <span className="muted">—</span>, sortValue: (r) => r.user || '' },
    { key: 'src', title: t('audit.source'), render: (r) => <Source r={r} /> },
    { key: 'dst', title: t('audit.destination'), render: (r) => <Dest r={r} />, sortValue: (r) => r.dst_host || r.dst || '' },
    { key: 'rule', title: t('audit.rule'), render: (r) => r.rule || <span className="muted">—</span>, defaultHidden: true },
    { key: 'exit', title: t('audit.exit'), render: (r) => <ExitCell r={r} /> },
    { key: 'egress', title: t('audit.egress'), render: (r) => <Egress r={r} />, defaultHidden: true },
    { key: 'up', title: '↑', menuTitle: t('audit.up'), render: (r) => <span className="nowrap num">{fmtBytes(r.up || 0)}</span>, sortValue: (r) => r.up || 0, className: 'num' },
    { key: 'down', title: '↓', menuTitle: t('audit.down'), render: (r) => <span className="nowrap num">{fmtBytes(r.down || 0)}</span>, sortValue: (r) => r.down || 0, className: 'num' },
    { key: 'result', title: t('audit.result'), render: (r) => <ResultBadge r={r} /> },
  ];

  const sel = (label: string, value: string, set: (v: string) => void, options: { value: string; label: string }[], testId: string) => (
    <div className={value ? 'audit-sel' : 'audit-sel empty'} data-testid={testId}>
      <Select value={value} onChange={(e) => set(e.target.value)} options={[{ value: '', label: value ? t('audit.any') : label }, ...options]} />
    </div>
  );

  return (
    <div className="hy-page audit-page" data-testid="audit-page">
      <AuditSummary stats={stats} />

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
          <Button onClick={() => load(false)} loading={loading && !records.length} data-testid="audit-refresh">{t('audit.refresh')}</Button>
          <Button onClick={() => { window.location.href = auditExportUrl(filter, 'csv'); }} data-testid="audit-export">{t('audit.exportCsv')}</Button>
        </div>
        <div className="audit-filters">
          <Input value={user} onChange={(e) => setUser(e.target.value)} placeholder={t('audit.user')} data-testid="audit-user" />
          <Input value={src} onChange={(e) => setSrc(e.target.value)} placeholder={t('audit.source')} data-testid="audit-src" />
          <Input value={dst} onChange={(e) => setDst(e.target.value)} placeholder={t('audit.destination')} data-testid="audit-dst" />
          {sel(t('audit.inbound'), inbound, setInbound, INBOUNDS.map((k) => ({ value: k, label: t(`audit.in.${k}`) })), 'audit-inbound')}
          {sel(t('audit.exit'), exit, setExit, ['direct', 'node', 'blocked'].map((k) => ({ value: k, label: t(`audit.exitKind.${k}`) })), 'audit-exit')}
          {sel(t('audit.proto'), proto, setProto, [{ value: 'tcp', label: 'TCP' }, { value: 'udp', label: 'UDP' }], 'audit-proto')}
          {sel(t('audit.result'), result, setResult, ['ok', 'error', 'refused', 'blocked'].map((k) => ({ value: k, label: t(`audit.res.${k}`) })), 'audit-result')}
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

function AuditSummary({ stats }: { stats: AuditStats | null }) {
  const { t } = useTranslation();
  if (!stats) return null;
  const top = (title: string, list?: { key: string; bytes: number }[], addr = false) => (
    <div className="audit-top">
      <div className="audit-top-title">{title}</div>
      {(list || []).slice(0, 5).map((x) => (
        <div className="audit-top-row" key={x.key}>
          <span className="audit-top-key">{addr && <IPFlag addr={x.key} />}{x.key || '—'}</span>
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
      </div>
    </Card>
  );
}

function Source({ r }: { r: AuditRecord }) {
  const { t } = useTranslation();
  return (
    <span className="audit-addr">
      {r.src && <span className="mono nowrap"><IPFlag addr={r.src} />{r.src}</span>}
      {r.src_node && <span className="muted nowrap">{t('audit.viaNode', { node: r.src_node })}</span>}
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
    [t('audit.proto'), r.proto.toUpperCase()],
    [t('audit.inbound'), t(`audit.in.${r.inbound}`, { defaultValue: r.inbound })],
    [t('audit.user'), r.user || dash],
    [t('audit.source'), r.src ? <span className="mono">{r.src}</span> : dash],
    [t('audit.fromNode'), r.src_node || dash],
    [t('audit.requested'), r.dst ? <span className="mono">{r.dst}</span> : dash],
    [t('audit.host'), r.dst_host || dash],
    [t('audit.dialled'), r.dst_ip ? <span className="mono">{r.dst_ip}</span> : dash],
    [t('audit.rule'), r.rule || dash],
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
          <GeoBlock title={t('audit.source')} addr={r.src} />
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