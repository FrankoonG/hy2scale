import { useTranslation } from 'react-i18next';
import type { SetRef } from '@/api/policy';

/** Who uses a set: "Used by: user alice, policy Guest …". */
export function RefsLine({ refs }: { refs: SetRef[] }) {
  const { t } = useTranslation();
  return (
    <div className="pset-refs" data-testid="set-refs">
      <span className="muted">{t('policy.usedBy')}</span>
      {refs.map((r) => (
        <span key={`${r.kind}:${r.id}`} className="pset-ref">{t(`policy.refKind.${r.kind}`)} · {r.name}</span>
      ))}
    </div>
  );
}

/** Short text of the refs for a table cell. */
export function refsText(refs: SetRef[] | undefined, t: (k: string) => string): string {
  if (!refs?.length) return '—';
  const shown = refs.slice(0, 3).map((r) => `${t(`policy.refKind.${r.kind}`)} ${r.name}`);
  return refs.length > 3 ? `${shown.join(', ')} +${refs.length - 3}` : shown.join(', ');
}
