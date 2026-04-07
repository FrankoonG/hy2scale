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
  // Store animateFrom at open time so close animation uses the same origin
  const originRef = useRef(animateFrom);
  if (open && animateFrom) originRef.current = animateFrom;
  const from = originRef.current;

  useEffect(() => {
    if (!open) return;
    const handler = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [open, onClose]);

  // Calculate offset from viewport center to mouse position
  const getOffset = () => {
    if (!from) return { x: 0, y: 0 };
    const cx = window.innerWidth / 2;
    const cy = window.innerHeight / 2;
    return { x: from.x - cx, y: from.y - cy };
  };

  const off = getOffset();

  return createPortal(
    <AnimatePresence>
      {open && (
        <motion.div
          className="hy-modal-overlay"
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          transition={{ duration: 0.2 }}
          onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}
        >
          <motion.div
            className={`hy-modal${wide ? ' wide' : ''}`}
            style={{ willChange: 'transform, opacity' }}
            initial={{ scale: 0.15, opacity: 0, x: off.x, y: off.y }}
            animate={{ scale: 1, opacity: 1, x: 0, y: 0 }}
            exit={{ scale: 0.15, opacity: 0, x: off.x, y: off.y }}
            transition={{
              default: { duration: 0.35, ease: [0.16, 1, 0.3, 1] },
              opacity: { duration: 0.2, ease: 'easeOut' },
            }}
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
