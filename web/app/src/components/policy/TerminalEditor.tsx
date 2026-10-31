import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Input, Select, SegmentedControl } from '@hy2scale/ui';
import type { Terminal } from '@/api/policy';
import * as api from '@/api';
import { ExitPathList } from '@/components/ExitPathList';

type Kind = Terminal['type'];
const KINDS: Kind[] = ['exit', 'default', 'iface', 'call', 'deny'];

/** Where a policy rule sends a matching connection. */
export default function TerminalEditor({ value, onChange, policies, ifaceAvailable }: {
  value: Terminal;
  onChange: (t: Terminal) => void;
  /** Policy sets that may be called (the edited one excluded). */
  policies: { id: string; name: string }[];
  ifaceAvailable: boolean;
}) {
  const { t } = useTranslation();
  const { data: ifaces = [] } = useQuery({ queryKey: ['systemInterfaces'], queryFn: api.getInterfaces, enabled: value.type === 'iface' });

  const switchTo = (k: Kind) => {
    if (k === value.type) return;
    switch (k) {
      case 'deny': return onChange({ type: 'deny', reject: true });
      case 'exit': return onChange({ type: 'exit', paths: [''], mode: '' });
      case 'iface': return onChange({ type: 'iface', iface: '' });
      case 'call': return onChange({ type: 'call', policy: policies[0]?.id || '' });
      default: return onChange({ type: 'default' });
    }
  };

  return (
    <div className="term" data-testid="term-editor">
      <div className="term-kind" data-testid="term-kind">
        <Select
          value={value.type}
          onChange={(e) => switchTo(e.target.value as Kind)}
          options={KINDS.filter((k) => k !== 'iface' || ifaceAvailable || value.type === 'iface').filter((k) => k !== 'call' || policies.length > 0 || value.type === 'call')
            .map((k) => ({ value: k, label: t(`policy.termKind.${k}`) }))}
        />
      </div>
      {value.type === 'deny' && (
        <SegmentedControl<'reject' | 'drop'>
          items={[{ key: 'reject', label: t('policy.term.reject') }, { key: 'drop', label: t('policy.term.drop') }]}
          value={value.reject === false ? 'drop' : 'reject'}
          onChange={(k) => onChange({ type: 'deny', reject: k === 'reject' })}
        />
      )}
      {value.type === 'exit' && (
        <div className="term-exit">
          <ExitPathList
            label=""
            value={{ paths: value.paths.length ? value.paths : [''], mode: value.mode || '' }}
            onChange={(v) => onChange({ type: 'exit', paths: v.paths, mode: v.mode })}
          />
        </div>
      )}
      {value.type === 'iface' && (
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
      {value.type === 'call' && (
        <Select value={value.policy} onChange={(e) => onChange({ type: 'call', policy: e.target.value })}
          options={policies.map((p) => ({ value: p.id, label: p.name }))} />
      )}
    </div>
  );
}
