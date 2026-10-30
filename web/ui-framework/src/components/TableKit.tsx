import { useEffect, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import clsx from 'clsx';
import { motion, AnimatePresence } from 'framer-motion';
import { useUiLabels } from './UiLabels';
import { useDismiss } from '../hooks/useDismiss';
import { popMotion } from './Menu';
import { Select } from './Select';
import { readStored, writeStored } from '../hooks/useStoredState';
import { IconChevronLeft, IconChevronRight, IconColumns3, IconGripVertical } from '../icons';

// Shared table behaviour for Table and TreeTable: user-chosen columns
// (visibility + order, remembered per table) and pagination with a pager
// at the bottom of the panel.

export const PAGE_SIZES = [10, 20, 50, 100];
const DEFAULT_PAGE_SIZE = 20;

/** What every column of Table / TreeTable can declare for the column menu. */
export interface ColumnPrefsSpec {
  key: string;
  title: ReactNode;
  /** Name shown in the column menu when `title` is not plain text. */
  menuTitle?: string;
  /** false = always shown and fixed in place (e.g. name, actions). */
  hideable?: boolean;
  /** Hidden until the user turns it on. */
  defaultHidden?: boolean;
  /** Makes the column sortable by clicking its title. */
  sortValue?: (row: any) => string | number | null | undefined;
}

export type SortDir = 'asc' | 'desc';
export interface SortState { key: string; dir: SortDir }

interface StoredPrefs {
  order?: string[];
  hidden?: string[];
  size?: number;
  sort?: SortState | null;
}

function load(id: string): StoredPrefs {
  try {
    return JSON.parse(readStored('hy-table:' + id) || '{}') || {};
  } catch {
    return {};
  }
}

function save(id: string, p: StoredPrefs) {
  writeStored('hy-table:' + id, JSON.stringify(p));
}

/** A stable per-table id: the page path plus the column keys. */
export function tableId(explicit: string | undefined, keys: string[]): string {
  if (explicit) return explicit;
  const path = typeof location !== 'undefined' ? location.pathname.replace(/\/+$/, '') : '';
  return path + '|' + keys.join(',');
}

const isHideable = (c: ColumnPrefsSpec, i: number, n: number) =>
  c.hideable ?? !(i === 0 || c.key === 'actions' || i === n - 1 && !c.title);

/** Column visibility and order, remembered per table. */
export function useColumnPrefs<C extends ColumnPrefsSpec>(id: string, columns: C[]) {
  const [prefs, setPrefs] = useState<StoredPrefs>(() => load(id));
  useEffect(() => { setPrefs(load(id)); }, [id]);
  const update = (p: StoredPrefs) => { setPrefs(p); save(id, p); };

  // Recomputed on every render: columns carry the current titles and
  // render functions.
  const byKey = new Map(columns.map((c) => [c.key, c]));
  const fixedFirst = columns.filter((c, i) => !isHideable(c, i, columns.length) && i === 0);
  const fixedLast = columns.filter((c, i) => !isHideable(c, i, columns.length) && i !== 0);
  const movable = columns.filter((c, i) => isHideable(c, i, columns.length));
  const order = (prefs.order || []).filter((k) => byKey.has(k) && movable.some((m) => m.key === k));
  for (const m of movable) if (!order.includes(m.key)) order.push(m.key);
  const ordered = [...fixedFirst, ...order.map((k) => byKey.get(k)!), ...fixedLast];

  const hidden = new Set(prefs.hidden ?? columns.filter((c) => c.defaultHidden).map((c) => c.key));
  columns.forEach((c, i) => { if (!isHideable(c, i, columns.length)) hidden.delete(c.key); });

  const visible = ordered.filter((c) => !hidden.has(c.key));
  const menuItems = ordered
    .map((c) => ({ col: c, idx: columns.indexOf(c) }))
    .filter(({ col, idx }) => isHideable(col, idx, columns.length))
    .map(({ col }) => col);

  return {
    visible,
    menuItems,
    hidden,
    toggle: (k: string) => {
      const h = new Set(hidden);
      if (h.has(k)) h.delete(k); else h.add(k);
      update({ ...prefs, hidden: [...h] });
    },
    move: (from: string, to: string) => {
      const order = menuItems.map((c) => c.key);
      const a = order.indexOf(from), b = order.indexOf(to);
      if (a < 0 || b < 0 || a === b) return;
      order.splice(b, 0, ...order.splice(a, 1));
      update({ ...prefs, order });
    },
    reset: () => update({ size: prefs.size, sort: prefs.sort }),
    sort: prefs.sort && byKey.get(prefs.sort.key)?.sortValue ? prefs.sort : null,
    /** asc → desc → unsorted */
    cycleSort: (key: string) => {
      const cur = prefs.sort?.key === key ? prefs.sort.dir : null;
      const next: SortState | null = cur === null ? { key, dir: 'asc' } : cur === 'asc' ? { key, dir: 'desc' } : null;
      update({ ...prefs, sort: next });
    },
    customized: !!(prefs.order || prefs.hidden),
    pageSize: prefs.size && PAGE_SIZES.includes(prefs.size) ? prefs.size : DEFAULT_PAGE_SIZE,
    setPageSize: (size: number) => update({ ...prefs, size }),
  };
}

/** Pagination over `total` rows. */
export function usePager(total: number, size: number, resetKey: string) {
  const [page, setPage] = useState(1);
  const pages = Math.max(1, Math.ceil(total / size));
  useEffect(() => { setPage(1); }, [size, resetKey]);
  useEffect(() => { if (page > pages) setPage(pages); }, [page, pages]);
  const cur = Math.min(page, pages);
  return { page: cur, pages, setPage, start: (cur - 1) * size, end: cur * size };
}

/** The column button at the right end of a table's header row. */
export function ColumnMenu<C extends ColumnPrefsSpec>({ prefs }: { prefs: ReturnType<typeof useColumnPrefs<C>> }) {
  const L = useUiLabels();
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ top: number; right: number } | null>(null);
  const [drag, setDrag] = useState<string | null>(null);
  const btn = useRef<HTMLButtonElement>(null);
  const pop = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const place = () => {
      const r = btn.current?.getBoundingClientRect();
      if (r) setPos({ top: r.bottom + 4, right: window.innerWidth - r.right });
    };
    place();
    window.addEventListener('scroll', place, true);
    window.addEventListener('resize', place);
    return () => {
      window.removeEventListener('scroll', place, true);
      window.removeEventListener('resize', place);
    };
  }, [open]);
  useDismiss([pop, btn], () => setOpen(false), { enabled: open });

  if (prefs.menuItems.length === 0) return null;
  const label = (c: C) => c.menuTitle ?? (typeof c.title === 'string' ? c.title : c.key);
  return (
    <>
      <button
        ref={btn}
        type="button"
        className={clsx('hy-col-btn', (open || prefs.customized) && 'active')}
        title={L.columns}
        aria-label={L.columns}
        onClick={() => setOpen((o) => !o)}
      >
        <IconColumns3 size={15} />
      </button>
      {createPortal(
        <AnimatePresence>
          {open && pos && (
            <motion.div ref={pop} className="hy-col-menu" style={{ top: pos.top, right: pos.right, transformOrigin: 'top right' }} role="menu" {...popMotion}>
              <div className="hy-col-menu-head">
                <span>{L.columns}</span>
                {prefs.customized && (
                  <button type="button" className="hy-col-menu-reset" onClick={prefs.reset}>{L.reset}</button>
                )}
              </div>
              {prefs.menuItems.map((c) => (
                <label
                  key={c.key}
                  className={clsx('hy-col-menu-item', drag === c.key && 'dragging')}
                  draggable
                  onDragStart={(e) => { setDrag(c.key); e.dataTransfer.effectAllowed = 'move'; }}
                  onDragEnter={() => { if (drag && drag !== c.key) prefs.move(drag, c.key); }}
                  onDragOver={(e) => e.preventDefault()}
                  onDragEnd={() => setDrag(null)}
                >
                  <span className="hy-col-menu-grip" aria-hidden="true"><IconGripVertical size={12} /></span>
                  <input type="checkbox" checked={!prefs.hidden.has(c.key)} onChange={() => prefs.toggle(c.key)} />
                  <span className="hy-col-menu-label">{label(c)}</span>
                </label>
              ))}
            </motion.div>
          )}
        </AnimatePresence>,
        document.body,
      )}
    </>
  );
}

