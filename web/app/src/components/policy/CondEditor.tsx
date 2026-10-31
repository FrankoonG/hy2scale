import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import { Button, IconButton, Input, Select, SegmentedControl, IconX } from '@hy2scale/ui';
import type { Cond, CondField } from '@/api/policy';
import { FIELDS, fieldInfo, parseValues } from './shared';

const MAX_DEPTH = 4;

export interface CondContext {
  /** Condition sets that may be referenced (the edited one excluded). */
  conditions: { id: string; name: string }[];
  users: { id: string; name: string }[];
}

/**
 * Editor of a rule's condition. Groups (all / any) nest; any node can be
 * negated; leaves are a field and its values, or a condition set.
 */
export default function CondEditor({ value, onChange, ctx }: { value?: Cond; onChange: (c: Cond | undefined) => void; ctx: CondContext }) {
  const { t } = useTranslation();
  // The root is always edited as a group; one "all" with no items = always.
  const root: Cond = value && (value.type === 'all' || value.type === 'any') ? value : { type: 'all', items: value ? [value] : [] };
  const set = (c: Cond) => {
    if ((c.type === 'all' || c.type === 'any') && c.items.length === 0) onChange(undefined);
    else if (c.type === 'all' && c.items.length === 1) onChange(c.items[0]);
    else onChange(c);
  };
  return (
    <div className="cond" data-testid="cond-editor">
      <Node value={root} onChange={set} depth={1} ctx={ctx} root />
      {(root.type === 'all' || root.type === 'any') && root.items.length === 0 && <span className="cond-always">{t('policy.always')}</span>}
    </div>
  );
}

function Node({ value, onChange, onRemove, depth, ctx, root }: {
  value: Cond; onChange: (c: Cond) => void; onRemove?: () => void; depth: number; ctx: CondContext; root?: boolean;
}) {
  const { t } = useTranslation();
  const negated = value.type === 'not';
  const inner = value.type === 'not' ? value.item : value;
  const setInner = (c: Cond) => onChange(negated ? { type: 'not', item: c } : c);
  const toggleNot = () => onChange(negated ? inner : { type: 'not', item: inner });

  const notBtn = (
    <button type="button" className={clsx('cond-not', negated && 'on')} onClick={toggleNot} title={t('policy.negate')} data-testid="cond-not">
      {t('policy.not')}
    </button>
  );
  const remove = onRemove && <IconButton icon={IconX} label={t('app.delete')} variant="subtle" onClick={onRemove} />;

  if (inner.type === 'all' || inner.type === 'any') {
    const items = inner.items;
    const setItems = (next: Cond[]) => setInner({ type: inner.type, items: next });
    const add = (c: Cond) => setItems([...items, c]);
    return (
      <div className={clsx('cond-group', root && 'root', negated && 'negated')} data-testid="cond-group">
        <div className="cond-group-head">
          {!root && notBtn}
          {items.length > 1 && (
            <SegmentedControl<'all' | 'any'>
              items={[{ key: 'all', label: t('policy.all') }, { key: 'any', label: t('policy.any') }]}
              value={inner.type}
              onChange={(k) => setInner({ type: k, items })}
            />
          )}
          <span className="spacer" />
          {remove}
        </div>
        {items.map((c, i) => (
          <Node key={i} value={c} depth={depth + 1} ctx={ctx}
            onChange={(n) => setItems(items.map((x, j) => (j === i ? n : x)))}
            onRemove={() => setItems(items.filter((_, j) => j !== i))} />
        ))}
        <div className="cond-add">
          <Button size="sm" variant="ghost" onClick={() => add({ type: 'match', field: 'dst_ip', values: [] })} data-testid="cond-add-field">
            + {t('policy.addField')}
          </Button>
          {ctx.conditions.length > 0 && (
            <Button size="sm" variant="ghost" onClick={() => add({ type: 'ref', ref: ctx.conditions[0].id })} data-testid="cond-add-ref">
              + {t('policy.addRef')}
            </Button>
          )}
          {depth < MAX_DEPTH && (
            <Button size="sm" variant="ghost" onClick={() => add({ type: inner.type === 'all' ? 'any' : 'all', items: [] })} data-testid="cond-add-group">
              + {t('policy.addGroup')}
            </Button>
          )}
        </div>
      </div>
    );
  }

  if (inner.type === 'ref') {
    return (
      <div className={clsx('cond-leaf', negated && 'negated')} data-testid="cond-ref">
        {notBtn}
        <span className="cond-label">{t('policy.condSet')}</span>
        <div className="cond-value">
          <Select value={inner.ref} onChange={(e) => setInner({ type: 'ref', ref: e.target.value })}
            options={ctx.conditions.map((c) => ({ value: c.id, label: c.name }))} />
        </div>
        {remove}
      </div>
    );
  }

  if (inner.type !== 'match') return null;
  const info = fieldInfo(inner.field);
  return (
    <div className={clsx('cond-leaf', negated && 'negated')} data-testid="cond-leaf">
      {notBtn}
      <div className="cond-field">
        <Select value={inner.field} onChange={(e) => setInner({ type: 'match', field: e.target.value as CondField, values: [] })}
          options={FIELDS.map((f) => ({ value: f.field, label: t(`policy.field.${f.field}`) }))} />
      </div>
      <div className="cond-value">
        {info.input === 'text'
          ? <ValuesInput values={inner.values} placeholder={info.placeholder} onChange={(v) => setInner({ ...inner, values: v })} />
          : (
            <Chips
              options={info.input === 'user'
                ? ctx.users.map((u) => ({ value: u.id, label: u.name }))
                : (info.choices || []).map((c) => ({ value: c, label: inner.field === 'src_kind' ? t(`policy.kind.${c}`) : c.toUpperCase() }))}
              values={inner.values}
              onChange={(v) => setInner({ ...inner, values: v })}
            />
          )}
      </div>
      {remove}
    </div>
  );
}

/** Free-text values; the typed text is kept while editing. */
function ValuesInput({ values, placeholder, onChange }: { values: string[]; placeholder?: string; onChange: (v: string[]) => void }) {
  const [text, setText] = useState(values.join(', '));
  useEffect(() => {
    if (parseValues(text).join(',') !== values.join(',')) setText(values.join(', '));
  }, [values.join(',')]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <Input value={text} placeholder={placeholder} data-testid="cond-values"
      onChange={(e) => { setText(e.target.value); onChange(parseValues(e.target.value)); }} />
  );
}

function Chips({ options, values, onChange }: { options: { value: string; label: string }[]; values: string[]; onChange: (v: string[]) => void }) {
  const { t } = useTranslation();
  if (!options.length) return <span className="muted">{t('policy.noChoices')}</span>;
  return (
    <div className="cond-chips" data-testid="cond-chips">
      {options.map((o) => {
        const on = values.includes(o.value);
        return (
          <button key={o.value} type="button" className={clsx('cond-chip', on && 'on')}
            onClick={() => onChange(on ? values.filter((v) => v !== o.value) : [...values, o.value])}>
            {o.label}
          </button>
        );
      })}
    </div>
  );
}
