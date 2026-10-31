import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Button, FormGroup, Input, Modal, useConfirm, useToast } from '@hy2scale/ui';
import {
  createCondition, createPolicySet, deleteCondition, deletePolicySet, updateCondition, updatePolicySet,
  getPolicyCapabilities, PolicyValidationError,
  type ConditionSet, type End, type PolicyConfig, type PolicySet, type Rule,
} from '@/api/policy';
import * as api from '@/api';
import { useDialogInit } from '@/hooks/useFormInit';
import FlowEditor from './FlowEditor';
import { type SetKind } from './shared';
import { RefsLine } from './RefsLine';

/** Dialog of one condition set or policy set: its name and ordered rules. */
export default function SetModal({ open, onClose, animateFrom, kind, editing, cfg }: {
  open: boolean; onClose: () => void; animateFrom?: { x: number; y: number };
  kind: SetKind; editing: ConditionSet | PolicySet | null; cfg?: PolicyConfig;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const qc = useQueryClient();
  const { data: users = [] } = useQuery({ queryKey: ['users'], queryFn: api.getUsers, enabled: open });
  const { data: caps } = useQuery({ queryKey: ['policyCaps'], queryFn: getPolicyCapabilities, enabled: open && kind === 'policy', retry: false });
  const [name, setName] = useState('');
  const [rules, setRules] = useState<Rule[]>([]);
  const [def, setDef] = useState<End | undefined>(undefined);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<{ msg: string; path?: string } | null>(null);

  useDialogInit(open, `${kind}:${editing?.id ?? ''}`, () => {
    setName(editing?.name ?? '');
    setRules(editing ? editing.rules.map((r) => ({ ...r })) : []);
    setDef(editing?.default);
    setError(null);
  });

  const isPolicy = kind === 'policy';
  const condCtx = {
    conditions: (cfg?.conditions || []).filter((c) => !(kind === 'condition' && c.id === editing?.id)).map((c) => ({ id: c.id, name: c.name })),
    users: users.map((u) => ({ id: u.id, name: u.username })),
  };
  const callable = (cfg?.policies || []).filter((p) => !(isPolicy && p.id === editing?.id)).map((p) => ({ id: p.id, name: p.name }));

  const save = async () => {
    if (!name.trim()) { setError({ msg: t('policy.nameRequired') }); return; }
    setSaving(true);
    setError(null);
    try {
      const body = { name: name.trim(), rules, default: def };
      if (isPolicy) {
        const res = editing ? await updatePolicySet({ ...(editing as PolicySet), ...body }) : await createPolicySet(body);
        if (res?.warnings?.length) toast.info(res.warnings.join('; '));
      } else if (editing) await updateCondition({ ...(editing as ConditionSet), ...body });
      else await createCondition(body);
      await qc.invalidateQueries({ queryKey: ['policy'] });
      toast.success(t('policy.saved'));
      onClose();
    } catch (e: any) {
      setError({ msg: String(e?.message || e), path: e instanceof PolicyValidationError ? e.path : undefined });
    } finally {
      setSaving(false);
    }
  };

  const remove = async () => {
    if (!editing) return;
    if (!await confirm({ title: t('app.delete'), message: t('policy.deleteConfirm', { name: editing.name }), danger: true, confirmText: t('app.delete') })) return;
    try {
      if (isPolicy) await deletePolicySet(editing.id); else await deleteCondition(editing.id);
      await qc.invalidateQueries({ queryKey: ['policy'] });
      onClose();
    } catch (e: any) {
      const refs = e instanceof PolicyValidationError && e.refs?.length ? e.refs.map((r) => r.name).join(', ') : '';
      toast.error(refs ? t('policy.inUse', { refs }) : String(e?.message || e));
    }
  };

  const title = editing
    ? t(isPolicy ? 'policy.editPolicy' : 'policy.editCondition', { name: editing.name })
    : t(isPolicy ? 'policy.newPolicy' : 'policy.newCondition');

  return (
    <Modal open={open} onClose={onClose} animateFrom={animateFrom} wide title={title}
      footer={(
        <>
          {editing && <Button variant="danger" onClick={remove} data-testid="set-delete">{t('app.delete')}</Button>}
          <span className="spacer" />
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          <Button variant="primary" onClick={save} loading={saving} data-testid="set-save">{t('app.save')}</Button>
        </>
      )}>
      <div className="pset" data-testid="set-modal">
        <FormGroup label={t('policy.name')} required>
          <Input value={name} onChange={(e) => setName(e.target.value)} data-testid="set-name" />
        </FormGroup>
        {editing?.refs && editing.refs.length > 0 && <RefsLine refs={editing.refs} />}

        <div className="pset-rules-title">{t('policy.rules')}</div>
        <FlowEditor rules={rules} onRules={setRules} def={def} onDef={setDef}
          ctx={{ kind, cond: condCtx, policies: callable, ifaceAvailable: caps?.iface_terminal?.available ?? true }} />
        {error && (
          <div className="pset-error" data-testid="set-error">
            {error.msg}{error.path && <span className="mono"> · {error.path}</span>}
          </div>
        )}
      </div>
    </Modal>
  );
}
