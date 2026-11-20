import { useTranslation } from 'react-i18next';
import { PasswordInput, Select, SortableList } from '@hy2scale/ui';
import type { PolicySet } from '@/api/policy';

/** A password row of the user dialog: its password and this node's policy set ('' = off). */
export interface PasswordRow {
  id: string;
  password: string;
  policy: string;
}

/**
 * A row id not in rows. Chosen here rather than by the server so the
 * row's policy set can be sent with it.
 */
export function newRowId(rows: { id: string }[]): string {
  for (;;) {
    const b = crypto.getRandomValues(new Uint8Array(3));
    const id = 'r' + Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
    if (!rows.some((r) => r.id === id)) return id;
  }
}

/** Policy kind: the user's passwords, each signing in to a policy set of this node. */
export function PasswordRows({ rows, onChange, sets }: {
  rows: PasswordRow[];
  onChange: (rows: PasswordRow[]) => void;
  sets: PolicySet[];
}) {
  const { t } = useTranslation();
  const update = (id: string, patch: Partial<PasswordRow>) =>
    onChange(rows.map((r) => (r.id === id ? { ...r, ...patch } : r)));
  const options = (cur: string) => [
    { value: '', label: t('users.rowDisabled') },
    ...sets.map((s) => ({ value: s.id, label: s.name })),
    // a set removed since: kept until changed
    ...(cur && !sets.some((s) => s.id === cur) ? [{ value: cur, label: cur }] : []),
  ];

  return (
    <SortableList
      items={rows}
      onReorder={onChange}
      removeLabel={t('app.delete')}
      onRemove={(r) => onChange(rows.filter((x) => x.id !== r.id))}
      addLabel={t('users.addPassword')}
      onAdd={() => onChange([...rows, { id: newRowId(rows), password: '', policy: '' }])}
      testId="user-rows"
      renderItem={(r) => (
        <>
          <span className="user-row-no">#{rows.indexOf(r) + 1}</span>
          <div className="user-row-pw">
            <PasswordInput value={r.password} placeholder={t('users.password')}
              onChange={(e) => update(r.id, { password: e.target.value })}
              onGenerate={(pw) => update(r.id, { password: pw })} />
          </div>
          <div className="user-row-policy" data-testid="user-row-policy">
            <Select value={r.policy} onChange={(e) => update(r.id, { policy: e.target.value })} options={options(r.policy)} />
          </div>
        </>
      )}
    />
  );
}
