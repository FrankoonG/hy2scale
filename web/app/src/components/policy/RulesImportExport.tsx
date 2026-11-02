import { useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import {
  Button, Modal, Toggle, FormStack, Hint, IconDownload, IconUpload, useToast, useModalState, type ExpandAction,
} from '@hy2scale/ui';
import * as api from '@/api';
import {
  createCondition, createPolicySet, deleteCondition, deletePolicySet, getPolicy, updateCondition, updatePolicySet, updatePolicySettings,
  type PolicyConfig, type PolicySet,
} from '@/api/policy';
import { downloadBlob } from '@/utils/download';

const loadFile = () => import('./rulesFile');

/**
 * Export / import of every set as one YAML file (sets referenced by name).
 * Actions for the new-set button's menu, plus the import dialog.
 */
export function useRulesImportExport(cfg: PolicyConfig | undefined): { actions: ExpandAction[]; modal: ReactNode } {
  const { t } = useTranslation();
  const toast = useToast();
  const qc = useQueryClient();
  const fileRef = useRef<HTMLInputElement>(null);
  const dlg = useModalState();
  const [overwrite, setOverwrite] = useState(false);
  const [processing, setProcessing] = useState(false);
  const [result, setResult] = useState<{ text: string; errors: string[] } | null>(null);

  const doExport = async () => {
    try {
      const [{ exportRules }, users] = await Promise.all([loadFile(), api.getUsers()]);
      const text = exportRules(cfg ?? await getPolicy(), users);
      downloadBlob(new Blob([text], { type: 'application/yaml' }), 'rules.yaml');
      toast.success(t('import.exported'));
    } catch (e: any) {
      toast.error(String(e?.message || e));
    }
  };

  const doImport = async (file: File) => {
    setProcessing(true);
    setResult(null);
    const errors: string[] = [];
    let added = 0;
    let skipped = 0;
    try {
      const { parseRules, resolveSet } = await loadFile();
      const data = parseRules(await file.text());
      const [cur, users] = await Promise.all([getPolicy(), api.getUsers()]);
      const cond = new Map(cur.conditions.map((s) => [s.name, s.id]));
      const policy = new Map(cur.policies.map((s) => [s.name, s.id]));
      const user = new Map(users.map((u) => [u.username, u.id]));

      // 1. every imported set gets an id: an existing one (overwrite), a new one, or none (skipped)
      type Job = { kind: 'condition' | 'policy'; set: (typeof data.conditions)[number]; id: string; made?: boolean };
      const jobs: Job[] = [];
      for (const [kind, list, ids] of [['condition', data.conditions, cond], ['policy', data.policies, policy]] as const) {
        for (const s of list) {
          const have = ids.get(s.name);
          if (have && !overwrite) { skipped++; continue; }
          if (have) { jobs.push({ kind, set: s, id: have }); continue; }
          try {
            const made: PolicySet = kind === 'condition' ? await createCondition({ name: s.name, rules: [] }) : await createPolicySet({ name: s.name, rules: [] });
            ids.set(s.name, made.id);
            jobs.push({ kind, set: s, id: made.id, made: true });
          } catch (e: any) {
            errors.push(`${s.name}: ${e?.message || e}`);
          }
        }
      }
      // 2. their rules, references resolved (condition sets first)
      const failed: Job[] = [];
      for (const j of jobs) {
        const r = resolveSet(j.set, { cond, policy, user });
        if (r.missing.length) {
          errors.push(`${j.set.name}: ${t('rules.io.missing', { names: r.missing.join(', ') })}`);
          failed.push(j);
          continue;
        }
        try {
          const body = { id: j.id, name: j.set.name, rules: r.rules, ...(r.default ? { default: r.default } : {}) };
          if (j.kind === 'condition') await updateCondition(body); else await updatePolicySet(body);
          added++;
        } catch (e: any) {
          errors.push(`${j.set.name}: ${e?.message || e}`);
          failed.push(j);
        }
      }
      // sets created for an entry that failed are removed again (policies first)
      for (const j of [...failed].reverse()) {
        if (!j.made) continue;
        await (j.kind === 'condition' ? deleteCondition(j.id) : deletePolicySet(j.id)).catch(() => {});
        (j.kind === 'condition' ? cond : policy).delete(j.set.name);
      }
      // 3. the direct entry: set when there is none yet, or replaced on overwrite
      if (data.direct && (overwrite || !cur.direct)) {
        const id = policy.get(data.direct);
        if (id) await updatePolicySettings({ direct: id }).catch((e) => errors.push(`${t('policy.direct')}: ${e?.message || e}`));
      }
      setResult({ text: t('import.result', { added, skipped, errors: errors.length }), errors });
      await qc.invalidateQueries({ queryKey: ['policy'] });
    } catch (e: any) {
      toast.error(`${t('import.failed')}: ${e?.message || e}`);
    } finally {
      setProcessing(false);
    }
  };

  const close = () => { dlg.close(); setResult(null); };
  const actions: ExpandAction[] = [
    { key: 'import', label: t('import.import'), icon: IconUpload, onClick: (e) => dlg.show(undefined, e), testId: 'import-rules' },
    { key: 'export', label: t('import.export'), icon: IconDownload, onClick: doExport, testId: 'export-rules' },
  ];
  const modal = (
    <Modal open={dlg.open} onClose={close} title={t('import.importRules')} animateFrom={dlg.origin}
      footer={<Button onClick={close}>{t('app.close')}</Button>}>
      <FormStack>
        <div className="drop-target" role="button" tabIndex={0} data-testid="rules-import-drop"
          onClick={() => fileRef.current?.click()}
          onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); fileRef.current?.click(); } }}
          onDrop={(e) => { e.preventDefault(); const f = e.dataTransfer.files[0]; if (f) void doImport(f); }}
          onDragOver={(e) => e.preventDefault()}>
          {processing ? t('import.processing') : t('rules.io.dropHint')}
        </div>
        <input ref={fileRef} type="file" accept=".yaml,.yml" hidden data-testid="rules-import-file"
          onChange={(e) => { const f = e.target.files?.[0]; if (f) void doImport(f); e.target.value = ''; }} />
        <Toggle size="sm" label={t('import.overwrite')} checked={overwrite} onChange={(e) => setOverwrite(e.target.checked)} data-testid="rules-import-overwrite" />
        <Hint className="hint-small">{t('rules.io.overwriteHint')}</Hint>
        {result && (
          <div className="import-result" data-testid="rules-import-result">
            {result.text}
            {result.errors.map((x, i) => <div key={i} className="import-error">{x}</div>)}
          </div>
        )}
      </FormStack>
    </Modal>
  );
  return { actions, modal };
}
