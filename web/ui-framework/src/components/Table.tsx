import { type ReactNode } from 'react';
import clsx from 'clsx';

export interface Column<T> {
  key: string;
  title: ReactNode;
  render: (row: T, index: number) => ReactNode;
  className?: string;
  width?: string;
}

export interface TableProps<T> {
  columns: Column<T>[];
  data: T[];
  rowKey: (row: T, index: number) => string;
  rowClassName?: (row: T, index: number) => string;
  emptyText?: ReactNode;
}

export function Table<T>({ columns, data, rowKey, rowClassName, emptyText }: TableProps<T>) {
  if (data.length === 0 && emptyText) {
    return <div className="hy-empty" dangerouslySetInnerHTML={typeof emptyText === 'string' ? { __html: emptyText } : undefined}>{typeof emptyText !== 'string' ? emptyText : undefined}</div>;
  }

  return (
    <div className="hy-table-wrap">
      <table className="hy-table">
        <thead>
          <tr>
            {columns.map((col) => (
              <th key={col.key} style={col.width ? { width: col.width } : undefined} className={col.className}>
                {col.title}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {data.map((row, i) => (
            <tr key={rowKey(row, i)} className={clsx(rowClassName?.(row, i))}>
              {columns.map((col) => (
                <td key={col.key} className={col.className}>{col.render(row, i)}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
