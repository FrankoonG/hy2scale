import { type ReactNode } from 'react';
import { motion, AnimatePresence } from 'framer-motion';

export interface TabPanelProps {
  activeKey: string;
  children: ReactNode;
}

export function TabPanel({ activeKey, children }: TabPanelProps) {
  return (
    <AnimatePresence mode="wait">
      <motion.div
        key={activeKey}
        initial={{ opacity: 0, x: 20 }}
        animate={{ opacity: 1, x: 0 }}
        exit={{ opacity: 0, x: -20 }}
        transition={{ type: 'spring', stiffness: 400, damping: 30 }}
      >
        {children}
      </motion.div>
    </AnimatePresence>
  );
}
