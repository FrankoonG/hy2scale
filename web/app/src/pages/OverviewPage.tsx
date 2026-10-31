import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import clsx from 'clsx';
import { Card, Badge, Progress, KeyValue, SegmentedControl, IconChevronRight, IconCircleAlert } from '@hy2scale/ui';
import * as api from '@/api';
import type { TopologyNode, DiagPeerHistory, DiagHistoryBucket } from '@/api';
import type { IfaceEntry, IfaceFamilyKey } from '@/api/types';
import { useNodeStore } from '@/store/node';
import { fmtBytes, fmtRate, fmtDuration } from '@/hooks/useFormat';
import { useTheme } from '@/theme';
import VersionBadge from '@/components/VersionBadge';
import { hasUpdate, useUpgradeStore } from '@/components/upgrade';
import { PublicIP } from '@/components/CountryFlag';

// ---------------------------------------------------------------------------
// Live samples. Kept at module level so the chart and sparklines survive a
// page switch within the same tab (one sample per /stats poll, 2 s).
// ---------------------------------------------------------------------------
interface LiveSample { t: number; tx: number; rx: number; txb: number; rxb: number; conns: number; exits: number }
const LIVE_MAX = 180; // 6 min at 2 s
const liveBuf: LiveSample[] = [];
let liveTick = 0;

function useLiveSamples(stats: api.Stats | undefined, tick: number): LiveSample[] {
  const [, force] = useState(0);
  useEffect(() => {
    if (!stats || !tick || tick === liveTick) return;
    liveTick = tick;
    liveBuf.push({
      t: tick / 1000, tx: stats.tx_rate, rx: stats.rx_rate,
      txb: stats.tx_bytes, rxb: stats.rx_bytes, conns: stats.conns, exits: stats.exit_clients,
    });
    if (liveBuf.length > LIVE_MAX) liveBuf.splice(0, liveBuf.length - LIVE_MAX);
    force((v) => v + 1);
  }, [tick, stats]);
  return liveBuf;
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
type Range = 'live' | 'm1' | 'h1' | 'd1';
interface Point { t: number; tx: number; rx: number }

const portOf = (listen?: string | number | null): string => {
  if (listen == null || listen === '') return '';
  const s = String(listen);
  const i = s.lastIndexOf(':');
  return i >= 0 ? s.slice(i + 1) : s;
};

// Sum per-peer peak rates per bucket timestamp (peer-history rings).
function aggregate(hist: Record<string, DiagPeerHistory> | undefined, key: 'm1' | 'h1' | 'd1'): Point[] {
  if (!hist) return [];
  const m = new Map<number, Point>();
  for (const p of Object.values(hist)) {
    for (const b of p?.[key] || []) {
      const cur = m.get(b.ts) || { t: b.ts, tx: 0, rx: 0 };
      cur.tx += b.txPeak || 0;
      cur.rx += b.rxPeak || 0;
      m.set(b.ts, cur);
    }
  }
  return [...m.values()].sort((a, b) => a.t - b.t);
}

// Round up to a 1/2/4/5/8/10 step within the binary unit fmtRate uses.
function niceMax(v: number): number {
  if (v <= 0) return 1024;
  let unit = 1;
  while (v / unit >= 1024) unit *= 1024;
  const x = v / unit;
  const exp = Math.pow(10, Math.floor(Math.log10(x)));
  const f = x / exp;
  const nf = f <= 1 ? 1 : f <= 2 ? 2 : f <= 4 ? 4 : f <= 5 ? 5 : f <= 8 ? 8 : 10;
  return nf * exp * unit;
}
const fmtAxis = (v: number) => (v === 0 ? '0' : fmtRate(Math.round(v)).replace('.0 ', ' '));

function linkProps(go: () => void) {
  return {
    role: 'button' as const,
    tabIndex: 0,
    onClick: go,
    onKeyDown: (e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); go(); } },
  };
}

function TitleLink({ label, onClick }: { label: ReactNode; onClick: () => void }) {
  return (
    <button type="button" className="ov-title-link" onClick={onClick}>
      {label}<IconChevronRight size={12} />
    </button>
  );
}

