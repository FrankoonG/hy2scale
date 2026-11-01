import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Modal, Button, Badge, DetailGrid, IconButton, IconCopy, useToast } from '@hy2scale/ui';
import type { TopologyNode } from '@/api';
import * as api from '@/api';
import { useNodeStore } from '@/store/node';
import { useExitPaths } from '@/hooks/useExitPaths';
import { fmtRate } from '@/hooks/useFormat';
import { copyToClipboard } from '@/utils/clipboard';
import { versionAtLeast } from '@/utils/version';
import { latencyClass, pathLatencyClass } from '@/utils/latency';
import { fmtPunchAddr, punchesFor } from '@/utils/punch';
import VersionBadge from '@/components/VersionBadge';
import { PublicIP } from '@/components/CountryFlag';
import { buildGraph, type GraphEdge } from './topo/graphModel';
import { PathInfoExpand, type PathInfoExpandProps, type Precision } from './PathInfoExpand';

type Pos = { x: number; y: number };

interface Props {
  open: boolean;
  onClose: () => void;
  /** Row / graph key: '__self__', 'peer', 'selfId/peer', 'a/b/c'. */
  qpath: string | null;
  animateFrom?: Pos;
  /** Status cell, same as the list's status column. */
  renderStatus: (n: TopologyNode, key: string) => ReactNode;
  /** Opens the remote-connect flow; undefined = not offered here. */
  onRemoteConnect?: (chain: string[], anchor?: Pos) => void;
}

/** The topology entry a row / graph key points at. */
function findNode(topo: TopologyNode[], key: string, selfId: string): TopologyNode | undefined {
  const self = topo.find((n) => n.is_self);
  if (key === '__self__') return self;
  const segs = key.split('/');
  let list: TopologyNode[];
  let rest: string[];
  if (self && segs.length > 1 && (segs[0] === selfId || (!selfId && segs[0] === self.name))) {
    list = self.children || [];
    rest = segs.slice(1);
  } else {
    list = topo.filter((n) => !n.is_self);
    rest = segs;
  }
  let cur: TopologyNode | undefined;
  for (const seg of rest) {
    cur = list.find((n) => n.name === seg);
    if (!cur) return undefined;
    list = cur.children || [];
  }
  return cur;
}

function historyProps(snap: api.DiagPeerHistory | undefined): Pick<PathInfoExpandProps, 'latency' | 'online' | 'txPeak' | 'rxPeak'> {
  const b: Record<Precision, api.DiagHistoryBucket[]> = { m1: snap?.m1 ?? [], h1: snap?.h1 ?? [], d1: snap?.d1 ?? [] };
  const project = <T,>(fn: (x: api.DiagHistoryBucket) => T): Record<Precision, T[]> => ({
    m1: b.m1.map(fn), h1: b.h1.map(fn), d1: b.d1.map(fn),
  });
  return {
    latency: project((x) => ({ ms: x.latencyMs })),
    online: project((x) => ({ pct: x.onlinePct })),
    txPeak: project((x) => ({ bps: x.txPeak })),
    rxPeak: project((x) => ({ bps: x.rxPeak })),
  };
}

