import { type ReactNode, useRef, useEffect } from 'react';
import * as React from 'react';
import { motion, AnimatePresence } from 'framer-motion';
import clsx from 'clsx';
import type { SelectionState } from './Table';
import { ColumnMenu, Pager, SortTitle, sortBy, type SortState, tableId, useColumnPrefs, usePager } from './TableKit';

// Mirror of Table's isInteractiveDescendant — kept inline to avoid making
// it part of the framework's public surface. Walks up from the click
// target to the row and bails on any ancestor that's a native control,
// so row-body click selection never preempts an Edit / checkbox click.
function isInteractiveDescendant(target: EventTarget | null, row: HTMLElement): boolean {
  for (let el = target as HTMLElement | null; el && el !== row; el = el.parentElement) {
    const tag = el.tagName;
    if (tag === 'BUTTON' || tag === 'A' || tag === 'INPUT' || tag === 'SELECT' ||
        tag === 'TEXTAREA' || tag === 'LABEL' || el.isContentEditable) {
      return true;
    }
  }
  return false;
}

export interface TreeColumn<T> {
  key: string;
  title: ReactNode;
  render: (row: T, meta: TreeRowMeta) => ReactNode;
  className?: string;
  width?: string;
  /** Column menu: name when `title` is not text; false = always shown. */
  menuTitle?: string;
  hideable?: boolean;
  defaultHidden?: boolean;
  /** Sort key of a row (siblings sort among themselves). */
  sortValue?: (row: T) => string | number | null | undefined;
}

export interface TreeRowMeta {
  depth: number;
  isLast: boolean;
  guides: boolean[];
  isExpanded?: boolean;
  nodeKey: string;
}

export interface TreeNode<T> {
  data: T;
  key: string;
  children?: TreeNode<T>[];
  expanded?: boolean;
  className?: string;
  /** Stays first among its siblings whatever the sort (e.g. this node). */
  pinned?: boolean;
}

export interface TreeTableProps<T> {
  columns: TreeColumn<T>[];
  nodes: TreeNode<T>[];
  emptyText?: ReactNode;
  selection?: SelectionState;
  isSelectable?: (node: TreeNode<T>, meta: TreeRowMeta) => boolean;
  /** Optional full-width panel rendered under a row; return null for none. */
  renderDetail?: (node: TreeNode<T>, meta: TreeRowMeta) => ReactNode;
  /** Remembers column choices and page size (default: page path + columns). */
  id?: string;
  /** false = all rows on one page. Pages count top-level rows; children
   *  stay with their parent. */
  paginate?: boolean;
  /** The list's own action for a click on a row (outside its controls). */
  onRowClick?: (node: TreeNode<T>, meta: TreeRowMeta) => void;
}

function sortTree<T>(nodes: TreeNode<T>[], get: (r: T) => string | number | null | undefined, dir: SortState['dir']): TreeNode<T>[] {
  // Level by level: siblings sort among themselves; pinned rows stay first.
  const sorted = sortBy(nodes.filter((n) => !n.pinned), (n) => get(n.data), dir);
  return [...nodes.filter((n) => n.pinned), ...sorted].map((n) =>
    n.children ? { ...n, children: sortTree(n.children, get, dir) } : n);
}

function flattenNodes<T>(
  nodes: TreeNode<T>[],
  depth = 0,
  guides: boolean[] = [],
): { node: TreeNode<T>; meta: TreeRowMeta }[] {
  const result: { node: TreeNode<T>; meta: TreeRowMeta }[] = [];
  nodes.forEach((node, i) => {
    const isLast = i === nodes.length - 1;
    const meta: TreeRowMeta = { depth, isLast, guides: [...guides], isExpanded: node.expanded, nodeKey: node.key };
    result.push({ node, meta });
    if (node.children && node.expanded !== false) {
      const childGuides = depth === 0 ? [] : [...guides, !isLast];
      result.push(...flattenNodes(node.children, depth + 1, childGuides));
    }
  });
  return result;
}

function IndeterminateCheckbox({ checked, indeterminate, onChange }: { checked: boolean; indeterminate: boolean; onChange: () => void }) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => { if (ref.current) ref.current.indeterminate = indeterminate; }, [indeterminate]);
  return <input ref={ref} type="checkbox" checked={checked} onChange={onChange} />;
}