/** The pager at the bottom of a panel. Hidden when everything fits on the
 *  smallest page. */
export function Pager({ total, size, onSize, pager }: {
  total: number;
  size: number;
  onSize: (n: number) => void;
  pager: ReturnType<typeof usePager>;
}) {
  const L = useUiLabels();
  if (total <= PAGE_SIZES[0]) return null;
  const { page, pages, setPage } = pager;
  const nums: (number | '…')[] = [];
  for (let p = 1; p <= pages; p++) {
    if (p === 1 || p === pages || Math.abs(p - page) <= 1) nums.push(p);
    else if (nums[nums.length - 1] !== '…') nums.push('…');
  }
  return (
    <div className="hy-pager">
      <span className="hy-pager-total">{L.total(total)}</span>
      <div className="hy-pager-size">
        <span>{L.perPage}</span>
        <Select className="hy-pager-select" value={String(size)} onChange={(e) => onSize(Number(e.target.value))}
          options={PAGE_SIZES.map((n) => ({ value: String(n), label: String(n) }))} />
      </div>
      <div className="hy-pager-pages">
        <button type="button" className="hy-pager-btn" disabled={page <= 1} aria-label={L.prev} onClick={() => setPage(page - 1)}><IconChevronLeft size={14} /></button>
        {nums.map((n, i) => n === '…'
          ? <span key={'e' + i} className="hy-pager-gap">…</span>
          : <button key={n} type="button" className={clsx('hy-pager-btn', n === page && 'current')} onClick={() => setPage(n)}>{n}</button>)}
        <button type="button" className="hy-pager-btn" disabled={page >= pages} aria-label={L.next} onClick={() => setPage(page + 1)}><IconChevronRight size={14} /></button>
      </div>
    </div>
  );
}


