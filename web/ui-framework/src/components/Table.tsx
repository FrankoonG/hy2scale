import { type ReactNode, useRef, useEffect } from 'react';
import clsx from 'clsx';

export interface Column<T> {
  key: string;
  title: ReactNode;
  render: (row: T, index: number) => ReactNode;
  className?: string;
  width?: string;
}

export interface SelectionState {
  selected: Set<string>;
  toggle: (key: string) => void;
  toggleAll: () => void;
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
}

function IndeterminateCheckbox({ checked, indeterminate, onChange }: { checked: boolean; indeterminate: boolean; onChange: () => void }) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => { if (ref.current) ref.current.indeterminate = indeterminate; }, [indeterminate]);
  return <input ref={ref} type="checkbox" checked={checked} onChange={onChange} />;
}

export function Table<T>({ columns, data, rowKey, rowClassName, emptyText, selection }: TableProps<T>) {
  if (data.length === 0 && emptyText) {
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
        <tbody>
          {data.map((row, i) => {
            const key = rowKey(row, i);
            const isSelected = selection?.selected.has(key);
            return (
              <tr key={key} className={clsx(rowClassName?.(row, i), isSelected && 'selected')}>
                {selection && (
                  <td className="col-check">
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
  );
}
