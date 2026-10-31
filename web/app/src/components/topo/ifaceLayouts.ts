import type { IfaceEntry, IfaceFamilyKey, Egress } from '@/api/types';
import { isLinkLocal6 } from '@/utils/ipaddr';

/*
 * Interface topology layouts (built-in styles; nothing is user-placed).
 *
 *   lr       Internet ── gateways ── [this node: interface cards]   (left → right)
 *   tb       the same, top → bottom: Internet, a row of gateways, the node
 *   uplink   one column per uplink (gateway pair / direct / none), its
 *            interfaces hanging from a per-family trunk
 *   compact  the node's interfaces only, short cards in a grid
 *
 * Every interface (container networks included) is a full card; sub-interfaces
 * sit under their parent, tied to it by a bracket on the right (VLAN tag on
 * the bracket), except in "uplink", where each card sits under its own uplink.
 * Edges leave a card at the address line of their family, so the IPv4 and
 * IPv6 egress paths read end to end in their own colour. Edges never cross a
 * card: cards away from the frame edge reach it through a gutter lane.
 */

export type IfaceTopoStyle = 'lr' | 'tb' | 'uplink' | 'compact';
export const IFACE_TOPO_STYLES: IfaceTopoStyle[] = ['lr', 'tb', 'uplink', 'compact'];

export const FAMS: IfaceFamilyKey[] = ['ipv4', 'ipv6'];

// Geometry (px, diagram space). Card internals must match interfaces-topo.css.
const MARGIN = 20;
export const INET_W = 190;
const GW_W = 160;
export const GW_H = 38;
export const GW_LINE = 30;   // one family row of a multi-family uplink box
const GW_GAP = 10;
const COL_GAP_1 = 70;   // lr: Internet → gateways
const COL_GAP_2 = 90;   // lr: gateways → node frame
const ROW_GAP_1 = 46;   // tb: Internet → gateways
const ROW_GAP_2 = 60;   // tb: gateways → node frame
const FRAME_PAD = 12;
const FRAME_HEAD = 26;
const CARD_W = 220;
const INDENT = 20;
export const BRACKET = 12;     // card right edge → bracket line
const BRACKET_LABEL = 56;
const BORDER = 1;
const PAD_T = 4;
const NAME_H = 18;
const LINES_GAP = 2;
const LINE_H = 16;
const PAD_B = 4;
const GAP_TOP = 10;     // between top-level blocks
const GAP_ROW = 18;     // between rows of blocks (tb, compact)
const GAP_CHILD = 6;
export const VLAN_H = 16;
export const INET_HEAD = 30;
export const INET_LINE = 18;
const INET_PAD_B = 6;

const COL_GAP = 14;     // bracket-label area → next column's cards
const COL_STRIDE = CARD_W + BRACKET + BRACKET_LABEL + COL_GAP;
const LANE_IN = 8;      // gutter lane → card left edge
const LANE_ABOVE = 10;  // lr: top bus lane above the frame
const CORNER = 6;
const MAX_COLS = 8;
const MAX_GRID_COLS = 10;
const FIT_OK = 0.9;     // lr: fewest columns whose fit zoom reaches this

// uplink style
const TRUNK_W = 30;     // trunk area left of the cards
const TRUNK_X0 = 9;     // first trunk inside it; one per family
const TRUNK_STEP = 9;
const UP_COL_GAP = 36;
const UP_HANG = 22;     // uplink box → first card
const UP_ROW_GAP = 56;  // Internet → uplink boxes

export interface Box { x: number; y: number; w: number; h: number }
export type LineSpec = { type: 'addr' | 'pub'; fam: IfaceFamilyKey };
export interface CardL { x: IfaceEntry; box: Box; lines: LineSpec[]; child: boolean; faint: boolean; col: number }
/**
 * An edge endpoint on a card line. `lane` is the gutter lane x when the edge
 * does not leave straight from the card side; (ex, ey) is where the edge
 * leaves the frame, used to place the gateways.
 */
