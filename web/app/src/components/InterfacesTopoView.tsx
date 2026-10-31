import {
  useCallback, useLayoutEffect, useMemo, useRef, useState,
  type KeyboardEvent, type MouseEvent, type ReactNode,
} from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import {
  type useSelection, useStoredState, useToast, IconCheck, IconGlobe, IconScan, IconZoomIn, IconZoomOut, IconLayoutGrid,
} from '@hy2scale/ui';
import type { IfaceEntry, IfaceFamilyKey, Egress } from '@/api/types';
import IfaceTypeBadge, { ifaceType } from '@/components/IfaceTypeBadge';
import { usePanZoom, type View } from '@/hooks/usePanZoom';
import { PublicIP } from '@/components/CountryFlag';
import { exportPng, imageFileName } from '@/utils/exportImage';
import {
  chooseLayout, BRACKET, FAMS, IFACE_TOPO_STYLES, INET_HEAD, INET_LINE, VLAN_H, isOff, primaryAddr,
  type CardL, type GwL, type IfaceTopoStyle,
} from './topo/ifaceLayouts';
import { TopoMenuButton, TopoMenuCheck } from './topo/CanvasMenu';

/*
 * Interface topology: built-in layouts of this node's interfaces, their
 * gateways and the Internet (./topo/ifaceLayouts), chosen from the style
 * menu at the top left. Nodes are HTML (themeable, ellipsis, real tags);
 * edges are SVG painted via literal `color` + `stroke: currentColor` so Dark
 * Reader transforms them.
 */

type Sel = ReturnType<typeof useSelection>;

interface Props {
  list: IfaceEntry[];
  egress: Egress;
  loaded: boolean;
  emptyText: ReactNode;
  selection: Sel;
  /** The page's selection-driven action toolbar (shared with the list). */
  actions: ReactNode;
  hostName?: string;
  egressWarning: (x: IfaceEntry, fam: IfaceFamilyKey) => string;
  onOpenDetail: (name: string, anchor?: { x: number; y: number }) => void;
  /** Show hidden networks (bridges, veth, lo …): in this view's menu. */
  showHidden: boolean;
  onShowHidden: (on: boolean) => void;
  hiddenCount: number;
}

const FAM_LABEL: Record<IfaceFamilyKey, string> = { ipv4: 'IPv4', ipv6: 'IPv6' };
const FAM_SHORT: Record<IfaceFamilyKey, string> = { ipv4: 'v4', ipv6: 'v6' };
const STYLE_STORE = 'hy-iface-topo-style';
const MORPH_MS = 560; // matches the transitions in interfaces-topo.css
const styleCodec = {
  parse: (raw: string) => ((IFACE_TOPO_STYLES as string[]).includes(raw) ? (raw as IfaceTopoStyle) : undefined),
  serialize: (v: IfaceTopoStyle) => v,
};

const K_MIN = 0.3;
const K_MAX = 2.5;
const TOP_RESERVE = 52; // overlay bar height kept clear when fitting
const FIT_PAD_X = 32;
const FIT_PAD_Y = 16;

