import { type ReactNode, useRef } from 'react';
import { motion, AnimatePresence } from 'framer-motion';

export interface TabPanelProps {
  activeKey: string;
  children: ReactNode;
  /** Ordered list of tab keys — used to determine slide direction */
  keys?: string[];
}

export function TabPanel({ activeKey, children, keys }: TabPanelProps) {
  const prevKey = useRef(activeKey);
  const dirRef = useRef(1);

  if (keys && keys.length > 0 && prevKey.current !== activeKey) {
    const prevIdx = keys.indexOf(prevKey.current);
    const curIdx = keys.indexOf(activeKey);
    if (prevIdx >= 0 && curIdx >= 0) {
      dirRef.current = curIdx > prevIdx ? 1 : -1;
    }
  }

  if (prevKey.current !== activeKey) {
    prevKey.current = activeKey;
  }

  const dir = dirRef.current;

  return (
    <div style={{ position: 'relative' }}>
      <AnimatePresence initial={false} custom={dir}>
        <motion.div
          key={activeKey}
          custom={dir}
          initial="enter"
          animate="center"
          exit="exit"
          variants={{
            enter: (d: number) => ({ opacity: 0, x: 20 * d }),
            center: { opacity: 1, x: 0 },
            exit: (d: number) => ({ opacity: 0, x: -20 * d, position: 'absolute' as const, top: 0, left: 0, right: 0 }),
          }}
          transition={{ duration: 0.3, ease: 'easeInOut' }}
        >
          {children}
        </motion.div>
      </AnimatePresence>
    </div>
  );
}
