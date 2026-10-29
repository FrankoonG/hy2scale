import { useRef, useState, type RefObject } from 'react';
import { Reorder, useDragControls } from 'framer-motion';
import { Autocomplete, GripIcon } from '@hy2scale/ui';
import clsx from 'clsx';

interface Item { id: number; value: string; }
let nextId = 1;

export function toListItems(values: string[]): Item[] {
  return (values.length ? values : ['']).map((value) => ({ id: nextId++, value }));
}

export function fromListItems(items: Item[]): string[] {
  return items.map((i) => i.value.trim()).filter(Boolean);
}

export type ListItem = Item;

interface Props {
  items: Item[];
  onChange: (items: Item[]) => void;
  options: string[];
  placeholder: string;
  addLabel: string;
  deleteTitle: string;
  testId?: string;
}

/** Reorderable list of strings with autocomplete; the order is meaningful. */
export function OrderedList({ items, onChange, options, placeholder, addLabel, deleteTitle, testId }: Props) {
  const ref = useRef<HTMLUListElement>(null);
  const update = (id: number, value: string) => onChange(items.map((i) => (i.id === id ? { ...i, value } : i)));
  return (
    <div data-testid={testId}>
      <Reorder.Group ref={ref} axis="y" values={items} onReorder={onChange} className="addr-list"
        style={{ listStyle: 'none', padding: 0, margin: 0 }}>
        {items.map((item) => (
          <Row key={item.id} item={item} canDrag={items.length > 1} canRemove={items.length > 1} constraintsRef={ref}
            options={options} placeholder={placeholder} deleteTitle={deleteTitle}
            onUpdate={(v) => update(item.id, v)} onRemove={() => onChange(items.filter((i) => i.id !== item.id))} />
        ))}
      </Reorder.Group>
      <div className="addr-add-row" onClick={() => onChange([...items, { id: nextId++, value: '' }])}>{addLabel}</div>
    </div>
  );
}

interface RowProps {
  item: Item;
  canDrag: boolean;
  canRemove: boolean;
  constraintsRef: RefObject<HTMLElement | null>;
  options: string[];
  placeholder: string;
  deleteTitle: string;
  onUpdate: (v: string) => void;
  onRemove: () => void;
}

function Row({ item, canDrag, canRemove, constraintsRef, options, placeholder, deleteTitle, onUpdate, onRemove }: RowProps) {
  const controls = useDragControls();
  const [dragging, setDragging] = useState(false);
  return (
    <Reorder.Item value={item} dragListener={false} dragControls={controls} dragConstraints={constraintsRef}
      dragElastic={0.1}
      onDragStart={() => { setDragging(true); document.body.classList.add('dragging-active'); }}
      onDragEnd={() => { setDragging(false); document.body.classList.remove('dragging-active'); }}
      className={clsx('addr-row', dragging && 'dragging')} style={{ listStyle: 'none' }}>
      {canDrag && (
        <div className="addr-drag" onPointerDown={(e) => controls.start(e)}><GripIcon /></div>
      )}
      <Autocomplete options={options} value={item.value} onChange={onUpdate} placeholder={placeholder} />
      {canRemove && (
        <button type="button" className="addr-del" onClick={onRemove} title={deleteTitle}>×</button>
      )}
    </Reorder.Item>
  );
}