export default function InterfacesTopoView({
  list, egress, loaded, emptyText, selection, actions, hostName,
  egressWarning, onOpenDetail, showHidden, onShowHidden, hiddenCount,
}: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const [style, setStyle] = useStoredState<IfaceTopoStyle>(STYLE_STORE, 'lr', styleCodec);
  const byKey = useMemo(() => new Map(list.map((x) => [x.name, x])), [list]);

  // ---- viewport ----
  const wrapRef = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });
  // The host frame flows into as many columns as the viewport's shape needs.
  const availW = size.w - FIT_PAD_X;
  const availH = size.h - TOP_RESERVE - FIT_PAD_Y;
  const layout = useMemo(() => chooseLayout(style, list, egress, availW, availH), [style, list, egress, availW, availH]);
  const userMoved = useRef(false);
  // Shared canvas gestures: trackpad pan / pinch, mouse-wheel zoom, touch
  // pan + pinch; the toolbar glides.
  const { view, setView, animateTo, zoomBy, consumeClick, clampK } = usePanZoom({
    ref: wrapRef,
    minK: K_MIN,
    maxK: K_MAX,
    isOverlay: (el) => !!el.closest('[data-overlay]'),
    onUserMove: () => { userMoved.current = true; },
  });

  useLayoutEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const measure = () => setSize({ w: el.clientWidth, h: el.clientHeight });
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const fitTarget = useCallback((): View | null => {
    if (!size.w || !size.h) return null;
    const k = clampK(Math.min(1.15, availW / layout.w, availH / layout.h));
    const x = (size.w - layout.w * k) / 2;
    const y = TOP_RESERVE + Math.max(0, (size.h - TOP_RESERVE - layout.h * k) / 2);
    return { x, y, k };
  }, [size.w, size.h, availW, availH, layout.w, layout.h, clampK]);

  // Switching style (or showing hidden networks) morphs: boxes glide to
  // their new place, edges are redrawn after it, new boxes fade in, and
  // the view glides to the new fit.
  const [morph, setMorph] = useState(false);
  const morphRef = useRef(false);
  morphRef.current = morph;
  const morphTimer = useRef<number | undefined>(undefined);
  const morphTo = (change: () => void) => {
    userMoved.current = false;
    setMorph(true);
    change();
    window.clearTimeout(morphTimer.current);
    morphTimer.current = window.setTimeout(() => setMorph(false), MORPH_MS);
  };
  useLayoutEffect(() => () => window.clearTimeout(morphTimer.current), []);
  // What was on screen before this render, to tell new boxes apart.
  const shownRef = useRef<Set<string>>(new Set());
  const isNew = (key: string) => morph && !shownRef.current.has(key);

  // Keep the diagram fitted until the user pans or zooms.
  useLayoutEffect(() => {
    if (userMoved.current) return;
    const v = fitTarget();
    if (!v) return;
    if (morphRef.current) animateTo(v);
    else setView(v);
  }, [fitTarget, setView, animateTo]);

  const resetView = () => {
    userMoved.current = false;
    const v = fitTarget();
    if (v) animateTo(v);
  };

  // ---- selection ----
  const select = (name: string, e: MouseEvent | KeyboardEvent) => {
    if (e.ctrlKey || e.metaKey || e.shiftKey) selection.toggle(name);
    else selection.selectOnly(name);
  };
  const anchorOf = (el: Element) => {
    const r = el.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  };
  const nodeHandlers = (name: string) => ({
    'data-k': name,
    role: 'button',
    tabIndex: 0,
    'aria-pressed': selection.selected.has(name),
    onClick: (e: MouseEvent<HTMLDivElement>) => {
      e.stopPropagation();
      if (consumeClick() || e.detail > 1) return;
      select(name, e);
    },
    onDoubleClick: (e: MouseEvent<HTMLDivElement>) => {
      e.stopPropagation();
      if (!selection.selected.has(name)) selection.selectOnly(name);
      onOpenDetail(name, anchorOf(e.currentTarget));
    },
    onKeyDown: (e: KeyboardEvent<HTMLDivElement>) => {
      if (e.key === 'Enter') { e.preventDefault(); onOpenDetail(name, anchorOf(e.currentTarget)); }
      else if (e.key === ' ') { e.preventDefault(); select(name, e); }
    },
  });

  const onBlankClick = (e: MouseEvent<HTMLDivElement>) => {
    if (consumeClick()) return;
    const el = e.target as HTMLElement;
    if (el.closest('[data-k]') || el.closest('[data-overlay]')) return;
    selection.clear();
  };


  // ---- paint helpers ----
  const egressOf = (name: string) => FAMS.filter((f) => egress[f] === name);
  const egClass = (name: string, fam: IfaceFamilyKey) => {
    if (egress[fam] !== name) return '';
    const x = byKey.get(name);
    return clsx(fam === 'ipv4' ? 'eg4' : 'eg6', x && egressWarning(x, fam) && 'warn');
  };
  // Secondary facts (mode, VLAN, state) live in the card tooltip; the card
  // itself shows them through the type badge, bracket tag and dashed border.
  const cardTitle = (x: IfaceEntry) => {
    const parts = [x.name, t(`iface.type.${ifaceType(x)}`)];
    if (x.kind === 'child' && x.parent) parts.push(`↳ ${x.parent}`);
    if (x.kind === 'child') parts.push(t(x.mode === 'system' ? 'iface.modeSystem' : 'iface.modeBuiltin'));
    if (x.kind === 'child' && x.vlan?.enabled) parts.push(t('iface.vlanTag', { id: x.vlan.id }));
    if (x.kind === 'child' && x.enabled === false) parts.push(t('iface.disabledTag'));
    else if (!x.up) parts.push(t('iface.down'));
    return parts.join(' · ');
  };

  const addrValue = (x: IfaceEntry, fam: IfaceFamilyKey): ReactNode => {
    const f = x[fam];
    const addrs = f?.addrs || [];
    if (!f || f.mode === 'off' || !hasAnyAddr(x)) return <span className="iface-muted">—</span>;
    if (!addrs.length) {
      if (f.state === 'acquiring') return <span className="iface-pending">{t('iface.acquiring')}</span>;
      if (f.state === 'failed') return <span className="iface-bad">{t('iface.failed')}</span>;
      return <span className="iface-muted">—</span>;
    }
    const p = primaryAddr(x, fam);
    return (
      <>
        <span className="iface-topo-ellip">{p.prefix}</span>
        {addrs.length > 1 && <span className="iface-topo-more">+{addrs.length - 1}</span>}
      </>
    );
  };
  // A card with no address at all shows one bare "—" line.
  const hasAnyAddr = (x: IfaceEntry) => FAMS.some((fam) => {
    const f = x[fam];
    return (f?.addrs?.length || 0) > 0 || (!!f && f.mode !== 'off' && (f.state === 'acquiring' || f.state === 'failed'));
  });
  const addrTitle = (x: IfaceEntry, fam: IfaceFamilyKey) => (x[fam]?.addrs || []).map((a) => a.prefix).join('\n') || undefined;

  const pubValue = (x: IfaceEntry, fam: IfaceFamilyKey): ReactNode => {
    const p = x.public?.[fam];
    if (!p) return <span className="iface-muted">—</span>;
    if (!p.ok) return <span className="iface-bad">{t('iface.unavailable')}</span>;
    return <PublicIP className={clsx('iface-topo-ellip', egClass(x.name, fam) && 'iface-topo-pubip')} ip={p.ip} country={p.country} />;
  };

  const renderCard = (c: CardL) => {
    const x = c.x;
    const sel = selection.selected.has(x.name);
    return (
      <div
        key={x.name}
        className={clsx('iface-topo-card', c.child && 'child', isOff(x) && 'off', c.faint && 'faint', sel && 'sel', isNew(`c:${x.name}`) && 'enter')}
        style={{ left: c.box.x, top: c.box.y, width: c.box.w, height: c.box.h }}
        data-testid={`topo-card-${x.name}`}
        data-col={c.col}
        {...nodeHandlers(x.name)}
      >
        <div className="iface-topo-card-name" title={cardTitle(x)}>
          <span className="iface-topo-ellip">{x.name}</span>
          {egressOf(x.name).map((fam) => {
            const warn = egressWarning(x, fam);
            return (
              <span key={fam} className={clsx('iface-topo-eg', fam === 'ipv4' ? 'eg4' : 'eg6', warn && 'warn')}
                title={warn || t('iface.egressOn', { fam: FAM_LABEL[fam] })} data-testid={`topo-egress-${fam}`}>
                {FAM_SHORT[fam]}
              </span>
            );
          })}
          <IfaceTypeBadge iface={x} />
        </div>
        <div className="iface-topo-card-lines">
          {c.lines.map((l) => (
            <div key={`${l.type}-${l.fam}`} className={clsx('iface-topo-line', l.type)}
              title={l.type === 'addr' ? addrTitle(x, l.fam) : t('iface.publicFam', { fam: FAM_LABEL[l.fam] })}>
              <span className="iface-topo-line-label">{l.type === 'pub' ? '↗' : hasAnyAddr(x) ? FAM_SHORT[l.fam] : ''}</span>
              {l.type === 'addr' ? addrValue(x, l.fam) : pubValue(x, l.fam)}
            </div>
          ))}
        </div>
      </div>
    );
  };

  // ---- edges ----
  const edges: ReactNode[] = [];
  const hot: ReactNode[] = [];
  const pushEdge = (key: string, d: string, cls: string, flow: boolean) => {
    if (!d) return;
    const node = (
      <g key={key} className={clsx('iface-topo-edge', cls)}>
        <path d={d} className="iface-topo-edge-line" />
        {flow && <path d={d} className="iface-topo-edge-flow" />}
      </g>
    );
    (cls.includes('eg') ? hot : edges).push(node);
  };
  for (const g of layout.gws) {
    for (const m of g.members) {
      const cls = egClass(m.name, m.fam);
      pushEdge(`u-${g.key}-${m.name}-${m.fam}`, layout.up(m, g), clsx(cls, m.off && 'off'), !!cls && !m.off);
    }
    if (g.kind === 'local') continue;
    for (const l of g.lines) {
      const ms = g.members.filter((m) => m.fam === l.fam);
      const cls = ms.map((m) => egClass(m.name, m.fam)).find(Boolean) || '';
      const allOff = ms.every((m) => m.off);
      pushEdge(`g-${g.key}-${l.fam}`, layout.toInet(g, l.fam, !!cls), clsx(cls, allOff && 'off'), !!cls && !allOff);
    }
  }
  for (const d of layout.direct) {
    const cls = egClass(d.name, d.fam);
    pushEdge(`d-${d.name}-${d.fam}`, layout.directToInet(d, !!cls), clsx(cls, d.off && 'off'), !!cls && !d.off);
  }
  for (const [bi, b] of layout.brackets.entries()) {
    const last = b.childYs[b.childYs.length - 1].y;
    const right = b.x - BRACKET;
    const parts = [`M${right},${b.parentY} H${b.x} V${last}`];
    for (const c of b.childYs) parts.push(`M${b.x},${c.y} H${right}`);
    edges.push(<path key={`b-${bi}`} d={parts.join(' ')} className="iface-topo-bracket" />);
  }

  const gwEgress = (g: GwL) => g.members.map((m) => egClass(m.name, m.fam)).find(Boolean) || '';
  const gwRow = (l: { fam: IfaceFamilyKey; ip: string }) => (
    <div key={l.fam} className="iface-topo-gw-row">
      <span className="iface-topo-gw-label">{t(l.ip ? 'iface.gatewayFam' : 'iface.topo.directFam', { fam: FAM_LABEL[l.fam] })}</span>
      <span className="iface-topo-gw-ip iface-topo-ellip">{l.ip || '—'}</span>
    </div>
  );

  const inetPub = (fam: IfaceFamilyKey): ReactNode => {
    const x = byKey.get(egress[fam]);
    const p = x?.public?.[fam];
    const warn = x ? egressWarning(x, fam) : '';
    let v: ReactNode;
    if (p?.ok) v = <PublicIP className="iface-topo-ellip" ip={p.ip} country={p.country} />;
    else if (p) v = <span className="iface-bad">{t('iface.unavailable')}</span>;
    else v = <span className="iface-muted iface-topo-ellip">{egress[fam]}</span>;
    return (
      <div key={fam} className="iface-topo-inet-line" style={{ height: INET_LINE }}
        title={warn || t('iface.egressOn', { fam: FAM_LABEL[fam] })} data-testid={`topo-inet-${fam}`}>
        <span className={clsx('iface-topo-eg', fam === 'ipv4' ? 'eg4' : 'eg6', warn && 'warn')}>{FAM_SHORT[fam]}</span>
        {v}
      </div>
    );
  };

  const L = layout;
  const empty = !list.length;
  const stageRef = useRef<HTMLDivElement>(null);
  const saveImage = async () => {
    const el = stageRef.current;
    if (!el || !wrapRef.current) return;
    try {
      await exportPng(el, {
        width: L.w,
        height: L.h,
        background: getComputedStyle(wrapRef.current).backgroundColor,
        fileName: imageFileName(`${hostName || 'hy2scale'}-interfaces`),
        adjust: (clone) => {
          const st = (clone as HTMLElement).style;
          st.transform = 'none';
          st.left = '0px';
          st.top = '0px';
        },
      });
    } catch (err: any) {
      toast.error(String(err?.message || err));
    }
  };
  const styleLabel = (k: IfaceTopoStyle) => t(`iface.topo.style.${k}`);
  useLayoutEffect(() => {
    const keys = new Set<string>([...L.cards.map((c) => `c:${c.x.name}`), ...L.gws.map((g) => `g:${g.key}`)]);
    if (L.frame) keys.add('frame');
    if (L.inet) keys.add('inet');
    // Frozen while morphing so "new" holds for the whole transition.
    if (!morph) shownRef.current = keys;
  });

  return (
    <div
      ref={wrapRef}
      className={clsx('iface-topo', `is-${L.style}`, morph && 'is-morph')}
      data-testid="iface-topo"
      data-style={L.style}
      onClick={onBlankClick}
    >
      {empty ? (
        <div className="iface-topo-empty">{loaded ? emptyText : t('app.loading')}</div>
      ) : (
        <div
          ref={stageRef}
          className="iface-topo-stage"
          style={{ width: L.w, height: L.h, transform: `translate(${view.x}px,${view.y}px) scale(${view.k})` }}
        >
          {L.frame && (
            <div className={clsx('iface-topo-frame', isNew('frame') && 'enter')} style={{ left: L.frame.x, top: L.frame.y, width: L.frame.w, height: L.frame.h }}>
              <span className="iface-topo-frame-title iface-topo-ellip">{hostName || t('iface.topo.host')}</span>
            </div>
          )}
          <svg className="iface-topo-svg" width={L.w} height={L.h} aria-hidden="true">
            {edges}
            {hot}
          </svg>

          {L.inet && (
            <div className={clsx('iface-topo-inet', isNew('inet') && 'enter')} style={{ left: L.inet.x, top: L.inet.y, width: L.inet.w, height: L.inet.h }} data-testid="topo-inet">
              <div className="iface-topo-inet-head" style={{ height: INET_HEAD }}>
                <IconGlobe className="iface-topo-globe" size={16} />
                <span>{t('iface.topo.internet')}</span>
              </div>
              {L.inetLines.length
                ? L.inetLines.map(inetPub)
                : <div className="iface-topo-inet-line iface-muted" style={{ height: INET_LINE }}>{t('iface.topo.noEgress')}</div>}
            </div>
          )}

          {L.gws.map((g) => {
            const eg = gwEgress(g);
            const ips = g.lines.map((l) => l.ip).filter(Boolean).join(' ');
            const local = g.kind === 'local';
            return (
              <div
                key={g.key}
                className={clsx('iface-topo-gw', g.lines.length > 1 && 'multi', local && 'local', eg,
                  !local && g.members.every((m) => m.off) && 'off', isNew(`g:${g.key}`) && 'enter')}
                style={{ left: g.box.x, top: g.box.y, width: g.box.w, height: g.box.h }}
                title={local ? undefined : [...new Set(g.members.map((m) => m.name))].join(', ')}
                data-testid={local ? 'topo-uplink-none' : `topo-gw-${ips || 'direct'}`}
              >
                {local
                  ? <span className="iface-topo-gw-label">{t('iface.topo.noUplink')}</span>
                  : g.lines.map(gwRow)}
              </div>
            );
          })}

          {L.cards.map(renderCard)}

          {L.brackets.flatMap((b) => b.childYs.filter((c) => c.vlan != null).map((c) => (
            <span key={`v-${b.x}-${c.y}`} className={clsx('iface-topo-vlan', c.off && 'off')}
              style={{ left: b.x + 5, top: c.y - VLAN_H / 2, height: VLAN_H }}>
              {t('iface.vlanTag', { id: c.vlan })}
            </span>
          )))}
        </div>
      )}

      <div className="iface-topo-topbar" data-overlay="">
        <div className="iface-topo-tools">
          <button type="button" className="iface-topo-tool" onClick={resetView} title={t('iface.topo.fit')} data-testid="topo-fit">
            <IconScan size={14} />
          </button>
          <button type="button" className="iface-topo-tool" onClick={() => zoomBy(1 / 1.2)} title={t('iface.topo.zoomOut')}>
            <IconZoomOut size={14} />
          </button>
          <button type="button" className="iface-topo-tool" onClick={() => zoomBy(1.2)} title={t('iface.topo.zoomIn')}>
            <IconZoomIn size={14} />
          </button>
          <TopoMenuButton label={styleLabel(L.style)} title={t('iface.topo.styleTitle')} testId="iface-style-menu" icon={IconLayoutGrid}>
            {(close) => (
              <>
                <div className="hy-topo-canvas-sec">{t('iface.topo.styleTitle')}</div>
                {IFACE_TOPO_STYLES.map((k) => (
                  <button
                    key={k}
                    type="button"
                    role="menuitemradio"
                    aria-checked={k === style}
                    className={clsx('hy-dropdown-item', k === style && 'current')}
                    data-testid={`iface-style-${k}`}
                    onClick={() => { close(); if (k !== style) morphTo(() => setStyle(k)); }}
                  >
                    <span className="hy-dd-check">{k === style && <IconCheck size={14} />}</span>
                    <span className="hy-topo-canvas-name">{styleLabel(k)}</span>
                  </button>
                ))}
                <div className="hy-dd-divider" />
                <TopoMenuCheck checked={showHidden} onChange={(on) => morphTo(() => onShowHidden(on))} label={t('iface.showHidden')}
                  count={hiddenCount > 0 ? hiddenCount : undefined} testId="show-hidden" />
                <div className="hy-dd-divider" />
                <button type="button" role="menuitem" className="hy-dropdown-item" data-testid="topo-save-image"
                  disabled={empty} onClick={() => { close(); saveImage(); }}>
                  {t('topo.saveImage')}
                </button>
              </>
            )}
          </TopoMenuButton>
          <span className="iface-topo-legend">
            {FAMS.map((fam) => (
              <span key={fam} className={clsx('iface-topo-legend-item', fam === 'ipv4' ? 'eg4' : 'eg6')}>
                <svg width="18" height="6" aria-hidden="true"><path d="M1 3H17" /></svg>
                {t('iface.egressOn', { fam: FAM_LABEL[fam] })}
              </span>
            ))}
          </span>
        </div>
        <div className="iface-topo-actions">{actions}</div>
      </div>
    </div>
  );
}
