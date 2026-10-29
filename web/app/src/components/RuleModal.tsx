import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { Modal, Button, Input, FormGroup, FormStack, useToast } from '@hy2scale/ui';
import { ExitPathList, exitPathToApi, apiToExitPath, type ExitPathValue } from '@/components/ExitPathList';
import { TargetList } from '@/components/TargetList';
import { useDialogInit } from '@/hooks/useFormInit';
import type { RoutingRule } from '@/api';
import * as api from '@/api';

interface Props {
  open: boolean;
  onClose: () => void;
  animateFrom?: { x: number; y: number };
  /** Rule list the dialog adds to (the active tab). */
  type: 'ip' | 'domain';
  /** null = new rule. */
  editing: RoutingRule | null;
}

/** Add / edit one routing rule. */
export default function RuleModal({ open, onClose, animateFrom, type, editing }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();

  const [name, setName] = useState('');
  const [targets, setTargets] = useState<string[]>([]);
  const [exitPath, setExitPath] = useState<ExitPathValue>({ paths: [''], mode: '' });
  const [priority, setPriority] = useState('0');
  const [enabled, setEnabled] = useState(true);
  const [saving, setSaving] = useState(false);
  // TargetList seeds its rows on mount: remount it after each init.
  const [seed, setSeed] = useState(0);

  useDialogInit(open, editing?.id ?? null, () => {
    setName(editing?.name ?? '');
    setTargets(editing ? [...editing.targets] : []);
    setPriority(String(editing?.priority || 0));
    setExitPath(editing ? apiToExitPath(editing.exit_via, editing.exit_paths, editing.exit_mode) : { paths: [''], mode: '' });
    setEnabled(editing ? editing.enabled : true);
    setSeed((n) => n + 1);
  });

  const handleSave = async () => {
    const targetList = targets.filter(Boolean);
    if (targetList.length === 0) { toast.error(t('rules.targetsRequired')); return; }

    const exitData = exitPathToApi(exitPath) as { exit_via: string; exit_paths?: string[]; exit_mode?: '' | 'quality' | 'aggregate' };
    if (!exitData.exit_via) { toast.error(t('rules.exitRequired')); return; }

    setSaving(true);
    try {
      const prioN = parseInt(priority, 10);
      const ruleData = {
        id: editing?.id || `${type}-${Date.now().toString(36)}`,
        name, type, targets: targetList,
        ...exitData,
        priority: Number.isFinite(prioN) ? prioN : 0,
        enabled,
      };
      if (editing) await api.updateRule(editing.id, ruleData);
      else await api.createRule(ruleData);
      toast.success(t('rules.saved'));
      queryClient.invalidateQueries({ queryKey: ['rules'] });
      onClose();
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setSaving(false); }
  };

  return (
    <Modal
      open={open} onClose={onClose}
      title={editing ? t('app.edit') : t('rules.newRule')}
      animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          <Button variant="primary" onClick={handleSave} loading={saving}>
            {editing ? t('app.save') : t('rules.add')}
          </Button>
        </>
      }
    >
      <FormStack>
        <FormGroup label={t('rules.name')}>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('tls.optional')} />
        </FormGroup>
        <TargetList key={seed} type={type} value={targets} onChange={setTargets} />
        <FormGroup label={t('rules.priority')} className="field-narrow">
          <Input type="number" value={priority} onChange={(e) => setPriority(e.target.value)} placeholder="0" />
        </FormGroup>
        <ExitPathList value={exitPath} onChange={setExitPath} label={t('rules.exitVia')} />
      </FormStack>
    </Modal>
  );
}