export default function NodeDetailModal({ open, onClose, qpath, animateFrom, renderStatus, onRemoteConnect }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const { node: self, topology } = useNodeStore();
  const { isReachableAt } = useExitPaths();
  const selfId = self?.node_id || '';
  const selfName = self?.name;
  const key = qpath || '';
  const isSelf = key === '__self__';
  const n = key ? findNode(topology, key, selfId) : undefined;

  const { data: punchStatus = [] } = useQuery({
    queryKey: ['punchStatus'], queryFn: api.getPunchStatus, refetchInterval: 5000, enabled: open,
  });

  // Path as the graph shows it: outbound peers without the self prefix,
  // inbound paths starting at self.
  const { nodes: gNodes, edges } = useMemo(() => buildGraph(topology, selfId, selfName), [topology, selfId, selfName]);
  const raw = key && !isSelf ? key.split('/') : [];
  const fullPath = raw.length === 0 ? [] : raw[0] === selfId ? raw : [selfId, ...raw];
  const edgeOf = (a: string, b: string): GraphEdge | undefined =>
    edges.get(`${a}→${b}`) ?? edges.get(`${b}→${a}`) ?? edges.get(`?${a < b ? a : b}|${a < b ? b : a}`);
  const perHopLatencyMs: number[] = [];
  for (let i = 1; i < fullPath.length; i++) perHopLatencyMs.push(edgeOf(fullPath[i - 1], fullPath[i])?.segmentLatencyMs ?? -1);
  const totalLat = perHopLatencyMs.some((ms) => ms < 0) ? -1 : perHopLatencyMs.reduce((a, b) => a + b, 0);
  const hops = raw.map((k) => ({ key: k, name: gNodes.get(k)?.name || k }));
  const copyPath = hops.filter((h) => h.key !== selfId).map((h) => h.name).join('/');
  const recorderKey = fullPath.length >= 2 ? fullPath.slice(1).join('/') : '';

  // Per-path history rings, refreshed while the dialog is open.
  const [peerHistory, setPeerHistory] = useState<Record<string, api.DiagPeerHistory>>({});
  useEffect(() => {
    if (!open || isSelf) return;
    let cancelled = false;
    const tick = async () => {
      try {
        const res = await api.getPeerHistory();
        if (!cancelled) setPeerHistory(res.peers || {});
      } catch { /* next tick retries */ }
    };
    tick();
    const id = setInterval(tick, 30_000);
    return () => { cancelled = true; clearInterval(id); };
  }, [open, isSelf]);

  const fmtAddr = (a: string) => fmtPunchAddr(t, a);

  const items: [string, ReactNode][] = [];
  if (n) {
    items.push([t('nodes.status'), renderStatus(n, key)]);
    items.push([t('nodes.detail.direction'), isSelf ? t('nodes.detail.dirLocal')
      : n.direction === 'inbound' ? t('nodes.detail.dirIn')
      : n.direction === 'outbound' ? t('nodes.detail.dirOut') : '—']);
    const ver = isSelf ? self?.version : n.version;
    const lossMin = self?.lossless_min_version;
    const outdated = !isSelf && !!ver && !!lossMin && !n.incompatible && !versionAtLeast(ver, lossMin);
    items.push([t('nodes.detail.version'), ver ? (
      <VersionBadge peer version={ver} compat={n.compat} incompatible={n.incompatible} outdated={outdated}
        title={outdated ? t('nodes.updateForLossless', { v: lossMin }) : undefined} />
    ) : '—']);
    if (n.native) items.push([t('nodes.detail.type'), <Badge variant="muted">{t('nodes.native')}</Badge>]);
    if (n.invisible) items.push([t('nodes.detail.access'), <span title={t('nodes.invisibleTitle')}><Badge variant="muted">{t('nodes.invisible')}</Badge></span>]);
    if (n.restricted) items.push([t('nodes.detail.type'), <span title={t('nodes.restrictedTitle')}><Badge variant="muted" className="badge-restricted">{t('nodes.restricted')}</Badge></span>]);
    if (!isSelf && !n.native) items.push([t('nodes.nested'), n.nested ? t('app.yes') : t('app.no')]);

    const addrs = n.ip_statuses && n.ip_statuses.length > 0
      ? n.ip_statuses.map((s, i) => {
          const good = !!s.latency_ms && s.latency_ms > 0;
          const bad = s.latency_ms === -1;
          return (
            <span key={i}>
              {fmtAddr(s.addr)}{' '}
              <span className={good ? latencyClass(s.latency_ms!) : bad ? 'latency-off' : 'latency-na'}>
                {good ? `${s.latency_ms}ms` : bad ? t('nodes.timeout') : s.status}
              </span>
            </span>
          );
        })
      : (n.addrs && n.addrs.length > 0 ? n.addrs : n.addr ? [n.addr] : []).map((a) => <span key={a}>{fmtAddr(a)}</span>);
    if (addrs.length > 0) items.push([t('nodes.addresses'), <span className="detail-lines">{addrs}</span>]);
    if (n.via) items.push([t('nodes.detail.via'), n.via]);
    const punches = punchesFor(punchStatus, n.name);
    if (!isSelf && punches.length > 0) {
      items.push([t('punch.route'), (
        <span className="detail-lines" data-testid="detail-punch">
          {punches.map((p, i) => (
            <span key={i}>
              {fmtAddr(p.addr)}: {t(`punch.state.${p.state}`)}
              {p.reason ? ` (${t(`punch.reason.${p.reason}`, { defaultValue: p.reason })})` : ''}
              {p.mapping ? ` → ${p.mapping}` : ''}
            </span>
          ))}
        </span>
      )]);
    }
    const e = n.egress;
    const fam = (f: api.NodeEgressFamily | null, k: string) => f == null ? null
      : !f.ok ? <span key={k} className="iface-bad">{t('iface.unavailable')}</span>
      : f.ip ? (
        <span key={k}>
          <PublicIP ip={f.ip} />
          {n.egress_probed && <>{' '}<span className="iface-tag faint" data-testid="detail-egress-probed">{t('nodes.egressProbed')}</span></>}
        </span>
      ) : <span key={k}>—</span>;
    const egLines = e == null ? [] : [fam(e.ipv4, 'v4'), fam(e.ipv6, 'v6')].filter(Boolean);
    items.push([t('nodes.egressIp'), e == null
      ? <span className="iface-muted">{t('nodes.egressUnknown')}</span>
      : egLines.length ? <span className="detail-lines">{egLines}</span> : '—']);
    items.push([t('nodes.traffic'), (
      <span>
        <span className="stat-up">↑{fmtRate(n.tx_rate || 0)}</span>
        {' / '}
        <span className="stat-down">↓{fmtRate(n.rx_rate || 0)}</span>
      </span>
    )]);
    if (n.max_rate && n.max_rate > 0) items.push([t('nodes.bandwidth'), fmtRate(n.max_rate)]);
  }

  const pathEl = hops.length > 0 && (
    <div className="node-detail-path">
      <span className="hy-topo-pathinfo-label">{t('nodes.detail.path')}</span>
      <span className="hy-topo-pathinfo-chain" data-testid="detail-path">
        {hops.map((hop, i) => {
          const reach = hop.key === selfId || isReachableAt(hops.slice(0, i + 1).map((h) => h.name).join('/'));
          return (
            <span key={i}>
              {i > 0 && <span className="hy-topo-pathinfo-sep">/</span>}
              <span className={`hy-topo-pathinfo-hop ${reach ? 'reach-ok' : 'reach-bad'}`}>{hop.name}</span>
            </span>
          );
        })}
      </span>
      <span className={`hy-topo-pathinfo-lat ${pathLatencyClass(totalLat, n?.connected === false)}`}>
        {totalLat < 0 || n?.connected === false ? t('nodes.offline') : totalLat === 0 ? '—' : `${Math.round(totalLat)}ms`}
      </span>
      {copyPath && (
        <IconButton
          icon={IconCopy}
          iconSize={12}
          className="hy-topo-pathinfo-copy"
          label={t('nodes.graph.copyPath')}
          onClick={async () => {
            if (await copyToClipboard(copyPath)) toast.success(t('app.copied'));
            else toast.error(t('nodes.exportFailed'));
          }}
        />
      )}
    </div>
  );

  const chain = (() => {
    const s = selfId && key.startsWith(selfId + '/') ? key.slice(selfId.length + 1) : key;
    return s.split('/').filter(Boolean);
  })();
  const showRemote = !!n && !isSelf && !!onRemoteConnect && chain.length > 0;
  const remoteOk = !!n && !n.native && !n.restricted && n.connected !== false && !n.disabled;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={n ? (isSelf ? (n.name || selfName || 'self') : n.name) : (raw[raw.length - 1] || '')}
      animateFrom={animateFrom}
      wide
      footer={
        <>
          <span className="spacer" />
          <Button onClick={onClose}>{t('app.close')}</Button>
          {showRemote && (
            <Button
              variant="primary"
              disabled={!remoteOk}
              data-testid="detail-remote-btn"
              onClick={(ev) => {
                const r = (ev.currentTarget as HTMLElement).getBoundingClientRect();
                onRemoteConnect!(chain, { x: r.left + r.width / 2, y: r.top + r.height / 2 });
              }}
            >{t('nodes.openRemote')}</Button>
          )}
        </>
      }
    >
      <div data-testid={key ? `node-detail-${key}` : undefined}>
        {pathEl}
        <DetailGrid items={items} />
        {n && !isSelf && fullPath.length >= 2 && (
          <div className="node-detail-history">
            <PathInfoExpand {...historyProps(peerHistory[recorderKey])} perHopLatencyMs={perHopLatencyMs} />
          </div>
        )}
      </div>
    </Modal>
  );
}
