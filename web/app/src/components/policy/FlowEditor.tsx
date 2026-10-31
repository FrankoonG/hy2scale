import { useTranslation } from 'react-i18next';
import { Input, SortableList, Toggle } from '@hy2scale/ui';
import type { Action, End, Rule } from '@/api/policy';
import CondEditor, { type CondContext } from './CondEditor';
import TerminalEditor from './TerminalEditor';
import { builtinDefault, newId, type SetKind } from './shared';

/** Nested flows deeper than this are refused by the node. */
export const MAX_FLOW_DEPTH = 8;

export interface FlowCtx {
  kind: SetKind;
  cond: CondContext;
  policies: { id: string; name: string }[];
  ifaceAvailable: boolean;
}

/**
 * One flow — a set's rules or a nested flow's: ordered rules (when → then)
 * and the end when none matches. The same editor serves both set kinds;
 * only the ends offered differ. A nested flow's end may be "continue".
 */
export default function FlowEditor({ rules, onRules, def, onDef, ctx, depth = 0, testId }: {
  rules: Rule[];
  onRules: (rs: Rule[]) => void;
  def: End | undefined;
  onDef: (d: End | undefined) => void;
  ctx: FlowCtx;
  depth?: number;
  testId?: string;
}) {
  const { t } = useTranslation();
  const nested = depth > 0;
  const update = (id: string, patch: Partial<Rule>) => onRules(rules.map((r) => (r.id === id ? { ...r, ...patch } : r)));
  const addRule = () => onRules([...rules, {
    id: newId(), enabled: true,
    then: ctx.kind === 'policy' ? { type: 'exit', paths: [''], mode: '' } : { type: 'result', result: true },
  }]);

  return (
    <div className={nested ? 'pflow nested' : 'pflow'} data-testid={testId ?? 'flow'}>
      <SortableList
        items={rules}
        onReorder={onRules}
        removeLabel={t('app.delete')}
        onRemove={(r) => onRules(rules.filter((x) => x.id !== r.id))}
        onAdd={addRule}
        addLabel={`+ ${t('policy.addRule')}`}
        minItems={0}
        testId={nested ? undefined : 'set-rules'}
        renderItem={(r) => (
          <div className={r.enabled ? 'prule' : 'prule off'} data-testid="set-rule">
            <div className="prule-head">
              <Toggle size="sm" checked={r.enabled} onChange={(e) => update(r.id, { enabled: e.target.checked })} />
              <Input className="prule-name" value={r.name || ''} placeholder={t('policy.ruleName')}
                onChange={(e) => update(r.id, { name: e.target.value || undefined })} />
            </div>
            <div className="prule-row">
              <span className="prule-k">{t('policy.when')}</span>
              <CondEditor value={r.when} onChange={(c) => update(r.id, { when: c })} ctx={ctx.cond} />
            </div>
            <div className="prule-row">
              <span className="prule-k">{t('policy.then')}</span>
              <TerminalEditor value={r.then} onChange={(x) => update(r.id, { then: x as Action })}
                kind={ctx.kind} policies={ctx.policies} ifaceAvailable={ctx.ifaceAvailable}
                allowFlow={depth + 1 < MAX_FLOW_DEPTH || r.then.type === 'flow'} />
            </div>
            {r.then.type === 'flow' && (() => {
              const f = r.then;
              return (
                <FlowEditor rules={f.rules} onRules={(rs) => update(r.id, { then: { ...f, rules: rs } })}
                  def={f.default} onDef={(d) => update(r.id, { then: { ...f, default: d } })}
                  ctx={ctx} depth={depth + 1} testId="sub-flow" />
              );
            })()}
          </div>
        )}
      />
      <div className="prule-default" data-testid={nested ? 'flow-default' : 'set-default'}>
        <span className="prule-k">{t('policy.otherwise')}</span>
        <TerminalEditor value={nested ? def : def ?? builtinDefault(ctx.kind)} onChange={(x) => onDef(x as End | undefined)}
          kind={ctx.kind} policies={ctx.policies} ifaceAvailable={ctx.ifaceAvailable} allowContinue={nested} />
      </div>
    </div>
  );
}
