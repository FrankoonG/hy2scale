import {
  useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState,
  type KeyboardEvent, type MouseEvent, type PointerEvent as ReactPointerEvent, type ReactNode,
} from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import { type useSelection } from '@hy2scale/ui';
import type { IfaceEntry, IfaceFamilyKey, Egress } from '@/api/types';
import { isLinkLocal6 } from '@/utils/ipaddr';
import IfaceTypeBadge, { ifaceType } from '@/components/IfaceTypeBadge';

/*
 * Interface topology: a deterministic, layered left-to-right diagram.
 *
 *   [Internet] ── [gateways] ── [this node: NICs ┐ sub-interfaces]
 *
 * Column 1 is the Internet with the node's egress public IPs; column 2 the
 * upstream gateways (one per family + gateway address, shared when several
 * interfaces use the same one); column 3 a frame for this node holding the
 * interfaces in list order (every interface, container networks included,
 * is a full card on the same grid), sub-interfaces indented under their
 * parent and tied to it by a bracket on the right (VLAN tag on the bracket).
 * When the cards would not fit the viewport at a readable zoom, the frame
 * flows into top-aligned columns, filled in order, a parent never split
 * from its sub-interfaces. Uplink edges leave a card at the address line of
 * the family they belong to, so the IPv4 and IPv6 egress paths read end to
 * end in their own colour; cards in later columns reach the gateways via a
 * lane in the gutter to their left and a bus above the frame. Gateways are
 * placed at the barycenter of where their edges leave the frame, which
 * keeps edges short and mostly uncrossed without any force simulation.
 *
 * Nodes are HTML (themeable, ellipsis, real tags); edges are SVG painted via
 * literal `color` + `stroke: currentColor` so Dark Reader transforms them.
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
}

const FAMS: IfaceFamilyKey[] = ['ipv4', 'ipv6'];
const FAM_LABEL: Record<IfaceFamilyKey, string> = { ipv4: 'IPv4', ipv6: 'IPv6' };
const FAM_SHORT: Record<IfaceFamilyKey, string> = { ipv4: 'v4', ipv6: 'v6' };

// Geometry (px, diagram space). Card internals must match the CSS in the
// "Interfaces topology" section of components.css.
const MARGIN = 20;
const INET_W = 190;
const GW_W = 160;
const GW_H = 38;
const GW_GAP = 10;
const COL_GAP_1 = 70;   // Internet → gateways
const COL_GAP_2 = 90;   // gateways → node frame
const FRAME_PAD = 12;
const FRAME_HEAD = 26;
const CARD_W = 220;
const INDENT = 20;
const BRACKET = 12;     // card right edge → bracket line
const BRACKET_LABEL = 56;
const BORDER = 1;
const PAD_T = 4;
const NAME_H = 18;
const LINES_GAP = 2;
const LINE_H = 16;
const PAD_B = 4;
const GAP_TOP = 10;     // between top-level blocks
const GAP_CHILD = 6;
const VLAN_H = 16;
const INET_HEAD = 30;
const INET_LINE = 18;
const INET_PAD_B = 6;

const COL_GAP = 14;     // bracket-label area → next column's cards
const COL_STRIDE = CARD_W + BRACKET + BRACKET_LABEL + COL_GAP;
const LANE_IN = 8;      // gutter lane → card left edge
const LANE_ABOVE = 10;  // top bus lane above the frame
const CORNER = 6;
const MAX_COLS = 8;
const FIT_OK = 0.9;     // fewest columns whose fit zoom reaches this

interface Box { x: number; y: number; w: number; h: number }
type LineSpec = { type: 'addr' | 'pub'; fam: IfaceFamilyKey };
interface CardL { x: IfaceEntry; box: Box; lines: LineSpec[]; child: boolean; faint: boolean; col: number }
/**
 * An edge endpoint on a card line. Cards in column 0 connect straight from
 * their left side; cards further right reach the frame's left edge through
 * their gutter lane (`lane` x) and the bus lane above the frame, so edges
 * never cut across other cards. `ey` is where the edge leaves the frame,
 * used for gateway / Internet placement.
 */
