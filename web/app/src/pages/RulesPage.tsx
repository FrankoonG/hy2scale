import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Badge, Button, Card, Table, useModalState, type Column } from '@hy2scale/ui';
import { dismissPolicyNotice, getPolicy, type ConditionSet, type PolicySet } from '@/api/policy';
import SetModal from '@/components/policy/SetModal';
import DirectModal from '@/components/policy/DirectModal';
import TestModal from '@/components/policy/TestModal';
import { refsText } from '@/components/policy/RefsLine';
import type { SetKind } from '@/components/policy/shared';

/**
 * Rules: policy sets (where connections go, or whether they may) and
 * condition sets (reusable true / false), plus the direct entry.
 */
export default function RulesPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { data: cfg } = useQuery({ queryKey: ['policy'], queryFn: getPolicy });
  const edit = useModalState<{ kind: SetKind; set: PolicySet | ConditionSet | null }>();
  const direct = useModalState();
  const test = useModalState();

  const policies = cfg?.policies || [];
  const conditions = cfg?.conditions || [];

  const policyCols: Column<PolicySet>[] = [
    {
      key: 'name', title: t('policy.name'), sortValue: (p) => p.name,
      render: (p) => (
        <span className="pset-name">
          <strong>{p.name}</strong>
          {cfg?.direct === p.id && <Badge variant="blue">{t('policy.directEntryBadge')}</Badge>}
        </span>
      ),
    },
    { key: 'rules', title: t('policy.rules'), sortValue: (p) => p.rules.length, render: (p) => p.rules.length },
    { key: 'refs', title: t('policy.usedBy'), render: (p) => <span className="muted">{refsText(p.refs, t)}</span> },
  ];
  const condCols: Column<ConditionSet>[] = [
    { key: 'name', title: t('policy.name'), sortValue: (c) => c.name, render: (c) => <strong>{c.name}</strong> },
    { key: 'rules', title: t('policy.rules'), sortValue: (c) => c.rules.length, render: (c) => c.rules.length },
    { key: 'refs', title: t('policy.usedBy'), render: (c) => <span className="muted">{refsText(c.refs, t)}</span> },
  ];

  const dismiss = async () => {
    await dismissPolicyNotice().catch(() => {});
    qc.invalidateQueries({ queryKey: ['policy'] });
  };

  return (
    <div className="hy-page rules-page" data-testid="rules-page">
      {cfg?.migrated_notice && (
        <div className="hy-warn-banner policy-notice" data-testid="policy-notice">
          <span>{t('policy.migratedNotice')}</span>
          <Button size="sm" onClick={dismiss}>{t('app.close')}</Button>
        </div>
      )}

      <Card
        title={t('policy.policies')}
        count={policies.length}
        noPadding
        actions={(
          <div className="button-row">
            <Button onClick={(e) => direct.show(undefined, e)} data-testid="rules-direct">{t('policy.direct')}</Button>
            <Button onClick={(e) => test.show(undefined, e)} data-testid="rules-test">{t('policy.test')}</Button>
            <Button variant="primary" onClick={(e) => edit.show({ kind: 'policy', set: null }, e)} data-testid="rules-new-policy">
              + {t('policy.newPolicy')}
            </Button>
          </div>
        )}
      >
        <Table id="policy.policies" columns={policyCols} data={policies} rowKey={(p) => p.id} paginate={false}
          emptyText={t('policy.noPolicies')}
          onRowClick={(p, _i, o) => edit.show({ kind: 'policy', set: p }, o)} />
      </Card>

      <Card
        title={t('policy.conditions')}
        count={conditions.length}
        noPadding
        actions={(
          <Button variant="primary" onClick={(e) => edit.show({ kind: 'condition', set: null }, e)} data-testid="rules-new-condition">
            + {t('policy.newCondition')}
          </Button>
        )}
      >
        <Table id="policy.conditions" columns={condCols} data={conditions} rowKey={(c) => c.id} paginate={false}
          emptyText={t('policy.noConditions')}
          onRowClick={(c, _i, o) => edit.show({ kind: 'condition', set: c }, o)} />
      </Card>

      <SetModal {...edit.modalProps} kind={edit.data?.kind ?? 'policy'} editing={edit.data?.set ?? null} cfg={cfg} />
      <DirectModal {...direct.modalProps} cfg={cfg} />
      <TestModal {...test.modalProps} cfg={cfg} />
    </div>
  );
}
