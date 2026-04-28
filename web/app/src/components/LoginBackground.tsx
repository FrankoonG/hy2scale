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
const YIR = 146, YIG = 64, YIB = 14;     // native inner: #92400e
const FR = 252, FG = 165, FB = 165;      // offline outer: #fca5a5
const FIR = 220, FIG = 38, FIB = 38;     // offline inner: #dc2626

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

type CircleKind = 'normal' | 'hub' | 'native' | 'offline';
interface Link { a: number; b: number; width: number; offline: boolean; }
interface Circle { x: number; y: number; idx: number; kind: CircleKind; glow: number; }
interface Ripple { x: number; y: number; time: number; force: number; }

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
          // Sample once and partition the [0,1) range deterministically:
          //   [0.00, 0.04) → native  (~4%, sparse — matches "rare upstream")
          //   [0.04, 0.07) → offline (~3%, rarer still — visual outlier)
          //   [0.07, 0.19) → hub     (~12%, same as before)
          //   else         → normal
          // Hash uses (c, r, 7) so the layout is stable across resizes.
          const k = hash(c, r, 7);
          let kind: CircleKind;
          if (k < 0.04) kind = 'native';
          else if (k < 0.07) kind = 'offline';
          else if (k < 0.19) kind = 'hub';
          else kind = 'normal';
          circles.push({
            x: c * GRID + ox,
            y: r * GRID,
            idx: idx++,
            kind,
            glow: 0,
          });
        }
      }
      // Per-circle native link count, used to enforce NATIVE_MAX_LINKS while
      // building. Tracked here (not on Circle) since it's a build-time
      // constraint, not a runtime state.
      const nativeLinkCount = new Int32Array(circles.length);
      for (let i = 0; i < circles.length; i++) {
        const ci = circles[i];
        for (let j = i + 1; j < circles.length; j++) {
          const cj = circles[j];
          const dx = ci.x - cj.x;
          const dy = ci.y - cj.y;
          const d = Math.sqrt(dx * dx + dy * dy);
          if (d > GRID * 1.8) continue;
          const r = hash(i, j, 42);
          const eitherHub = ci.kind === 'hub' || cj.kind === 'hub';
          const bothHub = ci.kind === 'hub' && cj.kind === 'hub';
          const eitherNative = ci.kind === 'native' || cj.kind === 'native';
          // Native upstreams sit at the leaf of the topology — the real
          // graph never has another native peer next to one (they're all
          // independent endpoints), and they should advertise "few
          // connections" visually. Cap probability and per-node fan-out.
          let prob: number;
          if (eitherNative) prob = 0.35;
          else if (bothHub) prob = 0.75;
          else if (eitherHub) prob = 0.50;
          else prob = 0.22;
          if (r >= prob) continue;
          // Hard cap for native: skip if either native endpoint already has
          // NATIVE_MAX_LINKS edges. Mirrors the "no nesting" rule.
          if (ci.kind === 'native' && nativeLinkCount[i] >= NATIVE_MAX_LINKS) continue;
          if (cj.kind === 'native' && nativeLinkCount[j] >= NATIVE_MAX_LINKS) continue;
          let lw: number;
          if (bothHub) lw = 1.0 + hash(i, j, 99) * 2.8;
          else if (eitherHub) lw = 0.8 + hash(i, j, 99) * 2.0;
          else if (eitherNative) lw = 0.7 + hash(i, j, 99) * 1.0;
          else lw = 0.5 + hash(i, j, 99) * 1.3;
          // Edge offline iff either endpoint is offline — matches the
          // graph's red-dashed-line rule for unreachable hops.
          const off = ci.kind === 'offline' || cj.kind === 'offline';
          links.push({ a: i, b: j, width: lw, offline: off });
          if (ci.kind === 'native') nativeLinkCount[i]++;
          if (cj.kind === 'native') nativeLinkCount[j]++;
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

        if (c.kind === 'hub' && g > 0.12) {
          const hubAlpha = (g - 0.12) / 0.88 * 0.55;
          ctx.beginPath();
          ctx.arc(c.x, c.y, HUB_INNER, 0, Math.PI * 2);
          ctx.fillStyle = `rgba(${HR},${HG},${HB},${hubAlpha})`;
          ctx.fill();
        } else if (c.kind === 'native' && g > 0.12) {
          // Native gets a small amber inner circle so its yellow dot
          // reads as "intentional special state," not "stray colour."
          // Smaller than HUB_INNER to keep it leaf-shaped.
          const niAlpha = (g - 0.12) / 0.88 * 0.6;
          ctx.beginPath();
          ctx.arc(c.x, c.y, HUB_INNER * 0.7, 0, Math.PI * 2);
          ctx.fillStyle = `rgba(${YIR},${YIG},${YIB},${niAlpha})`;
          ctx.fill();
        } else if (c.kind === 'offline' && g > 0.12) {
          const oiAlpha = (g - 0.12) / 0.88 * 0.5;
          ctx.beginPath();
          ctx.arc(c.x, c.y, HUB_INNER * 0.7, 0, Math.PI * 2);
          ctx.fillStyle = `rgba(${FIR},${FIG},${FIB},${oiAlpha})`;
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
