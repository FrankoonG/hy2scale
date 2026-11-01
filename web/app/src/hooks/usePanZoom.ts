import { useCallback, useEffect, useRef, useState, type RefObject } from 'react';

/** Canvas transform: screen = world × k + (x, y). */
export interface View { x: number; y: number; k: number }

export interface PanZoomOptions {
  ref: RefObject<HTMLElement | SVGSVGElement | null>;
  minK?: number;
  maxK?: number;
  initial?: View;
  /** Targets the canvas gestures ignore (toolbars, panels, menus). */
  isOverlay?: (el: Element) => boolean;
  /**
   * A single primary press on the canvas. Return true when the component
   * takes it (node drag, handle drag …); the hook then leaves that pointer
   * alone. Unclaimed presses pan.
   */
  claim?: (e: PointerEvent) => boolean;
  /** A claimed press was taken over by a two-finger gesture. */
  onClaimCancel?: () => void;
  /** Applied to every gesture-driven view (e.g. keep content on screen). */
  clamp?: (v: View) => View;
  /** The user moved the view by a gesture, wheel or toolbar. */
  onUserMove?: () => void;
}

const ANIM_MS = 240;
const TRACKPAD_STREAK_MS = 400;

/**
 * Shared pan / zoom for the topology canvases.
 *
 * - Trackpad (macOS first): two-finger scroll = wheel without ctrlKey →
 *   pan; pinch = wheel with ctrlKey (Chrome / Firefox / Edge) or Safari
 *   gesture events → zoom around the cursor. Both are applied as they
 *   arrive, so a browser that interleaves them during one gesture gets
 *   pan and zoom together.
 * - Mouse wheel (a large vertical step outside a trackpad scroll, or line
 *   deltas) → smooth zoom around the cursor.
 * - Touch: one finger on the canvas pans; two fingers pan by their
 *   midpoint and zoom by their distance at the same time.
 * - Toolbar zoom / fit glide (animateTo); trackpad and touch are immediate.
 */
