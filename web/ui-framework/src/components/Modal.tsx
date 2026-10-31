import { type ReactNode, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { motion } from 'framer-motion';

export interface ModalProps {
  open: boolean;
  onClose: () => void;
  title?: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
  animateFrom?: { x: number; y: number };
  children: ReactNode;
}

/*
 * Modal motion. Every open and close is queued and resolved once per React
 * commit (in a microtask, after all layout effects), so the modals involved
 * in one update are animated together:
 *
 *  - a modal closes and another opens: the old content fades out, then the
 *    new frame takes the old frame's place and resizes to its own, its
 *    content fading in (one dialog turning into the next);
 *  - a modal opens over another one: it grows out of that modal's frame,
 *    and shrinks back into it when it closes;
 *  - otherwise: it grows from the pointer (animateFrom) and back.
 */

interface Rect { left: number; top: number; width: number; height: number }

const EASE_OUT = 'cubic-bezier(0.16, 1, 0.3, 1)';
const EASE_MORPH = 'cubic-bezier(0.2, 0.8, 0.2, 1)';
const SWAP_FADE = 120;
const SWAP_MORPH = 340;

/** Transform that lays a box of rect `r` over `to`. */
function mapTo(r: Rect, to: Rect): string {
  const dx = to.left + to.width / 2 - (r.left + r.width / 2);
  const dy = to.top + to.height / 2 - (r.top + r.height / 2);
  return `translate(${dx}px, ${dy}px) scale(${to.width / r.width}, ${to.height / r.height})`;
}

class ModalMotion {
  overlay: HTMLDivElement | null = null;
  backdrop: HTMLDivElement | null = null;
  frame: HTMLDivElement | null = null;
  origin: { x: number; y: number } | undefined;
  parent: ModalMotion | null = null;
  shown = false;
  restore = false;
  onEntered = () => {};
  onExited = () => {};
  private anims: Animation[] = [];

  rect(): Rect {
    const r = this.frame!.getBoundingClientRect();
    return { left: r.left, top: r.top, width: r.width, height: r.height };
  }

  private parts(): HTMLElement[] {
    return this.frame ? (Array.from(this.frame.children) as HTMLElement[]) : [];
  }

  private play(el: HTMLElement | null, kf: Keyframe[], opts: KeyframeAnimationOptions): Animation | null {
    if (!el || typeof el.animate !== 'function') return null;
    const a = el.animate(kf, { fill: 'both', ...opts });
    this.anims.push(a);
    return a;
  }

  /** Runs `done` once every animation started since the last stop ends. */
  private whenDone(done: () => void) {
    const mine = this.anims.slice();
    Promise.all(mine.map((a) => a.finished)).then(() => {
      if (mine.every((a) => this.anims.includes(a))) done();
    }, () => { /* cancelled: a newer motion took over */ });
  }

  stop() {
    for (const a of this.anims) a.cancel();
    this.anims = [];
  }

  /** Before the first paint of an opening modal. */
  hide() {
    if (this.frame) this.frame.style.opacity = '0';
    if (this.backdrop) this.backdrop.style.opacity = '0';
  }

  private unhide() {
    if (this.frame) this.frame.style.opacity = '';
    if (this.backdrop) this.backdrop.style.opacity = '';
  }

  private settle() {
    this.stop();
    this.onEntered();
  }

  private pointerOffset() {
    const o = this.origin;
    if (!o) return { x: 0, y: 0 };
    return { x: o.x - window.innerWidth / 2, y: o.y - window.innerHeight / 2 };
  }

  enterFromPointer() {
    this.stop();
    const { x, y } = this.pointerOffset();
    this.play(this.frame, [{ transform: `translate(${x}px, ${y}px) scale(0.15)` }, { transform: 'none' }], { duration: 470, easing: EASE_OUT });
    this.play(this.frame, [{ opacity: 0 }, { opacity: 1 }], { duration: 270, easing: 'ease-out' });
    this.play(this.backdrop, [{ opacity: 0 }, { opacity: 1 }], { duration: 200 });
    this.unhide();
    this.whenDone(() => this.settle());
  }

  exitToPointer() {
    this.stop();
    const { x, y } = this.pointerOffset();
    this.play(this.frame, [{ transform: 'none' }, { transform: `translate(${x}px, ${y}px) scale(0.15)` }], { duration: 470, easing: EASE_OUT });
    this.play(this.frame, [{ opacity: 1 }, { opacity: 0 }], { duration: 270, easing: 'ease-out' });
    this.play(this.backdrop, [{ opacity: 1 }, { opacity: 0 }], { duration: 200 });
    this.whenDone(() => this.onExited());
  }

  /** Takes the place of `prev` (which is closing) and resizes to its own frame. */
  replace(prev: ModalMotion, from: Rect) {
    this.stop();
    prev.stop();
    const at = { duration: 1, delay: SWAP_FADE };
    for (const p of prev.parts()) prev.play(p, [{ opacity: 1 }, { opacity: 0 }], { duration: SWAP_FADE, easing: 'ease-in' });
    // The swap happens in one frame: old frame and backdrop out, new in.
    prev.play(prev.frame, [{ opacity: 1 }, { opacity: 0 }], at);
    prev.play(prev.backdrop, [{ opacity: 1 }, { opacity: 0 }], at);
    this.play(this.backdrop, [{ opacity: 0 }, { opacity: 1 }], at);
    this.play(this.frame, [{ opacity: 0 }, { opacity: 1 }], at);
    this.play(this.frame, [{ transform: mapTo(this.rect(), from) }, { transform: 'none' }], { duration: SWAP_MORPH, delay: SWAP_FADE, easing: EASE_MORPH });
    for (const p of this.parts()) this.play(p, [{ opacity: 0 }, { opacity: 1 }], { duration: 220, delay: SWAP_FADE + 140, easing: 'ease-out' });
    this.unhide();
    prev.whenDone(() => prev.onExited());
    this.whenDone(() => this.settle());
  }

  /** Grows out of the frame of the modal below. */
  enterFrom(from: Rect) {
    this.stop();
    this.play(this.backdrop, [{ opacity: 0 }, { opacity: 1 }], { duration: 260 });
    this.play(this.frame, [{ opacity: 0 }, { opacity: 1 }], { duration: 180, easing: 'ease-out' });
    this.play(this.frame, [{ transform: mapTo(this.rect(), from) }, { transform: 'none' }], { duration: 380, easing: EASE_MORPH });
    for (const p of this.parts()) this.play(p, [{ opacity: 0 }, { opacity: 1 }], { duration: 220, delay: 160, easing: 'ease-out' });
    this.unhide();
    this.whenDone(() => this.settle());
  }

  /** Shrinks back into the frame of the modal below. */
  exitTo(to: Rect) {
    this.stop();
    for (const p of this.parts()) this.play(p, [{ opacity: 1 }, { opacity: 0 }], { duration: SWAP_FADE, easing: 'ease-in' });
    this.play(this.frame, [{ transform: 'none' }, { transform: mapTo(this.rect(), to) }], { duration: 300, easing: EASE_MORPH });
    this.play(this.frame, [{ opacity: 1 }, { opacity: 0 }], { duration: 200, delay: 100 });
    this.play(this.backdrop, [{ opacity: 1 }, { opacity: 0 }], { duration: 260 });
    this.whenDone(() => this.onExited());
  }
}

/** Modals currently open, bottom to top. */
const openStack: ModalMotion[] = [];
let pending: { kind: 'open' | 'close'; m: ModalMotion }[] = [];

function queueMotion(kind: 'open' | 'close', m: ModalMotion) {
  if (!pending.length) queueMicrotask(flushMotion);
  pending.push({ kind, m });
}

function flushMotion() {
  const events = pending;
  pending = [];
  // The last event per modal wins (opened and closed again in one go).
  const last = new Map<ModalMotion, 'open' | 'close'>();
  for (const e of events) last.set(e.m, e.kind);
  const opens: ModalMotion[] = [];
  const closes: { m: ModalMotion; rect: Rect }[] = [];
  for (const [m, kind] of last) {
    if (kind === 'open') {
      if (m.frame && !m.shown) opens.push(m);
    } else if (m.shown && m.frame) {
      closes.push({ m, rect: m.rect() });
    } else {
      m.onExited();
    }
  }
  for (const c of closes) {
    c.m.shown = false;
    const i = openStack.indexOf(c.m);
    if (i >= 0) openStack.splice(i, 1);
  }
  // One dialog turning into the next: the topmost closing one is replaced.
  closes.sort((a, b) => zOf(a.m) - zOf(b.m));
  while (opens.length && closes.length) {
    const o = opens.shift()!;
    const c = closes.pop()!;
    o.shown = true;
    o.parent = c.m.parent && c.m.parent.shown ? c.m.parent : null;
    openStack.push(o);
    o.replace(c.m, c.rect);
  }
  for (const { m } of closes) {
    if (m.parent && m.parent.shown && m.parent.frame) m.exitTo(m.parent.rect());
    else m.exitToPointer();
  }
  for (const o of opens) {
    const below = openStack[openStack.length - 1];
    o.shown = true;
    o.parent = below || null;
    openStack.push(o);
    if (below && below.frame) o.enterFrom(below.rect());
    else o.enterFromPointer();
  }
}

/** Stacking order of a modal's overlay (later in the body = on top). */
function zOf(m: ModalMotion): number {
  const el = m.overlay;
  return el && el.parentNode ? Array.prototype.indexOf.call(el.parentNode.children, el) : -1;
}

export function Modal({ open, onClose, title, footer, wide, animateFrom, children }: ModalProps) {
  const ref = useRef<ModalMotion | null>(null);
  if (!ref.current) ref.current = new ModalMotion();
  const m = ref.current;
  // Origin stored at open time, so closing goes back to the same point.
  if (open && animateFrom) m.origin = animateFrom;

  // Mounted while open and while the close animation runs.
  const [present, setPresent] = useState(open);
  if (open && !present) setPresent(true);
  // Pointer events stay off while the modal moves, so tooltips under the
  // cursor are not triggered by the animation.
  const [interactive, setInteractive] = useState(false);
  m.onEntered = () => setInteractive(true);
  m.onExited = () => { m.parent = null; setPresent(false); };

  const wasOpen = useRef(false);
  useLayoutEffect(() => {
    if (!present) return;
    if (open && !wasOpen.current) {
      wasOpen.current = true;
      setInteractive(false);
      m.hide();
      queueMotion('open', m);
    } else if (!open && wasOpen.current) {
      wasOpen.current = false;
      setInteractive(false);
      if (m.overlay) m.overlay.style.pointerEvents = 'none';
      queueMotion('close', m);
    }
  }, [open, present]); // eslint-disable-line react-hooks/exhaustive-deps

  // Unmounted while open (e.g. the page went away). The re-run puts back
  // a modal that is still open (StrictMode mounts effects twice).
  useEffect(() => {
    if (m.restore) { m.restore = false; m.shown = true; openStack.push(m); }
    return () => {
      const i = openStack.indexOf(m);
      m.restore = i >= 0;
      m.shown = false;
      if (i >= 0) openStack.splice(i, 1);
    };
  }, [m]);

  // Track mousedown origin to prevent close when dragging text outside the modal
  const mouseDownOnOverlay = useRef(false);
  const isBackdrop = (t: EventTarget) => t === m.overlay || t === m.backdrop;

  useEffect(() => {
    if (!open) return;
    const handler = (e: KeyboardEvent) => {
      // Only the topmost modal answers Escape, and not when a popover in it
      // (a select list, a menu) took it.
      if (e.key === 'Escape' && !e.defaultPrevented && openStack[openStack.length - 1] === m) onClose();
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [open, onClose, m]);

  // While closing, the content of the last open render is shown (callers
  // often clear the record the modal displays when they close it).
  const shownProps = useRef({ title, footer, wide, children });
  if (open) shownProps.current = { title, footer, wide, children };
  ({ title, footer, wide, children } = shownProps.current);

  if (!present) return null;
  return createPortal(
    <div
      ref={(el) => { m.overlay = el; }}
      className="hy-modal-overlay hy-modal-layer"
      style={{ pointerEvents: interactive && open ? 'auto' : 'none' }}
      onMouseDown={(e) => { mouseDownOnOverlay.current = isBackdrop(e.target); }}
      onClick={(e) => { if (isBackdrop(e.target) && mouseDownOnOverlay.current) onClose(); mouseDownOnOverlay.current = false; }}
    >
      <div ref={(el) => { m.backdrop = el; }} className="hy-modal-backdrop" />
      <div
        ref={(el) => { m.frame = el; }}
        className={`hy-modal${wide ? ' wide' : ''}`}
        style={{ willChange: 'transform, opacity' }}
      >
        {title && (
          <div className="hy-modal-header">
            <h2>{title}</h2>
          </div>
        )}
        <SmoothBody>{children}</SmoothBody>
        {footer && <div className="hy-modal-footer">{footer}</div>}
      </div>
    </div>,
    document.body,
  );
}

/**
 * Measures inner content height via ResizeObserver and animates
 * the outer wrapper's height with a spring transition.
 * First measurement is instant (no animation on open).
 */
function SmoothBody({ children }: { children: ReactNode }) {
  const innerRef = useRef<HTMLDivElement>(null);
  const [h, setH] = useState<number>(0);
  const count = useRef(0);

  useEffect(() => {
    const el = innerRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => {
      setH(el.offsetHeight);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // Skip animation for the first two measurements (mount + first content render)
  const shouldAnimate = count.current > 1;
  if (h > 0) count.current++;

  // Modal max-height is 85vh; header ~60px, footer ~60px => body max ≈ 85vh - 120
  const maxH = typeof window !== 'undefined' ? window.innerHeight * 0.85 - 120 : 9999;
  const cappedH = h > 0 ? Math.min(h, maxH) : 0;
  const needsScroll = h > maxH;

  return (
    <motion.div
      className="hy-modal-body"
      animate={{ height: cappedH || 'auto' }}
      initial={false}
      transition={shouldAnimate
        ? { type: 'spring', stiffness: 300, damping: 30 }
        : { duration: 0 }
      }
      style={{ overflow: needsScroll ? 'auto' : 'hidden', flex: 'none', padding: 0 }}
    >
      <div ref={innerRef} style={{ padding: '20px 24px' }}>{children}</div>
    </motion.div>
  );
}
