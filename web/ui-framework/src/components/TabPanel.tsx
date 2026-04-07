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
  let direction = 1; // 1 = forward (left), -1 = backward (right)

  if (keys && keys.length > 0) {
    const prevIdx = keys.indexOf(prevKey.current);
    const curIdx = keys.indexOf(activeKey);
    if (prevIdx >= 0 && curIdx >= 0) {
      direction = curIdx > prevIdx ? 1 : -1;
    }
  }

  if (prevKey.current !== activeKey) {
    prevKey.current = activeKey;
  }

  const offset = 20 * direction;

  return (
    <AnimatePresence mode="wait" initial={false}>
      <motion.div
        key={activeKey}
        initial={{ opacity: 0, x: offset }}
        animate={{ opacity: 1, x: 0 }}
        exit={{ opacity: 0, x: -offset }}
        transition={{ duration: 0.1, ease: 'easeInOut' }}
      >
        {children}
      </motion.div>
    </AnimatePresence>
  );
}