export interface Port { name: string; fam: IfaceFamilyKey; px: number; py: number; lane: number | null; ex: number; ey: number; off: boolean }
/** One family of an uplink: a gateway address, or '' = no gateway (direct). */
export interface GwLine { fam: IfaceFamilyKey; ip: string }
export interface GwL { key: string; kind: 'gw' | 'local'; lines: GwLine[]; box: Box; members: Port[] }
export interface Bracket { parentY: number; x: number; childYs: { y: number; vlan: number | null; off: boolean }[] }
export interface Layout {
  style: IfaceTopoStyle;
  w: number; h: number;
  /** columns of cards (lr: frame columns) */
  cols: number;
  inet: Box | null; inetLines: IfaceFamilyKey[];
  frame: Box | null;
  cards: CardL[];
  gws: GwL[];
  brackets: Bracket[];
  direct: Port[];
  /** member port → its uplink box */
  up: (p: Port, g: GwL) => string;
  /** uplink box → Internet for one family; `hot`: to that family's line */
  toInet: (g: GwL, fam: IfaceFamilyKey, hot: boolean) => string;
  /** direct port → Internet */
  directToInet: (p: Port, hot: boolean) => string;
}

const byName = (a: IfaceEntry, b: IfaceEntry) => a.name.localeCompare(b.name, undefined, { numeric: true });
export const hasAddr = (x: IfaceEntry) => (x.ipv4?.addrs?.length || 0) + (x.ipv6?.addrs?.length || 0) > 0;
const isLoopback = (x: IfaceEntry) => x.name === 'lo' || !!x.ipv4?.addrs?.some((a) => a.prefix.startsWith('127.'));
export const isOff = (x: IfaceEntry) => x.enabled === false || !x.up;

export function primaryAddr(x: IfaceEntry, fam: IfaceFamilyKey) {
  const a = x[fam]?.addrs || [];
  if (fam === 'ipv6' && a.length > 1 && isLinkLocal6(a[0].prefix)) return a.find((y) => !isLinkLocal6(y.prefix)) || a[0];
  return a[0];
}

/** Each family's public address sits right under its local address; compact cards have none. */
function cardLines(x: IfaceEntry, compact: boolean): LineSpec[] {
  const out: LineSpec[] = [];
  for (const fam of FAMS) {
    const f = x[fam];
    const pending = !!f && f.mode !== 'off' && (f.state === 'acquiring' || f.state === 'failed');
    if ((f?.addrs?.length || 0) > 0 || pending) out.push({ type: 'addr', fam });
    if (!compact && x.public?.[fam]) out.push({ type: 'pub', fam });
  }
  if (!out.some((l) => l.type === 'addr')) out.unshift({ type: 'addr', fam: 'ipv4' });
  return out;
}

const cardHeight = (n: number) => BORDER * 2 + PAD_T + NAME_H + LINES_GAP + n * LINE_H + PAD_B;
const lineCenter = (box: Box, i: number) => box.y + BORDER + PAD_T + NAME_H + LINES_GAP + i * LINE_H + LINE_H / 2;
const addrLine = (lines: LineSpec[], fam: IfaceFamilyKey) => Math.max(0, lines.findIndex((l) => l.type === 'addr' && l.fam === fam));

/** A top-level card with its sub-interfaces; never split across columns or rows. */
interface Block { head: IfaceEntry; kids: IfaceEntry[]; faint: boolean; h: number }

function buildBlocks(list: IfaceEntry[], compact: boolean): Block[] {
  const children = list.filter((x) => x.kind === 'child');
  const others = list.filter((x) => x.kind !== 'child');
  const known = new Set(others.map((x) => x.name));
  // Same order as the list view: NICs, other links (addressed first),
  // loopback, container networks.
  const rank = (x: IfaceEntry) => (x.kind === 'physical' ? 0 : x.container ? 4 : isLoopback(x) ? 3 : hasAddr(x) ? 1 : 2);
  const main = [...others].sort((a, b) => rank(a) - rank(b) || byName(a, b));
  const orphans = children.filter((c) => !known.has(c.parent)).sort(byName);
  const ch = (x: IfaceEntry) => cardHeight(cardLines(x, compact).length);
  const blocks: Block[] = main.map((p) => {
    const kids = children.filter((c) => c.parent === p.name).sort(byName);
    return { head: p, kids, faint: isLoopback(p), h: ch(p) + kids.reduce((s, k) => s + GAP_CHILD + ch(k), 0) };
  });
  for (const o of orphans) blocks.push({ head: o, kids: [], faint: false, h: ch(o) });
  return blocks;
}

