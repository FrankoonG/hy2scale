import { useCallback, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import clsx from 'clsx';
import { motion, AnimatePresence } from 'framer-motion';
import { useDropdownPosition } from '../hooks/useDropdownPosition';
import { useDismiss } from '../hooks/useDismiss';
import { useUiLabels } from './UiLabels';
import { IconChevronDown, IconSearch, IconX } from '../icons';

export interface MultiSelectOption {
  value: string;
  label: string;
  /** Secondary text after the label (an address, a name). */
  detail?: string;
  disabled?: boolean;
}

export interface MultiSelectProps {
  options: MultiSelectOption[];
  value: string[];
  onChange: (value: string[]) => void;
  /** Shown when nothing is selected. */
  placeholder?: string;
  disabled?: boolean;
  className?: string;
  /** Search box at the top of the list (default: on). */
  searchable?: boolean;
  testId?: string;
}

const MAX_HEIGHT = 300;

/**
 * Multiple choice in a select-like box: the chosen values show as small
 * tags (an ellipsis tag when they do not fit), the list has a search box
 * pinned at its top and a checkbox per option. Values not among the
 * options stay selected and show as tags.
 */
export function MultiSelect({
  options, value, onChange, placeholder, disabled, className, searchable = true, testId,
}: MultiSelectProps) {
  const L = useUiLabels();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [fit, setFit] = useState(value.length);
  const rootRef = useRef<HTMLDivElement>(null);
  const boxRef = useRef<HTMLDivElement>(null);
  const tagsRef = useRef<HTMLDivElement>(null);
  const dropRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const pos = useDropdownPosition(open, boxRef, MAX_HEIGHT);

  useDismiss([rootRef, dropRef], () => setOpen(false), { enabled: open });

  const labelOf = (v: string) => options.find((o) => o.value === v)?.label ?? v;
  const selected = new Set(value);

  const toggle = useCallback((v: string) => {
    onChange(selected.has(v) ? value.filter((x) => x !== v) : [...value, v]);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value, onChange]);

  // How many tags fit: measured on the full row, the rest folds into "…".
  useLayoutEffect(() => {
    const row = tagsRef.current;
    if (!row) return;
    const measure = () => {
      const tags = Array.from(row.querySelectorAll<HTMLElement>('[data-tag]'));
      const limit = row.clientWidth - (tags.length > 1 ? 34 : 0); // room for the "…" tag
      let n = 0;
      for (const el of tags) {
        if (el.offsetLeft + el.offsetWidth > limit && n > 0) break;
        n++;
      }
      setFit(n);
    };
    setFit(value.length);
    const raf = requestAnimationFrame(measure);
    const ro = new ResizeObserver(measure);
    ro.observe(row);
    return () => { cancelAnimationFrame(raf); ro.disconnect(); };
  }, [value.join('\u0000'), options.length]);

  const openList = () => {
    if (disabled) return;
    setQuery('');
    setOpen((o) => !o);
  };

  const q = query.trim().toLowerCase();
  const shown = q ? options.filter((o) => `${o.label} ${o.value} ${o.detail ?? ''}`.toLowerCase().includes(q)) : options;
  const hidden = value.slice(fit);

  const dropStyle = pos ? {
    position: 'fixed' as const,
    left: pos.left,
    width: pos.width,
    ...(pos.flip ? { bottom: window.innerHeight - pos.top, top: 'auto' as const } : { top: pos.top }),
  } : undefined;

  return (
    <div ref={rootRef} className={clsx('hy-multi-wrap', className)} data-testid={testId}>
      <div
        ref={boxRef}
        role="combobox"
        aria-expanded={open}
        tabIndex={disabled ? -1 : 0}
        className={clsx('hy-select hy-multi', open && 'open', disabled && 'disabled')}
        onClick={openList}
        onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ' || e.key === 'ArrowDown') { e.preventDefault(); if (!open) openList(); } }}
      >
        <div ref={tagsRef} className="hy-multi-tags">
          {value.length === 0 && <span className="hy-select-label placeholder">{placeholder}</span>}
          {value.map((v, i) => (
            <span key={v} data-tag className="hy-multi-tag" style={i >= fit ? { visibility: 'hidden', position: 'absolute' } : undefined}
              title={labelOf(v)}>
              <span className="hy-multi-tag-text">{labelOf(v).trim()}</span>
              {!disabled && (
                <button type="button" className="hy-multi-tag-x" aria-label={L.clear} tabIndex={-1}
                  onClick={(e) => { e.stopPropagation(); toggle(v); }}>
                  <IconX size={10} strokeWidth={2.5} />
                </button>
              )}
            </span>
          ))}
          {hidden.length > 0 && (
            <span className="hy-multi-tag hy-multi-more" title={hidden.map(labelOf).join(', ')} data-testid={testId ? `${testId}-more` : undefined}>
              …
            </span>
          )}
        </div>
        <IconChevronDown className="hy-select-arrow" size={14} />
      </div>
      {createPortal(
        <AnimatePresence>
          {open && pos && (
            <motion.div
              ref={dropRef}
              className="hy-dropdown hy-multi-dropdown"
              style={{ ...dropStyle, maxHeight: MAX_HEIGHT, transformOrigin: pos.flip ? 'bottom' : 'top' }}
              initial={{ opacity: 0, scaleY: 0.6 }}
              animate={{ opacity: 1, scaleY: 1 }}
              exit={{ opacity: 0, scaleY: 0.6 }}
              transition={{ duration: 0.25, ease: [0.16, 1, 0.3, 1] }}
              onAnimationComplete={() => searchRef.current?.focus()}
            >
              {searchable && (
                <div className="hy-multi-search">
                  <IconSearch size={14} />
                  <input ref={searchRef} value={query} onChange={(e) => setQuery(e.target.value)} placeholder={L.search}
                    data-testid={testId ? `${testId}-search` : undefined} />
                </div>
              )}
              <div className="hy-multi-list" role="listbox" aria-multiselectable>
                {shown.length === 0 && <div className="hy-multi-empty">{L.noResults}</div>}
                {shown.map((o) => (
                  <label key={o.value} className={clsx('hy-multi-item', selected.has(o.value) && 'on', o.disabled && 'disabled')}>
                    <input type="checkbox" checked={selected.has(o.value)} disabled={o.disabled} onChange={() => toggle(o.value)} />
                    <span className="hy-multi-item-label">{o.label}</span>
                    {o.detail && <span className="hy-multi-item-detail">{o.detail}</span>}
                  </label>
                ))}
              </div>
            </motion.div>
          )}
        </AnimatePresence>,
        document.body,
      )}
    </div>
  );
}
