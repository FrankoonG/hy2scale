import { type ReactNode } from 'react';
import * as React from 'react';
import clsx from 'clsx';
import { ColumnMenu, EmptyRow, IndeterminateCheckbox, Pager, SortTitle, isInteractiveDescendant, rowOrigin, sortBy, tableId, useColumnPrefs, usePager } from './TableKit';

export interface Column<T> {
  key: string;
  title: ReactNode;
  render: (row: T, index: number) => ReactNode;
  className?: string;
  width?: string;
  /** Column menu: name when `title` is not text; false = always shown. */
  menuTitle?: string;
  hideable?: boolean;
  defaultHidden?: boolean;
  /** Sort key of a row; the title becomes clickable (asc → desc → off). */
  sortValue?: (row: T) => string | number | null | undefined;
}

export interface SelectionState {
  selected: Set<string>;
  toggle: (key: string) => void;
  toggleAll: () => void;
  // selectOnly replaces the selection with one key (e.g. a graph click).
  // Tables no longer bind it to row clicks: rows run the list's onRowClick.
  selectOnly: (key: string) => void;
  isAllSelected: boolean;
  isSomeSelected: boolean;
}

export interface TableProps<T> {
  columns: Column<T>[];
  data: T[];
  rowKey: (row: T, index: number) => string;
  rowClassName?: (row: T, index: number) => string | undefined;
  emptyText?: ReactNode;
  selection?: SelectionState;
  /** Remembers column choices and page size (default: page path + columns). */
  id?: string;
  /** false = all rows on one page. */
  paginate?: boolean;
  /** The list's own action for a click on a row (outside its controls). */
  /** `origin`: the row's first column, for a dialog's open animation. */
  onRowClick?: (row: T, index: number, origin: { x: number; y: number }) => void;
}

export function Table<T>({ columns: allColumns, data: rawData, rowKey, rowClassName, emptyText, selection, id, paginate = true, onRowClick: rowAction }: TableProps<T>) {
  const tid = tableId(id, allColumns.map((c) => c.key));
  const prefs = useColumnPrefs(tid, allColumns);
  const columns = prefs.visible;
  const sortCol = prefs.sort ? allColumns.find((c) => c.key === prefs.sort!.key) : undefined;
  const data = sortCol?.sortValue ? sortBy(rawData, sortCol.sortValue, prefs.sort!.dir) : rawData;
  const size = paginate ? prefs.pageSize : Math.max(1, data.length);
  const pager = usePager(data.length, size, tid);
  if (data.length === 0 && emptyText) {
    return <EmptyRow>{emptyText}</EmptyRow>;
  }
  const pageRows = paginate ? data.slice(pager.start, pager.end) : data;
  const offset = paginate ? pager.start : 0;

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
        <tbody>
          {pageRows.map((row, pi) => {
            const i = offset + pi;
            const key = rowKey(row, i);
            const isSelected = selection?.selected.has(key);
            const onRowClick = rowAction
              ? (e: React.MouseEvent<HTMLTableRowElement>) => {
                  if (isInteractiveDescendant(e.target, e.currentTarget)) return;
                  rowAction(row, i, rowOrigin(e.currentTarget));
                }
              : undefined;
            // Click anywhere in the col-check cell — including its padding —
            // toggles the checkbox and never falls through to the row's
            // selectOnly handler. Accidental row-clicks while aiming at a
            // small checkbox were the most common error in user testing.
            const onCheckCellClick = selection
              ? (e: React.MouseEvent<HTMLTableCellElement>) => {
                  e.stopPropagation();
                  if ((e.target as HTMLElement).tagName !== 'INPUT') {
                    selection.toggle(key);
                  }
                }
              : undefined;
            return (
              <tr
                key={key}
                data-row-key={key}
                className={clsx(rowClassName?.(row, i), isSelected && 'selected', rowAction && 'hy-row-action')}
                onClick={onRowClick}
              >
                {selection && (
                  <td className="col-check" onClick={onCheckCellClick}>
                    <input type="checkbox" checked={!!isSelected} onChange={() => selection.toggle(key)} />
                  </td>
                )}
                {columns.map((col) => (
                  <td key={col.key} className={col.className}>{col.render(row, i)}</td>
                ))}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
    {paginate && <Pager total={data.length} size={prefs.pageSize} onSize={prefs.setPageSize} pager={pager} />}
    </div>
  );
}
