import { useState, useCallback, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Card, Button, Table, Toggle, Badge, Modal, Input, Tabs, TabPanel,
  FormGroup, Autocomplete, useToast, useConfirm, useSelection, type Column,
} from '@hy2scale/ui';
import { ExitPathList, exitPathToApi, apiToExitPath, type ExitPathValue } from '@/components/ExitPathList';
import { TargetList } from '@/components/TargetList';
import { ExitViaCell } from '@/components/ExitViaCell';
import ImportExportButton from '@/components/ImportExportButton';
import ResponsiveActions from '@/components/ResponsiveActions';
import { useExitPaths } from '@/hooks/useExitPaths';
import type { RoutingRule } from '@/api';
import * as api from '@/api';

export default function RulesPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirmDlg = useConfirm();
  const queryClient = useQueryClient();

  const { data } = useQuery({ queryKey: ['rules'], queryFn: api.getRules });
  const { exitPaths } = useExitPaths();

  const rules = data?.rules || [];
  const available = data?.available !== false;

  const [tab, setTab] = useState('ip');

  const ipRules = rules.filter((r) => r.type === 'ip');
  const domainRules = rules.filter((r) => r.type === 'domain');
  const currentRules = tab === 'ip' ? ipRules : domainRules;

  const selection = useSelection(currentRules.map((r) => r.id));

  const [modalOpen, setModalOpen] = useState(false);
  const [editId, setEditId] = useState<string | null>(null);
  const [clickPos, setClickPos] = useState<{ x: number; y: number } | undefined>();

  // Rule form
  const [name, setName] = useState('');
  const [targets, setTargets] = useState<string[]>([]);
  const [exitPath, setExitPath] = useState<ExitPathValue>({ paths: [''], mode: '' });
  const [useTun, setUseTun] = useState(false);
  const [tunExitVia, setTunExitVia] = useState('');
  const [priority, setPriority] = useState<string>('0');
  const [ruleEnabled, setRuleEnabled] = useState(true);
  const [saving, setSaving] = useState(false);

  const resetForm = () => {
    setName(''); setTargets([]); setExitPath({ paths: [''], mode: '' });
    setUseTun(false); setTunExitVia(''); setPriority('0'); setRuleEnabled(true);
  };

  const openAdd = (e: MouseEvent) => {
    setClickPos({ x: e.clientX, y: e.clientY });
    setEditId(null);
    resetForm();
    setModalOpen(true);
  };

  const openEdit = (r: RoutingRule, e: MouseEvent) => {
    setClickPos({ x: e.clientX, y: e.clientY });
    setEditId(r.id);
    setName(r.name);
    setTargets([...r.targets]);
    setUseTun(!!r.use_tun);
    setTunExitVia(r.use_tun ? r.exit_via : '');
    setPriority(String(r.priority || 0));
    setExitPath(apiToExitPath(r.exit_via, r.exit_paths, r.exit_mode));
    setRuleEnabled(r.enabled);
    setModalOpen(true);
  };

  const handleSave = async () => {
    const targetList = targets.filter(Boolean);
    if (targetList.length === 0) { toast.error(t('rules.targetsRequired')); return; }

    let exitData: { exit_via: string; exit_paths?: string[]; exit_mode?: '' | 'quality' | 'aggregate' };
    if (useTun) {
      if (!tunExitVia.trim()) { toast.error(t('rules.exitRequired')); return; }
      exitData = { exit_via: tunExitVia.trim(), exit_paths: undefined, exit_mode: '' };
    } else {
      exitData = exitPathToApi(exitPath) as typeof exitData;
    }

    setSaving(true);
    try {
      const prioN = parseInt(priority, 10);
      const ruleData = {
        name, type: tab as 'ip' | 'domain', targets: targetList,
        ...exitData,
        use_tun: useTun || undefined,
        priority: Number.isFinite(prioN) ? prioN : 0,
        enabled: ruleEnabled,
      };
      if (editId) {
        await api.updateRule(editId, ruleData);
      } else {
        await api.createRule(ruleData);
      }
      toast.success(t('rules.saved'));
      queryClient.invalidateQueries({ queryKey: ['rules'] });
      setModalOpen(false);
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setSaving(false); }
  };

  const bulkToggle = useCallback(async (enabled: boolean) => {
    try {
      await Promise.all([...selection.selected].map((id) => api.toggleRule(id, enabled)));
      toast.success(`${enabled ? t('app.bulkEnable') : t('app.bulkDisable')}: ${selection.count}`);
      queryClient.invalidateQueries({ queryKey: ['rules'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [selection, queryClient, toast, t]);

  const bulkDelete = useCallback(async () => {
    const ok = await confirmDlg({
      title: t('app.bulkDelete'), message: t('rules.deleteConfirm'),
      danger: true, confirmText: t('app.delete'), cancelText: t('app.cancel'),
    });
    if (!ok) return;
    try {
      await Promise.all([...selection.selected].map((id) => api.deleteRule(id)));
      toast.success(`${t('app.bulkDelete')}: ${selection.count}`);
      queryClient.invalidateQueries({ queryKey: ['rules'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [selection, confirmDlg, queryClient, toast, t]);

  const unavailableBanner = !available ? (
    <div className="hy-warn-banner">
      {t('rules.unavailable')}
    </div>
  ) : null;

  const columns: Column<RoutingRule>[] = [
    { key: 'name', title: t('rules.name'), render: (r) => (
      <span style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        <strong>{r.name || '—'}</strong>
        {r.tun_active && <Badge variant="blue">{t('rules.tun')}</Badge>}
      </span>
    )},
    { key: 'targets', title: t('rules.targets'), render: (r) => <span className="mono" style={{ fontSize: 11 }}>{r.targets.slice(0, 3).join(', ')}{r.targets.length > 3 ? ` +${r.targets.length - 3}` : ''}</span> },
    { key: 'exit', title: t('rules.exitVia'), render: (r) => <ExitViaCell exitVia={r.exit_via} exitPaths={r.exit_paths} exitMode={r.exit_mode} /> },
    {
      key: 'actions', title: '', width: '40px', render: (r) => (
        <button className="hy-row-edit" onClick={(e) => openEdit(r, e as any)} title={t('app.edit')}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M11 4H4a2 2 0 00-2 2v14a2 2 0 002 2h14a2 2 0 002-2v-7"/><path d="M18.5 2.5a2.12 2.12 0 013 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
        </button>
      ),
    },
  ];

  const tabTitle = tab === 'ip' ? t('rules.ipRules') : t('rules.domainRules');

  return (
    <div className="hy-page">
      <Tabs
        items={[
          { key: 'ip', label: t('rules.ipRules') },
          { key: 'domain', label: t('rules.domainRules') },
        ]}
        activeKey={tab}
        onChange={setTab}
      />

      <TabPanel fill activeKey={tab} keys={['ip', 'domain']}>
        {unavailableBanner}
        <Card
          fill={1}
          title={tabTitle}
          count={currentRules.length}
          actions={
            <ResponsiveActions
              selectedCount={selection.count}
              onClearSelection={selection.clear}
              selectedLabel={t('app.selected', { count: selection.count })}
            >
              {(() => {
                const sel = [...selection.selected];
                const items = sel.map((id) => currentRules.find((r) => r.id === id)).filter(Boolean);
                const hasDisabled = items.some((r) => !r!.enabled);
                const hasEnabled = items.some((r) => r!.enabled);
                return <>
                  {hasDisabled && <Button size="sm" onClick={() => bulkToggle(true)}>{t('app.bulkEnable')}</Button>}
                  {hasEnabled && <Button size="sm" onClick={() => bulkToggle(false)}>{t('app.bulkDisable')}</Button>}
                  {selection.count > 0 && <Button size="sm" variant="danger" onClick={bulkDelete}>{t('app.bulkDelete')}</Button>}
                </>;
              })()}
              <ImportExportButton target="rules" />
              <Button size="sm" variant="primary" onClick={openAdd}>{t('rules.newRule')}</Button>
            </ResponsiveActions>
          }
          noPadding
        >
          <Table
            columns={columns}
            data={currentRules}
            rowKey={(r) => r.id}
            rowClassName={(r) => !r.enabled ? 'disabled-row' : undefined}
            emptyText={t('rules.noRules')}
            selection={selection}
          />
        </Card>
      </TabPanel>

      {/* Rule Modal — type determined by active tab */}
      <Modal
        open={modalOpen} onClose={() => setModalOpen(false)}
        title={editId ? t('app.edit') : t('rules.newRule')}
        animateFrom={clickPos}
        footer={
          <>
            <Button onClick={() => setModalOpen(false)}>{t('app.cancel')}</Button>
            <Button variant="primary" onClick={handleSave} loading={saving}>
              {editId ? t('app.save') : t('rules.add')}
            </Button>
          </>
        }
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <FormGroup label={t('rules.name')}>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('tls.optional')} />
          </FormGroup>
          <TargetList type={tab as 'ip' | 'domain'} value={targets} onChange={setTargets} />

          <div style={{ display: 'flex', gap: 14, alignItems: 'flex-start' }}>
            <div style={{ flex: tab === 'ip' ? '0 0 120px' : 1 }}>
              <FormGroup label={t('rules.priority')}>
                <Input
                  type="number"
                  value={priority}
                  onChange={(e) => setPriority(e.target.value)}
                  placeholder="0"
                />
              </FormGroup>
            </div>
            {tab === 'ip' && (
              <div style={{ flex: 1 }}>
                <FormGroup label={t('rules.useTun')}>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                    <Toggle checked={useTun} onChange={(e) => {
                      const on = e.target.checked;
                      setUseTun(on);
                      if (on && !tunExitVia) {
                        const first = exitPath.paths.find(Boolean);
                        if (first) setTunExitVia(first);
                      }
                    }} />
                    <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('rules.useTunHint')}</div>
                  </div>
                </FormGroup>
              </div>
            )}
          </div>

          {useTun ? (
            <FormGroup label={t('rules.exitVia')} required>
              <Autocomplete
                value={tunExitVia}
                onChange={setTunExitVia}
                options={exitPaths}
                placeholder={t('users.exitViaHint')}
              />
            </FormGroup>
          ) : (
            <ExitPathList value={exitPath} onChange={setExitPath} label={t('rules.exitVia')} />
          )}
        </div>
      </Modal>
    </div>
  );
}
