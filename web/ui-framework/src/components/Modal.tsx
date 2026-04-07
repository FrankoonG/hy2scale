import { type ReactNode, useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { motion, AnimatePresence } from 'framer-motion';

export interface ModalProps {
  open: boolean;
  onClose: () => void;
  title?: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
  animateFrom?: { x: number; y: number };
  children: ReactNode;
}

export function Modal({ open, onClose, title, footer, wide, animateFrom, children }: ModalProps) {
  useEffect(() => {
    if (!open) return;
    const handler = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [open, onClose]);

  const getOrigin = () => {
    if (!animateFrom) return undefined;
    const cx = window.innerWidth / 2;
    const cy = window.innerHeight / 2;
    const ox = ((animateFrom.x - cx) / cx) * 100;
    const oy = ((animateFrom.y - cy) / cy) * 100;
    return `${50 + ox}% ${50 + oy}%`;
  };

  return createPortal(
    <AnimatePresence>
      {open && (
        <motion.div
          className="hy-modal-overlay"
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          transition={{ duration: 0.15 }}
          onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}
        >
          <motion.div
            className={`hy-modal${wide ? ' wide' : ''}`}
            initial={{ scale: 0.3, opacity: 0 }}
            animate={{ scale: 1, opacity: 1 }}
            exit={{ scale: 0.9, opacity: 0 }}
            transition={{
              type: 'spring',
              stiffness: 400,
              damping: 25,
              mass: 0.8,
            }}
            style={{ transformOrigin: getOrigin() }}
          >
            {title && (
              <div className="hy-modal-header">
                <h2>{title}</h2>
                <button className="hy-icon-btn" onClick={onClose}>✕</button>
              </div>
            )}
            <SmoothBody>{children}</SmoothBody>
            {footer && <div className="hy-modal-footer">{footer}</div>}
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>,
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
