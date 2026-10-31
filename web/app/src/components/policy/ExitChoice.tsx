import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { FormGroup, SegmentedControl, Select } from '@hy2scale/ui';
import { getPolicy } from '@/api/policy';
import { ExitPathList, type ExitPathValue } from '@/components/ExitPathList';

/**
 * Exit via: an exit group (paths, as before; empty = direct) or one policy
 * set. `policy` non-empty means the policy set is chosen.
 */
export default function ExitChoice({ policy, onPolicy, exit, onExit, label }: {
  policy: string; onPolicy: (id: string) => void;
  exit: ExitPathValue; onExit: (v: ExitPathValue) => void;
  label?: string;
}) {
  const { t } = useTranslation();
  const { data: cfg } = useQuery({ queryKey: ['policy'], queryFn: getPolicy, retry: false });
  const policies = cfg?.policies || [];
  const [mode, setMode] = useState<'exit' | 'policy'>(policy ? 'policy' : 'exit');
  useEffect(() => { setMode(policy ? 'policy' : 'exit'); }, [policy]);

  const pick = (m: 'exit' | 'policy') => {
    setMode(m);
    if (m === 'exit') onPolicy('');
    else if (!policy && policies[0]) onPolicy(policies[0].id);
  };

  return (
    <FormGroup label={label ?? t('users.exitVia')}>
      <div className="exit-choice" data-testid="exit-choice">
        <SegmentedControl<'exit' | 'policy'>
          items={[
            { key: 'exit', label: t('policy.exitGroup'), testId: 'exit-choice-exit' },
            { key: 'policy', label: t('policy.policySet'), testId: 'exit-choice-policy', disabled: policies.length === 0 },
          ]}
          value={mode}
          onChange={pick}
        />
        {mode === 'policy' ? (
          <div data-testid="exit-choice-select">
            <Select value={policy} onChange={(e) => onPolicy(e.target.value)}
              options={policies.map((p) => ({ value: p.id, label: p.name }))} />
          </div>
        ) : (
          <ExitPathList label="" value={exit} onChange={onExit} />
        )}
      </div>
    </FormGroup>
  );
}
