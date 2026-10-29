import { useCallback, useState } from 'react';

/** Screen point a modal grows from / shrinks back to. */
export interface ModalOrigin { x: number; y: number }

/**
 * Where a modal should animate from: the pointer position of a mouse event,
 * the centre of an element (or of an event's currentTarget when the event
 * has no pointer position, e.g. keyboard activation), or a point as is.
 */
export function originOf(src?: { clientX?: number; clientY?: number; currentTarget?: unknown } | Element | ModalOrigin | null): ModalOrigin | undefined {
  if (!src) return undefined;
  if (typeof Element !== 'undefined' && src instanceof Element) {
    const r = src.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  }
  const e = src as { clientX?: number; clientY?: number; currentTarget?: unknown; x?: number; y?: number };
  if (typeof e.clientX === 'number' && typeof e.clientY === 'number' && (e.clientX || e.clientY)) {
    return { x: e.clientX, y: e.clientY };
  }
  if (e.currentTarget && typeof Element !== 'undefined' && e.currentTarget instanceof Element) return originOf(e.currentTarget);
  if (typeof e.x === 'number' && typeof e.y === 'number') return { x: e.x, y: e.y };
  return undefined;
}

export interface ModalState<T> {
  open: boolean;
  /** The record the modal shows; kept after close so the exit animation
   *  still has content. */
  data: T | undefined;
  origin: ModalOrigin | undefined;
  /** Opens with `data`, animating from `from` (event, element or point);
   *  without `from` the previous origin is reused. */
  show: (data?: T, from?: Parameters<typeof originOf>[0]) => void;
  close: () => void;
  /** Spread onto <Modal>: open, onClose, animateFrom. */
  modalProps: { open: boolean; onClose: () => void; animateFrom: ModalOrigin | undefined };
}

/** Open/close state, current record and animation origin of one modal. */
export function useModalState<T = undefined>(): ModalState<T> {
  const [s, setS] = useState<{ open: boolean; data: T | undefined; origin: ModalOrigin | undefined }>(
    { open: false, data: undefined, origin: undefined },
  );
  const show = useCallback((data?: T, from?: Parameters<typeof originOf>[0]) => {
    const o = originOf(from);
    setS((p) => ({ open: true, data, origin: o ?? p.origin }));
  }, []);
  const close = useCallback(() => setS((p) => (p.open ? { ...p, open: false } : p)), []);
  return {
    open: s.open,
    data: s.data,
    origin: s.origin,
    show,
    close,
    modalProps: { open: s.open, onClose: close, animateFrom: s.origin },
  };
}
