import { useState, useRef, useCallback, useLayoutEffect, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { motion, AnimatePresence } from 'framer-motion';

export interface TooltipProps {
  content: ReactNode;
  children: ReactNode;
}

interface TooltipPos {
  x: number;
  y: number;
  originX: string; // transform-origin X for scale animation
  originY: string; // transform-origin Y for scale animation
}

const GAP = 6;

export function Tooltip({ content, children }: TooltipProps) {
  const [show, setShow] = useState(false);
  const [pos, setPos] = useState<TooltipPos | null>(null);
  const wrapRef = useRef<HTMLSpanElement>(null);
  const tipRef = useRef<HTMLDivElement>(null);

  // Recompute position after the tooltip renders (so we know its size)
  useLayoutEffect(() => {
    if (!show || !wrapRef.current || !tipRef.current) return;
    const triggerRect = wrapRef.current.getBoundingClientRect();
    // Use offsetWidth/Height — unaffected by CSS transforms (scale animation)
    const tipW = tipRef.current.offsetWidth;
    const tipH = tipRef.current.offsetHeight;
    const vw = window.innerWidth;
    const vh = window.innerHeight;

    // Default: below-right of trigger
    let x = triggerRect.left;
    let y = triggerRect.bottom + GAP;
    let originX = '0%';
    let originY = '0%';

    // Flip up if not enough space below
    if (y + tipH > vh - GAP && triggerRect.top - tipH - GAP > GAP) {
      y = triggerRect.top - tipH - GAP;
      originY = '100%';
    }

    // Flip left if not enough space on the right
    if (x + tipW > vw - GAP) {
      x = triggerRect.right - tipW;
      originX = '100%';
      if (x < GAP) x = GAP;
    }

    // Clamp to viewport
    if (x < GAP) { x = GAP; originX = '0%'; }
    if (y < GAP) { y = GAP; originY = '0%'; }

    setPos({ x, y, originX, originY });
  }, [show]);

  const handleEnter = useCallback(() => {
    // Set initial position near trigger for first render measurement
    if (wrapRef.current) {
      const rect = wrapRef.current.getBoundingClientRect();
      setPos({ x: rect.left, y: rect.bottom + GAP, originX: '0%', originY: '0%' });
    }
    setShow(true);
  }, []);

  return (
    <span
      ref={wrapRef}
      className="hy-tooltip-wrap"
      onMouseEnter={handleEnter}
      onMouseLeave={() => setShow(false)}
    >
      {children}
      {createPortal(
        <AnimatePresence>
          {show && content && (
            <motion.div
              ref={tipRef}
              className="hy-tooltip"
              style={{
                left: pos?.x ?? 0,
                top: pos?.y ?? 0,
                transformOrigin: `${pos?.originX ?? '0%'} ${pos?.originY ?? '0%'}`,
              }}
              initial={{ opacity: 0, scale: 0.4 }}
              animate={{ opacity: 1, scale: 1 }}
              exit={{ opacity: 0, scale: 0.4 }}
              transition={{
                default: { duration: 0.25, ease: [0.16, 1, 0.3, 1] },
                opacity: { duration: 0.15 },
              }}
            >
              {content}
            </motion.div>
          )}
        </AnimatePresence>,
        document.body
      )}
    </span>
  );
}