interface Port { name: string; fam: IfaceFamilyKey; px: number; py: number; lane: number | null; ey: number; off: boolean }
interface GwL { key: string; fam: IfaceFamilyKey; ip: string; box: Box; members: Port[] }
interface Bracket { parentY: number; x: number; childYs: { y: number; vlan: number | null; off: boolean }[] }
interface Layout {
  w: number; h: number;
  inet: Box; inetLines: IfaceFamilyKey[];
  frame: Box;
  laneY: number;
  cols: number;
  cards: CardL[];
  gws: GwL[];
  brackets: Bracket[];
  direct: Port[];
}

const byName = (a: IfaceEntry, b: IfaceEntry) => a.name.localeCompare(b.name, undefined, { numeric: true });
const hasAddr = (x: IfaceEntry) => (x.ipv4?.addrs?.length || 0) + (x.ipv6?.addrs?.length || 0) > 0;
const isLoopback = (x: IfaceEntry) => x.name === 'lo' || !!x.ipv4?.addrs?.some((a) => a.prefix.startsWith('127.'));
const isOff = (x: IfaceEntry) => x.enabled === false || !x.up;

function primaryAddr(x: IfaceEntry, fam: IfaceFamilyKey) {
  const a = x[fam]?.addrs || [];
  if (fam === 'ipv6' && a.length > 1 && isLinkLocal6(a[0].prefix)) return a.find((y) => !isLinkLocal6(y.prefix)) || a[0];
  return a[0];
}

function cardLines(x: IfaceEntry): LineSpec[] {
  // Each family's public address sits right under its local address.
  const out: LineSpec[] = [];
  for (const fam of FAMS) {
    const f = x[fam];
    const pending = !!f && f.mode !== 'off' && (f.state === 'acquiring' || f.state === 'failed');
    if ((f?.addrs?.length || 0) > 0 || pending) out.push({ type: 'addr', fam });
    if (x.public?.[fam]) out.push({ type: 'pub', fam });
  }
  if (!out.some((l) => l.type === 'addr')) out.unshift({ type: 'addr', fam: 'ipv4' });
  return out;
}

const cardHeight = (n: number) => BORDER * 2 + PAD_T + NAME_H + LINES_GAP + n * LINE_H + PAD_B;
const lineCenter = (box: Box, i: number) => box.y + BORDER + PAD_T + NAME_H + LINES_GAP + i * LINE_H + LINE_H / 2;

/** A top-level card with its sub-interfaces; never split across columns. */
interface Block { head: IfaceEntry; kids: IfaceEntry[]; faint: boolean; h: number }

function buildBlocks(list: IfaceEntry[]): Block[] {
  const children = list.filter((x) => x.kind === 'child');
  const others = list.filter((x) => x.kind !== 'child');
  const known = new Set(others.map((x) => x.name));
  // Same order as the list view: NICs, other links (addressed first),
  // loopback, container networks; every interface is a full card.
  const rank = (x: IfaceEntry) => (x.kind === 'physical' ? 0 : x.container ? 4 : isLoopback(x) ? 3 : hasAddr(x) ? 1 : 2);
  const main = [...others].sort((a, b) => rank(a) - rank(b) || byName(a, b));
  const orphans = children.filter((c) => !known.has(c.parent)).sort(byName);
  const ch = (x: IfaceEntry) => cardHeight(cardLines(x).length);
  const blocks: Block[] = main.map((p) => {
    const kids = children.filter((c) => c.parent === p.name).sort(byName);
    return { head: p, kids, faint: isLoopback(p), h: ch(p) + kids.reduce((s, k) => s + GAP_CHILD + ch(k), 0) };
  });
  for (const o of orphans) blocks.push({ head: o, kids: [], faint: false, h: ch(o) });
  return blocks;
}

/** Column index per block: in order, at most `ncol` columns, shortest tallest column. */
function partition(hs: number[], ncol: number): number[] {
  if (ncol <= 1 || hs.length <= 1) return hs.map(() => 0);
  if (ncol >= hs.length) return hs.map((_, i) => i);
  const pack = (limit: number) => {
    const out: number[] = [];
    let col = 0;
    let acc = -1;
    for (const h of hs) {
      if (acc < 0) acc = h;
      else if (acc + GAP_TOP + h > limit) { col++; acc = h; }
      else acc += GAP_TOP + h;
      out.push(col);
    }
    return out;
  };
  const cols = (p: number[]) => p[p.length - 1] + 1;
  let lo = Math.max(...hs) - 1; // infeasible: every block alone
  let hi = hs.reduce((s, h) => s + h, 0) + GAP_TOP * (hs.length - 1); // feasible: one column
  while (hi - lo > 1) {
    const mid = Math.floor((lo + hi) / 2);
    if (cols(pack(mid)) <= ncol) hi = mid; else lo = mid;
  }
  return pack(hi);
}