// Inline sparkline next to a metric value.
function MiniSpark({ data, cls }: { data: number[]; cls: string }) {
  if (data.length < 2) return null;
  const W = 64, H = 20;
  const max = Math.max(1, ...data);
  const step = W / (data.length - 1);
  const pts = data.map((v, j) => `${(j * step).toFixed(1)},${(H - 1 - (v / max) * (H - 2)).toFixed(1)}`);
  const line = 'M ' + pts.join(' L ');
  return (
    <svg className={clsx('ov-mini-spark', cls)} viewBox={`0 0 ${W} ${H}`} width={W} height={H} preserveAspectRatio="none" aria-hidden="true">
      <path className="ov-spark-area" d={`${line} L ${W},${H} L 0,${H} Z`} />
      <path className="ov-spark-line" d={line} vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

function Metric({ label, value, spark }: { label: string; value: ReactNode; spark?: ReactNode }) {
  return (
    <div className="ov-metric">
      <div className="ov-metric-label">{label}</div>
      <div className="ov-metric-row"><span className="ov-metric-value">{value}</span>{spark}</div>
    </div>
  );
}

interface Part { key: string; label: string; value: number; color: string }

// Ring chart. Colours are literal hex applied via currentColor.
function Donut({ parts, center, sub, size = 104 }: { parts: Part[]; center: ReactNode; sub?: ReactNode; size?: number }) {
  const sw = 11;
  const r = (size - sw) / 2;
  const c = 2 * Math.PI * r;
  const shown = parts.filter((p) => p.value > 0);
  const total = shown.reduce((a, p) => a + p.value, 0);
  let off = 0;
  return (
    <div className="ov-donut" style={{ width: size, height: size }}>
      <svg viewBox={`0 0 ${size} ${size}`} width={size} height={size} aria-hidden="true">
        <circle className="ov-donut-track" cx={size / 2} cy={size / 2} r={r} strokeWidth={sw} />
        {total > 0 && shown.map((p) => {
          const len = (p.value / total) * c;
          const gap = shown.length > 1 ? Math.min(2, len / 2) : 0;
          const el = (
            <circle key={p.key} className="ov-donut-seg" style={{ color: p.color }}
              cx={size / 2} cy={size / 2} r={r} strokeWidth={sw}
              strokeDasharray={`${Math.max(0, len - gap)} ${c}`} strokeDashoffset={-off}
              transform={`rotate(-90 ${size / 2} ${size / 2})`} />
          );
          off += len;
          return el;
        })}
      </svg>
      <div className="ov-donut-center">
        <div className="ov-donut-val">{center}</div>
        {sub != null && <div className="ov-donut-sub">{sub}</div>}
      </div>
    </div>
  );
}

function Legend({ parts, onClick }: { parts: Part[]; onClick?: () => void }) {
  return (
    <div className="ov-legend">
      {parts.map((p) => (
        <div key={p.key} className={clsx('ov-legend-row', onClick && 'ov-click')} {...(onClick ? linkProps(onClick) : {})}>
          <i className="ov-legend-sq" style={{ color: p.color }} />
          <span className="ov-legend-label">{p.label}</span>
          <span className="ov-legend-num">{p.value}</span>
        </div>
      ))}
    </div>
  );
}

// Proportional stacked bar with an inline legend.
function SegBar({ parts }: { parts: Part[] }) {
  const total = parts.reduce((a, p) => a + p.value, 0);
  return (
    <div className="ov-segbar-wrap">
      <div className="ov-segbar">
        {total > 0 && parts.filter((p) => p.value > 0).map((p) => (
          <i key={p.key} style={{ color: p.color, flexGrow: p.value }} title={`${p.label} ${p.value}`} />
        ))}
      </div>
      <div className="ov-segbar-legend">
        {parts.map((p) => (
          <span key={p.key} className={clsx('ov-segbar-item', !p.value && 'zero')}>
            <i className="ov-legend-sq" style={{ color: p.color }} />{p.label}<b>{p.value}</b>
          </span>
        ))}
      </div>
    </div>
  );
}

// Literal hex per theme (inline colours stay readable for Dark Reader in
// the light theme; the dark theme has its own set).
const C_LIGHT = { ok: '#22c55e', bad: '#ef4444', warn: '#f59e0b', off: '#b8bfcc', pri: '#4361ee' };
const C_DARK = { ok: '#22c55e', bad: '#f05252', warn: '#f59e0b', off: '#4b5262', pri: '#5a78f8' };
const SVC_LIGHT: Record<string, string> = {
  hy2: '#4361ee', socks5: '#0ea5e9', http: '#14b8a6', ss: '#8b5cf6',
  l2tp: '#f59e0b', ikev2: '#ec4899', wg: '#22c55e', openvpn: '#f97316',
};
const SVC_DARK: Record<string, string> = { ...SVC_LIGHT, hy2: '#5a78f8', ss: '#a78bfa' };

const KV = KeyValue;

// ---------------------------------------------------------------------------
// Traffic chart (upload / download), time-scaled, with hover crosshair.
// ---------------------------------------------------------------------------
function TrafficChart({ data, range }: { data: Point[]; range: Range }) {
  const { t, i18n } = useTranslation();
  const ref = useRef<HTMLDivElement | null>(null);
  const [w, setW] = useState(600);
  const [H, setH] = useState(200);
  const [hover, setHover] = useState<number | null>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const update = () => { setW(Math.max(200, el.clientWidth)); setH(Math.max(160, el.clientHeight)); };
    update();
    const ro = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(update) : null;
    ro?.observe(el);
    return () => ro?.disconnect();
  }, []);
  useEffect(() => { setHover(null); }, [range]);

  const PL = 62, PR = 6, PT = 8, PB = 22;
  const iw = Math.max(10, w - PL - PR);
  const ih = H - PT - PB;
  const n = data.length;
  const max = niceMax(n ? Math.max(...data.map((d) => Math.max(d.tx, d.rx))) : 0);
  const t0 = n ? data[0].t : 0;
  const t1 = n ? data[n - 1].t : 1;
  const span = t1 - t0 || 1;
  const x = (ts: number) => PL + ((ts - t0) / span) * iw;
  const y = (v: number) => PT + ih - (Math.min(v, max) / max) * ih;

  const fmtTime = (ts: number, long = false) => {
    const d = new Date(ts * 1000);
    const lang = i18n.language;
    if (range === 'live') return d.toLocaleTimeString(lang, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
    if (range === 'm1') return d.toLocaleTimeString(lang, { hour: '2-digit', minute: '2-digit', hour12: false });
    if (range === 'h1') return d.toLocaleString(lang, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: long ? '2-digit' : undefined, hour12: false });
    return d.toLocaleDateString(lang, { month: '2-digit', day: '2-digit' });
  };

  const path = (key: 'tx' | 'rx') => {
    if (n < 2) return { line: '', area: '' };
    const pts = data.map((d) => `${x(d.t).toFixed(1)},${y(d[key]).toFixed(1)}`);
    const line = 'M ' + pts.join(' L ');
    const base = (PT + ih).toFixed(1);
    return { line, area: `${line} L ${x(t1).toFixed(1)},${base} L ${x(t0).toFixed(1)},${base} Z` };
  };
  const up = path('tx');
  const down = path('rx');

  const ticks = [0, 0.25, 0.5, 0.75, 1];
  const xCount = w < 420 ? 3 : 5;
  const xTicks = n >= 2 ? Array.from({ length: xCount }, (_, i) => t0 + (span * i) / (xCount - 1)) : [];

  const onMove = (clientX: number) => {
    const el = ref.current;
    if (!el || n < 2) return;
    const px = clientX - el.getBoundingClientRect().left;
    const ts = t0 + ((px - PL) / iw) * span;
    let best = 0;
    for (let i = 1; i < n; i++) if (Math.abs(data[i].t - ts) < Math.abs(data[best].t - ts)) best = i;
    setHover(best);
  };

  const hv = hover != null && hover < n ? data[hover] : null;
  const cur = hv || (n ? data[n - 1] : null);
  const peakTx = n ? Math.max(...data.map((d) => d.tx)) : 0;
  const peakRx = n ? Math.max(...data.map((d) => d.rx)) : 0;
  const tipLeft = hv ? x(hv.t) : 0;
  const flip = hv ? tipLeft > w - 170 : false;

  return (
    <>
      <div className="ov-chart-legend">
        <span className="ov-legend-item"><i className="ov-legend-dot up" />{t('overview.upload')} <b className="ov-legend-val">{cur ? fmtRate(cur.tx) : '—'}</b></span>
        <span className="ov-legend-item"><i className="ov-legend-dot down" />{t('overview.download')} <b className="ov-legend-val">{cur ? fmtRate(cur.rx) : '—'}</b></span>
        <span className="ov-legend-peak">{t('overview.peak')} {fmtRate(peakTx)} / {fmtRate(peakRx)}</span>
      </div>
      <div
        className="ov-chart"
        ref={ref}
        onPointerMove={(e) => onMove(e.clientX)}
        onPointerDown={(e) => onMove(e.clientX)}
        onPointerLeave={() => setHover(null)}
      >
        <svg viewBox={`0 0 ${w} ${H}`} width={w} height={H} role="img" aria-label={t('overview.traffic')}>
          {ticks.map((f) => (
            <g key={f}>
              <line className="ov-chart-grid" x1={PL} x2={PL + iw} y1={PT + ih - f * ih} y2={PT + ih - f * ih} />
              <text className="ov-chart-axis" x={PL - 8} y={PT + ih - f * ih + 3} textAnchor="end">{fmtAxis(f * max)}</text>
            </g>
          ))}
          {xTicks.map((ts, i) => (
            <text
              key={i}
              className="ov-chart-axis"
              x={x(ts)}
              y={H - 6}
              textAnchor={i === 0 ? 'start' : i === xTicks.length - 1 ? 'end' : 'middle'}
            >{fmtTime(ts)}</text>
          ))}
          {n >= 2 && (
            <>
              <g className="ov-chart-down">
                <path className="ov-chart-area" d={down.area} />
                <path className="ov-chart-line" d={down.line} />
              </g>
              <g className="ov-chart-up">
                <path className="ov-chart-area" d={up.area} />
                <path className="ov-chart-line" d={up.line} />
              </g>
            </>
          )}
          {hv && (
            <>
              <line className="ov-chart-cross" x1={x(hv.t)} x2={x(hv.t)} y1={PT} y2={PT + ih} />
              <g className="ov-chart-down"><circle className="ov-chart-dot" cx={x(hv.t)} cy={y(hv.rx)} r={4} /></g>
              <g className="ov-chart-up"><circle className="ov-chart-dot" cx={x(hv.t)} cy={y(hv.tx)} r={4} /></g>
            </>
          )}
        </svg>
        {n < 2 && <div className="ov-chart-empty">{t('overview.noData')}</div>}
        {hv && (
          <div
            className="ov-chart-tip"
            style={{ left: flip ? tipLeft - 12 : tipLeft + 12, transform: flip ? 'translateX(-100%)' : undefined }}
          >
            <div className="ov-chart-tip-time">{fmtTime(hv.t, true)}</div>
            <div className="ov-chart-tip-row"><i className="ov-legend-dot up" />{fmtRate(hv.tx)}</div>
            <div className="ov-chart-tip-row"><i className="ov-legend-dot down" />{fmtRate(hv.rx)}</div>
          </div>
        )}
      </div>
    </>
  );
}

