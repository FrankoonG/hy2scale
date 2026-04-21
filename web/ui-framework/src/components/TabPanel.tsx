import { type ReactNode, type CSSProperties, useRef } from 'react';
import { motion, AnimatePresence } from 'framer-motion';

export interface TabPanelProps {
  activeKey: string;
  children: ReactNode;
  /** Ordered list of tab keys — used to determine slide direction */
  keys?: string[];
  /**
   * When true, the panel (and its active child) flexes to fill remaining
   * height inside a `.hy-page` container. Use together with `Card fill={…}`
   * so the tabbed content scrolls internally.
   */
  fill?: boolean;
}

export function TabPanel({ activeKey, children, keys, fill }: TabPanelProps) {
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

  const rootStyle: CSSProperties = fill
    ? { position: 'relative', flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' }
    : { position: 'relative' };
  const motionStyle: CSSProperties | undefined = fill
    ? { flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column', gap: 20 }
    : undefined;

  return (
    <div style={rootStyle}>
      <AnimatePresence initial={false} custom={dir}>
        <motion.div
          key={activeKey}
          custom={dir}
          initial="enter"
          animate="center"
          exit="exit"
          style={motionStyle}
          variants={{
            enter: (d: number) => ({ opacity: 0, x: 20 * d }),
            center: { opacity: 1, x: 0, transition: { duration: 0.35, ease: 'easeOut' } },
            exit: (d: number) => ({ opacity: 0, x: -20 * d, position: 'absolute' as const, top: 0, left: 0, right: 0, transition: { duration: 0.2 } }),
          }}
        >
          {children}
        </motion.div>
      </AnimatePresence>
    </div>
  );
}