function computeLayout(blocks: Block[], egress: Egress, ncol: number): Layout {
  const gx = INET_W + COL_GAP_1;
  const fx = gx + GW_W + COL_GAP_2;
  const colX = (c: number) => fx + FRAME_PAD + c * COL_STRIDE;
  const laneY = -LANE_ABOVE;
  const assign = partition(blocks.map((b) => b.h), ncol);
  const cols = blocks.length ? assign[assign.length - 1] + 1 : 1;
  const cards: CardL[] = [];
  const brackets: Bracket[] = [];
  const colY = new Array<number>(cols).fill(FRAME_HEAD - GAP_TOP);

  blocks.forEach((b, i) => {
    const col = assign[i];
    const cx = colX(col);
    let y = colY[col] + GAP_TOP;
    const place = (x: IfaceEntry, child: boolean, faint: boolean): CardL => {
      const lines = cardLines(x);
      const box = { x: child ? cx + INDENT : cx, y, w: child ? CARD_W - INDENT : CARD_W, h: cardHeight(lines.length) };
      const c = { x, box, lines, child, faint, col };
      cards.push(c);
      y += box.h;
      return c;
    };
    const pc = place(b.head, false, b.faint);
    if (b.kids.length) {
      const br: Bracket = { parentY: pc.box.y + pc.box.h / 2, x: cx + CARD_W + BRACKET, childYs: [] };
      for (const k of b.kids) {
        y += GAP_CHILD;
        const kc = place(k, true, false);
        br.childYs.push({ y: kc.box.y + kc.box.h / 2, vlan: k.vlan?.enabled ? k.vlan.id : null, off: isOff(k) });
      }
      brackets.push(br);
    }
    colY[col] = y;
  });
  const bottom = Math.max(FRAME_HEAD, ...colY);
  const frame = { x: fx, y: 0, w: FRAME_PAD * 2 + cols * COL_STRIDE - COL_GAP, h: bottom + FRAME_PAD };

  // Uplinks: one gateway node per family + gateway address. A link-local
  // IPv6 gateway is only the same router on the same link.
  const gwMap = new Map<string, GwL>();
  const direct: Port[] = [];
  const port = (c: CardL, fam: IfaceFamilyKey, li: number): Port => {
    const py = lineCenter(c.box, li);
    const lane = c.col > 0 ? colX(c.col) - LANE_IN : null;
    return { name: c.x.name, fam, px: c.box.x, py, lane, ey: lane == null ? py : laneY, off: isOff(c.x) };
  };
  for (const c of cards) {
    for (const fam of FAMS) {
      const f = c.x[fam];
      const gw = f && f.mode !== 'off' ? f.gateway : '';
      const li = Math.max(0, c.lines.findIndex((l) => l.type === 'addr' && l.fam === fam));
      if (gw) {
        const key = `${fam}|${gw}${fam === 'ipv6' && isLinkLocal6(gw) ? `%${c.x.name}` : ''}`;
        let g = gwMap.get(key);
        if (!g) { g = { key, fam, ip: gw, box: { x: gx, y: 0, w: GW_W, h: GW_H }, members: [] }; gwMap.set(key, g); }
        g.members.push(port(c, fam, li));
      } else if (c.x.public?.[fam]?.ok) {
        const pi = c.lines.findIndex((l) => l.type === 'pub' && l.fam === fam);
        direct.push(port(c, fam, pi >= 0 ? pi : li));
      }
    }
  }
  // Barycentric placement on where the edges leave the frame, then a
  // forward sweep removes overlaps and a uniform shift recentres the column.
  const gws = [...gwMap.values()];
  const target = (g: GwL) => g.members.reduce((s, m) => s + m.ey, 0) / g.members.length;
  gws.sort((a, b) => target(a) - target(b) || (a.fam === b.fam ? 0 : a.fam === 'ipv4' ? -1 : 1));
  let last = -Infinity;
  for (const g of gws) {
    const want = target(g) - GW_H / 2;
    g.box.y = Math.max(want, last + GW_GAP);
    last = g.box.y + GW_H;
  }
  if (gws.length) {
    const shift = gws.reduce((s, g) => s + (target(g) - GW_H / 2 - g.box.y), 0) / gws.length;
    for (const g of gws) g.box.y += shift;
  }

  const inetLines = FAMS.filter((f) => !!egress[f]);
  const inetH = INET_HEAD + Math.max(1, inetLines.length) * INET_LINE + INET_PAD_B;
  const anchors = [...gws.map((g) => g.box.y + GW_H / 2), ...direct.map((d) => d.ey)];
  const inetCy = anchors.length
    ? anchors.reduce((s, v) => s + v, 0) / anchors.length
    : (cards[0] ? cards[0].box.y + cards[0].box.h / 2 : FRAME_HEAD + inetH / 2);
  const inet = { x: 0, y: inetCy - inetH / 2, w: INET_W, h: inetH };

  // Normalise to a (MARGIN, MARGIN) origin.
  const usesLane = [...gws.flatMap((g) => g.members), ...direct].some((m) => m.lane != null);
  const minY = Math.min(0, usesLane ? laneY : 0, inet.y, ...gws.map((g) => g.box.y));
  const maxY = Math.max(frame.y + frame.h, inet.y + inet.h, ...gws.map((g) => g.box.y + GW_H));
  const dy = MARGIN - minY;
  const dx = MARGIN;
  const mv = (b: Box) => { b.x += dx; b.y += dy; };
  const mvP = (m: Port) => { m.px += dx; m.py += dy; m.ey += dy; if (m.lane != null) m.lane += dx; };
  mv(inet); mv(frame);
  cards.forEach((c) => mv(c.box));
  for (const g of gws) { mv(g.box); g.members.forEach(mvP); }
  direct.forEach(mvP);
  for (const b of brackets) { b.parentY += dy; b.x += dx; b.childYs.forEach((c) => { c.y += dy; }); }

  return {
    w: frame.x + frame.w + MARGIN,
    h: maxY - minY + MARGIN * 2,
    inet, inetLines, frame, laneY: laneY + dy, cols, cards, gws, brackets, direct,
  };
}