// ---------------------------------------------------------------------------
// Direct peers
// ---------------------------------------------------------------------------
interface PeerRow {
  name: string;
  connected: boolean;
  disabled: boolean;
  inbound: boolean;
  outbound: boolean;
  latency: number;
  tx: number;
  rx: number;
  bad: boolean; // incompatible / conflict / unsupported
}

function directPeers(topology: TopologyNode[]): PeerRow[] {
  const m = new Map<string, PeerRow>();
  const add = (n: TopologyNode, dir: 'in' | 'out') => {
    const cur = m.get(n.name);
    const bad = !!(n.incompatible || n.conflict || n.unsupported);
    if (!cur) {
      m.set(n.name, {
        name: n.name, connected: n.connected, disabled: n.disabled,
        inbound: dir === 'in', outbound: dir === 'out',
        latency: n.latency_ms, tx: n.tx_rate || 0, rx: n.rx_rate || 0, bad,
      });
      return;
    }
    cur.inbound ||= dir === 'in';
    cur.outbound ||= dir === 'out';
    cur.connected ||= n.connected;
    cur.bad ||= bad;
    cur.tx += n.tx_rate || 0;
    cur.rx += n.rx_rate || 0;
    if (n.latency_ms > 0 && (cur.latency <= 0 || n.latency_ms < cur.latency)) cur.latency = n.latency_ms;
  };
  for (const n of topology) {
    if (n.is_self) (n.children || []).forEach((c) => add(c, 'in'));
    else add(n, 'out');
  }
  return [...m.values()];
}

