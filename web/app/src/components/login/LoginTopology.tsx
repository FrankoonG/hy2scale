import { useEffect, useRef, useState, type CSSProperties } from 'react';
import { useTranslation } from 'react-i18next';
import { TopoEngine } from './engine';
import { generate, newSeed } from './generate';
import { VBH, VBW } from './spec';
import BrandLogo from './BrandLogo';

const ROTATE_S = 8;
const TAG_ROW = 24;
const TAG_ROWS = 5;

const reducedMotion = () => window.matchMedia('(prefers-reduced-motion: reduce)').matches;

/** Login left panel: a random mesh topology every few seconds, its features listed below. */
export default function LoginTopology() {
  const { t, i18n } = useTranslation();
  const svgRef = useRef<SVGSVGElement>(null);
  const fillRef = useRef<HTMLSpanElement>(null);
  const engineRef = useRef<TopoEngine | null>(null);
  const hover = useRef(false);
  const tRef = useRef(t);
  tRef.current = t;
  const [tags, setTags] = useState<string[]>([]);
  const [swap, setSwap] = useState(false);
  const [paused, setPaused] = useState(false);

  useEffect(() => {
    const svg = svgRef.current!;
    const engine = new TopoEngine(svg, { t: (k) => tRef.current(`login.topo.${k}`), reducedMotion });
    engineRef.current = engine;
    let swapTimer = 0;
    const next = (first = false) => {
      const spec = generate(newSeed());
      engine.show(spec, first);
      if (first) { setTags(spec.tags); return; }
      // tags: fade the list out, swap, fade back in
      setSwap(true);
      window.clearTimeout(swapTimer);
      swapTimer = window.setTimeout(() => { setTags(spec.tags); setSwap(false); }, 300);
    };
    next(true);

    let raf = 0, last = performance.now(), rot = 0;
    const tick = (now: number) => {
      // animation steps are capped (a long frame must not jump), the rotation
      // clock is not: slow devices still rotate on time
      const real = Math.min(1, (now - last) / 1000);
      last = now;
      if (!document.hidden) {
        engine.step(Math.min(0.05, real));
        if (!hover.current) rot += real;
        if (fillRef.current) fillRef.current.style.width = `${Math.min(100, (rot / ROTATE_S) * 100).toFixed(2)}%`;
        if (rot >= ROTATE_S) { rot = 0; next(); }
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => { cancelAnimationFrame(raf); window.clearTimeout(swapTimer); engine.destroy(); engineRef.current = null; };
  }, []);

  useEffect(() => { engineRef.current?.relabel(); }, [i18n.language]);

  // the list repeats so the window is always full; the track moves down one copy per loop
  const copies = tags.length ? Math.ceil(TAG_ROWS / tags.length) + 1 : 0;
  const track = Array.from({ length: copies }, (_, c) => tags.map((k) => ({ k, c }))).flat();

  return (
    <div
      className="lg-left"
      onPointerEnter={() => { hover.current = true; setPaused(true); }}
      onPointerLeave={() => { hover.current = false; setPaused(false); }}
    >
      <div className="lg-brand"><BrandLogo /><span>HY2 SCALE</span></div>
      <div className="lg-topo">
        <svg ref={svgRef} className="lg-tsvg" viewBox={`0 0 ${VBW} ${VBH}`} aria-hidden="true" />
      </div>
      <div className="lg-foot">
        <div className={`lg-tags${swap ? ' swap' : ''}`} aria-live="polite">
          <ul className="lg-tag-track" key={tags.join('|')}
            style={{ '--from': `-${tags.length * TAG_ROW}px`, '--dur': `${tags.length * 2.6}s` } as CSSProperties}>
            {track.map(({ k, c }, i) => (
              <li key={i} className="lg-tag" aria-hidden={c ? true : undefined}>{t(`login.topo.tag.${k}`)}</li>
            ))}
          </ul>
        </div>
        <div className={`lg-progress${paused ? ' paused' : ''}`} aria-hidden="true"><span ref={fillRef} /></div>
      </div>
    </div>
  );
}