export function TreeTable<T>({ columns: allColumns, nodes: rawNodes, emptyText, selection, isSelectable, renderDetail, id, paginate = true, onRowClick: rowAction }: TreeTableProps<T>) {
  const tid = tableId(id, allColumns.map((c) => c.key));
  const prefs = useColumnPrefs(tid, allColumns);
  const columns = prefs.visible;
  const sortCol = prefs.sort ? allColumns.find((c) => c.key === prefs.sort!.key) : undefined;
  const nodes = sortCol?.sortValue ? sortTree(rawNodes, sortCol.sortValue, prefs.sort!.dir) : rawNodes;
  const size = paginate ? prefs.pageSize : Math.max(1, nodes.length);
  const pager = usePager(nodes.length, size, tid);
  const rows = flattenNodes(paginate ? nodes.slice(pager.start, pager.end) : nodes);

  if (nodes.length === 0 && emptyText) {
    return <div className="hy-empty" dangerouslySetInnerHTML={typeof emptyText === 'string' ? { __html: emptyText } : undefined}>{typeof emptyText !== 'string' ? emptyText : undefined}</div>;
  }

  return (
    <div className="hy-table-box">
    <ColumnMenu prefs={prefs} />
    <div className="hy-table-wrap">
      <table className={clsx('hy-table', prefs.menuItems.length > 0 && 'has-col-menu')}>
        <thead>
          <tr>
            {selection && (
              <th
                className="col-check"
                onClick={(e) => {
                  e.stopPropagation();
                  if ((e.target as HTMLElement).tagName !== 'INPUT') {
                    selection.toggleAll();
                  }
                }}
              >
                <IndeterminateCheckbox
                  checked={selection.isAllSelected}
                  indeterminate={selection.isSomeSelected}
                  onChange={selection.toggleAll}
                />
              </th>
            )}
            {columns.map((col) => (
              <th key={col.key} style={col.width ? { width: col.width } : undefined} className={col.className} aria-sort={prefs.sort?.key === col.key ? (prefs.sort.dir === 'asc' ? 'ascending' : 'descending') : undefined}>
                <SortTitle title={col.title} active={prefs.sort?.key === col.key} dir={prefs.sort?.dir} onClick={col.sortValue ? () => prefs.cycleSort(col.key) : undefined} />
              </th>
            ))}
          </tr>
        </thead>
        <AnimatePresence>
          <tbody>
            {rows.map(({ node, meta }) => {
              const selectable = !selection || !isSelectable || isSelectable(node, meta);
              const isSelected = selection && selectable && selection.selected.has(node.key);
              const onRowClick = rowAction
                ? (e: React.MouseEvent<HTMLTableRowElement>) => {
                    if (isInteractiveDescendant(e.target, e.currentTarget)) return;
                    rowAction(node, meta);
                  }
                : undefined;
              // Mirror Table's enlarged-hit-area for the col-check cell:
              // the entire td (including its padding) toggles the checkbox
              // and stops the click from bubbling up to the row's
              // selectOnly handler.
              const onCheckCellClick = (selection && selectable)
                ? (e: React.MouseEvent<HTMLTableCellElement>) => {
                    e.stopPropagation();
                    if ((e.target as HTMLElement).tagName !== 'INPUT') {
                      selection.toggle(node.key);
                    }
                  }
                : undefined;
              const detail = renderDetail ? renderDetail(node, meta) : null;
              return (
                <React.Fragment key={node.key}>
                <motion.tr
                  key={node.key}
                  data-row-key={node.key}
                  className={clsx(node.className, isSelected && 'selected', rowAction && 'hy-row-action')}
                  onClick={onRowClick}
                  layout
                  initial={{ opacity: 0 }}
                  animate={{ opacity: 1 }}
                  exit={{ opacity: 0 }}
                  transition={{ type: 'spring', stiffness: 500, damping: 35 }}
                >
                  {selection && (
                    <td className="col-check" onClick={onCheckCellClick}>
                      {selectable ? (
                        <input type="checkbox" checked={!!isSelected} onChange={() => selection.toggle(node.key)} />
                      ) : null}
                    </td>
                  )}
                  {columns.map((col) => (
                    <td key={col.key} className={col.className}>{col.render(node.data, meta)}</td>
                  ))}
                </motion.tr>
                {detail != null && detail !== false && (
                  <tr key={`${node.key}::detail`} className={clsx('hy-tree-detail', node.className)} data-row-key={`${node.key}::detail`}>
                    <td colSpan={columns.length + (selection ? 1 : 0)}>{detail}</td>
                  </tr>
                )}
                </React.Fragment>
              );
            })}
          </tbody>
        </AnimatePresence>
      </table>
    </div>
    {paginate && <Pager total={nodes.length} size={prefs.pageSize} onSize={prefs.setPageSize} pager={pager} />}
    </div>
  );
}

// Helper component for tree cell with guide lines
export interface TreeCellProps {
  meta: TreeRowMeta;
  children: ReactNode;
}

export function TreeCell({ meta, children }: TreeCellProps) {
  return (
    <div className="tree-cell">
      {meta.depth > 0 && meta.guides.slice(0, meta.depth - 1).map((hasLine, i) => (
        <span key={i} className={clsx('tree-guide', hasLine && 'tree-guide-active')} aria-hidden="true" />
      ))}
      {meta.depth > 0 && (
        <span className={clsx('tree-branch', meta.isLast && 'tree-last')} aria-hidden="true" />
      )}
      {children}
    </div>
  );
}
