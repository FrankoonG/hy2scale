import { useEffect, useRef } from 'react';

// Logo colors
const CR = 77, CG = 110, CB = 163;       // outer circle: #4D6EA3
const HR = 16, HG = 37, HB = 69;         // hub inner:    #102545

const GRID = 36;
const RADIUS = 11;
const HUB_INNER = 6.5;

const BASE_ALPHA = 0.022;
const MAX_ALPHA = 0.36;

// Ripple — force controls range and duration
const RIPPLE_WIDTH = 100;         // ring thickness (px) at force=1
const RIPPLE_FADE_IN = 0.25;      // leading edge fade-in (s)
const AUTO_INTERVAL = 2200;       // ms between auto-ripples

// Force ranges
const MIN_FORCE = 0.12;           // auto-ripple minimum
const MAX_FORCE = 1.0;            // max from rapid clicks

// Force → parameters
function rippleMaxRadius(force: number) { return 40 + force * 360; }   // ~40px min – 400px max
const HALO_RATIO = 0.5;            // halo extends 50% beyond maxR
function rippleTotalRadius(force: number) { return rippleMaxRadius(force) * (1 + HALO_RATIO); }
// Single continuous expansion time covering core + halo
function rippleExpandTime(force: number) { return 0.8 + force * 2.0; } // 0.8s – 2.8s
// Fade starts when core reaches maxR (which is partway through the easing curve)
function rippleFadeTime(force: number) { return 1.5 + force * 2.5; }
// Core reaches maxR at this fraction of the total easing curve
function rippleCorePhase(force: number) { return rippleMaxRadius(force) / rippleTotalRadius(force); }
function rippleLife(force: number) { return rippleExpandTime(force) + rippleFadeTime(force); }
// Ease-out cubic: fast start, gentle deceleration
function easeOutCubic(t: number) { return 1 - Math.pow(1 - t, 3); }

// Click combo tracking
const COMBO_WINDOW = 600;         // ms — clicks within this window stack force

function hash(x: number, y: number, seed: number) {
  let h = (x * 374761393 + y * 668265263 + seed * 1274126177) | 0;
  h = ((h ^ (h >> 13)) * 1103515245 + 12345) | 0;
  return ((h & 0x7fffffff) / 0x7fffffff);
}

