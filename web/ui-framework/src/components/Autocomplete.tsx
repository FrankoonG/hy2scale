import { useState, useRef, useCallback, useEffect, type KeyboardEvent } from 'react';
import { createPortal } from 'react-dom';
import clsx from 'clsx';
import { motion, AnimatePresence } from 'framer-motion';
import { useDropdownPosition } from '../hooks/useDropdownPosition';
import { useDismiss } from '../hooks/useDismiss';
import { useUiLabels } from './UiLabels';
import { IconX } from '../icons';

export interface AutocompleteProps {
  options: string[];
  value: string;
  onChange: (val: string) => void;
  placeholder?: string;
  className?: string;
  disabled?: boolean;
}

const MAX_HEIGHT = 180;

export function Autocomplete({
  options, value, onChange, placeholder, className, disabled,
}: AutocompleteProps) {
  const L = useUiLabels();
  const [open, setOpen] = useState(false);
  const [highlighted, setHighlighted] = useState(-1);
  const ref = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const pos = useDropdownPosition(open, ref, MAX_HEIGHT);

  // Close on a press outside the input and the portalled list (Escape is
  // handled by the input's key handler)
  useDismiss([ref, listRef], () => setOpen(false), { enabled: open, escape: false });

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
            className="hy-icon-btn tone-danger hy-autocomplete-clear"
            onClick={() => { onChange(''); inputRef.current?.focus(); }}
            tabIndex={-1}
            title={L.clear}
            aria-label={L.clear}
          >
            <IconX size={14} />
          </button>
        )}
      </div>
      {createPortal(
        <AnimatePresence>
          {showList && pos && (
            <motion.div
              ref={listRef}
              className="hy-autocomplete-list"
              style={{ ...dropdownStyle, transformOrigin: pos.flip ? 'bottom' : 'top' }}
              initial={{ opacity: 0, scaleY: 0.6 }}
              animate={{ opacity: 1, scaleY: 1 }}
              exit={{ opacity: 0, scaleY: 0.6 }}
              transition={{ duration: 0.25, ease: [0.16, 1, 0.3, 1] }}
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
