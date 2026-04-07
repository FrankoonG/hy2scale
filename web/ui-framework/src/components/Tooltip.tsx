import { useState, useRef, type ReactNode } from 'react';
import { motion, AnimatePresence } from 'framer-motion';

export interface TooltipProps {
  content: ReactNode;
  children: ReactNode;
}

export function Tooltip({ content, children }: TooltipProps) {
  const [show, setShow] = useState(false);
  const posRef = useRef({ x: 0, y: 0 });

  const handleEnter = (e: React.MouseEvent) => {
    posRef.current = { x: e.clientX, y: e.clientY };
    setShow(true);
  };

  return (
    <span
      className="hy-tooltip-wrap"
      onMouseEnter={handleEnter}
      onMouseLeave={() => setShow(false)}
    >
      {children}
      <AnimatePresence>
        {show && content && (
          <motion.div
            className="hy-tooltip"
            style={{ left: posRef.current.x, top: posRef.current.y }}
            initial={{ opacity: 0, y: 4 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: 4 }}
            transition={{ duration: 0.15, ease: 'easeOut' }}
          >
            {content}
          </motion.div>
        )}
      </AnimatePresence>
    </span>
  );
}