/**
 * Fewest columns whose fit zoom in the viewport (aw × ah, px) reaches
 * FIT_OK; otherwise the column count with the best fit. Without a measured
 * viewport, one column.
 */
function chooseLayout(list: IfaceEntry[], egress: Egress, aw: number, ah: number): Layout {
  const blocks = buildBlocks(list);
  let best = computeLayout(blocks, egress, 1);
  if (aw <= 0 || ah <= 0) return best;
  const fit = (l: Layout) => Math.min(aw / l.w, ah / l.h);
  let bestK = fit(best);
  for (let n = 2; bestK < FIT_OK && n <= Math.min(MAX_COLS, blocks.length); n++) {
    const l = computeLayout(blocks, egress, n);
    if (l.cols < n) break;
    const k = fit(l);
    if (k > bestK) { best = l; bestK = k; }
  }
  return best;
}

const curve = (x1: number, y1: number, x2: number, y2: number) => {
  const mx = (x1 + x2) / 2;
  return `M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}`;
};

/** Card port → target point; right-hand columns run up their gutter lane and along the top bus lane. */
const route = (p: Port, laneY: number, frameX: number, tx: number, ty: number) => {
  if (p.lane == null) return curve(p.px, p.py, tx, ty);
  const l = p.lane;
  const r = Math.min(CORNER, (p.py - laneY) / 2);
  const mx = (frameX + tx) / 2;
  return `M${p.px},${p.py} H${l + r} Q${l},${p.py} ${l},${p.py - r} V${laneY + r} Q${l},${laneY} ${l - r},${laneY} H${frameX}`
    + ` C${mx},${laneY} ${mx},${ty} ${tx},${ty}`;
};

interface View { x: number; y: number; k: number }
const K_MIN = 0.3;
const K_MAX = 2.5;
const clampK = (k: number) => Math.max(K_MIN, Math.min(K_MAX, k));
const TOP_RESERVE = 52; // overlay bar height kept clear when fitting
const FIT_PAD_X = 32;
const FIT_PAD_Y = 16;

