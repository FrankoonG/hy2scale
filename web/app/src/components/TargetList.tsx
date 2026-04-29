import { useState, useRef, useCallback, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { Reorder, useDragControls } from 'framer-motion';
import { Input, FormGroup, GripIcon } from '@hy2scale/ui';
import clsx from 'clsx';

interface TargetItem {
  id: number;
  value: string;
}

interface TargetListProps {
  type: 'ip' | 'domain';
  value: string[];
  onChange: (targets: string[]) => void;
  label?: string;
}

let nextId = 1;

// Validation patterns
const IP_RE = /^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$/;
const CIDR_RE = /^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\/\d{1,2}$/;
const RANGE_RE = /^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}-\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$/;
const DOMAIN_RE = /^(\*\.)?[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*\.[a-zA-Z]{2,}$/;

function validateIP(val: string): boolean {
  if (!val) return true;
  return IP_RE.test(val) || CIDR_RE.test(val) || RANGE_RE.test(val);
}

function validateDomain(val: string): boolean {
  if (!val) return true;
  return DOMAIN_RE.test(val);
}

export function TargetList({ type, value, onChange, label }: TargetListProps) {
  const { t } = useTranslation();
  const validate = type === 'ip' ? validateIP : validateDomain;

  // Items are the source of truth for the form. Initial state is captured from
  // `value` at mount time. This component is consumed inside a Modal that
  // unmounts on close, so a fresh mount always picks up the latest external
  // value via the initializer — no render-phase sync needed.
  const [items, setItems] = useState<TargetItem[]>(() =>
    (value.length > 0 ? value : ['']).map((v) => ({ id: nextId++, value: v }))
  );
  // Track what we last emitted so the post-commit effect doesn't echo the
  // initial mount value back to the parent on the very first render.
  const lastEmitted = useRef<string>(JSON.stringify(value.length > 0 ? value : ['']));

  // Emit AFTER commit, never inside a setItems updater. Calling parent setState
  // from within an updater is impure and races with concurrent re-renders
  // (e.g. zustand topology polls): a render where the parent's value still
  // lags the internal items can mis-trigger any render-phase sync logic and
  // remount the input element, which drops keyboard focus mid-typing.
  useEffect(() => {
    const vals = items.map((it) => it.value);
    const j = JSON.stringify(vals);
    if (j !== lastEmitted.current) {
      lastEmitted.current = j;
      onChange(vals);
    }
  }, [items, onChange]);

  const updateItem = useCallback((id: number, val: string) => {
    setItems((prev) => prev.map((it) => it.id === id ? { ...it, value: val } : it));
  }, []);

  const addItem = useCallback(() => {
    setItems((prev) => [...prev, { id: nextId++, value: '' }]);
  }, []);

  const removeItem = useCallback((id: number) => {
    setItems((prev) => {
      const next = prev.filter((it) => it.id !== id);
      if (next.length === 0) next.push({ id: nextId++, value: '' });
      return next;
    });
  }, []);

  const handleReorder = useCallback((newItems: TargetItem[]) => {
    setItems(newItems);
  }, []);

  const listRef = useRef<HTMLUListElement>(null);
  const placeholder = type === 'ip' ? t('rules.ipHint') : t('rules.domainHint');
  const addLabel = type === 'ip' ? t('rules.addIP') : t('rules.addDomain');

  return (
    <FormGroup label={label || (type === 'ip' ? t('rules.ipTargets') : t('rules.domainTargets'))} required>
      <Reorder.Group
        ref={listRef}
        axis="y"
        values={items}
        onReorder={handleReorder}
        className="addr-list"
        style={{ listStyle: 'none', padding: 0, margin: 0 }}
      >
        {items.map((item) => (
          <TargetRow
            key={item.id}
            item={item}
            canDrag={items.length > 1}
            canRemove={items.length > 1}
            constraintsRef={listRef}
            placeholder={placeholder}
            validate={validate}
            onUpdate={(val) => updateItem(item.id, val)}
            onRemove={() => removeItem(item.id)}
          />
        ))}
      </Reorder.Group>
      <div className="addr-add-row" onClick={addItem}>
        {addLabel}
      </div>
    </FormGroup>
  );
}

interface TargetRowProps {
  item: TargetItem;
  canDrag: boolean;
  canRemove: boolean;
  constraintsRef: React.RefObject<HTMLElement | null>;
  placeholder: string;
  validate: (val: string) => boolean;
  onUpdate: (val: string) => void;
  onRemove: () => void;
}

function TargetRow({ item, canDrag, canRemove, constraintsRef, placeholder, validate, onUpdate, onRemove }: TargetRowProps) {
  const controls = useDragControls();
  const [dragging, setDragging] = useState(false);
  const isValid = validate(item.value);

  return (
    <Reorder.Item
      value={item}
      dragListener={false}
      dragControls={controls}
      dragConstraints={constraintsRef}
      dragElastic={0.1}
      onDragStart={() => { setDragging(true); document.body.classList.add('dragging-active'); }}
      onDragEnd={() => { setDragging(false); document.body.classList.remove('dragging-active'); }}
      className={clsx('addr-row', dragging && 'dragging')}
      style={{ listStyle: 'none' }}
    >
      {canDrag && (
        <div className="addr-drag" onPointerDown={(e) => controls.start(e)}>
          <GripIcon />
        </div>
      )}
      <Input
        value={item.value}
        onChange={(e) => onUpdate(e.target.value)}
        placeholder={placeholder}
        error={!isValid}
        style={{ fontFamily: 'var(--mono)', flex: 1 }}
      />
      {canRemove && (
        <button type="button" className="addr-del" onClick={onRemove} title="×">
          ×
        </button>
      )}
    </Reorder.Item>
  );
}
