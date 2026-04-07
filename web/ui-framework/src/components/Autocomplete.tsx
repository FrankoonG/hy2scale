import { useState, useRef, useCallback, useEffect, type KeyboardEvent } from 'react';
import clsx from 'clsx';
import { useClickOutside } from '../hooks/useClickOutside';

export interface AutocompleteProps {
  options: string[];
  value: string;
  onChange: (val: string) => void;
  placeholder?: string;
  freeSolo?: boolean;
  className?: string;
  disabled?: boolean;
}

export function Autocomplete({
  options, value, onChange, placeholder, freeSolo = true, className, disabled,
}: AutocompleteProps) {
  const [open, setOpen] = useState(false);
  const [highlighted, setHighlighted] = useState(-1);
  const ref = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  useClickOutside(ref, () => setOpen(false));

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
      {open && filtered.length > 0 && (
        <div className="hy-autocomplete-list">
          {filtered.map((opt, i) => (
            <div
              key={opt}
              className={clsx('hy-autocomplete-item', i === highlighted && 'highlighted')}
              onMouseDown={(e) => { e.preventDefault(); select(opt); }}
            >
              {opt}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