function meshCount(topology: TopologyNode[], selfName?: string): number {
  const names = new Set<string>();
  const walk = (list?: TopologyNode[]) => {
    for (const n of list || []) {
      if (!n.is_self && n.name !== selfName && n.connected) names.add(n.name);
      walk(n.children);
    }
  };
  walk(topology);
  return names.size;
}

// Last 24 finished hours of a peer's availability.
function hourStrip(h1: DiagHistoryBucket[] | undefined) {
  const byTs = new Map<number, DiagHistoryBucket>();
  for (const b of h1 || []) byTs.set(b.ts, b);
  const nowH = Math.floor(Date.now() / 1000 / 3600) * 3600;
  let on = 0, tot = 0;
  const cells = Array.from({ length: 24 }, (_, i) => {
    const ts = nowH - (24 - i) * 3600;
    const b = byTs.get(ts);
    if (!b || !b.samples) return { ts, cls: '', b };
    on += b.onlinePct * b.samples;
    tot += b.samples;
    const cls = b.onlinePct >= 0.99 ? 'ok' : b.onlinePct >= 0.5 ? 'warn' : 'bad';
    return { ts, cls, b };
  });
  return { cells, pct: tot ? (on / tot) * 100 : null };
}

const PEER_LIMIT = 8;

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------
export default function OverviewPage() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const node = useNodeStore((s) => s.node);
  const update = hasUpdate(useUpgradeStore((s) => s.status));
  const topology = useNodeStore((s) => s.topology);
  const [range, setRange] = useState<Range>('live');
  const dark = useTheme().theme === 'dark';
  const C = dark ? C_DARK : C_LIGHT;
  const SVC_COLORS = dark ? SVC_DARK : SVC_LIGHT;

  const { data: stats, dataUpdatedAt: statsTick } = useQuery({ queryKey: ['stats'], queryFn: api.getStats, refetchInterval: 2000 });
  const { data: runtime } = useQuery({ queryKey: ['runtime'], queryFn: api.getRuntime, refetchInterval: 5000 });
  const { data: history } = useQuery({ queryKey: ['peerHistory'], queryFn: api.getPeerHistory, refetchInterval: 30000 });
  const { data: users = [] } = useQuery({ queryKey: ['users'], queryFn: api.getUsers, refetchInterval: 5000 });
  const { data: sessions = [] } = useQuery({ queryKey: ['sessions'], queryFn: api.getSessions, refetchInterval: 3000 });
  const { data: proxies = [] } = useQuery({ queryKey: ['proxies'], queryFn: api.getProxies, refetchInterval: 10000 });
  const { data: ss } = useQuery({ queryKey: ['ss'], queryFn: api.getSS, refetchInterval: 10000 });
  const { data: l2tp } = useQuery({ queryKey: ['l2tp'], queryFn: api.getL2TP, refetchInterval: 5000 });
  const { data: ikev2 } = useQuery({ queryKey: ['ikev2'], queryFn: api.getIKEv2, refetchInterval: 5000 });
  const { data: wg } = useQuery({ queryKey: ['wireguard'], queryFn: api.getWireGuard, refetchInterval: 5000 });
  const { data: ovpn } = useQuery({ queryKey: ['openvpn'], queryFn: api.getOpenVPN, refetchInterval: 5000 });
  const { data: ifaces } = useQuery({ queryKey: ['ifaces'], queryFn: api.listInterfaces, refetchInterval: 10000 });
  const { data: rules } = useQuery({ queryKey: ['rules'], queryFn: api.getRules, refetchInterval: 15000 });
  const { data: certs = [] } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts, refetchInterval: 60000 });

  const live = useLiveSamples(stats, statsTick);

  // ---- chart data ----
  const chartData: Point[] = useMemo(() => {
    if (range === 'live') return live.map((s) => ({ t: s.t, tx: s.tx, rx: s.rx }));
    return aggregate(history?.peers, range);
    // live is a mutable module buffer; statsTick marks its changes
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [range, history, statsTick, live.length]);

  // ---- peers ----
  const peers = useMemo(() => directPeers(topology), [topology]);
  const peersOnline = peers.filter((p) => p.connected && !p.disabled).length;
  const peersOffline = peers.filter((p) => !p.connected && !p.disabled);
  const peersBad = peers.filter((p) => p.bad && !p.disabled);
  const mesh = useMemo(() => meshCount(topology, node?.node_id), [topology, node?.node_id]);
  const peerRows = useMemo(() => {
    const rank = (p: PeerRow) => (p.disabled ? 3 : !p.connected ? 0 : p.bad ? 1 : 2);
    return [...peers].sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
  }, [peers]);

  // ---- sessions ----
  const byProto = useMemo(() => {
    const m: Record<string, number> = {};
    for (const s of sessions) m[s.protocol] = (m[s.protocol] || 0) + 1;
    return m;
  }, [sessions]);
  const onlineUsers = useMemo(() => new Set(sessions.map((s) => s.username)).size, [sessions]);

  // ---- users ----
  const userStats = useMemo(() => {
    const now = Date.now();
    let enabled = 0, expired = 0, expiring = 0, over = 0, near = 0;
    for (const u of users) {
      if (u.enabled) enabled++;
      const exp = u.expiry_date ? Date.parse(u.expiry_date) : NaN;
      if (!Number.isNaN(exp)) {
        if (exp < now) expired++;
        else if (exp - now <= 7 * 86400000) expiring++;
      }
      if (u.traffic_limit > 0) {
        const r = u.traffic_used / u.traffic_limit;
        if (r >= 1) over++;
        else if (r >= 0.9) near++;
      }
    }
    const top = [...users].filter((u) => u.traffic_used > 0).sort((a, b) => b.traffic_used - a.traffic_used).slice(0, 5);
    return { enabled, expired, expiring, over, near, top };
  }, [users]);
  const topMax = Math.max(1, ...userStats.top.map((u) => u.traffic_used));

  // ---- services ----
  const socks = proxies.find((p) => p.protocol === 'socks5');
  const http = proxies.find((p) => p.protocol === 'http');
  const services: { key: string; label: string; on: boolean; running?: boolean; port: string; devices: number }[] = [
    { key: 'hy2', label: t('proxies.hy2'), on: !!node?.server?.listen, port: portOf(node?.server?.listen), devices: byProto.hy2 || 0 },
    { key: 'socks5', label: t('proxies.socks5'), on: !!socks?.enabled, port: portOf(socks?.listen), devices: byProto.socks5 || 0 },
    { key: 'http', label: t('proxies.http'), on: !!http?.enabled, port: portOf(http?.listen), devices: byProto.http || 0 },
    { key: 'ss', label: t('proxies.ss'), on: !!ss?.enabled, port: portOf(ss?.listen), devices: byProto.ss || 0 },
    { key: 'l2tp', label: t('proxies.l2tp'), on: !!l2tp?.enabled, running: l2tp?.running, port: portOf(l2tp?.listen), devices: l2tp?.connected || 0 },
    { key: 'ikev2', label: t('proxies.ikev2'), on: !!ikev2?.enabled, running: ikev2?.running, port: '500/4500', devices: ikev2?.connected || 0 },
    { key: 'wg', label: t('proxies.wireguard'), on: !!wg?.enabled, running: wg?.running, port: portOf(wg?.listen_port), devices: wg?.connected || 0 },
    { key: 'openvpn', label: t('proxies.openvpn'), on: !!ovpn?.enabled, running: ovpn?.running, port: portOf(ovpn?.port), devices: ovpn?.connected || 0 },
  ];
  // Admin page of a service: WireGuard lives under Endpoints.
  const svcPage = (key: string) => (key === 'wg' ? '/endpoints' : '/proxies');
  const servicesOn = services.filter((s) => s.on).length;
  const servicesDown = services.filter((s) => s.on && s.running === false);

  // ---- network ----
  const egress = ifaces?.egress || { ipv4: '', ipv6: '' };
  const ifList = ifaces?.interfaces || [];
  const ifUp = ifList.filter((x) => x.up).length;
  const egressInfo = (fam: IfaceFamilyKey) => {
    const name = egress[fam];
    if (!name) return { name: '', iface: undefined as IfaceEntry | undefined, ok: true };
    const iface = ifList.find((x) => x.name === name);
    const ok = !!iface && iface.up && iface.egress_usable?.[fam]?.ok !== false;
    return { name, iface, ok };
  };
  const eg4 = egressInfo('ipv4');
  const eg6 = egressInfo('ipv6');
  const rulesList = rules?.rules || [];
  const rulesOn = rulesList.filter((r) => r.enabled).length;

  // ---- certificates ----
  const certInfo = useMemo(() => {
    const now = Date.now();
    const list = certs
      .map((c) => {
        const ts = Date.parse(c.not_after);
        return { c, days: Number.isNaN(ts) ? null : Math.floor((ts - now) / 86400000) };
      })
      .sort((a, b) => (a.days ?? Infinity) - (b.days ?? Infinity));
    const expired = list.filter((x) => x.days != null && x.days < 0).length;
    const expiring = list.filter((x) => x.days != null && x.days >= 0 && x.days <= 30).length;
    return { list, expired, expiring };
  }, [certs]);

  // ---- attention ----
  const issues: { level: 'red' | 'orange'; label: string; count?: number; to: string }[] = [];
  if (node?.limited) issues.push({ level: 'orange', label: t('overview.issue.limited'), to: '/settings' });
  if (peersOffline.length) issues.push({ level: 'red', label: t('overview.issue.peersOffline'), count: peersOffline.length, to: '/nodes' });
  if (peersBad.length) issues.push({ level: 'orange', label: t('overview.issue.peersIncompat'), count: peersBad.length, to: '/nodes' });
  for (const s of servicesDown) issues.push({ level: 'red', label: t('overview.issue.serviceDown', { name: s.label }), to: svcPage(s.key) });
  if (!eg4.ok) issues.push({ level: 'orange', label: t('overview.issue.egress', { fam: 'IPv4' }), to: '/interfaces' });
  if (!eg6.ok) issues.push({ level: 'orange', label: t('overview.issue.egress', { fam: 'IPv6' }), to: '/interfaces' });
  if (certInfo.expired) issues.push({ level: 'red', label: t('overview.issue.certsExpired'), count: certInfo.expired, to: '/tls' });
  if (certInfo.expiring) issues.push({ level: 'orange', label: t('overview.issue.certsExpiring'), count: certInfo.expiring, to: '/tls' });
  if (userStats.over) issues.push({ level: 'orange', label: t('overview.issue.usersOverQuota'), count: userStats.over, to: '/users' });

  // ---- sparks ----
  const sConns = live.map((s) => s.conns);
  const sExits = live.map((s) => s.exits);

  const rangeTabs: { key: Range; label: string }[] = [
    { key: 'live', label: t('overview.range.live') },
    { key: 'm1', label: t('overview.range.m1') },
    { key: 'h1', label: t('overview.range.h1') },
    { key: 'd1', label: t('overview.range.d1') },
  ];

  const egressLine = (fam: IfaceFamilyKey, e: ReturnType<typeof egressInfo>) => {
    if (!e.name) return <span className="ov-muted">{t('overview.none')}</span>;
    const pub = e.iface?.public?.[fam];
    return (
      <>
        <span className={clsx(!e.ok && 'ov-warn-text')}>{e.name}</span>
        {pub?.ok && pub.ip ? <span className="ov-muted"> · <PublicIP ip={pub.ip} /></span> : null}
      </>
    );
  };

  const fmtDays = (d: number | null) => (d == null ? '—' : t('overview.days', { count: d }));

  return (
    <div className="hy-page ov-page">
      <div className="ov-grid">
        {/* This node */}
        <Card className="ov-span-5" title={t('overview.thisNode')}>
          <div className="ov-node-head">
            <span className="ov-node-name">{node?.node_id || '—'}</span>
            {node?.version && <VersionBadge version={node.version} limited={node.limited} update={update} />}
            {node?.exit_node && <Badge variant="blue">{t('overview.exitNode')}</Badge>}
            <span className="ov-health">
              {issues.length === 0
                ? <Badge variant="green">{t('overview.healthy')}</Badge>
                : <Badge variant={issues.some((i) => i.level === 'red') ? 'red' : 'orange'}>{t('overview.attention')} · {issues.length}</Badge>}
            </span>
          </div>
          <div className="hy-kv">
            <KV label={t('overview.uptime')}>{runtime ? fmtDuration(runtime.uptime_seconds) : '—'}</KV>
            <KV label={t('overview.memory')}>{runtime ? `${fmtBytes(runtime.heap_alloc)} / ${fmtBytes(runtime.sys)}` : '—'}</KV>
            <KV label={t('overview.goroutines')}>{runtime?.goroutines ?? '—'}</KV>
            <KV label={t('overview.services')} onClick={() => navigate('/proxies')}>{servicesOn} / {services.length}</KV>
            <KV label={t('overview.meshNodesLabel')} onClick={() => navigate('/nodes')}>{mesh}</KV>
          </div>
          {issues.length > 0 && (
            <div className="ov-issues">
              {issues.map((it, i) => (
                <div key={i} className={clsx('ov-issue', it.level)} {...linkProps(() => navigate(it.to))}>
                  <IconCircleAlert className="ov-issue-mark" size={16} />
                  <span className="ov-issue-label">{it.label}</span>
                  {it.count != null && <span className="ov-issue-count">{it.count}</span>}
                </div>
              ))}
            </div>
          )}
        </Card>

        {/* Peers */}
        <Card
          className="ov-span-7"
          title={<TitleLink label={t('stats.peers')} onClick={() => navigate('/nodes')} />}
          actions={<span className="ov-head-meta">{t('overview.meshNodes', { count: mesh })}</span>}
        >
          {peers.length > 0 && (
            <SegBar parts={[
              { key: 'on', label: t('overview.online'), value: peersOnline, color: C.ok },
              { key: 'bad', label: t('overview.offline'), value: peersOffline.length, color: C.bad },
              { key: 'off', label: t('overview.disabled'), value: peers.filter((p) => p.disabled).length, color: C.off },
            ]} />
          )}
          {peerRows.length === 0 ? (
            <div className="ov-empty">{t('overview.noPeers')}</div>
          ) : (
            <div className="ov-list">
              <div className="ov-peer ov-peer-head">
                <span />
                <span>{t('overview.col.name')}</span>
                <span className="ov-r">{t('overview.col.latency')}</span>
                <span className="ov-strip-wrap">{t('overview.col.avail24h')}</span>
                <span className="ov-r">{t('overview.col.rate')}</span>
              </div>
              {peerRows.slice(0, PEER_LIMIT).map((p) => {
                const strip = hourStrip(history?.peers?.[p.name]?.h1);
                const dot = p.disabled ? 'off' : !p.connected ? 'bad' : p.bad ? 'warn' : 'ok';
                return (
                  <div key={p.name} className={clsx('ov-peer', p.disabled && 'ov-dim')} {...linkProps(() => navigate('/nodes'))}>
                    <i className={clsx('ov-dot', dot)} />
                    <span className="ov-peer-name">
                      <span className="ov-ellipsis">{p.name}</span>
                      {p.inbound && <span className="ov-tag">{t('overview.dirIn')}</span>}
                      {p.outbound && <span className="ov-tag">{t('overview.dirOut')}</span>}
                      {p.disabled && <span className="ov-tag off">{t('overview.disabled')}</span>}
                    </span>
                    <span className="ov-r ov-mono">
                      {p.disabled ? '—' : !p.connected ? <span className="ov-bad-text">{t('overview.offline')}</span> : p.latency > 0 ? `${p.latency} ms` : '—'}
                    </span>
                    <span className="ov-strip-wrap">
                      <span className="ov-strip">
                        {strip.cells.map((c) => (
                          <i key={c.ts} className={c.cls}
                            title={`${new Date(c.ts * 1000).toLocaleString(i18n.language, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })}${c.b && c.b.samples ? ` · ${Math.round(c.b.onlinePct * 100)}%${c.b.latencyMs >= 0 ? ` · ${c.b.latencyMs} ms` : ''}` : ''}`} />
                        ))}
                      </span>
                      <span className="ov-strip-pct">{strip.pct == null ? '—' : `${strip.pct >= 99.95 ? '100' : strip.pct.toFixed(1)}%`}</span>
                    </span>
                    <span className="ov-r ov-rate">
                      <span className="stat-up">↑ {fmtRate(p.tx)}</span>
                      <span className="stat-down">↓ {fmtRate(p.rx)}</span>
                    </span>
                  </div>
                );
              })}
              {peerRows.length > PEER_LIMIT && (
                <div className="ov-more" {...linkProps(() => navigate('/nodes'))}>{t('overview.more', { count: peerRows.length - PEER_LIMIT })}</div>
              )}
            </div>
          )}
        </Card>

        {/* Services */}
        <Card
          className="ov-span-5"
          title={<TitleLink label={t('overview.services')} onClick={() => navigate('/proxies')} />}
          actions={<span className="ov-head-meta">{t('overview.enabledOf', { on: servicesOn, total: services.length })}</span>}
        >
          <div className="ov-donut-row">
            <Donut
              parts={services.filter((s) => s.on).map((s) => ({ key: s.key, label: s.label, value: s.devices, color: SVC_COLORS[s.key] }))}
              center={sessions.length}
              sub={t('overview.devices')}
            />
            <div className="ov-donut-side">
              <div className="ov-big"><b>{onlineUsers}</b><span>{t('overview.onlineUsersLabel')}</span></div>
              <div className="ov-big"><b>{servicesOn}<em> / {services.length}</em></b><span>{t('overview.servicesOn')}</span></div>
            </div>
          </div>
          <div className="ov-list">
            <div className="ov-svc ov-peer-head">
              <span />
              <span>{t('overview.col.service')}</span>
              <span className="ov-r">{t('overview.col.port')}</span>
              <span className="ov-r">{t('overview.col.devices')}</span>
            </div>
            {services.map((s) => {
              const dot = !s.on ? 'off' : s.running === false ? 'bad' : 'ok';
              return (
                <div key={s.key} className={clsx('ov-svc', !s.on && 'ov-dim')} {...linkProps(() => navigate(svcPage(s.key)))}>
                  <i className={clsx('ov-dot', dot)} />
                  <span className="ov-svc-name">
                    {s.label}{s.on && <i className="ov-legend-sq" style={{ color: SVC_COLORS[s.key] }} />}
                  </span>
                  <span className="ov-r ov-mono ov-muted">{s.on ? s.port || '—' : ''}</span>
                  <span className="ov-r ov-mono">
                    {!s.on ? <span className="ov-muted">{t('overview.off')}</span>
                      : s.running === false ? <span className="ov-bad-text">{t('overview.stopped')}</span>
                      : s.devices}
                  </span>
                </div>
              );
            })}
          </div>
        </Card>

        {/* Traffic */}
        <Card
          className="ov-span-7"
          title={t('overview.traffic')}
          actions={
            <SegmentedControl items={rangeTabs} value={range} onChange={setRange} />
          }
        >
          <TrafficChart data={chartData} range={range} />
          <div className="ov-metrics">
            <Metric label={t('overview.totalUp')} value={<span className="stat-up">{stats ? fmtBytes(stats.tx_bytes) : '—'}</span>} />
            <Metric label={t('overview.totalDown')} value={<span className="stat-down">{stats ? fmtBytes(stats.rx_bytes) : '—'}</span>} />
            <Metric label={t('stats.connections')} value={stats?.conns ?? '—'} spark={<MiniSpark data={sConns} cls="ov-spark-pri" />} />
            <Metric label={t('stats.exitClients')} value={stats?.exit_clients ?? '—'} spark={<MiniSpark data={sExits} cls="ov-spark-pri" />} />
          </div>
        </Card>

        {/* Users */}
        <Card
          className="ov-span-4"
          title={<TitleLink label={t('nav.users')} onClick={() => navigate('/users')} />}
        >
          {(() => {
            const parts: Part[] = [
              { key: 'on', label: t('overview.online'), value: onlineUsers, color: C.ok },
              { key: 'idle', label: t('overview.idle'), value: Math.max(0, userStats.enabled - onlineUsers), color: C.pri },
              { key: 'off', label: t('overview.disabled'), value: users.length - userStats.enabled, color: C.off },
            ];
            return (
              <div className="ov-donut-row">
                <Donut parts={parts} center={users.length} sub={t('overview.total')} />
                <Legend parts={parts} onClick={() => navigate('/users')} />
              </div>
            );
          })()}
          {(userStats.expired + userStats.expiring + userStats.over + userStats.near) > 0 && (
            <div className="ov-flags">
              {userStats.over > 0 && <Badge variant="red">{t('overview.overQuota')} · {userStats.over}</Badge>}
              {userStats.near > 0 && <Badge variant="orange">{t('overview.nearQuota')} · {userStats.near}</Badge>}
              {userStats.expired > 0 && <Badge variant="muted">{t('overview.expired')} · {userStats.expired}</Badge>}
              {userStats.expiring > 0 && <Badge variant="orange">{t('overview.expiringSoon')} · {userStats.expiring}</Badge>}
            </div>
          )}
          <div className="ov-sub">{t('overview.topUsage')}</div>
          {userStats.top.length === 0 ? (
            <div className="ov-empty">{t('overview.noData')}</div>
          ) : userStats.top.map((u) => {
            const limited = u.traffic_limit > 0;
            const pct = limited ? (u.traffic_used / u.traffic_limit) * 100 : (u.traffic_used / topMax) * 100;
            return (
              <div key={u.id} className="ov-usage">
                <span className={clsx('ov-ellipsis', !u.enabled && 'ov-muted')}>{u.username}</span>
                <span className="ov-mono">{fmtBytes(u.traffic_used)}{limited && <span className="ov-muted"> / {fmtBytes(u.traffic_limit)}</span>}</span>
                <Progress value={pct} variant={limited ? (pct >= 100 ? 'red' : pct >= 90 ? 'orange' : 'default') : 'default'} />
              </div>
            );
          })}
        </Card>

        {/* Network */}
        <Card
          className="ov-span-4"
          title={<TitleLink label={t('overview.network')} onClick={() => navigate('/interfaces')} />}
        >
          {ifaces && (
            <SegBar parts={[
              { key: 'up', label: t('overview.ifUp'), value: ifUp, color: C.ok },
              { key: 'down', label: t('overview.ifDown'), value: ifList.length - ifUp, color: C.off },
            ]} />
          )}
          <div className="hy-kv">
            <KV label={t('iface.egressOn', { fam: 'IPv4' })} onClick={() => navigate('/interfaces')}>{ifaces ? egressLine('ipv4', eg4) : '—'}</KV>
            <KV label={t('iface.egressOn', { fam: 'IPv6' })} onClick={() => navigate('/interfaces')}>{ifaces ? egressLine('ipv6', eg6) : '—'}</KV>
            <KV label={t('nav.rules')} onClick={() => navigate('/rules')}>
              {rules ? (rules.available === false ? <span className="ov-muted">{t('overview.unavailable')}</span> : t('overview.enabledOf', { on: rulesOn, total: rulesList.length })) : '—'}
            </KV>
            {rules?.available !== false && rules?.capture && (
              <KV label={t('overview.capture')} onClick={() => navigate('/rules')}>{rules.capture.toUpperCase()}</KV>
            )}
          </div>
        </Card>

        {/* Certificates */}
        <Card
          className="ov-span-4"
          title={<TitleLink label={t('overview.certs')} onClick={() => navigate('/tls')} />}
          actions={certs.length ? <span className="ov-head-meta">{t('overview.totalOf', { count: certs.length })}</span> : undefined}
        >
          {certInfo.list.length === 0 ? (
            <div className="ov-empty">{t('overview.noCerts')}</div>
          ) : (
            <>
            <SegBar parts={[
              { key: 'ok', label: t('overview.valid'), value: certInfo.list.length - certInfo.expired - certInfo.expiring, color: C.ok },
              { key: 'soon', label: t('overview.within30d'), value: certInfo.expiring, color: C.warn },
              { key: 'exp', label: t('overview.expired'), value: certInfo.expired, color: C.bad },
            ]} />
            <div className="hy-kv">
              {certInfo.list.slice(0, 5).map(({ c, days }) => (
                <KV key={c.id} label={<span className="ov-ellipsis">{c.name || c.id}{c.is_ca && <span className="ov-tag">CA</span>}</span>} onClick={() => navigate('/tls')}>
                  {days != null && days < 0
                    ? <Badge variant="red">{t('overview.expired')}</Badge>
                    : days != null && days <= 30
                      ? <Badge variant="orange">{fmtDays(days)}</Badge>
                      : <span className="ov-muted">{fmtDays(days)}</span>}
                </KV>
              ))}
              {certInfo.list.length > 5 && (
                <div className="ov-more" {...linkProps(() => navigate('/tls'))}>{t('overview.more', { count: certInfo.list.length - 5 })}</div>
              )}
            </div>
            </>
          )}
        </Card>
      </div>
    </div>
  );
}
