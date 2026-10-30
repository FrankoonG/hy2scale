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
 *   gesture events → zoom around the cursor.
 * - Classic mouse wheel (line deltas, or large integer steps with no
 *   horizontal component and no trackpad activity just before) → zoom.
 * - Touch: one finger on the canvas pans; two fingers pan by their
 *   midpoint and zoom by their distance at the same time.
 * - Toolbar zoom / fit glide (animateTo); gestures are immediate.
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

  // ---- animation (toolbar zoom / fit) ----
  const anim = useRef(0);
  const stopAnim = useCallback(() => {
    if (anim.current) { cancelAnimationFrame(anim.current); anim.current = 0; }
  }, []);
  useEffect(() => stopAnim, [stopAnim]);
  const animateTo = useCallback((to: View, dur = ANIM_MS) => {
    stopAnim();
    const from = viewRef.current;
    const t0 = performance.now();
    const step = (now: number) => {
      const p = Math.min(1, (now - t0) / dur);
      const e = 1 - Math.pow(1 - p, 3);
      // Geometric scale interpolation keeps the zoom speed even.
      const k = from.k * Math.pow(to.k / from.k, e);
      apply({ k, x: from.x + (to.x - from.x) * e, y: from.y + (to.y - from.y) * e });
      anim.current = p < 1 ? requestAnimationFrame(step) : 0;
    };
    anim.current = requestAnimationFrame(step);
  }, [apply, stopAnim]);

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
          zoomAtNow(Math.pow(1.15, -Math.max(-5, Math.min(5, e.deltaY / 3))), p.x, p.y);
        } else if (mouseLike) {
          zoomAtNow(Math.pow(1.15, -Math.max(-5, Math.min(5, e.deltaY / 100))), p.x, p.y);
        } else {
          // Trackpad pinch: small continuous deltas.
          zoomAtNow(Math.exp(-Math.max(-30, Math.min(30, e.deltaY)) * 0.01), p.x, p.y);
        }
        return;
      }
      if (mouseLike) {
        const notches = e.deltaMode === 1 ? e.deltaY / 3 : e.deltaMode === 2 ? e.deltaY : e.deltaY / 100;
        zoomAtNow(Math.pow(1.15, -Math.max(-5, Math.min(5, notches))), p.x, p.y);
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
  }, [opts.ref, clampK, applyGesture, zoomAtNow, stopAnim]);

  return { view, viewRef, setView, animateTo, zoomBy, stopAnim, consumeClick, clampK };
}