/** Places a block's cards top-down from (cx, y); returns the bottom. */
function placeBlock(b: Block, cx: number, y: number, col: number, compact: boolean, cards: CardL[], brackets: Bracket[]) {
  const place = (x: IfaceEntry, child: boolean, faint: boolean): CardL => {
    const lines = cardLines(x, compact);
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
  return y;
}

/** The uplinks of the node's cards: one box per family + gateway, plus the direct ports. */
function collectUplinks(cards: CardL[], port: (c: CardL, fam: IfaceFamilyKey, li: number) => Port, box: Box) {
  const gwMap = new Map<string, GwL>();
  const direct: Port[] = [];
  for (const c of cards) {
    for (const fam of FAMS) {
      const f = c.x[fam];
      const gw = f && f.mode !== 'off' ? f.gateway : '';
      const li = addrLine(c.lines, fam);
      if (gw) {
        // A link-local IPv6 gateway is only the same router on the same link.
        const key = `${fam}|${gw}${fam === 'ipv6' && isLinkLocal6(gw) ? `%${c.x.name}` : ''}`;
        let g = gwMap.get(key);
        if (!g) { g = { key, kind: 'gw', lines: [{ fam, ip: gw }], box: { ...box }, members: [] }; gwMap.set(key, g); }
        g.members.push(port(c, fam, li));
      } else if (c.x.public?.[fam]?.ok) {
        const pi = c.lines.findIndex((l) => l.type === 'pub' && l.fam === fam);
        direct.push(port(c, fam, pi >= 0 ? pi : li));
      }
    }
  }
  return { gws: [...gwMap.values()], direct };
}

/** Spreads boxes along one axis around their targets without overlap, then recentres. */
function spread<T>(items: T[], target: (t: T) => number, size: number, gap: number, set: (t: T, v: number) => void, get: (t: T) => number) {
  items.sort((a, b) => target(a) - target(b));
  let last = -Infinity;
  for (const it of items) {
    const v = Math.max(target(it) - size / 2, last + gap);
    set(it, v);
    last = v + size;
  }
  if (items.length) {
    const shift = items.reduce((s, it) => s + (target(it) - size / 2 - get(it)), 0) / items.length;
    for (const it of items) set(it, get(it) + shift);
  }
}

const inetHeight = (n: number) => INET_HEAD + Math.max(1, n) * INET_LINE + INET_PAD_B;

/** Moves everything so the bounding box starts at (MARGIN, MARGIN); returns the size. */
function normalise(l: Omit<Layout, 'w' | 'h' | 'up' | 'toInet' | 'directToInet' | 'style' | 'cols'>, extra: { x: number; y: number }[] = []) {
  const boxes = [l.inet, l.frame, ...l.cards.map((c) => c.box), ...l.gws.map((g) => g.box)].filter(Boolean) as Box[];
  const ports = [...l.gws.flatMap((g) => g.members), ...l.direct];
  const xs = [...boxes.map((b) => b.x), ...ports.map((p) => p.lane ?? p.px), ...extra.map((e) => e.x)];
  const ys = [...boxes.map((b) => b.y), ...ports.map((p) => p.ey), ...extra.map((e) => e.y)];
  const minX = Math.min(...xs);
  const minY = Math.min(...ys);
  const dx = MARGIN - minX;
  const dy = MARGIN - minY;
  const mv = (b: Box) => { b.x += dx; b.y += dy; };
  for (const b of boxes) mv(b);
  for (const p of ports) { p.px += dx; p.py += dy; p.ex += dx; p.ey += dy; if (p.lane != null) p.lane += dx; }
  for (const b of l.brackets) { b.parentY += dy; b.x += dx; b.childYs.forEach((c) => { c.y += dy; }); }
  const maxX = Math.max(...boxes.map((b) => b.x + b.w), ...l.brackets.map((b) => b.x + BRACKET_LABEL));
  const maxY = Math.max(...boxes.map((b) => b.y + b.h));
  return { dx, dy, w: maxX + MARGIN, h: maxY + MARGIN };
}

const curveH = (x1: number, y1: number, x2: number, y2: number) => {
  const mx = (x1 + x2) / 2;
  return `M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}`;
};
const curveV = (x1: number, y1: number, x2: number, y2: number) => {
  const my = (y1 + y2) / 2;
  return `M${x1},${y1} C${x1},${my} ${x2},${my} ${x2},${y2}`;
};
/** Card side → gutter lane → up to y (no closing curve). */
const toLaneTop = (p: Port, lane: number, top: number) => {
  const r = Math.min(CORNER, Math.max(0, (p.py - top) / 2), Math.max(0, (p.px - lane) / 2));
  return `M${p.px},${p.py} H${lane + r} Q${lane},${p.py} ${lane},${p.py - r} V${top}`;
};

// ------------------------------------------------------------------ lr

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

/**
 * Internet | gateways | node frame, left to right. When the cards would not
 * fit the viewport at a readable zoom, the frame flows into top-aligned
 * columns; cards in later columns reach the gateways via a lane in the
 * gutter to their left and a bus above the frame. Gateways sit at the
 * barycenter of where their edges leave the frame.
 */
function layoutLR(blocks: Block[], egress: Egress, ncol: number): Layout {
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
    colY[col] = placeBlock(b, colX(col), colY[col] + GAP_TOP, col, false, cards, brackets);
  });
  const frame = { x: fx, y: 0, w: FRAME_PAD * 2 + cols * COL_STRIDE - COL_GAP, h: Math.max(FRAME_HEAD, ...colY) + FRAME_PAD };

  const { gws, direct } = collectUplinks(cards, (c, fam, li) => {
    const py = lineCenter(c.box, li);
    const lane = c.col > 0 ? colX(c.col) - LANE_IN : null;
    return { name: c.x.name, fam, px: c.box.x, py, lane, ex: fx, ey: lane == null ? py : laneY, off: isOff(c.x) };
  }, { x: gx, y: 0, w: GW_W, h: GW_H });
  const target = (g: GwL) => g.members.reduce((s, m) => s + m.ey, 0) / g.members.length + GW_H / 2;
  spread(gws, target, GW_H, GW_GAP, (g, v) => { g.box.y = v; }, (g) => g.box.y);

  const inetLines = FAMS.filter((f) => !!egress[f]);
  const inetH = inetHeight(inetLines.length);
  const anchors = [...gws.map((g) => g.box.y + GW_H / 2), ...direct.map((d) => d.ey)];
  const inetCy = anchors.length
    ? anchors.reduce((s, v) => s + v, 0) / anchors.length
    : (cards[0] ? cards[0].box.y + cards[0].box.h / 2 : FRAME_HEAD + inetH / 2);
  const inet = { x: 0, y: inetCy - inetH / 2, w: INET_W, h: inetH };

  const base = { inet, inetLines, frame, cards, gws, brackets, direct };
  const usesLane = [...gws.flatMap((g) => g.members), ...direct].some((m) => m.lane != null);
  const n = normalise(base, usesLane ? [{ x: fx, y: laneY }] : []);
  const lane0 = laneY + n.dy;
  const inetPort = (fam: IfaceFamilyKey, hot: boolean) => {
    const i = hot ? inetLines.indexOf(fam) : -1;
    return { x: inet.x + inet.w, y: i >= 0 ? inet.y + INET_HEAD + i * INET_LINE + INET_LINE / 2 : inet.y + INET_HEAD / 2 };
  };
  // Right-hand columns run up their gutter lane and along the top bus lane.
  const route = (p: Port, tx: number, ty: number) => {
    if (p.lane == null) return curveH(p.px, p.py, tx, ty);
    const l = p.lane;
    const r = Math.min(CORNER, (p.py - lane0) / 2);
    const mx = (frame.x + tx) / 2;
    return `M${p.px},${p.py} H${l + r} Q${l},${p.py} ${l},${p.py - r} V${lane0 + r} Q${l},${lane0} ${l - r},${lane0} H${frame.x}`
      + ` C${mx},${lane0} ${mx},${ty} ${tx},${ty}`;
  };
  return {
    style: 'lr', ...base, cols, w: n.w, h: n.h,
    up: (p, g) => route(p, g.box.x + g.box.w, g.box.y + g.box.h / 2),
    toInet: (g, fam, hot) => { const q = inetPort(fam, hot); return curveH(g.box.x, g.box.y + g.box.h / 2, q.x, q.y); },
    directToInet: (p, hot) => { const q = inetPort(p.fam, hot); return route(p, q.x, q.y); },
  };
}

