import { lazy, Suspense } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Badge, Button, Card, ExpandButton, Table, useModalState, type Column } from '@hy2scale/ui';
import { dismissPolicyNotice, getPolicy, type ConditionSet, type PolicySet } from '@/api/policy';
import SetModal from '@/components/policy/SetModal';
import DirectModal from '@/components/policy/DirectModal';
import TestModal from '@/components/policy/TestModal';
import { refsText } from '@/components/policy/RefsLine';
import { usePageView } from '@/store/pageView';
import type { SetKind } from '@/components/policy/shared';

const RulesGraph = lazy(() => import('@/components/policy/graph/RulesGraph'));

/**
 * Rules: one list of policy sets (where connections go, or whether they
 * may) and condition sets (reusable true / false), plus the direct entry.
 */
export default function RulesPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { data: cfg } = useQuery({ queryKey: ['policy'], queryFn: getPolicy });
  const edit = useModalState<{ kind: SetKind; set: PolicySet | ConditionSet | null }>();
  const direct = useModalState();
  const test = useModalState();
  const [view] = usePageView('rules');

  type Row = { kind: SetKind; set: PolicySet | ConditionSet };
  const rows: Row[] = [
    ...(cfg?.policies || []).map((set) => ({ kind: 'policy' as const, set })),
    ...(cfg?.conditions || []).map((set) => ({ kind: 'condition' as const, set })),
  ];

  const cols: Column<Row>[] = [
    {
      key: 'name', title: t('policy.name'), sortValue: (r) => r.set.name,
      render: (r) => (
        <span className="pset-name">
          <strong>{r.set.name}</strong>
          {cfg?.direct === r.set.id && <Badge variant="blue">{t('policy.directEntryBadge')}</Badge>}
        </span>
      ),
    },
    {
      key: 'kind', title: t('policy.setKind'), sortValue: (r) => r.kind,
      render: (r) => <Badge variant={r.kind === 'policy' ? 'green' : 'muted'}>{t(r.kind === 'policy' ? 'policy.kindPolicy' : 'policy.kindCondition')}</Badge>,
    },
    { key: 'rules', title: t('policy.rules'), sortValue: (r) => r.set.rules.length, render: (r) => r.set.rules.length },
    { key: 'refs', title: t('policy.usedBy'), render: (r) => <span className="muted">{refsText(r.set.refs, t)}</span> },
  ];
  const dismiss = async () => {
    await dismissPolicyNotice().catch(() => {});
    qc.invalidateQueries({ queryKey: ['policy'] });
  };

  const buttons = (
    <div className="button-row">
      <Button onClick={(e) => direct.show(undefined, e)} data-testid="rules-direct">{t('policy.direct')}</Button>
      <Button onClick={(e) => test.show(undefined, e)} data-testid="rules-test">{t('policy.test')}</Button>
      <ExpandButton variant="primary" label={`+ ${t('policy.newPolicy')}`} moreLabel={t('app.moreActions')}
        testId="rules-new-policy"
        onClick={(e) => edit.show({ kind: 'policy', set: null }, e)}
        actions={[{ key: 'condition', label: t('policy.newCondition'), testId: 'rules-new-condition',
          onClick: (e) => edit.show({ kind: 'condition', set: null }, e) }]} />
    </div>
  );
  const modals = (
    <>
      <SetModal {...edit.modalProps} kind={edit.data?.kind ?? 'policy'} editing={edit.data?.set ?? null} cfg={cfg} />
      <DirectModal {...direct.modalProps} cfg={cfg} />
      <TestModal {...test.modalProps} cfg={cfg} />
    </>
  );

  if (view === 'topo') {
    return (
      <div className="hy-topo-full">
        <Suspense fallback={null}>
          <RulesGraph cfg={cfg} onDirect={(e) => direct.show(undefined, e)}
            actions={<Button onClick={(e) => test.show(undefined, e)} data-testid="rules-test">{t('policy.test')}</Button>} />
        </Suspense>
        {modals}
      </div>
    );
  }

  return (
    <div className="hy-page rules-page" data-testid="rules-page">
      {cfg?.migrated_notice && (
        <div className="hy-warn-banner policy-notice" data-testid="policy-notice">
          <span>{t('policy.migratedNotice')}</span>
          <Button size="sm" onClick={dismiss}>{t('app.close')}</Button>
        </div>
      )}

      <Card
        title={t('policy.sets')}
        count={rows.length}
        noPadding
        actions={buttons}
      >
        <Table id="policy.sets" columns={cols} data={rows} rowKey={(r) => r.set.id}
          emptyText={t('policy.noSets')}
          onRowClick={(r, _i, o) => edit.show({ kind: r.kind, set: r.set }, o)} />
      </Card>
      {modals}
    </div>
  );
}
