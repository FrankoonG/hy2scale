import { useState, useRef, useCallback, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { Input, FormGroup, SortableList } from '@hy2scale/ui';
import { validateIP } from '@/utils/ipaddr';

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

const DOMAIN_RE = /^(\*\.)?[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*\.[a-zA-Z]{2,}$/;

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

  const placeholder = type === 'ip' ? t('rules.ipHint') : t('rules.domainHint');
  const addLabel = type === 'ip' ? t('rules.addIP') : t('rules.addDomain');

  return (
    <FormGroup label={label || (type === 'ip' ? t('rules.ipTargets') : t('rules.domainTargets'))} required>
      <SortableList
        items={items}
        onReorder={handleReorder}
        removeLabel={t('app.delete')}
        onRemove={(item) => removeItem(item.id)}
        addLabel={addLabel}
        onAdd={addItem}
        renderItem={(item) => (
          <Input
            value={item.value}
            onChange={(e) => updateItem(item.id, e.target.value)}
            placeholder={placeholder}
            error={!validate(item.value)}
            className="mono"
          />
        )}
      />
    </FormGroup>
  );
}
