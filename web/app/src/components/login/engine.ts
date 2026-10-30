import type { Chip, Flow, LinkKind, LinkState, Note, SpecEvent, SpecNode, TopoSpec, Zone } from './spec';

/*
 * Imperative SVG renderer for the login topology. show(spec) morphs from the
 * current topology: nodes that persist (same id and role) move, new ones
 * scale in, links draw in segment by segment, gone ones fade out. step(dt)
 * advances animation, timed events (punches, failures) and packets.
 */

const NS = 'http://www.w3.org/2000/svg';
const MOVE = 0.9;                                   // node move duration (s)
const RAD: Record<string, number> = { self: 18, device: 18, internet: 18, admin: 18, relay: 11, exit: 11, peer: 11, remote: 11, native: 10 };
const TERM: Record<string, string> = { internet: 'globe', admin: 'browser' };

// Lucide paths (ISC, see web/ui-framework/src/icons) for icons drawn inside the SVG.
const ICON: Record<string, string> = {
  lock: '<rect width="18" height="11" x="3" y="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>',
  key: '<path d="M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z"/><circle cx="16.5" cy="7.5" r=".5" fill="currentColor"/>',
  laptop: '<path d="M20 16V7a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v9m16 0H4m16 0 1.28 2.55a1 1 0 0 1-.9 1.45H3.62a1 1 0 0 1-.9-1.45L4 16"/>',
  phone: '<rect width="14" height="20" x="5" y="2" rx="2" ry="2"/><path d="M12 18h.01"/>',
  monitor: '<rect width="20" height="14" x="2" y="3" rx="2"/><line x1="8" x2="16" y1="21" y2="21"/><line x1="12" x2="12" y1="17" y2="21"/>',
  router: '<rect width="20" height="8" x="2" y="14" rx="2"/><path d="M6.01 18H6"/><path d="M10.01 18H10"/><path d="M15 10v4"/><path d="M17.84 7.17a4 4 0 0 0-5.66 0"/><path d="M20.66 4.34a8 8 0 0 0-11.31 0"/>',
  browser: '<rect width="20" height="16" x="2" y="4" rx="2"/><path d="M2 9h20"/><path d="M6 6.5h.01"/><path d="M9 6.5h.01"/>',
  globe: '<circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20"/><path d="M2 12h20"/>',
};
const ZAP = '<path d="M4 14a1 1 0 0 1-.78-1.63l9.9-10.2a.5.5 0 0 1 .86.46l-1.92 6.02A1 1 0 0 0 13 10h7a1 1 0 0 1 .78 1.63l-9.9 10.2a.5.5 0 0 1-.86-.46l1.92-6.02A1 1 0 0 0 11 14z"/>';

type Attrs = Record<string, string | number>;
function se<K extends keyof SVGElementTagNameMap>(tag: K, attrs: Attrs | null, parent?: Element): SVGElementTagNameMap[K] {
  const e = document.createElementNS(NS, tag);
  if (attrs) for (const k in attrs) e.setAttribute(k, String(attrs[k]));
  parent?.appendChild(e);
  return e;
}
const clamp01 = (v: number) => (v < 0 ? 0 : v > 1 ? 1 : v);
const eio = (t: number) => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2);
const ease = (v: number, target: number, dt: number, k: number) => v + (target - v) * Math.min(1, dt * k);
const f1 = (v: number) => v.toFixed(1);
type Pt = [number, number];
const quad = (P0: Pt, C: Pt, P2: Pt, t: number): Pt => {
  const u = 1 - t;
  return [u * u * P0[0] + 2 * u * t * C[0] + t * t * P2[0], u * u * P0[1] + 2 * u * t * C[1] + t * t * P2[1]];
};

interface Fading { alpha: number; target: number; showAt: number; dying: boolean }

interface NodeE extends Fading {
  key: string; id: string; role: string; spec: SpecNode;
  g: SVGGElement; bd: SVGGElement; tl: SVGTextElement; r: number;
  x: number; y: number; fx: number; fy: number; tx: number; ty: number;
  badge?: string; state?: string;
}

interface ChipE { g: SVGGElement; rect: SVGRectElement; text: SVGTextElement; t: number; dy: number; key?: string }