// ------------------------------------------------------------------ tb / compact

/**
 * The node frame as a grid: blocks fill rows of `ncol` equal slots, so the
 * gutters line up through every row. With uplinks (tb), Internet and a row
 * of gateways sit above the frame; an edge leaves a card on its left, runs
 * up the gutter lane of its slot and out of the frame top.
 */
function layoutGrid(blocks: Block[], egress: Egress, ncol: number, compact: boolean): Layout {
  const slotX = (c: number) => FRAME_PAD + c * COL_STRIDE;
  const cards: CardL[] = [];
  const brackets: Bracket[] = [];
  let y = FRAME_HEAD;
  for (let r = 0; r * ncol < blocks.length; r++) {
    let bottom = y;
    for (let c = 0; c < ncol && r * ncol + c < blocks.length; c++) {
      bottom = Math.max(bottom, placeBlock(blocks[r * ncol + c], slotX(c), y, c, compact, cards, brackets));
    }
    y = bottom + GAP_ROW;
  }
  const cols = Math.max(1, Math.min(ncol, blocks.length));
  const frame = { x: 0, y: 0, w: FRAME_PAD * 2 + cols * COL_STRIDE - COL_GAP, h: Math.max(FRAME_HEAD, y - GAP_ROW) + FRAME_PAD };

  if (compact) {
    const base = { inet: null, inetLines: [] as IfaceFamilyKey[], frame, cards, gws: [] as GwL[], brackets, direct: [] as Port[] };
    const n = normalise(base);
    const none = () => '';
    return { style: 'compact', ...base, cols, w: n.w, h: n.h, up: none, toInet: none, directToInet: none };
  }

  const { gws, direct } = collectUplinks(cards, (c, fam, li) => {
    const lane = slotX(c.col) - LANE_IN;
    return { name: c.x.name, fam, px: c.box.x, py: lineCenter(c.box, li), lane, ex: lane, ey: 0, off: isOff(c.x) };
  }, { x: 0, y: -ROW_GAP_2 - GW_H, w: GW_W, h: GW_H });
  const target = (g: GwL) => g.members.reduce((s, m) => s + m.ex, 0) / g.members.length;
  spread(gws, target, GW_W, GW_GAP * 2, (g, v) => { g.box.x = v; }, (g) => g.box.x);

  const inetLines = FAMS.filter((f) => !!egress[f]);
  const inetH = inetHeight(inetLines.length);
  const anchors = [...gws.map((g) => g.box.x + GW_W / 2), ...direct.map((d) => d.ex)];
  const inetCx = anchors.length ? anchors.reduce((s, v) => s + v, 0) / anchors.length : frame.w / 2;
  const inetBottom = (gws.length ? -ROW_GAP_2 - GW_H - ROW_GAP_1 : -ROW_GAP_2);
  const inet = { x: inetCx - INET_W / 2, y: inetBottom - inetH, w: INET_W, h: inetH };

  const base = { inet, inetLines, frame, cards, gws, brackets, direct };
  const n = normalise(base);
  const inetPort = (fam: IfaceFamilyKey, hot: boolean) => ({
    x: inet.x + inet.w * (hot && inetLines.includes(fam) ? (fam === 'ipv4' ? 0.35 : 0.65) : 0.5),
    y: inet.y + inet.h,
  });
  const top = frame.y;
  const out = (p: Port, tx: number, ty: number) => `${toLaneTop(p, p.lane!, top)} ${curveV(p.lane!, top, tx, ty).replace(/^M[^C]*/, '')}`;
  return {
    style: 'tb', ...base, cols, w: n.w, h: n.h,
    up: (p, g) => out(p, g.box.x + g.box.w / 2, g.box.y + g.box.h),
    toInet: (g, fam, hot) => { const q = inetPort(fam, hot); return curveV(g.box.x + g.box.w / 2, g.box.y, q.x, q.y); },
    directToInet: (p, hot) => { const q = inetPort(p.fam, hot); return out(p, q.x, q.y); },
  };
}

