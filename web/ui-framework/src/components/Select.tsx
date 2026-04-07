import { useState, useRef, useCallback, useEffect } from 'react';
import clsx from 'clsx';
import { motion, AnimatePresence } from 'framer-motion';
import { useClickOutside } from '../hooks/useClickOutside';

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

export function Select({ options, value, onChange, placeholder, disabled, className, style }: SelectProps) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useClickOutside(ref, () => setOpen(false));

  const selected = options.find((o) => o.value === value);
  const label = selected?.label || placeholder || '';

  const handleSelect = useCallback((val: string) => {
    setOpen(false);
    if (onChange) onChange({ target: { value: val } });
  }, [onChange]);

  // Close on Escape
  useEffect(() => {
    if (!open) return;
    const handler = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false); };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [open]);

  return (
    <div ref={ref} className={clsx('hy-select-wrap', className)} style={style}>
      <button
        type="button"
        className={clsx('hy-select', open && 'open')}
        onClick={() => !disabled && setOpen(!open)}
        disabled={disabled}
      >
        <span className={clsx('hy-select-label', !selected && 'placeholder')}>{label}</span>
        <svg className="hy-select-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <polyline points="6 9 12 15 18 9" />
        </svg>
      </button>
      <AnimatePresence>
        {open && (
          <motion.div
            className="hy-dropdown"
            style={{ top: '100%', left: 0, right: 0, marginTop: 4 }}
            initial={{ opacity: 0, y: -8 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -8 }}
            transition={{ duration: 0.15, ease: 'easeOut' }}
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
      </AnimatePresence>
    </div>
  );
}