/** Status pill of a punch link; width, text and icon morph between states. */
interface Pill {
  g: SVGGElement; rect: SVGRectElement; dot: SVGCircleElement; zap: SVGSVGElement;
  texts: [SVGTextElement, SVGTextElement]; front: 0 | 1;
  key?: string; w: number; tw: number; textMix: number; upMix: number; op: [number, number];
}

interface LinkE extends Fading {
  key: string; id: string; kind: LinkKind; state: LinkState;
  g: SVGGElement; path: SVGPathElement; fx: SVGGElement; chipG: SVGGElement; chips: ChipE[]; chipsSized: boolean;
  A: NodeE; B: NodeE; curve: number; p: number; drawAt: number;
  pill?: Pill; burst?: SVGCircleElement; probes?: SVGCircleElement[]; upAt?: number;
}

interface NoteE extends Fading { key: string; g: SVGGElement; label?: { rect: SVGRectElement; text: SVGTextElement; key: string } }

interface FlowE extends Flow { next: number; rr: number; lc: number; dc: number }

interface Packet {
  g: SVGGElement; pts: Pt[]; cum: number[]; len: number;
  segs: { L: LinkE; i1: number; s1: number }[]; links: LinkE[];
  s: number; op: number; fade: number; dead?: boolean; done?: boolean;
}

export interface EngineOptions {
  /** Labels: role.*, chip.*, punch.* keys. */
  t: (key: string) => string;
  reducedMotion: () => boolean;
}

export class TopoEngine {
  private ly: { z: SVGGElement; l: SVGGElement; c: SVGGElement; p: SVGGElement; n: SVGGElement };
  private nodes = new Map<string, NodeE>();
  private links = new Map<string, LinkE>();
  private zones = new Map<string, NoteE>();
  private notes = new Map<string, NoteE>();
  private packets: Packet[] = [];
  private flows: FlowE[] = [];
  private events: (SpecEvent & { done: boolean })[] = [];
  private ts = 0;
  private gen = 0;
  private rm = false;

  constructor(svg: SVGSVGElement, private opts: EngineOptions) {
    const g = (cls: string) => se('g', { class: cls }, svg);
    this.ly = { z: g('ly-z'), l: g('ly-l'), c: g('ly-c'), p: g('pk'), n: g('ly-n') };
  }

  destroy() {
    Object.values(this.ly).forEach((e) => e.remove());
  }

  // ---------------------------------------------------------------- show
  show(spec: TopoSpec, instant = false) {
    this.ts = 0;
    this.rm = this.opts.reducedMotion();
    // reduced motion / instant: fresh elements (crossfade) instead of morphing
    const suf = this.rm || instant ? '@' + ++this.gen : '';
    this.packets.forEach((p) => p.g.remove());
    this.packets = [];

    // nodes
    const keepN = new Set<string>(), byId: Record<string, NodeE> = {};
    let persistent = 0;
    spec.nodes.forEach((n) => { const e = this.nodes.get(n.id + ':' + n.role + suf); if (e && !e.dying) persistent++; });
    spec.nodes.forEach((n, i) => {
      const key = n.id + ':' + n.role + suf;
      let e = this.nodes.get(key);
      if (e) { e.fx = e.x; e.fy = e.y; e.tx = n.x; e.ty = n.y; e.spec = n; e.dying = false; e.target = 1; e.showAt = 0; }
      else { e = this.mkNode(n, key); this.nodes.set(key, e); e.showAt = this.rm ? 0 : (persistent ? 0.3 : 0.08) + i * 0.06; }
      this.nodeLabel(e); this.nodeBadge(e); this.setNodeState(e, n.state);
      keepN.add(key); byId[n.id] = e;
    });
    this.nodes.forEach((e, k) => { if (!keepN.has(k)) { e.dying = true; e.target = 0; e.showAt = 0; e.fx = e.tx = e.x; e.fy = e.ty = e.y; } });

    // links (parallel links between one pair are told apart by id)
    const keepL = new Set<string>(), pair: Record<string, number> = {};
    spec.links.forEach((l) => { const pk = [l.a, l.b].sort().join('|'); pair[pk] = (pair[pk] || 0) + 1; });
    spec.links.forEach((l, i) => {
      const A = byId[l.a], B = byId[l.b], pk = [l.a, l.b].sort().join('|');
      const key = `${pk}|${l.kind}|${A.role}|${B.role}${pair[pk] > 1 ? '|' + l.id : ''}${suf}`;
      let e = this.links.get(key);
      if (e && !e.dying) {
        e.A = A; e.B = B; e.curve = l.curve || 0; e.id = l.id; this.setChips(e, l.chips);
        e.target = 1; e.showAt = l.appear || 0; e.drawAt = l.appear || 0;
        if (l.appear) { e.p = 0; e.alpha = 0; }
      } else {
        if (e) this.dropLink(e);
        e = this.mkLink(l.id, l.kind, key, A, B, l.curve || 0, l.chips);
        this.links.set(key, e);
        const d = this.rm ? 0 : l.appear ?? (persistent ? 0.62 : 0.45) + i * 0.06;
        e.showAt = d; e.drawAt = d;
        if (this.rm) e.p = 1;
      }
      this.setLinkState(e, l.state || 'up');
      keepL.add(key);
    });
    this.links.forEach((e, k) => { if (!keepL.has(k)) { e.dying = true; e.target = 0; e.showAt = 0; } });

    // zones + notes
    const keep = new Set<string>();
    const sync = <T extends Zone | Note>(list: T[], map: Map<string, NoteE>, zone: boolean) => list.forEach((z, i) => {
      const key = (zone ? 'z' : 'n') + JSON.stringify(z) + suf;
      let e = map.get(key);
      if (!e) { e = this.mkNote(z, key, zone); map.set(key, e); e.showAt = this.rm ? 0 : 0.55 + i * 0.1; }
      else { e.dying = false; e.target = 1; }
      keep.add(key);
    });
    sync(spec.zones, this.zones, true);
    sync(spec.notes, this.notes, false);
    [this.zones, this.notes].forEach((m) => m.forEach((e, k) => { if (!keep.has(k)) { e.dying = true; e.target = 0; e.showAt = 0; } }));

    // timeline
    this.flows = spec.flows.map((f) => ({ ...f, next: Math.max(f.from ?? 1.25, 1.25), rr: 0, lc: 0, dc: 0 }));
    this.events = spec.events.map((ev) => ({ ...ev, done: false }));
  }