// ------------------------------------------------------------------ uplink

/**
 * One column per uplink: the families' gateways together (or "direct" for a
 * family with a public address and no gateway, or no uplink at all). The
 * column's interfaces hang from one trunk per family, entering each card at
 * that family's address line. Columns holding the egress come first; the
 * interfaces without an uplink come last and wrap into several columns.
 */
function layoutUplink(list: IfaceEntry[], egress: Egress, localPer: number): Layout {
  const blocks = buildBlocks(list, false);
  const order = blocks.flatMap((b) => [b.head, ...b.kids]);
  const kids = new Set(blocks.flatMap((b) => b.kids.map((k) => k.name)));
  const faint = new Set(blocks.filter((b) => b.faint).map((b) => b.head.name));
  interface Group { key: string; kind: 'gw' | 'local'; lines: GwLine[]; items: IfaceEntry[] }
  const groups = new Map<string, Group>();
  for (const x of order) {
    const lines: GwLine[] = [];
    for (const fam of FAMS) {
      const f = x[fam];
      const gw = f && f.mode !== 'off' ? f.gateway : '';
      if (gw) lines.push({ fam, ip: gw + (fam === 'ipv6' && isLinkLocal6(gw) ? `%${x.name}` : '') });
      else if (x.public?.[fam]?.ok) lines.push({ fam, ip: '' });
    }
    const key = lines.length ? lines.map((l) => `${l.fam}|${l.ip}`).join(' ') : 'local';
    let g = groups.get(key);
    if (!g) {
      g = { key, kind: lines.length ? 'gw' : 'local', lines: lines.map((l) => ({ fam: l.fam, ip: l.ip.replace(/%.*$/, '') })), items: [] };
      groups.set(key, g);
    }
    g.items.push(x);
  }
  const rank = (g: Group) => {
    if (g.kind === 'local') return 9;
    if (egress.ipv4 && g.items.some((x) => x.name === egress.ipv4)) return 0;
    if (egress.ipv6 && g.items.some((x) => x.name === egress.ipv6)) return 1;
    return g.lines.every((l) => !l.ip) ? 3 : 2;
  };
  const list2 = [...groups.values()].sort((a, b) => rank(a) - rank(b) || b.items.length - a.items.length);

  const colW = TRUNK_W + CARD_W;
  const boxH = (g: Group) => (g.kind === 'local' ? GW_H : 8 + g.lines.length * GW_LINE);
  const top = 0;
  const cards: CardL[] = [];
  const gws: GwL[] = [];
  let x = 0;
  let col = 0;
  for (const g of list2) {
    const local = g.kind === 'local';
    // No trunks in the "no uplink" column: its cards wrap into sub-columns.
    const per = local ? Math.max(1, localPer) : g.items.length;
    const nsub = Math.max(1, Math.ceil(g.items.length / per));
    const subW = local ? CARD_W : colW;
    const w = nsub * subW + (nsub - 1) * UP_COL_GAP;
    const box = { x, y: top, w: Math.min(w, colW), h: boxH(g) };
    if (g.kind === 'local') box.w = w;
    const gl: GwL = { key: g.key, kind: g.kind, lines: g.lines, box, members: [] };
    gws.push(gl);
    g.items.forEach((it, i) => {
      const sub = Math.floor(i / per);
      const cx = x + sub * (subW + UP_COL_GAP) + (local ? 0 : TRUNK_W);
      const prev = cards.length && i % per !== 0 ? cards[cards.length - 1].box : null;
      const lines = cardLines(it, false);
      const cy = prev ? prev.y + prev.h + GAP_TOP : top + box.h + UP_HANG;
      const cb = { x: cx, y: cy, w: CARD_W, h: cardHeight(lines.length) };
      const c: CardL = { x: it, box: cb, lines, child: kids.has(it.name), faint: faint.has(it.name), col };
      cards.push(c);
      if (g.kind === 'gw') {
        g.lines.forEach((l, fi) => {
          const lane = x + TRUNK_X0 + fi * TRUNK_STEP;
          const li = addrLine(lines, l.fam);
          const pi = !l.ip ? lines.findIndex((s) => s.type === 'pub' && s.fam === l.fam) : -1;
          const py = lineCenter(cb, pi >= 0 ? pi : li);
          gl.members.push({ name: it.name, fam: l.fam, px: cx, py, lane, ex: lane, ey: top + box.h, off: isOff(it) });
        });
      }
    });
    x += w + UP_COL_GAP;
    col++;
  }

  const inetLines = FAMS.filter((f) => !!egress[f]);
  const inetH = inetHeight(inetLines.length);
  const up = gws.filter((g) => g.kind === 'gw');
  const anchors = up.map((g) => g.box.x + g.box.w / 2);
  const inetCx = anchors.length ? (Math.min(...anchors) + Math.max(...anchors)) / 2 : (gws[0]?.box.w || INET_W) / 2;
  const inet = { x: inetCx - INET_W / 2, y: top - UP_ROW_GAP - inetH, w: INET_W, h: inetH };

  const base = { inet, inetLines, frame: null, cards, gws, brackets: [] as Bracket[], direct: [] as Port[] };
  const n = normalise(base);
  const inetPort = (fam: IfaceFamilyKey, hot: boolean) => ({
    x: inet.x + inet.w * (hot && inetLines.includes(fam) ? (fam === 'ipv4' ? 0.35 : 0.65) : 0.5),
    y: inet.y + inet.h,
  });
  return {
    style: 'uplink', ...base, cols: col, w: n.w, h: n.h,
    // Into the card at its line, up the family trunk to the uplink box.
    up: (p, g) => {
      const l = p.lane!;
      const r = Math.min(CORNER, (p.py - (g.box.y + g.box.h)) / 2);
      return `M${p.px},${p.py} H${l + r} Q${l},${p.py} ${l},${p.py - r} V${g.box.y + g.box.h}`;
    },
    toInet: (g, fam, hot) => {
      const i = g.lines.findIndex((l) => l.fam === fam);
      const sx = g.box.x + (g.lines.length > 1 ? g.box.w * (i === 0 ? 0.35 : 0.65) : g.box.w / 2);
      const q = inetPort(fam, hot);
      return curveV(sx, g.box.y, q.x, q.y);
    },
    directToInet: () => '',
  };
}

