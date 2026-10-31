import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Button, Modal } from '@hy2scale/ui';
import { Document, isMap, isNode, isScalar, isSeq, parseDocument, visit, type Node } from 'yaml';
import type { End, Rule } from '@/api/policy';
import { PolicyValidationError } from '@/api/policy';
import * as api from '@/api';
import { useNodeStore } from '@/store/node';
import { useDialogInit } from '@/hooks/useFormInit';
import type { SetKind } from './shared';

import YamlEditor from './YamlEditor';

export interface SetBody { name: string; rules: Rule[]; default?: End }

// Rules in YAML: nabled is written only when false; missing = true.
const forYaml = (rules: Rule[]): unknown[] => rules.map(({ enabled, then, ...r }) => ({
  ...r, ...(enabled ? {} : { enabled: false }),
  then: then.type === 'flow' ? { ...then, rules: forYaml(then.rules) } : then,
}));
const fromYaml = (rules: unknown): Rule[] => (Array.isArray(rules) ? rules : []).map((r: any) => ({
  ...r, enabled: r?.enabled !== false,
  ...(r?.then?.type === 'flow' ? { then: { ...r.then, rules: fromYaml(r.then.rules) } } : {}),
}));

/** The YAML text of a set; references carry the referenced set's name as a comment. */
export function toYaml(body: SetBody, names: Map<string, string>): string {
  const obj: Record<string, unknown> = { name: body.name, rules: forYaml(body.rules) };
  if (body.default) obj.default = body.default;
  const doc = new Document(obj);
  visit(doc, {
    Pair(_, pair) {
      const k = isScalar(pair.key) ? String(pair.key.value) : '';
      if ((k === 'ref' || k === 'policy') && isScalar(pair.value)) {
        const n = names.get(String(pair.value.value));
        if (n) pair.value.comment = ` ${n}`;
      }
    },
  });
  return doc.toString({ lineWidth: 0 });
}

/** An error path ("rules[1].then.paths[0]") → the closest YAML node's range. */
function rangeOf(text: string, path: string): [number, number] {
  const doc = parseDocument(text);
  const parts = path.split(/\.|\[(\d+)\]/).filter((x) => x !== undefined && x !== '').map((x) => (/^\d+$/.test(x) ? Number(x) : x));
  for (let n = parts.length; n >= 0; n--) {
    const node = doc.getIn(parts.slice(0, n), true);
    if (isNode(node) && (node as Node).range) {
      const r = (node as Node).range!;
      // a whole map / list: mark its first line only
      if (isMap(node) || isSeq(node)) {
        const end = text.indexOf('\n', r[0]);
        return [r[0], end < 0 ? text.length : end];
      }
      return [r[0], r[1]];
    }
  }
  return [0, Math.max(0, text.indexOf('\n'))];
}

/**
 * The YAML view of a set (stacked over its dialog). Saving parses the text,
 * saves through the same API as the form, and maps a validation error back
 * to its line.
 */
export default function YamlModal({ open, onClose, kind, body, names, conditions, policies, save }: {
  open: boolean;
  onClose: () => void;
  kind: SetKind;
  body: SetBody;
  names: Map<string, string>;
  conditions: { id: string; name: string }[];
  policies: { id: string; name: string }[];
  /** Saves the set; rejects with PolicyValidationError. */
  save: (b: SetBody) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [text, setText] = useState('');
  const [error, setError] = useState('');
  const [extra, setExtra] = useState<{ from: number; to: number; message: string }[]>([]);
  const [saving, setSaving] = useState(false);
  const { data: users = [] } = useQuery({ queryKey: ['users'], queryFn: api.getUsers, enabled: open });
  const { data: ifaces = [] } = useQuery({ queryKey: ['systemInterfaces'], queryFn: api.getInterfaces, enabled: open });
  const exitPaths = useNodeStore((s) => s.exitPaths);

  useDialogInit(open, 'yaml', () => {
    setText(toYaml(body, names));
    setError('');
    setExtra([]);
  });

  const submit = async () => {
    const doc = parseDocument(text);
    if (doc.errors.length) {
      const e = doc.errors[0];
      setError(t('policy.yamlSyntax', { line: e.linePos?.[0]?.line ?? '?', msg: e.message.split('\n')[0].replace(/ at line \d+, column \d+:?$/, '') }));
      return;
    }
    const obj = doc.toJS() as Partial<SetBody> | null;
    if (!obj || typeof obj !== 'object' || Array.isArray(obj)) {
      setError(t('policy.yamlNotMap'));
      return;
    }
    setSaving(true);
    setError('');
    try {
      await save({ name: String(obj.name ?? ''), rules: fromYaml(obj.rules), default: obj.default as End | undefined });
    } catch (e: any) {
      const msg = String(e?.message || e);
      setError(msg);
      if (e instanceof PolicyValidationError && e.path) {
        const [from, to] = rangeOf(text, e.path);
        setExtra([{ from, to, message: msg }]);
        setError(t('policy.yamlSyntax', { line: text.slice(0, from).split('\n').length, msg }));
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal open={open} onClose={onClose} wide title={t('policy.yamlTitle', { name: body.name || t(kind === 'policy' ? 'policy.newPolicy' : 'policy.newCondition') })}
      footer={(
        <>
          <span className="spacer" />
          <Button onClick={onClose}>{t('app.cancel')}</Button>
          <Button variant="primary" onClick={submit} loading={saving} data-testid="yaml-save">{t('app.save')}</Button>
        </>
      )}>
      <div className="yaml-wrap" data-testid="yaml-modal">
        <YamlEditor value={text} onChange={setText} extra={extra}
            data={{
              kind, conditions, policies, exitPaths: exitPaths || [],
              users: users.map((u) => ({ id: u.id, name: u.username })),
              ifaces: (ifaces as any[]).map((i) => i.name),
            }} />
        {error && <div className="pset-error" data-testid="yaml-error">{error}</div>}
      </div>
    </Modal>
  );
}
