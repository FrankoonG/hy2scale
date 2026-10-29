import { useState, useRef, useCallback } from 'react';
import { createPortal } from 'react-dom';
import clsx from 'clsx';
import { motion, AnimatePresence } from 'framer-motion';
import { useDropdownPosition } from '../hooks/useDropdownPosition';
import { useDismiss } from '../hooks/useDismiss';
import { IconChevronDown } from '../icons';

export interface SelectOption {
  value: string;
  label: string;
  disabled?: boolean;
}

export interface SelectProps {
  options: SelectOption[];
  value?: string;
  onChange?: (e: { target: { value: string } }) => void;
  placeholder?: string;
  disabled?: boolean;
  className?: string;
  style?: React.CSSProperties;
}

const MAX_HEIGHT = 240;

export function Select({ options, value, onChange, placeholder, disabled, className, style }: SelectProps) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const btnRef = useRef<HTMLButtonElement>(null);
  const dropRef = useRef<HTMLDivElement>(null);
  const pos = useDropdownPosition(open, btnRef, MAX_HEIGHT);

  // Close on a press outside the trigger and the portalled list, or Escape
  useDismiss([ref, dropRef], () => setOpen(false), { enabled: open });

  const selected = options.find((o) => o.value === value);
  const label = selected?.label || placeholder || '';

  const handleSelect = useCallback((val: string) => {
    setOpen(false);
    if (onChange) onChange({ target: { value: val } });
  }, [onChange]);

  const dropdownStyle = pos ? {
    position: 'fixed' as const,
    left: pos.left,
    width: pos.width,
    ...(pos.flip
      ? { bottom: window.innerHeight - pos.top, top: 'auto' as const }
      : { top: pos.top }),
  } : undefined;

  return (
    <div ref={ref} className={clsx('hy-select-wrap', className)} style={style}>
      <button
        ref={btnRef}
        type="button"
        className={clsx('hy-select', open && 'open')}
        onClick={() => !disabled && setOpen(!open)}
        disabled={disabled}
      >
        <span className={clsx('hy-select-label', !selected && 'placeholder')}>{label}</span>
        <IconChevronDown className="hy-select-arrow" size={14} />
      </button>
      {createPortal(
        <AnimatePresence>
          {open && pos && (
            <motion.div
              ref={dropRef}
              className="hy-dropdown hy-select-dropdown"
              style={{ ...dropdownStyle, transformOrigin: pos.flip ? 'bottom' : 'top' }}
              initial={{ opacity: 0, scaleY: 0.6 }}
              animate={{ opacity: 1, scaleY: 1 }}
              exit={{ opacity: 0, scaleY: 0.6 }}
              transition={{ duration: 0.25, ease: [0.16, 1, 0.3, 1] }}
            >
              {options.map((opt) => (
                <button
                  key={opt.value}
                  type="button"
                  className={clsx('hy-dropdown-item', opt.value === value && 'active')}
                  disabled={opt.disabled}
                  onClick={() => handleSelect(opt.value)}
                >
                  {opt.label}
                </button>
              ))}
            </motion.div>
          )}
        </AnimatePresence>,
        document.body,
      )}
    </div>
  );
}
