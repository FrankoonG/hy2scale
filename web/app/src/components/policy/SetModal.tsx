import { lazy, Suspense, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Button, FormGroup, Input, Modal, Tabs, useConfirm, useToast } from '@hy2scale/ui';
import {
  createCondition, createPolicySet, deleteCondition, deletePolicySet, updateCondition, updatePolicySet,
  getPolicyCapabilities, PolicyValidationError,
  type ConditionSet, type End, type PolicyConfig, type PolicySet, type Rule,
} from '@/api/policy';
import * as api from '@/api';
import { useDialogInit } from '@/hooks/useFormInit';
import { useNodeStore } from '@/store/node';
import FlowEditor from './FlowEditor';
import { type SetKind } from './shared';
import { RefsLine } from './RefsLine';

// Config mode loads on demand: CodeMirror and the YAML parser stay out of
// the main bundle.
const ConfigEditor = lazy(() => import('./ConfigEditor'));
const loadConfig = () => import('./setConfig');

type Tab = 'flow' | 'config';
interface Diag { from: number; to: number; message: string }

/**
 * Dialog of one condition set or policy set: its name, and its rules
 * either as a flow (form) or in config mode (YAML). Leaving config mode
 * and saving from it both require the text to parse.
 */
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
  const [tab, setTab] = useState<Tab>('flow');
  const [text, setText] = useState('');
  const [diags, setDiags] = useState<Diag[]>([]);
  const { data: ifaces = [] } = useQuery({ queryKey: ['systemInterfaces'], queryFn: api.getInterfaces, enabled: open && tab === 'config' });
  const exitPaths = useNodeStore((s) => s.exitPaths);

  useDialogInit(open, `${kind}:${editing?.id ?? ''}`, () => {
    setName(editing?.name ?? '');
    setRules(editing ? editing.rules.map((r) => ({ ...r })) : []);
    setDef(editing?.default);
    setError(null);
    setTab('flow');
    setDiags([]);
  });

  const isPolicy = kind === 'policy';
  const condCtx = {
    conditions: (cfg?.conditions || []).filter((c) => !(kind === 'condition' && c.id === editing?.id)).map((c) => ({ id: c.id, name: c.name })),
    users: users.map((u) => ({ id: u.id, name: u.username })),
  };
  const callable = (cfg?.policies || []).filter((p) => !(isPolicy && p.id === editing?.id)).map((p) => ({ id: p.id, name: p.name }));
  const names = new Map([...(cfg?.conditions || []), ...(cfg?.policies || [])].map((s) => [s.id, s.name]));

  /** The config text as a flow; null (with the error shown) when it does not parse. */
  const parseText = async () => {
    const r = (await loadConfig()).parseConfig(text);
    if ('error' in r) {
      setError({ msg: t('policy.yamlSyntax', { line: r.error.line, msg: r.error.msg }) });
      setDiags([{ from: r.error.from, to: r.error.to, message: r.error.msg }]);
      return null;
    }
    return r.body;
  };

  const switchTab = async (k: string) => {
    if (k === tab) return;
    if (k === 'config') {
      setText((await loadConfig()).toYaml({ rules, default: def }, names));
      setDiags([]);
      setError(null);
      setTab('config');
      return;
    }
    const body = await parseText();
    if (!body) return;
    setRules(body.rules);
    setDef(body.default);
    setError(null);
    setTab('flow');
  };

  const save = async () => {
    if (!name.trim()) { setError({ msg: t('policy.nameRequired') }); return; }
    let body: { rules: Rule[]; default?: End } = { rules, default: def };
    if (tab === 'config') {
      const parsed = await parseText();
      if (!parsed) return;
      body = parsed;
    }
    setSaving(true);
    setError(null);
    try {
      const full = { name: name.trim(), ...body };
      if (isPolicy) {
        const res = editing ? await updatePolicySet({ ...(editing as PolicySet), ...full }) : await createPolicySet(full);
        if (res?.warnings?.length) toast.info(res.warnings.join('; '));
      } else if (editing) await updateCondition({ ...(editing as ConditionSet), ...full });
      else await createCondition(full);
      await qc.invalidateQueries({ queryKey: ['policy'] });
      toast.success(t('policy.saved'));
      onClose();
    } catch (e: any) {
      const msg = String(e?.message || e);
      const path = e instanceof PolicyValidationError ? e.path : undefined;
      // In config mode, a validation error is marked on its line.
      const at = tab === 'config' && path ? (await loadConfig()).locate(text, path) : null;
      if (at) {
        setDiags([{ from: at.from, to: at.to, message: msg }]);
        setError({ msg: t('policy.yamlSyntax', { line: at.line, msg }) });
      } else {
        setError({ msg, path });
      }
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

        <div className="pset-tabs" data-testid="set-tabs">
          <Tabs variant="underline" activeKey={tab} onChange={switchTab}
            items={[{ key: 'flow', label: t('policy.tabFlow') }, { key: 'config', label: t('policy.tabConfig') }]} />
        </div>
        {tab === 'flow' ? (
          <FlowEditor rules={rules} onRules={setRules} def={def} onDef={setDef}
            ctx={{ kind, cond: condCtx, policies: callable, ifaceAvailable: caps?.iface_terminal?.available ?? true }} />
        ) : (
          <Suspense fallback={<div className="config-editor" />}>
            <ConfigEditor value={text} onChange={(v) => { setText(v); setError(null); }} extra={diags}
              data={{
                kind, conditions: condCtx.conditions, policies: callable, exitPaths: exitPaths || [],
                users: condCtx.users, ifaces: (ifaces as any[]).map((i) => i.name),
              }} />
          </Suspense>
        )}
        {error && (
          <div className="pset-error" data-testid="set-error">
            {error.msg}{error.path && <span className="mono"> · {error.path}</span>}
          </div>
        )}
      </div>
    </Modal>
  );
}
