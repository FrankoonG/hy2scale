import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Input, Select, SegmentedControl } from '@hy2scale/ui';
import type { Action } from '@/api/policy';
import * as api from '@/api';
import { ExitPathList } from '@/components/ExitPathList';
import { newId, type SetKind } from './shared';

/** Choices of the action selector; true/false are the two result ends. */
type Choice = 'exit' | 'default' | 'iface' | 'call' | 'deny' | 'true' | 'false' | 'flow' | 'continue';

const ENDS: Record<SetKind, Choice[]> = {
  policy: ['exit', 'default', 'iface', 'call', 'deny'],
  condition: ['true', 'false'],
};

const choiceOf = (v: Action | undefined): Choice => {
  if (!v) return 'continue';
  if (v.type === 'result') return v.result ? 'true' : 'false';
  return v.type;
};

/**
 * A rule's then, or a flow's default: an end of the set's kind, a nested
 * flow (rules only), or "continue" (nested defaults only = undefined).
 */
export default function TerminalEditor({ value, onChange, kind, policies, ifaceAvailable, allowFlow, allowContinue }: {
  value: Action | undefined;
  onChange: (t: Action | undefined) => void;
  kind: SetKind;
  /** Policy sets that may be called (the edited one excluded). */
  policies: { id: string; name: string }[];
  ifaceAvailable: boolean;
  allowFlow?: boolean;
  allowContinue?: boolean;
}) {
  const { t } = useTranslation();
  const cur = choiceOf(value);
  const { data: ifaces = [] } = useQuery({ queryKey: ['systemInterfaces'], queryFn: api.getInterfaces, enabled: cur === 'iface' });

  const switchTo = (k: Choice) => {
    if (k === cur) return;
    switch (k) {
      case 'deny': return onChange({ type: 'deny', reject: true });
      case 'exit': return onChange({ type: 'exit', paths: [''], mode: '' });
      case 'iface': return onChange({ type: 'iface', iface: '' });
      case 'call': return onChange({ type: 'call', policy: policies[0]?.id || '' });
      case 'default': return onChange({ type: 'default' });
      case 'true': return onChange({ type: 'result', result: true });
      case 'false': return onChange({ type: 'result', result: false });
      case 'flow': return onChange({ type: 'flow', rules: [{ id: newId(), enabled: true, then: kind === 'policy' ? { type: 'exit', paths: [''], mode: '' } : { type: 'result', result: true } }] });
      case 'continue': return onChange(undefined);
    }
  };

  const choices: Choice[] = [
    ...ENDS[kind]
      .filter((k) => k !== 'iface' || ifaceAvailable || cur === 'iface')
      .filter((k) => k !== 'call' || policies.length > 0 || cur === 'call'),
    ...(allowFlow ? ['flow' as Choice] : []),
    ...(allowContinue ? ['continue' as Choice] : []),
  ];

  return (
    <div className="term" data-testid="term-editor">
      <div className="term-kind" data-testid="term-kind">
        <Select value={cur} onChange={(e) => switchTo(e.target.value as Choice)}
          options={choices.map((k) => ({ value: k, label: t(`policy.termKind.${k}`) }))} />
      </div>
      {value?.type === 'deny' && (
        <SegmentedControl<'reject' | 'drop'>
          items={[{ key: 'reject', label: t('policy.term.reject') }, { key: 'drop', label: t('policy.term.drop') }]}
          value={value.reject ? 'reject' : 'drop'}
          onChange={(k) => onChange({ type: 'deny', reject: k === 'reject' })}
        />
      )}
      {value?.type === 'exit' && (
        <div className="term-exit">
          <ExitPathList
            label=""
            value={{ paths: value.paths.length ? value.paths : [''], mode: value.mode || '' }}
            onChange={(v) => onChange({ type: 'exit', paths: v.paths, mode: v.mode })}
          />
        </div>
      )}
      {value?.type === 'iface' && (
        <div className="term-iface">
          <Select
            value={value.iface}
            placeholder={t('policy.pickIface')}
            onChange={(e) => onChange({ ...value, iface: e.target.value })}
            options={ifaces.map((i: any) => ({ value: i.name, label: i.name }))}
          />
          <Input value={value.src || ''} placeholder={t('policy.srcAddrOptional')}
            onChange={(e) => onChange({ ...value, src: e.target.value.trim() || undefined })} />
        </div>
      )}
      {value?.type === 'call' && (
        <Select value={value.policy} onChange={(e) => onChange({ type: 'call', policy: e.target.value })}
          options={policies.map((p) => ({ value: p.id, label: p.name }))} />
      )}
    </div>
  );
}