  /** Re-applies labels after a language change. */
  relabel() {
    this.nodes.forEach((e) => this.nodeLabel(e));
    this.links.forEach((e) => {
      e.chips.forEach((c) => { if (c.key) { c.text.textContent = this.opts.t('chip.' + c.key); this.chipSize(c); } });
      if (e.pill?.key) { const k = e.pill.key; e.pill.key = undefined; this.setPill(e, k, true); }
    });
    this.notes.forEach((e) => { if (e.label) { e.label.text.textContent = this.opts.t('chip.' + e.label.key); this.chipSize(e.label); } });
  }

  // ---------------------------------------------------------------- elements
  private mkNode(n: SpecNode, key: string): NodeE {
    const r = RAD[n.role], g = se('g', { class: 'tn tn-' + n.role }, this.ly.n);
    if (n.role === 'self') { se('circle', { class: 'ring', r: 18 }, g); se('circle', { class: 'so', r: 18 }, g); se('circle', { class: 'si', r: 8 }, g); }
    else if (r === 18) {
      se('circle', { class: 'term', r: 18 }, g);
      const i = se('svg', { class: 'ticon', x: -9, y: -9, width: 18, height: 18, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', 'stroke-width': 1.8, 'stroke-linecap': 'round', 'stroke-linejoin': 'round' }, g);
      i.innerHTML = ICON[n.icon || TERM[n.role] || 'laptop'];
    } else if (n.role === 'native') se('circle', { class: 'nat', r: 10 }, g);
    else { se('circle', { class: 'nd', r: 11 }, g); if (n.role === 'relay') se('circle', { class: 'hb', r: 5 }, g); }
    const bd = se('g', { class: 'bd' }, g), tl = se('text', { class: 'tl', y: r + 15 }, g);
    return { key, id: n.id, role: n.role, g, bd, tl, r, x: n.x, y: n.y, fx: n.x, fy: n.y, tx: n.x, ty: n.y, alpha: 0, target: 1, showAt: 0, dying: false, spec: n };
  }

  private nodeLabel(e: NodeE) {
    const n = e.spec;
    e.tl.textContent = '';
    if (n.label === false) return;
    e.tl.appendChild(document.createTextNode(this.opts.t('role.' + n.role)));
    if (n.tag) { const s = se('tspan', { class: 'tg' }, e.tl); s.textContent = ' ' + n.tag; }
  }

  private nodeBadge(e: NodeE) {
    const b = e.spec.badge, r = e.r;
    if (e.badge === b) return;
    e.badge = b;
    if (!b) { e.bd.innerHTML = ''; return; }
    if (b === 'quota') {   // 80 % used ring
      const R = r + 4, a = Math.PI * 2 * 0.8 - Math.PI / 2;
      e.bd.innerHTML = `<circle class="qt" r="${R}"/><path class="qa" d="M0 ${-R} A${R} ${R} 0 1 1 ${(R * Math.cos(a)).toFixed(2)} ${(R * Math.sin(a)).toFixed(2)}"/>`;
      return;
    }
    const o = r * 0.78;
    e.bd.innerHTML = `<g transform="translate(${f1(o)} ${f1(-o)})"><circle class="bdc" r="7.5"/><svg class="bdi ${b}" x="-4.5" y="-4.5" width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round">${ICON[b]}</svg></g>`;
  }

  private setNodeState(e: NodeE, s?: string) { e.state = s; e.g.classList.toggle('off', s === 'off'); }

  private mkLink(id: string, kind: LinkKind, key: string, A: NodeE, B: NodeE, curve: number, chips?: Chip[]): LinkE {
    const g = se('g', { class: `tlk k-${kind}` }, this.ly.l), path = se('path', { class: 'le' }, g);
    const chipG = se('g', null, this.ly.c), fx = se('g', null, this.ly.c);
    const e: LinkE = { key, id, kind, state: 'up', g, path, fx, chipG, chips: [], chipsSized: false, A, B, curve, p: 0, drawAt: 0, alpha: 0, target: 1, showAt: 0, dying: false };
    if (kind === 'punch') {
      const pg = se('g', { class: 'pst', opacity: 0 }, fx);
      const zap = se('svg', { class: 'pzap', width: 10, height: 10, y: -5, viewBox: '0 0 24 24', fill: 'currentColor' }, pg);
      zap.innerHTML = ZAP;
      e.pill = {
        g: pg, rect: se('rect', { rx: 8, y: -8, height: 16 }, pg), dot: se('circle', { class: 'pdot', r: 2.6 }, pg), zap,
        texts: [se('text', null, pg), se('text', { opacity: 0 }, pg)], front: 0, w: 0, tw: 0, textMix: 1, upMix: 0, op: [1, 0],
      };
      pg.insertBefore(e.pill.rect, pg.firstChild);
      e.burst = se('circle', { class: 'pburst', r: 0, opacity: 0 }, fx);
      e.probes = [se('circle', { class: 'probe', r: 2.4, opacity: 0 }, fx), se('circle', { class: 'probe', r: 2.4, opacity: 0 }, fx)];
    }
    this.setChips(e, chips);
    return e;
  }

  private dropLink(e: LinkE) { e.g.remove(); e.chipG.remove(); e.fx.remove(); }

  private setLinkState(e: LinkE, s: LinkState) {
    if (e.pill) e.upAt = s !== 'up' ? undefined : e.state === 'up' ? e.upAt : this.ts;
    e.state = s;
    e.g.setAttribute('class', `tlk k-${e.kind} s-${s}`);
  }

  private setChips(e: LinkE, list?: Chip[]) {
    e.chipG.textContent = '';
    e.chips = (list || []).map((cs) => {
      const g = se('g', { class: 'chip' }, e.chipG), rect = se('rect', null, g), text = se('text', null, g);
      text.textContent = cs.key ? this.opts.t('chip.' + cs.key) : cs.text || '';
      return { g, rect, text, t: cs.t ?? 0.5, dy: cs.dy || 0, key: cs.key };
    });
    e.chipsSized = false;
  }

  private chipSize(c: { rect: SVGRectElement; text: SVGTextElement }) {
    const w = Math.ceil(textWidth(c.text, 6.1)) + 12;
    c.rect.setAttribute('width', String(w)); c.rect.setAttribute('height', '16');
    c.rect.setAttribute('x', String(-w / 2)); c.rect.setAttribute('y', '-8'); c.rect.setAttribute('rx', '4');
  }

  private mkNote(n: Zone | Note, key: string, zone: boolean): NoteE {
    const g = se('g', { class: zone ? 'zone' : 'note' }, zone ? this.ly.z : this.ly.c);
    if (zone) {
      const z = n as Zone;
      se('rect', { x: z.x, y: z.y, width: z.w, height: z.h, rx: 10 }, g);
      se('text', { x: z.x + 10, y: z.y + z.h - 9 }, g).textContent = z.label;
    } else {
      const note = n as Note, rect = se('rect', null, g), text = se('text', null, g);
      text.textContent = note.key ? this.opts.t('chip.' + note.key) : note.text || '';
      g.setAttribute('transform', `translate(${n.x} ${n.y})`);
      requestAnimationFrame(() => this.chipSize({ rect, text }));
      if (note.key) return { key, g, alpha: 0, target: 1, showAt: 0, dying: false, label: { rect, text, key: note.key } };
    }
    return { key, g, alpha: 0, target: 1, showAt: 0, dying: false };
  }

  /**
   * Punch status pill: "● punching" → "⚡ direct" → a lone ⚡. A new label
   * enters on the back text element and crossfades with the front one while
   * the pill width eases to fit it; the dot / bolt and the fill cross over too.
   */
  private setPill(e: LinkE, key: string, instant = false) {
    const p = e.pill!;
    if (p.key === key) return;
    p.key = key;
    const back = (1 - p.front) as 0 | 1;
    p.texts[back].textContent = key ? this.opts.t('punch.' + key) : '';
    const w = key ? textWidth(p.texts[back], 6.5) : 0;
    p.tw = key ? Math.ceil(10 + 4 + w + 14) : 18;
    p.front = back;
    if (instant || !p.w) { p.w = p.tw; p.op = back ? [0, 1] : [1, 0]; p.textMix = key ? 1 : 0; }
  }

  // ---------------------------------------------------------------- frame
  step(dt: number) {
    this.ts += dt;
    const ts = this.ts, rm = this.rm, k = rm ? 5 : 8;
    const fade = (e: Fading, kk: number) => { if (ts >= e.showAt) e.alpha = ease(e.alpha, e.target, dt, kk); };

    for (const ev of this.events) {
      if (ev.done || ts < ev.at) continue;
      ev.done = true;
      if (ev.link) for (const e of this.links.values()) if (!e.dying && e.id === ev.link) {
        this.setLinkState(e, ev.state as LinkState);
        if (ev.state === 'offline') this.packets.forEach((p) => { if (p.links.includes(e)) p.dead = true; });
      }
      if (ev.node) for (const e of this.nodes.values()) if (!e.dying && e.id === ev.node) this.setNodeState(e, ev.state);
    }

    for (const [key, e] of this.nodes) {
      fade(e, e.dying ? 10 : k);
      if (e.dying && e.alpha < 0.01) { e.g.remove(); this.nodes.delete(key); continue; }
      const m = rm ? 1 : eio(clamp01(ts / MOVE));
      e.x = e.fx + (e.tx - e.fx) * m; e.y = e.fy + (e.ty - e.fy) * m;
      const s = rm ? 1 : 0.55 + 0.45 * e.alpha, dim = e.spec.state === 'dim' ? 0.4 : 1;
      e.g.setAttribute('transform', `translate(${e.x.toFixed(2)} ${e.y.toFixed(2)}) scale(${s.toFixed(3)})`);
      e.g.setAttribute('opacity', (e.alpha * dim).toFixed(3));
    }

    for (const [key, e] of this.links) {
      fade(e, e.dying ? 12 : k);
      if (e.dying && e.alpha < 0.01) { this.dropLink(e); this.links.delete(key); continue; }
      if (!e.dying && ts >= e.drawAt) e.p = Math.min(1, e.p + dt / 0.5);
      const C = this.ctrl(e), ra = e.A.r + 3, rb = e.B.r + 3;
      let dx = C[0] - e.A.x, dy = C[1] - e.A.y, dl = Math.hypot(dx, dy) || 1;
      const P0: Pt = [e.A.x + (dx / dl) * ra, e.A.y + (dy / dl) * ra];
      dx = C[0] - e.B.x; dy = C[1] - e.B.y; dl = Math.hypot(dx, dy) || 1;
      const P2: Pt = [e.B.x + (dx / dl) * rb, e.B.y + (dy / dl) * rb];
      const p = e.p, Q1: Pt = [P0[0] + (C[0] - P0[0]) * p, P0[1] + (C[1] - P0[1]) * p], Q2 = quad(P0, C, P2, p);
      e.path.setAttribute('d', p <= 0 ? '' : `M${f1(P0[0])} ${f1(P0[1])}Q${f1(Q1[0])} ${f1(Q1[1])} ${f1(Q2[0])} ${f1(Q2[1])}`);
      const dimL = e.state === 'dim' ? 0.75 : 1;
      e.g.setAttribute('opacity', (e.alpha * dimL).toFixed(3));
      if (!e.chipsSized && e.chips.length && e.alpha > 0) { e.chips.forEach((c) => this.chipSize(c)); e.chipsSized = true; }
      for (const c of e.chips) {
        const q = quad(P0, C, P2, c.t);
        c.g.setAttribute('transform', `translate(${f1(q[0])} ${f1(q[1] + c.dy)})`);
        c.g.setAttribute('opacity', (e.alpha * (p >= c.t ? 1 : 0) * dimL).toFixed(3));
      }
      if (e.pill) this.stepPunch(e, dt, P0, C, P2);
    }

    for (const m of [this.zones, this.notes]) for (const [key, e] of m) {
      fade(e, e.dying ? 10 : k);
      if (e.dying && e.alpha < 0.01) { e.g.remove(); m.delete(key); continue; }
      e.g.setAttribute('opacity', e.alpha.toFixed(3));
    }

    if (rm) return;   // no packets under reduced motion
    for (const f of this.flows) {
      if (ts < f.next || ts >= (f.until ?? 1e9)) continue;
      f.next = ts + (f.every || 0.42) * (0.75 + Math.random() * 0.5);
      this.spawn(f);
    }
    for (const p of this.packets) {
      if (p.dead) {
        p.fade -= dt * 6;
        if (p.fade <= 0) { p.g.remove(); p.done = true; continue; }
        p.g.setAttribute('opacity', (p.fade * p.op).toFixed(3));
        continue;
      }
      let si = 0;
      while (si < p.segs.length - 1 && p.s >= p.segs[si].s1) si++;
      p.s += (p.segs[si].L.state === 'degraded' ? 42 : 120) * dt;
      if (p.s >= p.len) { p.g.remove(); p.done = true; continue; }
      let lo = 0, hi = p.cum.length - 1;
      while (lo < hi - 1) { const md = (lo + hi) >> 1; if (p.cum[md] <= p.s) lo = md; else hi = md; }
      const sg = p.cum[hi] - p.cum[lo] || 1, t = (p.s - p.cum[lo]) / sg;
      const x = p.pts[lo][0] + (p.pts[hi][0] - p.pts[lo][0]) * t, y = p.pts[lo][1] + (p.pts[hi][1] - p.pts[lo][1]) * t;
      p.op = Math.min(1, p.s / 16, (p.len - p.s) / 16);
      p.g.setAttribute('transform', `translate(${f1(x)} ${f1(y)})`);
      p.g.setAttribute('opacity', p.op.toFixed(3));
    }
    this.packets = this.packets.filter((p) => !p.done);
  }

  private stepPunch(e: LinkE, dt: number, P0: Pt, C: Pt, P2: Pt) {
    const pl = e.pill!, rm = this.rm;
    const m = quad(P0, C, P2, 0.5), up = e.state === 'up', since = up ? Math.max(0, this.ts - (e.upAt ?? -9)) : 0;
    const vis = e.p >= 1 ? e.alpha : 0;
    this.setPill(e, up ? (since < 2.4 ? 'punched' : '') : 'punching', rm);

    // morph: width, text crossfade, dot → bolt, fill (CSS transition on .up)
    const kk = rm ? 30 : 9;
    pl.w = ease(pl.w, pl.tw, dt, kk);
    pl.op[pl.front] = ease(pl.op[pl.front], pl.key ? 1 : 0, dt, rm ? 30 : 7);
    pl.op[1 - pl.front] = ease(pl.op[1 - pl.front], 0, dt, rm ? 30 : 14);
    pl.textMix = ease(pl.textMix, pl.key ? 1 : 0, dt, kk);
    pl.upMix = ease(pl.upMix, up ? 1 : 0, dt, rm ? 30 : 8);
    pl.g.classList.toggle('up', up);
    const x0 = -pl.w / 2 + 4 + 3 * pl.textMix;
    pl.rect.setAttribute('x', f1(-pl.w / 2)); pl.rect.setAttribute('width', f1(pl.w));
    pl.dot.setAttribute('cx', f1(x0 + 5)); pl.dot.setAttribute('opacity', (1 - pl.upMix).toFixed(3));
    pl.zap.setAttribute('x', f1(x0)); pl.zap.setAttribute('opacity', pl.upMix.toFixed(3));
    pl.texts.forEach((t, i) => { t.setAttribute('x', f1(x0 + 14)); t.setAttribute('opacity', pl.op[i].toFixed(3)); });
    pl.g.setAttribute('transform', `translate(${f1(m[0])} ${f1(m[1] - 13)})`);
    pl.g.setAttribute('opacity', vis.toFixed(3));

    // both ends probe towards each other until the hole opens …
    const ph = (this.ts * 0.85) % 1, probing = !up && !rm && e.p >= 1;
    [quad(P0, C, P2, ph * 0.48), quad(P0, C, P2, 1 - ph * 0.48)].forEach((q, j) => {
      const c = e.probes![j];
      c.setAttribute('cx', f1(q[0])); c.setAttribute('cy', f1(q[1]));
      c.setAttribute('opacity', probing ? (Math.sin(ph * Math.PI) * vis).toFixed(3) : '0');
    });
    // … then meet: a ripple from the middle
    const k = since / 0.9, burst = up && !rm && k >= 0 && k < 1;
    e.burst!.setAttribute('cx', f1(m[0])); e.burst!.setAttribute('cy', f1(m[1]));
    e.burst!.setAttribute('r', burst ? f1(5 + k * 26) : '0');
    e.burst!.setAttribute('opacity', burst ? ((1 - k) * 0.85 * vis).toFixed(3) : '0');
  }

  private ctrl(e: LinkE): Pt {
    const ax = e.A.x, ay = e.A.y, bx = e.B.x, by = e.B.y, dx = bx - ax, dy = by - ay, len = Math.hypot(dx, dy) || 1, k = e.curve * len;
    return [(ax + bx) / 2 - (dy / len) * k, (ay + by) / 2 + (dx / len) * k];
  }

  private spawn(f: FlowE) {
    const path = f.paths[f.rr++ % f.paths.length];
    const down = f.dir === 'down' || (f.dir === 'both' && f.dc++ % 2 === 1);
    const seq = down ? path.slice().reverse() : path;
    const pts: Pt[] = [], segs: Packet['segs'] = [], ls: LinkE[] = [];
    for (let i = 0; i < seq.length - 1; i++) {
      const a = seq[i], b = seq[i + 1];
      let cands = [...this.links.values()].filter((e) => !e.dying && e.alpha > 0.6 && e.p >= 1 && (e.state === 'up' || e.state === 'degraded')
        && ((e.A.id === a && e.B.id === b) || (e.A.id === b && e.B.id === a)));
      if (f.only) { const o = cands.filter((e) => f.only!.includes(e.id)); if (o.length) cands = o; }
      if (!cands.length) return;
      const L = cands[f.lc++ % cands.length], fwd = L.A.id === a, C = this.ctrl(L);
      const P0: Pt = [L.A.x, L.A.y], P2: Pt = [L.B.x, L.B.y];
      for (let j = i ? 1 : 0; j <= 18; j++) { const t = j / 18; pts.push(quad(P0, C, P2, fwd ? t : 1 - t)); }
      segs.push({ L, i1: pts.length - 1, s1: 0 });
      ls.push(L);
    }
    const cum = [0];
    for (let i = 1; i < pts.length; i++) cum.push(cum[i - 1] + Math.hypot(pts[i][0] - pts[i - 1][0], pts[i][1] - pts[i - 1][1]));
    segs.forEach((s) => { s.s1 = cum[s.i1]; });
    const g = se('g', { opacity: 0 }, this.ly.p), cls = down ? 'down' : 'up';
    se('circle', { class: cls + ' halo', r: 6.5 }, g);
    se('circle', { class: cls, r: 2.9 }, g);
    this.packets.push({ g, pts, cum, len: cum[cum.length - 1], segs, links: ls, s: 0, op: 0, fade: 1 });
  }
}

function textWidth(t: SVGTextElement, perChar: number): number {
  let w = 0;
  try { w = t.getComputedTextLength(); } catch { /* not rendered */ }
  return w || (t.textContent || '').length * perChar;
}
