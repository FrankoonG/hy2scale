import { useState, useRef, type ReactNode } from 'react';
import clsx from 'clsx';
import { motion, AnimatePresence } from 'framer-motion';
import { useClickOutside } from '../hooks/useClickOutside';

export interface DropdownItem {
  key: string;
  label: ReactNode;
  danger?: boolean;
  onClick: () => void;
}

export interface DropdownMenuProps {
  trigger: ReactNode;
  items: DropdownItem[];
}

export function DropdownMenu({ trigger, items }: DropdownMenuProps) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useClickOutside(ref, () => setOpen(false));

  return (
    <div ref={ref} style={{ position: 'relative', display: 'inline-flex' }}>
      <span onClick={() => setOpen(!open)}>{trigger}</span>
      <AnimatePresence>
        {open && (
          <motion.div
            className="hy-dropdown"
            style={{ top: '100%', right: 0 }}
            initial={{ opacity: 0, y: -8 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -8 }}
            transition={{ duration: 0.15, ease: 'easeOut' }}
          >
            {items.map((item) => (
              <button
                key={item.key}
                className={clsx('hy-dropdown-item', item.danger && 'danger')}
                onClick={() => { item.onClick(); setOpen(false); }}
              >
                {item.label}
              </button>
            ))}
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  );
}