export function usePanZoom(opts: PanZoomOptions) {
  const minK = opts.minK ?? 0.3;
  const maxK = opts.maxK ?? 3;
  const [view, setViewState] = useState<View>(opts.initial ?? { x: 0, y: 0, k: 1 });
  const viewRef = useRef(view);
  const optsRef = useRef(opts);
  optsRef.current = opts;

  const clampK = useCallback((k: number) => Math.max(minK, Math.min(maxK, k)), [minK, maxK]);

  const apply = useCallback((v: View) => {
    viewRef.current = v;
    setViewState(v);
  }, []);
  const applyGesture = useCallback((v: View) => {
    const c = optsRef.current.clamp;
    apply(c ? c(v) : v);
    optsRef.current.onUserMove?.();
  }, [apply]);

  // ---- animation (toolbar zoom / fit, wheel glide) ----
  const anim = useRef(0);
  const wheelTarget = useRef<{ v: View; px: number; py: number } | null>(null);
  const stopAnim = useCallback(() => {
    if (anim.current) { cancelAnimationFrame(anim.current); anim.current = 0; }
    wheelTarget.current = null;
  }, []);
  useEffect(() => stopAnim, [stopAnim]);
  const animateTo = useCallback((to: View, dur = ANIM_MS) => {
    stopAnim();
    const from = viewRef.current;
    const t0 = performance.now();
    // The world point at the centre moves in a straight line (a zoom in
    // place keeps it still); interpolating the offset itself would make
    // the view swing sideways when the offset is large.
    const r = optsRef.current.ref.current?.getBoundingClientRect();
    const cx = r ? r.width / 2 : 0, cy = r ? r.height / 2 : 0;
    const a = { x: (cx - from.x) / from.k, y: (cy - from.y) / from.k };
    const b = { x: (cx - to.x) / to.k, y: (cy - to.y) / to.k };
    const step = (now: number) => {
      const p = Math.min(1, (now - t0) / dur);
      const e = 1 - Math.pow(1 - p, 3);
      // Geometric scale interpolation keeps the zoom speed even.
      const k = from.k * Math.pow(to.k / from.k, e);
      apply(p < 1
        ? { k, x: cx - (a.x + (b.x - a.x) * e) * k, y: cy - (a.y + (b.y - a.y) * e) * k }
        : to);
      anim.current = p < 1 ? requestAnimationFrame(step) : 0;
    };
    anim.current = requestAnimationFrame(step);
  }, [apply, stopAnim]);

  // ---- smooth wheel zoom ----
  // Each wheel notch moves a target; the view eases toward it every frame,
  // keeping the world point under the cursor fixed. Notches that arrive
  // mid-glide stack onto the target instead of restarting.
  const zoomSmooth = useCallback((factor: number, cx: number, cy: number) => {
    const base = anim.current && wheelTarget.current ? wheelTarget.current.v : viewRef.current;
    const k = clampK(base.k * factor);
    const s = k / base.k;
    wheelTarget.current = { v: { k, x: cx - (cx - base.x) * s, y: cy - (cy - base.y) * s }, px: cx, py: cy };
    optsRef.current.onUserMove?.();
    if (anim.current) return;
    let last = performance.now();
    const step = (now: number) => {
      const t = wheelTarget.current;
      if (!t) { anim.current = 0; return; }
      const dt = Math.min(64, now - last);
      last = now;
      const cur = viewRef.current;
      const a = 1 - Math.exp(-dt / 70);
      const kk = cur.k * Math.pow(t.v.k / cur.k, a);
      // World point under the cursor in the target view stays under it.
      const wx = (t.px - t.v.x) / t.v.k, wy = (t.py - t.v.y) / t.v.k;
      const done = Math.abs(Math.log(t.v.k / kk)) < 0.002;
      const next = done ? t.v : { k: kk, x: t.px - wx * kk, y: t.py - wy * kk };
      const c = optsRef.current.clamp;
      apply(c ? c(next) : next);
      if (done) { wheelTarget.current = null; anim.current = 0; return; }
      anim.current = requestAnimationFrame(step);
    };
    anim.current = requestAnimationFrame(step);
  }, [apply, clampK]);

  /** Immediate set (stops any glide). */
  const setView = useCallback((v: View) => { stopAnim(); apply(v); }, [apply, stopAnim]);

  const zoomAtNow = useCallback((factor: number, cx: number, cy: number) => {
    stopAnim();
    const v = viewRef.current;
    const k = clampK(v.k * factor);
    const s = k / v.k;
    applyGesture({ k, x: cx - (cx - v.x) * s, y: cy - (cy - v.y) * s });
  }, [clampK, applyGesture, stopAnim]);

  /** Toolbar zoom around the canvas centre, animated. */
  const zoomBy = useCallback((factor: number) => {
    const el = optsRef.current.ref.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    const v = viewRef.current;
    const k = clampK(v.k * factor);
    const s = k / v.k;
    const cx = r.width / 2, cy = r.height / 2;
    optsRef.current.onUserMove?.();
    animateTo({ k, x: cx - (cx - v.x) * s, y: cy - (cy - v.y) * s });
  }, [clampK, animateTo]);

  // ---- click suppression after a pan / pinch ----
  const suppressClick = useRef(false);
  const consumeClick = useCallback(() => suppressClick.current, []);
  const armSuppress = () => {
    suppressClick.current = true;
    window.setTimeout(() => { suppressClick.current = false; }, 0);
  };

  // ---- wheel, Safari gestures, pointers ----
  useEffect(() => {
    const el = opts.ref.current;
    if (!el) return;
    const lastTrackpad = { t: -Infinity };
    let safariGesture: { k0: number } | null = null;
    const overlay = (t: EventTarget | null) => {
      const f = optsRef.current.isOverlay;
      return !!f && t instanceof Element && f(t);
    };
    const local = (cx: number, cy: number) => {
      const r = el.getBoundingClientRect();
      return { x: cx - r.left, y: cy - r.top };
    };

    const onWheel = (e: WheelEvent) => {
      if (overlay(e.target)) return;
      e.preventDefault();
      const now = performance.now();
      const p = local(e.clientX, e.clientY);
      // Mouse wheel vs trackpad. A wheel notch is one large vertical step
      // (≈100 px, more with display scaling, and often fractional then);
      // trackpad scrolling starts with small steps and may move sideways.
      // Large steps inside an ongoing trackpad scroll (momentum) stay
      // trackpad. Firefox reports mouse wheels in lines (deltaMode 1).
      const trackpadLike = e.deltaMode === 0
        && (e.deltaX !== 0 || Math.abs(e.deltaY) < 40 || now - lastTrackpad.t <= TRACKPAD_STREAK_MS);
      if (trackpadLike) lastTrackpad.t = now;
      const mouseLike = !trackpadLike;
      if (e.ctrlKey) {
        if (safariGesture) return;
        if (mouseLike && e.deltaMode !== 0) {
          zoomSmooth(Math.pow(1.15, -Math.max(-5, Math.min(5, e.deltaY / 3))), p.x, p.y);
        } else if (mouseLike) {
          zoomSmooth(Math.pow(1.15, -Math.max(-5, Math.min(5, e.deltaY / 100))), p.x, p.y);
        } else {
          // Trackpad pinch: small continuous deltas.
          zoomAtNow(Math.exp(-Math.max(-30, Math.min(30, e.deltaY)) * 0.01), p.x, p.y);
        }
        return;
      }
      if (mouseLike) {
        const notches = e.deltaMode === 1 ? e.deltaY / 3 : e.deltaMode === 2 ? e.deltaY : e.deltaY / 100;
        zoomSmooth(Math.pow(1.2, -Math.max(-5, Math.min(5, notches))), p.x, p.y);
        return;
      }
      // Two-finger scroll → pan. Shift+wheel scrolls horizontally on some
      // platforms, which deltaX already reflects.
      stopAnim();
      const v = viewRef.current;
      applyGesture({ ...v, x: v.x - e.deltaX, y: v.y - e.deltaY });
    };

    // Safari (macOS trackpad pinch): non-standard GestureEvent with a
    // cumulative `scale`.
    const onGestureStart = (e: Event) => {
      if (overlay(e.target)) return;
      e.preventDefault();
      stopAnim();
      safariGesture = { k0: viewRef.current.k };
    };
    const onGestureChange = (e: Event) => {
      if (!safariGesture) return;
      e.preventDefault();
      const ge = e as Event & { scale: number; clientX: number; clientY: number };
      const v = viewRef.current;
      const k = clampK(safariGesture.k0 * (ge.scale || 1));
      const p = local(ge.clientX, ge.clientY);
      const s = k / v.k;
      applyGesture({ k, x: p.x - (p.x - v.x) * s, y: p.y - (p.y - v.y) * s });
    };
    const onGestureEnd = (e: Event) => {
      if (!safariGesture) return;
      e.preventDefault();
      safariGesture = null;
    };

    type G =
      | { kind: 'pan'; id: number; sx: number; sy: number; v0: View; moved: boolean; touch: boolean }
      | { kind: 'pinch'; m0: { x: number; y: number }; d0: number; v0: View }
      | { kind: 'claimed'; id: number };
    const pointers = new Map<number, { x: number; y: number; type: string }>();
    let g: G | null = null;

    const startPinch = () => {
      const pts = [...pointers.values()].filter((p) => p.type !== 'mouse').slice(0, 2);
      if (pts.length < 2) return false;
      const [a, b] = pts;
      g = {
        kind: 'pinch',
        m0: local((a.x + b.x) / 2, (a.y + b.y) / 2),
        d0: Math.max(1, Math.hypot(a.x - b.x, a.y - b.y)),
        v0: viewRef.current,
      };
      return true;
    };

    const onDown = (e: PointerEvent) => {
      if (overlay(e.target)) return;
      if (e.pointerType === 'mouse' && e.button !== 0) return;
      stopAnim();
      // A mouse release outside the window can be lost; never let a stale
      // entry block the next press.
      if (e.pointerType === 'mouse') {
        for (const [id, p] of pointers) if (p.type === 'mouse') pointers.delete(id);
        if (pointers.size === 0) g = null;
      }
      pointers.set(e.pointerId, { x: e.clientX, y: e.clientY, type: e.pointerType });
      if (e.pointerType !== 'mouse' && pointers.size >= 2) {
        if (g?.kind === 'claimed') optsRef.current.onClaimCancel?.();
        if (startPinch()) { e.preventDefault(); return; }
      }
      if (pointers.size > 1) return;
      if (optsRef.current.claim?.(e)) { g = { kind: 'claimed', id: e.pointerId }; return; }
      g = { kind: 'pan', id: e.pointerId, sx: e.clientX, sy: e.clientY, v0: viewRef.current, moved: false, touch: e.pointerType !== 'mouse' };
    };

    const onMove = (e: PointerEvent) => {
      const pt = pointers.get(e.pointerId);
      if (!pt) return;
      pt.x = e.clientX; pt.y = e.clientY;
      if (!g) return;
      if (g.kind === 'pinch') {
        const pts = [...pointers.values()].filter((p) => p.type !== 'mouse').slice(0, 2);
        if (pts.length < 2) return;
        const [a, b] = pts;
        const d = Math.max(1, Math.hypot(a.x - b.x, a.y - b.y));
        const m = local((a.x + b.x) / 2, (a.y + b.y) / 2);
        const k = clampK(g.v0.k * (d / g.d0));
        // World point that sat under the starting midpoint stays under the
        // current midpoint: two fingers moving together pan, spreading zooms.
        const wx = (g.m0.x - g.v0.x) / g.v0.k;
        const wy = (g.m0.y - g.v0.y) / g.v0.k;
        applyGesture({ k, x: m.x - wx * k, y: m.y - wy * k });
        return;
      }
      if (g.kind === 'pan' && g.id === e.pointerId) {
        const dx = e.clientX - g.sx;
        const dy = e.clientY - g.sy;
        if (!g.moved) {
          if (Math.hypot(dx, dy) < (g.touch ? 6 : 4)) return;
          g.moved = true;
        }
        applyGesture({ ...g.v0, x: g.v0.x + dx, y: g.v0.y + dy });
      }
    };

    const onUp = (e: PointerEvent) => {
      if (!pointers.has(e.pointerId)) return;
      pointers.delete(e.pointerId);
      if (!g) return;
      if (g.kind === 'pinch') {
        armSuppress();
        const rest = [...pointers.entries()].filter(([, p]) => p.type !== 'mouse');
        if (rest.length >= 2) { startPinch(); return; }
        if (rest.length === 1) {
          // One finger left after a pinch: continue as a pan from here.
          const [id, p] = rest[0];
          g = { kind: 'pan', id, sx: p.x, sy: p.y, v0: viewRef.current, moved: true, touch: true };
          return;
        }
        g = null;
        return;
      }
      if (g.kind === 'pan' && g.id === e.pointerId) {
        if (g.moved) armSuppress();
        g = null;
        return;
      }
      if (g.kind === 'claimed' && g.id === e.pointerId) g = null;
      if (pointers.size === 0) g = null;
    };

    el.addEventListener('wheel', onWheel as EventListener, { passive: false });
    el.addEventListener('gesturestart', onGestureStart, { passive: false } as AddEventListenerOptions);
    el.addEventListener('gesturechange', onGestureChange, { passive: false } as AddEventListenerOptions);
    el.addEventListener('gestureend', onGestureEnd, { passive: false } as AddEventListenerOptions);
    el.addEventListener('pointerdown', onDown as EventListener);
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    window.addEventListener('pointercancel', onUp);
    return () => {
      el.removeEventListener('wheel', onWheel as EventListener);
      el.removeEventListener('gesturestart', onGestureStart);
      el.removeEventListener('gesturechange', onGestureChange);
      el.removeEventListener('gestureend', onGestureEnd);
      el.removeEventListener('pointerdown', onDown as EventListener);
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onUp);
    };
    // The ref target is stable for the component's lifetime.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [opts.ref, clampK, applyGesture, zoomAtNow, zoomSmooth, stopAnim]);

  return { view, viewRef, setView, animateTo, zoomBy, stopAnim, consumeClick, clampK };
}
