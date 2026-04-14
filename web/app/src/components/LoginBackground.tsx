import { useEffect, useRef } from 'react';

// Logo colors
const CR = 77, CG = 110, CB = 163;       // outer circle: #4D6EA3
const HR = 16, HG = 37, HB = 69;         // hub inner:    #102545

const GRID = 36;            // tighter grid
const RADIUS = 11;          // outer circle radius
const HUB_INNER = 6.5;      // hub inner circle radius
const INFLUENCE = 280;
const LINE_INFLUENCE = 220;

const BASE_ALPHA = 0.022;
const MAX_ALPHA = 0.36;

function hash(x: number, y: number, seed: number) {
  let h = (x * 374761393 + y * 668265263 + seed * 1274126177) | 0;
  h = ((h ^ (h >> 13)) * 1103515245 + 12345) | 0;
  return ((h & 0x7fffffff) / 0x7fffffff);
}

interface Link {
  a: number;
  b: number;
  width: number;  // line thickness
}

interface Circle {
  x: number;
  y: number;
  idx: number;
  isHub: boolean;
}

export default function LoginBackground() {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const mouse = useRef({ x: -9999, y: -9999 });
  const raf = useRef(0);

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

      // Build links with hub-aware probability and thickness
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

          // Hub nodes produce more connections
          const prob = bothHub ? 0.75 : eitherHub ? 0.50 : 0.22;
          if (r >= prob) continue;

          // Line width: hubs get thicker lines (higher throughput)
          let w: number;
          if (bothHub) {
            w = 1.0 + hash(i, j, 99) * 2.8;   // 1.0 – 3.8
          } else if (eitherHub) {
            w = 0.8 + hash(i, j, 99) * 2.0;    // 0.8 – 2.8
          } else {
            w = 0.5 + hash(i, j, 99) * 1.3;    // 0.5 – 1.8
          }

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

    // Clip a line segment to exclude the interior of a circle.
    // Returns the point on the circle boundary (or the original point if outside).
    function clipToCircle(
      fromX: number, fromY: number, toX: number, toY: number, r: number
    ): [number, number] {
      const dx = toX - fromX;
      const dy = toY - fromY;
      const len = Math.sqrt(dx * dx + dy * dy);
      if (len < r) return [fromX, fromY]; // fully inside, degenerate
      return [toX - (dx / len) * r, toY - (dy / len) * r];
    }

    function draw() {
      const w = window.innerWidth;
      const h = window.innerHeight;
      ctx.clearRect(0, 0, w, h);

      const mx = mouse.current.x;
      const my = mouse.current.y;

      // --- Pass 1: connection lines (clipped to circle edges, drawn BEHIND) ---
      ctx.lineCap = 'round';
      for (const link of links) {
        const ci = circles[link.a];
        const cj = circles[link.b];
        const midX = (ci.x + cj.x) / 2;
        const midY = (ci.y + cj.y) / 2;
        const dist = Math.sqrt((midX - mx) ** 2 + (midY - my) ** 2);
        if (dist > LINE_INFLUENCE) continue;

        const t = smoothstep(Math.max(0, 1 - dist / LINE_INFLUENCE));
        const alpha = t * 0.22;

        // Clip line to stop at circle boundaries
        const [x1, y1] = clipToCircle(cj.x, cj.y, ci.x, ci.y, RADIUS);
        const [x2, y2] = clipToCircle(ci.x, ci.y, cj.x, cj.y, RADIUS);

        ctx.beginPath();
        ctx.moveTo(x1, y1);
        ctx.lineTo(x2, y2);
        ctx.lineWidth = link.width;
        ctx.strokeStyle = `rgba(${CR},${CG},${CB},${alpha})`;
        ctx.stroke();
      }

      // --- Pass 2: circles (on top of lines) ---
      for (const c of circles) {
        const dx = c.x - mx;
        const dy = c.y - my;
        const dist = Math.sqrt(dx * dx + dy * dy);
        const t = smoothstep(Math.max(0, 1 - dist / INFLUENCE));
        const alpha = BASE_ALPHA + (MAX_ALPHA - BASE_ALPHA) * t;

        // Outer circle
        ctx.beginPath();
        ctx.arc(c.x, c.y, RADIUS, 0, Math.PI * 2);
        ctx.fillStyle = `rgba(${CR},${CG},${CB},${alpha})`;
        ctx.fill();

        // Hub inner circle
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

    function onMove(e: MouseEvent) {
      mouse.current.x = e.clientX;
      mouse.current.y = e.clientY;
    }

    function onLeave() {
      mouse.current.x = -9999;
      mouse.current.y = -9999;
    }

    window.addEventListener('resize', resize);
    window.addEventListener('mousemove', onMove);
    document.addEventListener('mouseleave', onLeave);
    resize();
    raf.current = requestAnimationFrame(draw);

    return () => {
      cancelAnimationFrame(raf.current);
      window.removeEventListener('resize', resize);
      window.removeEventListener('mousemove', onMove);
      document.removeEventListener('mouseleave', onLeave);
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
