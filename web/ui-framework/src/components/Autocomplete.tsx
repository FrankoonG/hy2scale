import { useState, useRef, useCallback, useEffect, type KeyboardEvent } from 'react';
import { createPortal } from 'react-dom';
import clsx from 'clsx';
import { motion, AnimatePresence } from 'framer-motion';
import { useDropdownPosition } from '../hooks/useDropdownPosition';

export interface AutocompleteProps {
  options: string[];
  value: string;
  onChange: (val: string) => void;
  placeholder?: string;
  freeSolo?: boolean;
  className?: string;
  disabled?: boolean;
}

const MAX_HEIGHT = 180;

export function Autocomplete({
  options, value, onChange, placeholder, freeSolo = true, className, disabled,
}: AutocompleteProps) {
  const [open, setOpen] = useState(false);
  const [highlighted, setHighlighted] = useState(-1);
  const ref = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const pos = useDropdownPosition(open, ref, MAX_HEIGHT);

  // Close when clicking outside both the trigger and the portal list
  useEffect(() => {
    if (!open) return;
    const handler = (e: MouseEvent) => {
      const target = e.target as Node;
      if (ref.current?.contains(target)) return;
      if (listRef.current?.contains(target)) return;
      setOpen(false);
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [open]);

  const filtered = options.filter((o) =>
    o.toLowerCase().includes(value.toLowerCase())
  ).slice(0, 15);

  const select = useCallback((val: string) => {
    onChange(val);
    setOpen(false);
    setHighlighted(-1);
  }, [onChange]);

  const handleKey = (e: KeyboardEvent) => {
    if (!open && e.key === 'ArrowDown') {
      setOpen(true);
      return;
    }
    if (!open) return;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setHighlighted((h) => Math.min(h + 1, filtered.length - 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setHighlighted((h) => Math.max(h - 1, 0));
    } else if (e.key === 'Enter' && highlighted >= 0) {
      e.preventDefault();
      select(filtered[highlighted]);
    } else if (e.key === 'Escape') {
      setOpen(false);
    }
  };

  useEffect(() => { setHighlighted(-1); }, [value]);

  const showList = open && filtered.length > 0;

  const dropdownStyle = pos ? {
    position: 'fixed' as const,
    left: pos.left,
    width: pos.width,
    ...(pos.flip
      ? { bottom: window.innerHeight - pos.top, top: 'auto' as const }
      : { top: pos.top }),
  } : undefined;

  return (
    <div ref={ref} className={clsx('hy-autocomplete', className)}>
      <div className="hy-input-wrap">
        <input
          ref={inputRef}
          className="hy-input mono"
          value={value}
          onChange={(e) => { onChange(e.target.value); setOpen(true); }}
          onFocus={() => setOpen(true)}
          onKeyDown={handleKey}
          placeholder={placeholder}
          disabled={disabled}
          style={{ fontFamily: 'var(--mono)' }}
        />
        {value && (
          <button
            type="button"
            className="hy-icon-btn"
            style={{ position: 'absolute', right: 4 }}
            onClick={() => { onChange(''); inputRef.current?.focus(); }}
            tabIndex={-1}
          >
            ✕
          </button>
        )}
      </div>
      {createPortal(
        <AnimatePresence>
          {showList && pos && (
            <motion.div
              ref={listRef}
              className="hy-autocomplete-list"
              style={{ ...dropdownStyle, transformOrigin: pos.flip ? 'bottom left' : 'top left' }}
              initial={{ opacity: 0, scale: 0.4 }}
              animate={{ opacity: 1, scale: 1 }}
              exit={{ opacity: 0, scale: 0.4 }}
              transition={{
                default: { duration: 0.25, ease: [0.16, 1, 0.3, 1] },
                opacity: { duration: 0.15 },
              }}
            >
              {filtered.map((opt, i) => (
                <div
                  key={opt}
                  className={clsx('hy-autocomplete-item', i === highlighted && 'highlighted')}
                  onMouseDown={(e) => { e.preventDefault(); select(opt); }}
                >
                  {opt}
                </div>
              ))}
            </motion.div>
          )}
        </AnimatePresence>,
        document.body,
      )}
    </div>
  );
}
