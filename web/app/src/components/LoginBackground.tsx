import { useEffect, useRef } from 'react';

// Logo colors
const CR = 77, CG = 110, CB = 163;       // outer circle: #4D6EA3 — regular peers
const HR = 16, HG = 37, HB = 69;         // hub inner:    #102545 — concentric for transit nodes
// Mirror of the topology graph's special-state palette (see
// web/ui-framework/src/styles/components.css `.hy-topo-dot.{native,offline}`).
// Keeping the login background's vocabulary in sync with the actual app
// graph so first-time viewers learn "yellow = native upstream, red = offline"
// before they even sign in.
const YR = 234, YG = 179, YB = 8;        // native outer: #eab308
const FR = 252, FG = 165, FB = 165;      // offline outer: #fca5a5
const FIR = 220, FIG = 38, FIB = 38;     // offline edge stroke: #dc2626 (dashed-line colour only — offline dots themselves render plain)

const GRID = 36;
const RADIUS = 11;
const HUB_INNER = 6.5;
// Native upstream caps: in the real graph a native peer can only be reached
// outbound by other peers (no nesting), so it never sits in the middle of a
// chain — visually that maps to "few connecting lines." 2 keeps the dot
// recognisable but distinctly leaf-like.
const NATIVE_MAX_LINKS = 2;

const BASE_ALPHA = 0.022;
const MAX_ALPHA = 0.36;

const AUTO_INTERVAL = 2200;
const MIN_FORCE = 0.12;
const MAX_FORCE = 1.0;
const COMBO_WINDOW = 600;

// Force → parameters
function rippleMaxRadius(force: number) { return 40 + force * 360; }
const HALO_RATIO = 0.5;
function rippleTotalRadius(force: number) { return rippleMaxRadius(force) * (1 + HALO_RATIO); }
function rippleExpandTime(force: number) { return 0.8 + force * 2.0; }
function rippleCorePhase(force: number) { return rippleMaxRadius(force) / rippleTotalRadius(force); }
function easeOutCubic(t: number) { return 1 - Math.pow(1 - t, 3); }

// Glow decay: per-frame multiplier — controls how fast circles fade after being lit
// ~0.992 at 60fps → halves in ~1.4s; ~0.996 → halves in ~2.9s
const GLOW_DECAY = 0.994;
// How fast glow rises when a ripple paints it (lerp toward target per frame)
const GLOW_RISE = 0.25;

function hash(x: number, y: number, seed: number) {
  let h = (x * 374761393 + y * 668265263 + seed * 1274126177) | 0;
  h = ((h ^ (h >> 13)) * 1103515245 + 12345) | 0;
  return ((h & 0x7fffffff) / 0x7fffffff);
}

// Three semantic kinds. The "hub-like" inner circle isn't its own kind —
// it's a render-time decision based on the connection count of a normal
// node (≥3 lines = transit-shaped → draw concentric inner). Native and
// offline are leaf states by their own rules and never get an inner
// circle no matter how many lines they end up with.
type CircleKind = 'normal' | 'native' | 'offline';
interface Link { a: number; b: number; width: number; offline: boolean; }
interface Circle { x: number; y: number; idx: number; kind: CircleKind; glow: number; linkCount: number; }
interface Ripple { x: number; y: number; time: number; force: number; }
// A normal node draws a concentric inner ring only when it has at least
// this many connecting lines — matches the user's "transit needs ≥ 3
// edges" rule. Anything below renders as a plain outer-only dot.
const CONCENTRIC_MIN_LINKS = 3;

