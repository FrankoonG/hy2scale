import { type ReactNode, type CSSProperties, useRef } from 'react';
import { motion, AnimatePresence } from 'framer-motion';

export interface TabPanelProps {
  activeKey: string;
  children: ReactNode;
  /** Ordered list of tab keys — used to determine slide direction */
  keys?: string[];
  /** When true, the active child's panels stack with the page gap. */
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

  // fill: the active child's panels stack with the page gap (panels take
  // their content's height; nothing scrolls inside them).
  const rootStyle: CSSProperties = { position: 'relative' };
  const motionStyle: CSSProperties | undefined = fill
    ? { display: 'flex', flexDirection: 'column', gap: 20 }
    : undefined;

  return (
    <div style={rootStyle}>
      {/* popLayout takes the leaving panel out of the flow itself; animating
          top/left from `auto` in the exit variant never completed, so leaving
          panels piled up invisible in the DOM. */}
      <AnimatePresence initial={false} custom={dir} mode="popLayout">
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
            exit: (d: number) => ({ opacity: 0, x: -20 * d, transition: { duration: 0.2 } }),
          }}
        >
          {children}
        </motion.div>
      </AnimatePresence>
    </div>
  );
}
