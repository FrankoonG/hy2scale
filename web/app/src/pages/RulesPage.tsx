import { useState, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Card, Button, Table, Tabs, TabPanel, useSelection, useModalState, type Column } from '@hy2scale/ui';
import { ExitViaCell, exitViaSortValue } from '@/components/ExitViaCell';
import ImportExportButton from '@/components/ImportExportButton';
import ResponsiveActions from '@/components/ResponsiveActions';
import RuleModal from '@/components/RuleModal';
import { BulkButtons, EditSelectedButton, pickSelected } from '@/components/SelectionActions';
import { useBulkAction } from '@/hooks/useBulkAction';
import { ListScope } from '@/hooks/useDeselectOnBlankClick';
import type { RoutingRule } from '@/api';
import * as api from '@/api';

type RuleType = 'ip' | 'domain';

export default function RulesPage() {
  const { t } = useTranslation();
  const { data } = useQuery({ queryKey: ['rules'], queryFn: api.getRules });
  const rules = data?.rules || [];

  const [tab, setTab] = useState<RuleType>('ip');
  const currentRules = rules.filter((r) => r.type === tab);

  const selection = useSelection(currentRules.map((r) => r.id));
  const selected = pickSelected(currentRules, selection.selected, (r) => r.id);
  const edit = useModalState<RoutingRule | null>();
  const bulk = useBulkAction(['rules']);

  const bulkToggle = (enabled: boolean) => bulk(selection.selected, (id) => api.toggleRule(id, enabled),
    { label: enabled ? t('app.bulkEnable') : t('app.bulkDisable') });
  const bulkDelete = () => bulk(selection.selected, (id) => api.deleteRule(id), {
    label: t('app.bulkDelete'),
    confirm: { title: t('app.bulkDelete'), message: t('app.bulkDeleteConfirm', { count: selection.count }), confirmText: t('app.delete'), danger: true },
  });

  const captureNote = data?.capture === 'dnat' ? 'rules.localOnlyCapture'
    : data?.capture === 'none' ? 'rules.noCapture' : '';

  const columns: Column<RoutingRule>[] = [
    { key: 'name', title: t('rules.name'), sortValue: (r) => r.name || null, render: (r) => <strong>{r.name || '—'}</strong> },
    {
      key: 'targets', title: t('rules.targets'), sortValue: (r) => r.targets[0] ?? null,
      render: (r) => <span className="mono rule-targets">{r.targets.slice(0, 3).join(', ')}{r.targets.length > 3 ? ` +${r.targets.length - 3}` : ''}</span>,
    },
    { key: 'exit', title: t('rules.exitVia'), sortValue: (r) => exitViaSortValue(r.exit_via, r.exit_paths), render: (r) => <ExitViaCell exitVia={r.exit_via} exitPaths={r.exit_paths} exitMode={r.exit_mode} /> },
  ];

  return (
    <div className="hy-page">
      <Tabs
        items={[
          { key: 'ip', label: t('rules.ipRules') },
          { key: 'domain', label: t('rules.domainRules') },
        ]}
        activeKey={tab}
        onChange={(k) => setTab(k as RuleType)}
      />

      <TabPanel fill activeKey={tab} keys={['ip', 'domain']}>
        {captureNote && <div className="hy-warn-banner">{t(captureNote)}</div>}
        <Card
          fill={1}
          title={tab === 'ip' ? t('rules.ipRules') : t('rules.domainRules')}
          count={currentRules.length}
          actions={
            <ResponsiveActions selectedCount={selection.count} onClearSelection={selection.clear}>
              <BulkButtons selected={selected} isEnabled={(r) => r.enabled} onToggle={bulkToggle} onDelete={bulkDelete} />
              <ImportExportButton target="rules" />
              <EditSelectedButton selected={selected} onEdit={(r, e) => edit.show(r, e)} />
              <Button size="sm" variant="primary" onClick={(e: MouseEvent) => edit.show(null, e)}>{t('rules.newRule')}</Button>
            </ResponsiveActions>
          }
          noPadding
        >
          <ListScope selection={selection}>
            <Table
              id={`rules.${tab}`}
              columns={columns}
              data={currentRules}
              rowKey={(r) => r.id}
              rowClassName={(r) => !r.enabled ? 'disabled-row' : undefined}
              emptyText={t('rules.noRules')}
              selection={selection}
            />
          </ListScope>
        </Card>
      </TabPanel>

      {/* Type follows the active tab */}
      <RuleModal {...edit.modalProps} type={tab} editing={edit.data ?? null} />
    </div>
  );
}