export default function LoginBackground() {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const raf = useRef(0);
  const ripples = useRef<Ripple[]>([]);
  const clickTimes = useRef<number[]>([]);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext('2d')!;

    let circles: Circle[] = [];
    let links: Link[] = [];
    let linkGlows: Float32Array = new Float32Array(0);

    function buildGrid() {
      circles = [];
      links = [];
      const w = window.innerWidth;
      const h = window.innerHeight;
      const cols = Math.ceil(w / GRID) + 3;
      const rows = Math.ceil(h / GRID) + 3;
      let idx = 0;
      for (let r = -1; r < rows; r++) {
        const ox = r % 2 === 0 ? 0 : GRID / 2;
        for (let c = -1; c < cols; c++) {
          // Initial kind (may flip to 'offline' below if the link pass
          // leaves us with zero connections). Hash partitions:
          //   [0.00, 0.04) → native  (~4%, sparse — leaf-only upstream)
          //   [0.04, 0.07) → offline (~3%, intentional pre-marked)
          //   else         → normal  (link count decides hub-shape later)
          // Hash uses (c, r, 7) so the layout is stable across resizes.
          const k = hash(c, r, 7);
          let kind: CircleKind;
          if (k < 0.04) kind = 'native';
          else if (k < 0.07) kind = 'offline';
          else kind = 'normal';
          circles.push({
            x: c * GRID + ox,
            y: r * GRID,
            idx: idx++,
            kind,
            glow: 0,
            linkCount: 0,
          });
        }
      }
      for (let i = 0; i < circles.length; i++) {
        const ci = circles[i];
        for (let j = i + 1; j < circles.length; j++) {
          const cj = circles[j];
          const dx = ci.x - cj.x;
          const dy = ci.y - cj.y;
          const d = Math.sqrt(dx * dx + dy * dy);
          if (d > GRID * 1.8) continue;
          const r = hash(i, j, 42);
          const eitherNative = ci.kind === 'native' || cj.kind === 'native';
          // Without a separate "hub" kind, link probability is driven by
          // how many edges the endpoints already accumulated — a more
          // organic "rich get richer" pattern that naturally produces a
          // few high-degree (≥3) nodes that will render as concentric
          // hubs at draw time, while most stay sparse. Native is capped
          // separately by NATIVE_MAX_LINKS, regardless of probability.
          const dense = ci.linkCount + cj.linkCount;
          let prob: number;
          if (eitherNative) prob = 0.30;
          else if (dense >= 4) prob = 0.55;
          else if (dense >= 2) prob = 0.40;
          else prob = 0.22;
          if (r >= prob) continue;
          if (ci.kind === 'native' && ci.linkCount >= NATIVE_MAX_LINKS) continue;
          if (cj.kind === 'native' && cj.linkCount >= NATIVE_MAX_LINKS) continue;
          let lw: number;
          if (dense >= 4) lw = 1.0 + hash(i, j, 99) * 2.4;
          else if (dense >= 2) lw = 0.8 + hash(i, j, 99) * 1.6;
          else if (eitherNative) lw = 0.7 + hash(i, j, 99) * 1.0;
          else lw = 0.5 + hash(i, j, 99) * 1.3;
          // Edge offline iff either endpoint is (or will become) offline.
          // Pre-marked offlines are caught here; orphan-promoted offlines
          // are handled in the post-pass below.
          const off = ci.kind === 'offline' || cj.kind === 'offline';
          links.push({ a: i, b: j, width: lw, offline: off });
          ci.linkCount++;
          cj.linkCount++;
        }
      }
      // Promote orphans: a normal circle that ended up with zero
      // connections is, by the user's rule, offline. Native circles that
      // ended up isolated keep their kind — a native upstream with no
      // line is rare but it's still semantically a native (just nothing
      // dialled it this layout). They render as plain yellow.
      for (const c of circles) {
        if (c.kind === 'normal' && c.linkCount === 0) {
          c.kind = 'offline';
        }
      }
      linkGlows = new Float32Array(links.length);
    }

    function resize() {
      const dpr = window.devicePixelRatio || 1;
      canvas!.width = window.innerWidth * dpr;
      canvas!.height = window.innerHeight * dpr;
      canvas!.style.width = window.innerWidth + 'px';
      canvas!.style.height = window.innerHeight + 'px';
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      buildGrid();
    }

    function clipToCircle(
      fromX: number, fromY: number, toX: number, toY: number, r: number
    ): [number, number] {
      const dx = toX - fromX;
      const dy = toY - fromY;
      const len = Math.sqrt(dx * dx + dy * dy);
      if (len < r) return [fromX, fromY];
      return [toX - (dx / len) * r, toY - (dy / len) * r];
    }

    // Calculate the "paint" intensity a ripple wants to apply at a given point RIGHT NOW
    // This is only used to determine what to paint — not the final displayed value
    function ripplePaint(rip: Ripple, px: number, py: number, now: number): number {
      const maxR = rippleMaxRadius(rip.force);
      const totalR = rippleTotalRadius(rip.force);
      const expandT = rippleExpandTime(rip.force);
      const age = (now - rip.time) / 1000;
      if (age > expandT) return 0; // only paint during expansion, decay handles the rest

      const dist = Math.sqrt((px - rip.x) ** 2 + (py - rip.y) ** 2);
      const currentR = easeOutCubic(Math.min(age / expandT, 1)) * totalR;
      if (dist > currentR) return 0;

      // Core vs halo intensity
      let spatial: number;
      if (dist <= Math.min(currentR, maxR)) {
        const frontier = Math.min(currentR, maxR);
        spatial = dist > frontier - 20 ? Math.max(0, (frontier - dist) / 20) : 1;
      } else {
        const haloPos = (dist - maxR) / (totalR - maxR);
        spatial = 0.55 * Math.max(0, 1 - haloPos * haloPos);
      }

      return spatial * Math.min(1, rip.force + 0.3);
    }

    function draw() {
      const w = window.innerWidth;
      const h = window.innerHeight;
      ctx.clearRect(0, 0, w, h);

      const now = performance.now();

      // Prune ripples that have finished expanding (decay handles the rest)
      ripples.current = ripples.current.filter(r =>
        (now - r.time) / 1000 < rippleExpandTime(r.force) + 0.1
      );

      // --- Update glow per circle: decay + paint from active ripples ---
      for (const c of circles) {
        // Natural decay
        c.glow *= GLOW_DECAY;

        // Paint from expanding ripples
        let paint = 0;
        for (const rip of ripples.current) {
          paint = Math.max(paint, ripplePaint(rip, c.x, c.y, now));
        }

        // Smoothly rise toward paint target (only if paint > current glow)
        if (paint > c.glow) {
          c.glow += (paint - c.glow) * GLOW_RISE;
        }
      }

      // --- Update glow per link ---
      for (let li = 0; li < links.length; li++) {
        const link = links[li];
        const ci = circles[link.a];
        const cj = circles[link.b];

        // Decay
        linkGlows[li] *= GLOW_DECAY;

        // Paint from ripples (use midpoint)
        const midX = (ci.x + cj.x) / 2;
        const midY = (ci.y + cj.y) / 2;
        let paint = 0;
        for (const rip of ripples.current) {
          paint = Math.max(paint, ripplePaint(rip, midX, midY, now));
        }
        if (paint > linkGlows[li]) {
          linkGlows[li] += (paint - linkGlows[li]) * GLOW_RISE;
        }
      }

      // --- Draw pass 1: lines ---
      ctx.lineCap = 'round';
      for (let li = 0; li < links.length; li++) {
        const g = linkGlows[li];
        if (g < 0.005) continue;
        const link = links[li];
        const ci = circles[link.a];
        const cj = circles[link.b];

        const [x1, y1] = clipToCircle(cj.x, cj.y, ci.x, ci.y, RADIUS);
        const [x2, y2] = clipToCircle(ci.x, ci.y, cj.x, cj.y, RADIUS);

        ctx.beginPath();
        ctx.moveTo(x1, y1);
        ctx.lineTo(x2, y2);
        ctx.lineWidth = link.width;
        if (link.offline) {
          // Dashed red — same vocabulary as the graph's offline edge.
          ctx.setLineDash([6, 4]);
          ctx.strokeStyle = `rgba(${FIR},${FIG},${FIB},${g * 0.32})`;
        } else {
          ctx.setLineDash([]);
          ctx.strokeStyle = `rgba(${CR},${CG},${CB},${g * 0.22})`;
        }
        ctx.stroke();
      }
      ctx.setLineDash([]);

      // --- Draw pass 2: circles ---
      for (const c of circles) {
        const g = c.glow;
        const alpha = BASE_ALPHA + (MAX_ALPHA - BASE_ALPHA) * g;

        // Per-kind outer-circle colour. Native = light yellow, offline =
        // light red, everyone else (hub OR normal) = the regular blue.
        // Inner circle (concentric) only for HUB — hubs are transit-y in
        // the real graph, native peers are leaf-only by definition, and
        // offline peers don't get a "transit" hint either.
        let or = CR, og = CG, ob = CB;
        if (c.kind === 'native')  { or = YR; og = YG; ob = YB; }
        else if (c.kind === 'offline') { or = FR; og = FG; ob = FB; }

        ctx.beginPath();
        ctx.arc(c.x, c.y, RADIUS, 0, Math.PI * 2);
        ctx.fillStyle = `rgba(${or},${og},${ob},${alpha})`;
        ctx.fill();

        // Concentric inner ring is RESERVED for normal circles with at
        // least CONCENTRIC_MIN_LINKS connections — those are the
        // "transit-shaped" nodes in the graph vocabulary. Native and
        // offline are leaf-only by their respective rules and never
        // render an inner ring no matter how many lines touch them.
        // A normal circle with <3 connections also stays plain.
        if (c.kind === 'normal' && c.linkCount >= CONCENTRIC_MIN_LINKS && g > 0.12) {
          const hubAlpha = (g - 0.12) / 0.88 * 0.55;
          ctx.beginPath();
          ctx.arc(c.x, c.y, HUB_INNER, 0, Math.PI * 2);
          ctx.fillStyle = `rgba(${HR},${HG},${HB},${hubAlpha})`;
          ctx.fill();
        }
      }

      raf.current = requestAnimationFrame(draw);
    }

    function onClick(e: MouseEvent) {
      const tag = (e.target as HTMLElement).tagName;
      if (tag === 'INPUT' || tag === 'BUTTON' || tag === 'LABEL' || tag === 'SELECT' || tag === 'TEXTAREA') return;

      const now = performance.now();
      clickTimes.current.push(now);
      clickTimes.current = clickTimes.current.filter(t => now - t < COMBO_WINDOW);

      const combo = clickTimes.current.length;
      const force = Math.min(MAX_FORCE, 0.2 + combo * 0.2);

      ripples.current.push({ x: e.clientX, y: e.clientY, time: now, force });
    }

    const autoTimer = setInterval(() => {
      if (document.hidden) return;
      const x = Math.random() * window.innerWidth;
      const y = Math.random() * window.innerHeight;
      const force = MIN_FORCE + Math.random() * 0.45;
      ripples.current.push({ x, y, time: performance.now(), force });
    }, AUTO_INTERVAL);

    setTimeout(() => {
      ripples.current.push({
        x: window.innerWidth * (0.3 + Math.random() * 0.4),
        y: window.innerHeight * (0.3 + Math.random() * 0.4),
        time: performance.now(),
        force: 0.5,
      });
    }, 500);

    window.addEventListener('resize', resize);
    window.addEventListener('click', onClick);
    resize();
    raf.current = requestAnimationFrame(draw);

    return () => {
      cancelAnimationFrame(raf.current);
      clearInterval(autoTimer);
      window.removeEventListener('resize', resize);
      window.removeEventListener('click', onClick);
    };
  }, []);

  return (
    <canvas
      ref={canvasRef}
      style={{
        position: 'fixed',
        top: 0,
        left: 0,
        width: '100%',
        height: '100%',
        pointerEvents: 'none',
        zIndex: 0,
      }}
    />
  );
}
