import { useRef, useState, type ReactNode, type RefObject } from 'react';
import { Reorder, useDragControls } from 'framer-motion';
import clsx from 'clsx';
import { IconGripVertical, IconX } from '../icons';

export interface SortableListProps<T extends { id: string | number }> {
  items: T[];
  /** New order after a drag. */
  onReorder: (items: T[]) => void;
  /** The row's editors (inputs, selects …) between grip and delete button. */
  renderItem: (item: T) => ReactNode;
  /** Delete button handler; the button shows while more than `minItems` rows exist. */
  onRemove?: (item: T) => void;
  /** Dashed "add" row under the list. */
  onAdd?: () => void;
  addLabel?: ReactNode;
  /** Accessible name / title of the delete button. */
  removeLabel: string;
  /** Rows that can never be deleted (default 1). */
  minItems?: number;
  testId?: string;
  className?: string;
}

/**
 * An ordered list of editable rows: drag by the grip to reorder, × to
 * delete, dashed row to append. Row state stays with the caller.
 */
export function SortableList<T extends { id: string | number }>({
  items, onReorder, renderItem, onRemove, onAdd, addLabel, removeLabel, minItems = 1, testId, className,
}: SortableListProps<T>) {
  const ref = useRef<HTMLUListElement>(null);
  const many = items.length > 1;
  const removable = !!onRemove && items.length > minItems;
  return (
    <div className={clsx('hy-sortable', className)} data-testid={testId}>
      <Reorder.Group ref={ref} axis="y" values={items} onReorder={onReorder} className="hy-sortable-list">
        {items.map((item) => (
          <Row key={item.id} item={item} draggable={many} constraintsRef={ref}
            onRemove={removable ? () => onRemove!(item) : undefined} removeLabel={removeLabel}>
            {renderItem(item)}
          </Row>
        ))}
      </Reorder.Group>
      {onAdd && <div className="hy-sortable-add" role="button" tabIndex={0} onClick={onAdd}
        onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onAdd(); } }}>{addLabel}</div>}
    </div>
  );
}

function Row<T>({ item, draggable, constraintsRef, onRemove, removeLabel, children }: {
  item: T;
  draggable: boolean;
  constraintsRef: RefObject<HTMLElement | null>;
  onRemove?: () => void;
  removeLabel: string;
  children: ReactNode;
}) {
  const controls = useDragControls();
  const [dragging, setDragging] = useState(false);
  return (
    <Reorder.Item
      value={item}
      dragListener={false}
      dragControls={controls}
      dragConstraints={constraintsRef}
      dragElastic={0.1}
      onDragStart={() => { setDragging(true); document.body.classList.add('dragging-active'); }}
      onDragEnd={() => { setDragging(false); document.body.classList.remove('dragging-active'); }}
      className={clsx('hy-sortable-row', dragging && 'dragging')}
    >
      {draggable && (
        <div className="hy-sortable-grip" onPointerDown={(e) => controls.start(e)} aria-hidden="true">
          <IconGripVertical size={14} />
        </div>
      )}
      {children}
      {onRemove && (
        <button type="button" className="hy-sortable-del" onClick={onRemove} title={removeLabel} aria-label={removeLabel}>
          <IconX size={16} />
        </button>
      )}
    </Reorder.Item>
  );
}
