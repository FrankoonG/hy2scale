import { Autocomplete, SortableList } from '@hy2scale/ui';

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
  const update = (id: number, value: string) => onChange(items.map((i) => (i.id === id ? { ...i, value } : i)));
  return (
    <SortableList
      items={items}
      onReorder={onChange}
      testId={testId}
      className="tight"
      removeLabel={deleteTitle}
      onRemove={(item) => onChange(items.filter((i) => i.id !== item.id))}
      addLabel={addLabel}
      onAdd={() => onChange([...items, { id: nextId++, value: '' }])}
      renderItem={(item) => (
        <Autocomplete options={options} value={item.value} onChange={(v) => update(item.id, v)} placeholder={placeholder} />
      )}
    />
  );
}
