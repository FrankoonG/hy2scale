import { useState, useCallback, type MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Card, Button, Table, Toggle, Badge, Modal, Input, Select, Textarea,
  FormGroup, useToast, useConfirm, useSelection, type Column,
} from '@hy2scale/ui';
import { ExitPathList, exitPathToApi, apiToExitPath, type ExitPathValue } from '@/components/ExitPathList';
import { ExitViaCell } from '@/components/ExitViaCell';
import ImportExportButton from '@/components/ImportExportButton';
import BulkActionBar from '@/components/BulkActionBar';
import type { RoutingRule } from '@/api';
import * as api from '@/api';

export default function RulesPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirmDlg = useConfirm();
  const queryClient = useQueryClient();

  const { data } = useQuery({ queryKey: ['rules'], queryFn: api.getRules });
  const { data: tunMode } = useQuery({ queryKey: ['tunMode'], queryFn: api.getTunMode });

  const rules = data?.rules || [];
  const available = data?.available !== false;

  const selection = useSelection(rules.map((r) => r.id));

  const [modalOpen, setModalOpen] = useState(false);
  const [editId, setEditId] = useState<string | null>(null);
  const [clickPos, setClickPos] = useState<{ x: number; y: number } | undefined>();

  // Rule form
  const [name, setName] = useState('');
  const [ruleType, setRuleType] = useState<'ip' | 'domain'>('ip');
  const [targets, setTargets] = useState('');
  const [exitPath, setExitPath] = useState<ExitPathValue>({ paths: [''], mode: '' });
  const [ruleEnabled, setRuleEnabled] = useState(true);
  const [saving, setSaving] = useState(false);

  const openAdd = (e: MouseEvent) => {
    setClickPos({ x: e.clientX, y: e.clientY });
    setEditId(null);
    setName(''); setRuleType('ip'); setTargets(''); setExitPath({ paths: [''], mode: '' }); setRuleEnabled(true);
    setModalOpen(true);
  };

  const openEdit = (r: RoutingRule, e: MouseEvent) => {
    setClickPos({ x: e.clientX, y: e.clientY });
    setEditId(r.id);
    setName(r.name); setRuleType(r.type); setTargets(r.targets.join('\n'));
    setExitPath(apiToExitPath(r.exit_via, r.exit_paths, r.exit_mode)); setRuleEnabled(r.enabled);
    setModalOpen(true);
  };

  const handleSave = async () => {
    const targetList = targets.split('\n').map((l) => l.trim()).filter(Boolean);
    if (targetList.length === 0) { toast.error(t('rules.targetsRequired')); return; }
    const exitData = exitPathToApi(exitPath);
    if (!exitData.exit_via) { toast.error(t('rules.exitRequired')); return; }

    setSaving(true);
    try {
      const ruleData = { name, type: ruleType, targets: targetList, ...exitData, enabled: ruleEnabled };
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

  // Bulk actions
  const bulkToggle = useCallback(async (enabled: boolean) => {
    try {
      await Promise.all([...selection.selected].map((id) => api.toggleRule(id, enabled)));
      toast.success(`${enabled ? t('app.bulkEnable') : t('app.bulkDisable')}: ${selection.count}`);
      selection.clear();
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
      selection.clear();
      queryClient.invalidateQueries({ queryKey: ['rules'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  }, [selection, confirmDlg, queryClient, toast, t]);

  // TUN mode handlers
  const handleTunToggle = async () => {
    try {
      await api.updateTunMode({ enabled: !tunMode?.enabled });
      toast.success(tunMode?.enabled ? t('rules.tunDisabled') : t('rules.tunEnabled'));
      queryClient.invalidateQueries({ queryKey: ['tunMode'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  };

  const handleTunMode = async (mode: 'mixed' | 'full') => {
    try {
      await api.updateTunMode({ mode });
      toast.success(t('rules.tunModeChanged'));
      queryClient.invalidateQueries({ queryKey: ['tunMode'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
  };

  if (!available) {
    return (
      <Card title={t('rules.title')}>
        <div style={{ color: 'var(--text-muted)', fontSize: 13 }}>{t('rules.unavailable')}</div>
      </Card>
    );
  }

  const columns: Column<RoutingRule>[] = [
    { key: 'name', title: t('rules.name'), render: (r) => <strong>{r.name}</strong> },
    { key: 'type', title: '', width: '70px', render: (r) => <Badge variant={r.type === 'ip' ? 'blue' : 'orange'}>{r.type === 'ip' ? 'IP' : 'Domain'}</Badge> },
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

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
      {/* TUN Mode Section */}
      <Card title={t('rules.tunMode')}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('rules.tunDesc')}</div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            <Toggle checked={tunMode?.enabled || false} onChange={handleTunToggle} />
            <Badge variant={tunMode?.enabled ? (tunMode.status === 'active' ? 'green' : 'orange') : 'muted'}>
              {tunMode?.enabled ? (tunMode.status === 'active' ? t('rules.tunActive') : t('rules.tunStarting')) : t('rules.tunInactive')}
            </Badge>
          </div>
          {tunMode?.enabled && (
            <div style={{ display: 'flex', gap: 12, fontSize: 12 }}>
              <label style={{ display: 'flex', alignItems: 'center', gap: 4, cursor: 'pointer' }}>
                <input type="radio" checked={tunMode.mode === 'mixed'} onChange={() => handleTunMode('mixed')} />
                {t('rules.tunModeMixed')}
              </label>
              <label style={{ display: 'flex', alignItems: 'center', gap: 4, cursor: 'pointer' }}>
                <input type="radio" checked={tunMode.mode === 'full'} onChange={() => handleTunMode('full')} />
                {t('rules.tunModeFull')}
              </label>
            </div>
          )}
        </div>
      </Card>

      {/* Rules Table */}
      <Card
        title={t('rules.title')}
        count={rules.length}
        actions={
          <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <BulkActionBar count={selection.count} onClear={selection.clear}>
              <Button size="sm" onClick={() => bulkToggle(true)}>{t('app.bulkEnable')}</Button>
              <Button size="sm" onClick={() => bulkToggle(false)}>{t('app.bulkDisable')}</Button>
              <Button size="sm" variant="danger" onClick={bulkDelete}>{t('app.bulkDelete')}</Button>
            </BulkActionBar>
            <ImportExportButton target="rules" />
            <Button size="sm" variant="primary" onClick={openAdd}>{t('rules.newRule')}</Button>
          </div>
        }
        noPadding
      >
        <Table
          columns={columns}
          data={rules}
          rowKey={(r) => r.id}
          rowClassName={(r) => !r.enabled ? 'disabled-row' : undefined}
          emptyText={t('rules.noRules')}
          selection={selection}
        />
      </Card>

      {/* Rule Modal */}
      <Modal
        open={modalOpen} onClose={() => setModalOpen(false)}
        title={editId ? t('app.edit') : t('rules.newRule')}
        animateFrom={clickPos}
        footer={
          <>
            <Button onClick={() => setModalOpen(false)}>{t('app.cancel')}</Button>
            <Button variant="primary" onClick={handleSave} loading={saving}>{t('app.save')}</Button>
          </>
        }
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <FormGroup label={t('rules.name')}>
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </FormGroup>
          <FormGroup label="Type">
            <Select
              value={ruleType}
              onChange={(e) => setRuleType(e.target.value as any)}
              options={[
                { value: 'ip', label: t('rules.ipRules') },
                { value: 'domain', label: t('rules.domainRules') },
              ]}
            />
          </FormGroup>
          <FormGroup label={ruleType === 'ip' ? t('rules.ipTargets') : t('rules.domainTargets')} required>
            <Textarea
              value={targets}
              onChange={(e) => setTargets(e.target.value)}
              rows={5}
              monospace
              placeholder={ruleType === 'ip' ? '1.2.3.0/24\n5.6.7.8' : 'example.com\n*.test.com'}
            />
          </FormGroup>
          <ExitPathList value={exitPath} onChange={setExitPath} label={t('rules.exitVia')} />
          <FormGroup label={t('app.enabled')}>
            <Toggle checked={ruleEnabled} onChange={(e) => setRuleEnabled(e.target.checked)} />
          </FormGroup>
        </div>
      </Modal>
    </div>
  );
}
