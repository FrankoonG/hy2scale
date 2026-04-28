import { type ReactNode, useRef, useEffect } from 'react';
import * as React from 'react';
import { motion, AnimatePresence } from 'framer-motion';
import clsx from 'clsx';
import type { SelectionState } from './Table';

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
}

export interface TreeTableProps<T> {
  columns: TreeColumn<T>[];
  nodes: TreeNode<T>[];
  emptyText?: ReactNode;
  selection?: SelectionState;
  isSelectable?: (node: TreeNode<T>, meta: TreeRowMeta) => boolean;
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

export function TreeTable<T>({ columns, nodes, emptyText, selection, isSelectable }: TreeTableProps<T>) {
  const rows = flattenNodes(nodes);

  if (rows.length === 0 && emptyText) {
    return <div className="hy-empty" dangerouslySetInnerHTML={typeof emptyText === 'string' ? { __html: emptyText } : undefined}>{typeof emptyText !== 'string' ? emptyText : undefined}</div>;
  }

  return (
    <div className="hy-table-wrap">
      <table className="hy-table">
        <thead>
          <tr>
            {selection && (
              <th className="col-check">
                <IndeterminateCheckbox
                  checked={selection.isAllSelected}
                  indeterminate={selection.isSomeSelected}
                  onChange={selection.toggleAll}
                />
              </th>
            )}
            {columns.map((col) => (
              <th key={col.key} style={col.width ? { width: col.width } : undefined} className={col.className}>
                {col.title}
              </th>
            ))}
          </tr>
        </thead>
        <AnimatePresence>
          <tbody>
            {rows.map(({ node, meta }) => {
              const selectable = !selection || !isSelectable || isSelectable(node, meta);
              const isSelected = selection && selectable && selection.selected.has(node.key);
              const onRowClick = (selection && selectable)
                ? (e: React.MouseEvent<HTMLTableRowElement>) => {
                    if (isInteractiveDescendant(e.target, e.currentTarget)) return;
                    selection.selectOnly(node.key);
                  }
                : undefined;
              return (
                <motion.tr
                  key={node.key}
                  data-row-key={node.key}
                  className={clsx(node.className, isSelected && 'selected', selection && selectable && 'hy-row-clickable')}
                  onClick={onRowClick}
                  layout
                  initial={{ opacity: 0 }}
                  animate={{ opacity: 1 }}
                  exit={{ opacity: 0 }}
                  transition={{ type: 'spring', stiffness: 500, damping: 35 }}
                >
                  {selection && (
                    <td className="col-check">
                      {selectable ? (
                        <input type="checkbox" checked={!!isSelected} onChange={() => selection.toggle(node.key)} />
                      ) : null}
                    </td>
                  )}
                  {columns.map((col) => (
                    <td key={col.key} className={col.className}>{col.render(node.data, meta)}</td>
                  ))}
                </motion.tr>
              );
            })}
          </tbody>
        </AnimatePresence>
      </table>
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
