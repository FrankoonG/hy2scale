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

  // Calculate transform origin from click position
  const getOrigin = () => {
    if (!animateFrom) return undefined;
    const cx = window.innerWidth / 2;
    const cy = window.innerHeight / 2;
    const ox = ((animateFrom.x - cx) / cx) * 100;
    const oy = ((animateFrom.y - cy) / cy) * 100;
    return `${50 + ox}% ${50 + oy}%`;
  };

  // Portal to document.body to escape any parent transform/overflow that would
  // break position:fixed (e.g. PageTransition's framer-motion transform)
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
            <AnimatedBody>{children}</AnimatedBody>
            {footer && <div className="hy-modal-footer">{footer}</div>}
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>,
    document.body,
  );
}

/** Wrapper that smoothly animates height when children change size */
function AnimatedBody({ children }: { children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const [height, setHeight] = useState<number | 'auto'>('auto');
  const initialized = useRef(false);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => {
      const h = el.scrollHeight;
      if (!initialized.current) {
        // First measure — set immediately without animation
        initialized.current = true;
        setHeight(h);
      } else {
        setHeight(h);
      }
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  return (
    <motion.div
      className="hy-modal-body"
      animate={{ height }}
      transition={{ type: 'spring', stiffness: 300, damping: 30 }}
      style={{ overflow: 'hidden' }}
    >
      <div ref={ref}>{children}</div>
    </motion.div>
  );
}