export default function InterfacesTopoView({
  list, egress, loaded, emptyText, selection, actions, hostName,
  egressWarning, onOpenDetail,
}: Props) {
  const { t } = useTranslation();
  const byKey = useMemo(() => new Map(list.map((x) => [x.name, x])), [list]);

  // ---- viewport ----
  const wrapRef = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });
  // The host frame flows into as many columns as the viewport's shape needs.
  const availW = size.w - FIT_PAD_X;
  const availH = size.h - TOP_RESERVE - FIT_PAD_Y;
  const layout = useMemo(() => chooseLayout(list, egress, availW, availH), [list, egress, availW, availH]);
  const [view, setView] = useState<View>({ x: 0, y: 0, k: 1 });
  const viewRef = useRef(view);
  viewRef.current = view;
  const userMoved = useRef(false);

  useLayoutEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const measure = () => setSize({ w: el.clientWidth, h: el.clientHeight });
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // Toolbar zoom/fit glide to the target; wheel and drag stay immediate.
  const anim = useRef(0);
  const stopAnim = () => { if (anim.current) { cancelAnimationFrame(anim.current); anim.current = 0; } };
  useEffect(() => stopAnim, []);
  const animateTo = (to: View) => {
    stopAnim();
    const from = viewRef.current;
    const t0 = performance.now();
    const DUR = 240;
    const step = (now: number) => {
      const p = Math.min(1, (now - t0) / DUR);
      const e = 1 - Math.pow(1 - p, 3);
      // Interpolate scale geometrically so zooming feels even.
      const k = from.k * Math.pow(to.k / from.k, e);
      setView({ k, x: from.x + (to.x - from.x) * e, y: from.y + (to.y - from.y) * e });
      anim.current = p < 1 ? requestAnimationFrame(step) : 0;
    };
    anim.current = requestAnimationFrame(step);
  };

  const fitTarget = useCallback((): View | null => {
    if (!size.w || !size.h) return null;
    const k = clampK(Math.min(1.15, availW / layout.w, availH / layout.h));
    const x = (size.w - layout.w * k) / 2;
    const y = TOP_RESERVE + Math.max(0, (size.h - TOP_RESERVE - layout.h * k) / 2);
    return { x, y, k };
  }, [size.w, size.h, availW, availH, layout.w, layout.h]);

  // Keep the diagram fitted until the user pans or zooms.
  useLayoutEffect(() => {
    if (userMoved.current) return;
    const v = fitTarget();
    if (v) { stopAnim(); setView(v); }
  }, [fitTarget]);

  const resetView = () => {
    userMoved.current = false;
    const v = fitTarget();
    if (v) animateTo(v);
  };

  const zoomAt = useCallback((factor: number, cx: number, cy: number) => {
    userMoved.current = true;
    stopAnim();
    setView((v) => {
      const k = clampK(v.k * factor);
      const s = k / v.k;
      return { k, x: cx - (cx - v.x) * s, y: cy - (cy - v.y) * s };
    });
  }, []);
  const zoomCenter = (factor: number) => {
    userMoved.current = true;
    const v = viewRef.current;
    const k = clampK(v.k * factor);
    const s = k / v.k;
    const cx = size.w / 2, cy = size.h / 2;
    animateTo({ k, x: cx - (cx - v.x) * s, y: cy - (cy - v.y) * s });
  };

  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      if ((e.target as HTMLElement).closest('[data-overlay]')) return;
      e.preventDefault();
      const r = el.getBoundingClientRect();
      zoomAt(Math.exp(-e.deltaY * 0.0015), e.clientX - r.left, e.clientY - r.top);
    };
    el.addEventListener('wheel', onWheel, { passive: false });
    return () => el.removeEventListener('wheel', onWheel);
  }, [zoomAt]);

  // Pan with one pointer, pinch with two. A press only becomes a pan after
  // 4px of travel so clicks on nodes still register as clicks.
  const pointers = useRef(new Map<number, { x: number; y: number }>());
  const gesture = useRef<{ sx: number; sy: number; v: View; panning: boolean; pinch?: { d: number; mx: number; my: number } } | null>(null);
  const suppressClick = useRef(false);

  const onPointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    if ((e.target as HTMLElement).closest('[data-overlay]')) return;
    if (e.pointerType === 'mouse' && e.button !== 0) return;
    stopAnim();
    pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
    const r = e.currentTarget.getBoundingClientRect();
    if (pointers.current.size === 2) {
      const [a, b] = [...pointers.current.values()];
      gesture.current = {
        sx: 0, sy: 0, v: viewRef.current, panning: true,
        pinch: { d: Math.max(1, Math.hypot(a.x - b.x, a.y - b.y)), mx: (a.x + b.x) / 2 - r.left, my: (a.y + b.y) / 2 - r.top },
      };
      try { e.currentTarget.setPointerCapture(e.pointerId); } catch { /* ignore */ }
      return;
    }
    gesture.current = { sx: e.clientX, sy: e.clientY, v: viewRef.current, panning: false };
  };

  const onPointerMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (!pointers.current.has(e.pointerId)) return;
    pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
    const g = gesture.current;
    if (!g) return;
    if (g.pinch && pointers.current.size >= 2) {
      const [a, b] = [...pointers.current.values()];
      const d = Math.max(1, Math.hypot(a.x - b.x, a.y - b.y));
      const k = clampK(g.v.k * (d / g.pinch.d));
      const s = k / g.v.k;
      userMoved.current = true;
      setView({ k, x: g.pinch.mx - (g.pinch.mx - g.v.x) * s, y: g.pinch.my - (g.pinch.my - g.v.y) * s });
      return;
    }
    const dxp = e.clientX - g.sx;
    const dyp = e.clientY - g.sy;
    if (!g.panning) {
      if (Math.hypot(dxp, dyp) < 4) return;
      g.panning = true;
      try { e.currentTarget.setPointerCapture(e.pointerId); } catch { /* ignore */ }
    }
    userMoved.current = true;
    setView({ ...g.v, x: g.v.x + dxp, y: g.v.y + dyp });
  };

  const onPointerUp = (e: ReactPointerEvent<HTMLDivElement>) => {
    pointers.current.delete(e.pointerId);
    const g = gesture.current;
    if (g?.panning) {
      suppressClick.current = true;
      window.setTimeout(() => { suppressClick.current = false; }, 0);
    }
    if (pointers.current.size === 0) gesture.current = null;
    else if (g?.pinch) {
      // One finger left after a pinch: continue as a pan from here.
      const [p] = [...pointers.current.values()];
      gesture.current = { sx: p.x, sy: p.y, v: viewRef.current, panning: true };
    }
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
      if (suppressClick.current || e.detail > 1) return;
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
    if (suppressClick.current) return;
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
    return <span className={clsx('iface-topo-ellip', egClass(x.name, fam) && 'iface-topo-pubip')}>{p.ip}</span>;
  };

  const renderCard = (c: CardL) => {
    const x = c.x;
    const sel = selection.selected.has(x.name);
    return (
      <div
        key={x.name}
        className={clsx('iface-topo-card', c.child && 'child', isOff(x) && 'off', c.faint && 'faint', sel && 'sel')}
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
  const inetPort = (fam?: IfaceFamilyKey) => {
    const { inet, inetLines } = layout;
    const i = fam ? inetLines.indexOf(fam) : -1;
    const y = i >= 0 ? inet.y + INET_HEAD + i * INET_LINE + INET_LINE / 2 : inet.y + INET_HEAD / 2;
    return { x: inet.x + inet.w, y };
  };
  const pushEdge = (key: string, d: string, cls: string, flow: boolean) => {
    const node = (
      <g key={key} className={clsx('iface-topo-edge', cls)}>
        <path d={d} className="iface-topo-edge-line" />
        {flow && <path d={d} className="iface-topo-edge-flow" />}
      </g>
    );
    (cls.includes('eg') ? hot : edges).push(node);
  };
  for (const g of layout.gws) {
    const gxR = g.box.x + g.box.w;
    const gy = g.box.y + g.box.h / 2;
    let gwCls = '';
    for (const m of g.members) {
      const cls = egClass(m.name, m.fam);
      if (cls) gwCls = cls;
      pushEdge(`u-${g.key}-${m.name}`, route(m, layout.laneY, layout.frame.x, gxR, gy), clsx(cls, m.off && 'off'), !!cls && !m.off);
    }
    const allOff = g.members.every((m) => m.off);
    const p = inetPort(gwCls ? g.fam : undefined);
    pushEdge(`g-${g.key}`, curve(g.box.x, gy, p.x, p.y), clsx(gwCls, allOff && 'off'), !!gwCls && !allOff);
  }
  for (const d of layout.direct) {
    const cls = egClass(d.name, d.fam);
    const p = inetPort(cls ? d.fam : undefined);
    pushEdge(`d-${d.name}-${d.fam}`, route(d, layout.laneY, layout.frame.x, p.x, p.y), clsx(cls, d.off && 'off'), !!cls && !d.off);
  }
  for (const [bi, b] of layout.brackets.entries()) {
    const last = b.childYs[b.childYs.length - 1].y;
    const right = b.x - BRACKET;
    const parts = [`M${right},${b.parentY} H${b.x} V${last}`];
    for (const c of b.childYs) parts.push(`M${b.x},${c.y} H${right}`);
    edges.push(<path key={`b-${bi}`} d={parts.join(' ')} className="iface-topo-bracket" />);
  }

  const gwEgress = (g: GwL) => g.members.map((m) => egClass(m.name, m.fam)).find(Boolean) || '';

  const inetPub = (fam: IfaceFamilyKey): ReactNode => {
    const x = byKey.get(egress[fam]);
    const p = x?.public?.[fam];
    const warn = x ? egressWarning(x, fam) : '';
    let v: ReactNode;
    if (p?.ok) v = <span className="iface-topo-ellip">{p.ip}</span>;
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

  return (
    <div
      ref={wrapRef}
      className="iface-topo"
      data-testid="iface-topo"
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      onClick={onBlankClick}
    >
      {empty ? (
        <div className="iface-topo-empty">{loaded ? emptyText : t('app.loading')}</div>
      ) : (
        <div
          className="iface-topo-stage"
          style={{ width: L.w, height: L.h, transform: `translate(${view.x}px,${view.y}px) scale(${view.k})` }}
        >
          <div className="iface-topo-frame" style={{ left: L.frame.x, top: L.frame.y, width: L.frame.w, height: L.frame.h }}>
            <span className="iface-topo-frame-title iface-topo-ellip">{hostName || t('iface.topo.host')}</span>
          </div>
          <svg className="iface-topo-svg" width={L.w} height={L.h} aria-hidden="true">
            {edges}
            {hot}
          </svg>

          <div className="iface-topo-inet" style={{ left: L.inet.x, top: L.inet.y, width: L.inet.w, height: L.inet.h }} data-testid="topo-inet">
            <div className="iface-topo-inet-head" style={{ height: INET_HEAD }}>
              <svg className="iface-topo-globe" viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
                <circle cx="12" cy="12" r="9" />
                <path d="M3 12h18M12 3c2.5 2.6 3.8 5.6 3.8 9s-1.3 6.4-3.8 9M12 3C9.5 5.6 8.2 8.6 8.2 12s1.3 6.4 3.8 9" />
              </svg>
              <span>{t('iface.topo.internet')}</span>
            </div>
            {L.inetLines.length
              ? L.inetLines.map(inetPub)
              : <div className="iface-topo-inet-line iface-muted" style={{ height: INET_LINE }}>{t('iface.topo.noEgress')}</div>}
          </div>

          {L.gws.map((g) => {
            const eg = gwEgress(g);
            return (
              <div key={g.key} className={clsx('iface-topo-gw', eg, g.members.every((m) => m.off) && 'off')}
                style={{ left: g.box.x, top: g.box.y, width: g.box.w, height: g.box.h }}
                title={g.members.map((m) => m.name).join(', ')} data-testid={`topo-gw-${g.ip}`}>
                <span className="iface-topo-gw-label">{t('iface.gatewayFam', { fam: FAM_LABEL[g.fam] })}</span>
                <span className="iface-topo-gw-ip iface-topo-ellip">{g.ip}</span>
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
            <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true"><path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5" /></svg>
          </button>
          <button type="button" className="iface-topo-tool" onClick={() => zoomCenter(1 / 1.2)} title={t('iface.topo.zoomOut')}>
            <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true"><path d="M5 12h14" /></svg>
          </button>
          <button type="button" className="iface-topo-tool" onClick={() => zoomCenter(1.2)} title={t('iface.topo.zoomIn')}>
            <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true"><path d="M5 12h14M12 5v14" /></svg>
          </button>
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