interface Link { a: number; b: number; width: number; }
interface Circle { x: number; y: number; idx: number; isHub: boolean; }
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
          circles.push({
            x: c * GRID + ox,
            y: r * GRID,
            idx: idx++,
            isHub: hash(c, r, 7) < 0.12,
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
          const eitherHub = ci.isHub || cj.isHub;
          const bothHub = ci.isHub && cj.isHub;
          const prob = bothHub ? 0.75 : eitherHub ? 0.50 : 0.22;
          if (r >= prob) continue;
          let w: number;
          if (bothHub) w = 1.0 + hash(i, j, 99) * 2.8;
          else if (eitherHub) w = 0.8 + hash(i, j, 99) * 2.0;
          else w = 0.5 + hash(i, j, 99) * 1.3;
          links.push({ a: i, b: j, width: w });
        }
      }
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

    function smoothstep(t: number) {
      return t * t * (3 - 2 * t);
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

    function getIntensity(px: number, py: number, now: number): number {
      let maxI = 0;
      for (const rip of ripples.current) {
        const life = rippleLife(rip.force);
        const maxR = rippleMaxRadius(rip.force);
        const totalR = rippleTotalRadius(rip.force);
        const expandT = rippleExpandTime(rip.force);
        const fadeT = rippleFadeTime(rip.force);
        const coreFrac = rippleCorePhase(rip.force); // ~0.667
        const age = (now - rip.time) / 1000;
        if (age > life) continue;

        const dist = Math.sqrt((px - rip.x) ** 2 + (py - rip.y) ** 2);

        // Single continuous ease-out expansion: 0 → totalR over expandT
        // Core (maxR) is reached at coreFrac of the curve, halo continues beyond
        const expandProgress = Math.min(age / expandT, 1);
        const currentR = easeOutCubic(expandProgress) * totalR;

        if (dist > currentR) continue;

        // Determine if this point is in core zone or halo zone
        let spatialFade: number;
        if (dist <= maxR) {
          // Core zone: full intensity with soft edge at frontier
          const frontier = Math.min(currentR, maxR);
          const edgeSoft = dist > frontier - 20 ? Math.max(0, (frontier - dist) / 20) : 1;
          spatialFade = edgeSoft;
        } else {
          // Halo zone: beyond maxR, gradient from ~0.55 → 0
          const haloPos = (dist - maxR) / (totalR - maxR);
          spatialFade = 0.55 * (1 - smoothstep(haloPos));
        }

        // Fade starts when core reaches maxR (coreFrac of easing = when easedProgress passes coreFrac)
        // Find the time when easeOutCubic(t/expandT) * totalR = maxR
        // i.e. easeOutCubic(t/expandT) = coreFrac → solve for coreTime
        const coreReachTime = expandT * (1 - Math.pow(1 - coreFrac, 1/3));
        let lifeFade = 1;
        if (age > coreReachTime) {
          lifeFade = 1 - smoothstep((age - coreReachTime) / fadeT);
        }

        const edgeFade = Math.min(1, age / RIPPLE_FADE_IN);

        const intensity = spatialFade * lifeFade * edgeFade * Math.min(1, rip.force + 0.3);
        maxI += intensity;
      }
      return Math.min(maxI, 1);
    }

    function draw() {
      const w = window.innerWidth;
      const h = window.innerHeight;
      ctx.clearRect(0, 0, w, h);

      const now = performance.now();

      // Prune dead ripples
      ripples.current = ripples.current.filter(r =>
        (now - r.time) / 1000 < rippleLife(r.force)
      );

      // --- Pass 1: connection lines ---
      ctx.lineCap = 'round';
      for (const link of links) {
        const ci = circles[link.a];
        const cj = circles[link.b];
        const midX = (ci.x + cj.x) / 2;
        const midY = (ci.y + cj.y) / 2;
        const t = getIntensity(midX, midY, now);
        if (t < 0.01) continue;

        const alpha = t * 0.22;
        const [x1, y1] = clipToCircle(cj.x, cj.y, ci.x, ci.y, RADIUS);
        const [x2, y2] = clipToCircle(ci.x, ci.y, cj.x, cj.y, RADIUS);

        ctx.beginPath();
        ctx.moveTo(x1, y1);
        ctx.lineTo(x2, y2);
        ctx.lineWidth = link.width;
        ctx.strokeStyle = `rgba(${CR},${CG},${CB},${alpha})`;
        ctx.stroke();
      }

      // --- Pass 2: circles ---
      for (const c of circles) {
        const t = getIntensity(c.x, c.y, now);
        const alpha = BASE_ALPHA + (MAX_ALPHA - BASE_ALPHA) * t;

        ctx.beginPath();
        ctx.arc(c.x, c.y, RADIUS, 0, Math.PI * 2);
        ctx.fillStyle = `rgba(${CR},${CG},${CB},${alpha})`;
        ctx.fill();

        if (c.isHub && t > 0.12) {
          const hubAlpha = (t - 0.12) / 0.88 * 0.55;
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
      // Track click times for combo
      clickTimes.current.push(now);
      clickTimes.current = clickTimes.current.filter(t => now - t < COMBO_WINDOW);

      // Force scales with combo count: 1 click = 0.35, 2 = 0.55, 3 = 0.75, 4+ = 1.0
      const combo = clickTimes.current.length;
      const force = Math.min(MAX_FORCE, 0.2 + combo * 0.2);

      ripples.current.push({ x: e.clientX, y: e.clientY, time: now, force });
    }

    // Auto-ripples with random force
    const autoTimer = setInterval(() => {
      const x = Math.random() * window.innerWidth;
      const y = Math.random() * window.innerHeight;
      const force = MIN_FORCE + Math.random() * 0.45; // 0.12 – 0.57
      ripples.current.push({ x, y, time: performance.now(), force });
    }, AUTO_INTERVAL);

    // Initial ripple
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