// ------------------------------------------------------------------ choose

/**
 * The layout of `style` that best fits the viewport (aw × ah, px): lr takes
 * the fewest columns whose fit zoom reaches FIT_OK, the grids the column
 * count with the best fit. Without a measured viewport, one column.
 */
export function chooseLayout(style: IfaceTopoStyle, list: IfaceEntry[], egress: Egress, aw: number, ah: number): Layout {
  const fit = (l: Layout) => Math.min(aw / l.w, ah / l.h);
  if (style === 'uplink') {
    // How many "no uplink" cards per sub-column: the best fit, never
    // shorter than the longest uplink column.
    const local = list.filter((x) => !FAMS.some((f) => {
      const v = x[f];
      return (v && v.mode !== 'off' && v.gateway) || x.public?.[f]?.ok;
    })).length;
    const first = layoutUplink(list, egress, Math.max(1, local));
    if (aw <= 0 || ah <= 0 || local <= 4) return first;
    const minPer = Math.max(4, ...first.gws.filter((g) => g.kind === 'gw').map((g) => new Set(g.members.map((m) => m.name)).size));
    let best = first;
    let bestK = fit(first);
    for (let per = minPer; per < local; per = Math.max(per + 1, Math.ceil(per * 1.15))) {
      const l = layoutUplink(list, egress, per);
      const k = fit(l);
      if (k > bestK + 0.001) { best = l; bestK = k; }
    }
    return best;
  }
  const compact = style === 'compact';
  const blocks = buildBlocks(list, compact);
  if (style === 'lr') {
    let best = layoutLR(blocks, egress, 1);
    if (aw <= 0 || ah <= 0) return best;
    let bestK = fit(best);
    for (let n = 2; bestK < FIT_OK && n <= Math.min(MAX_COLS, blocks.length); n++) {
      const l = layoutLR(blocks, egress, n);
      if (l.cols < n) break;
      const k = fit(l);
      if (k > bestK) { best = l; bestK = k; }
    }
    return best;
  }
  const max = Math.max(1, Math.min(MAX_GRID_COLS, blocks.length));
  if (aw <= 0 || ah <= 0) return layoutGrid(blocks, egress, Math.min(max, 3), compact);
  let best = layoutGrid(blocks, egress, 1, compact);
  let bestK = fit(best);
  for (let n = 2; n <= max; n++) {
    const l = layoutGrid(blocks, egress, n, compact);
    const k = fit(l);
    if (k > bestK + 0.001) { best = l; bestK = k; }
  }
  return best;
}