const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

/** Sorts rows by a column's sortValue; empty values go last either way. */
export function sortBy<R>(rows: R[], get: (r: R) => string | number | null | undefined, dir: SortDir): R[] {
  const sign = dir === 'asc' ? 1 : -1;
  return rows
    .map((r, i) => ({ r, i, v: get(r) }))
    .sort((a, b) => {
      const ea = a.v === null || a.v === undefined || a.v === '';
      const eb = b.v === null || b.v === undefined || b.v === '';
      if (ea || eb) return ea === eb ? a.i - b.i : ea ? 1 : -1;
      const c = typeof a.v === 'number' && typeof b.v === 'number' ? a.v - b.v : collator.compare(String(a.v), String(b.v));
      return c !== 0 ? sign * c : a.i - b.i;
    })
    .map((x) => x.r);
}

/** A column title, clickable when the column sorts. */
export function SortTitle({ title, active, dir, onClick }: { title: ReactNode; active: boolean; dir?: SortDir; onClick?: () => void }) {
  if (!onClick) return <>{title}</>;
  return (
    <button type="button" className={clsx('hy-sort-btn', active && 'active')} onClick={onClick}>
      <span>{title}</span>
      <span className="hy-sort-ind" aria-hidden="true">{active ? (dir === 'asc' ? '▲' : '▼') : '▲▼'}</span>
    </button>
  );
}

/** A header / row checkbox that can show the indeterminate state. */
export function IndeterminateCheckbox({ checked, indeterminate, onChange }: { checked: boolean; indeterminate: boolean; onChange: () => void }) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => { if (ref.current) ref.current.indeterminate = indeterminate; }, [indeterminate]);
  return <input ref={ref} type="checkbox" checked={checked} onChange={onChange} />;
}

/** A click inside a native control of the row (the control handles it, not the row). */
export function isInteractiveDescendant(target: EventTarget | null, row: HTMLElement): boolean {
  for (let el = target as HTMLElement | null; el && el !== row; el = el.parentElement) {
    const tag = el.tagName;
    if (tag === 'BUTTON' || tag === 'A' || tag === 'INPUT' || tag === 'SELECT' ||
        tag === 'TEXTAREA' || tag === 'LABEL' || el.isContentEditable) {
      return true;
    }
  }
  return false;
}

/** The empty-list message (plain text or nodes, never HTML). */
export function EmptyRow({ children }: { children: ReactNode }) {
  return <div className="hy-empty">{children}</div>;
}
