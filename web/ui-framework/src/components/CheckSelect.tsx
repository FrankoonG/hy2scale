import { useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import clsx from 'clsx';
import { motion, AnimatePresence } from 'framer-motion';
import { useDropdownPosition } from '../hooks/useDropdownPosition';
import { useDismiss } from '../hooks/useDismiss';
import { useUiLabels } from './UiLabels';
import { IconChevronDown } from '../icons';

export interface CheckSelectOption {
  value: string;
  label: string;
}

export interface CheckSelectProps {
  options: CheckSelectOption[];
  /** The ticked values. */
  value: string[];
  onChange: (value: string[]) => void;
  /** What the box shows while every option is ticked. */
  label: string;
  className?: string;
  testId?: string;
  /** Up to this many ticked options are named in the box (default 2). */
  maxNames?: number;
}

const MAX_HEIGHT = 300;

/**
 * A select-like box whose list ticks options on and off (it stays open
 * while ticking). Shows `label` while all are ticked, the ticked names when
 * up to `maxNames` are, else the label with a count.
 */
export function CheckSelect({ options, value, onChange, label, className, testId, maxNames = 2 }: CheckSelectProps) {
  const L = useUiLabels();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const btnRef = useRef<HTMLButtonElement>(null);
  const dropRef = useRef<HTMLDivElement>(null);
  const pos = useDropdownPosition(open, btnRef, MAX_HEIGHT);
  useDismiss([ref, dropRef], () => setOpen(false), { enabled: open });

  const on = new Set(value);
  const ticked = options.filter((o) => on.has(o.value));
  const all = ticked.length === options.length;
  const text = all ? label
    : ticked.length > 0 && ticked.length <= maxNames ? ticked.map((o) => o.label).join(', ')
      : `${label} (${ticked.length}/${options.length})`;
  const toggle = (v: string) => onChange(on.has(v) ? value.filter((x) => x !== v) : [...value, v]);

  const dropdownStyle = pos ? {
    position: 'fixed' as const,
    left: pos.left,
    minWidth: pos.width,
    ...(pos.flip ? { bottom: window.innerHeight - pos.top, top: 'auto' as const } : { top: pos.top }),
  } : undefined;

  return (
    <div ref={ref} className={clsx('hy-select-wrap hy-check-select', !all && 'partial', className)} data-testid={testId}>
      <button ref={btnRef} type="button" className={clsx('hy-select', open && 'open')} onClick={() => setOpen(!open)}>
        <span className={clsx('hy-select-label', all && 'placeholder')}>{text}</span>
        <IconChevronDown className="hy-select-arrow" size={14} />
      </button>
      {createPortal(
        <AnimatePresence>
          {open && pos && (
            <motion.div
              ref={dropRef}
              className="hy-dropdown hy-select-dropdown hy-check-dropdown"
              style={{ ...dropdownStyle, transformOrigin: pos.flip ? 'bottom' : 'top' }}
              initial={{ opacity: 0, scaleY: 0.6 }}
              animate={{ opacity: 1, scaleY: 1 }}
              exit={{ opacity: 0, scaleY: 0.6 }}
              transition={{ duration: 0.25, ease: [0.16, 1, 0.3, 1] }}
            >
              {!all && (
                <button type="button" className="hy-dropdown-item hy-check-reset" onClick={() => onChange(options.map((o) => o.value))}>
                  {L.reset}
                </button>
              )}
              {options.map((o) => (
                <label key={o.value} className="hy-dropdown-item hy-check-item">
                  <input type="checkbox" checked={on.has(o.value)} onChange={() => toggle(o.value)} data-value={o.value} />
                  <span>{o.label}</span>
                </label>
              ))}
            </motion.div>
          )}
        </AnimatePresence>,
        document.body,
      )}
    </div>
  );
}
