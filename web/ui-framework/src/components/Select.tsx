import { useState, useRef, useCallback, useEffect } from 'react';
import { createPortal } from 'react-dom';
import clsx from 'clsx';
import { motion, AnimatePresence } from 'framer-motion';
import { useDropdownPosition } from '../hooks/useDropdownPosition';

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

  // Close when clicking outside both the trigger and the portal dropdown
  useEffect(() => {
    if (!open) return;
    const handler = (e: MouseEvent) => {
      const target = e.target as Node;
      if (ref.current?.contains(target)) return;
      if (dropRef.current?.contains(target)) return;
      setOpen(false);
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [open]);

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
        <svg className="hy-select-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <polyline points="6 9 12 15 18 9" />
        </svg>
      </button>
      {createPortal(
        <AnimatePresence>
          {open && pos && (
            <motion.div
              ref={dropRef}
              className="hy-dropdown hy-select-dropdown"
              style={dropdownStyle}
              initial={{ opacity: 0, y: pos.flip ? 8 : -8 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0, y: pos.flip ? 8 : -8 }}
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
        </AnimatePresence>,
        document.body,
      )}
    </div>
  );
}
