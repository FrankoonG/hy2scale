import { createContext, memo, useContext } from 'react';
import { useTranslation } from 'react-i18next';
import { Handle, Position, type NodeProps } from '@xyflow/react';
import clsx from 'clsx';
import { describeCond, describeTerminal } from '../shared';
import type { Analysis, EndData, GNode, RuleData, SetData, SrcData } from './model';

/** What the nodes read besides their own data. */
export interface NodeCtx {
  a: Analysis;
  errors: Map<string, string>;
  direct: string;          // set node id of the direct entry
  names: { cond: Map<string, string>; user: Map<string, string> };
  sets: Map<string, string>; // set id → name (hand-overs)
}
export const GraphCtx = createContext<NodeCtx | null>(null);
const useCtx = () => useContext(GraphCtx)!;

const Err = ({ id }: { id: string }) => {
  const msg = useCtx().errors.get(id);
  return msg ? <div className="rg-err" data-testid="rg-err">{msg}</div> : null;
};

export const SetNode = memo(({ id, data, selected }: NodeProps<GNode>) => {
  const { t } = useTranslation();
  const ctx = useCtx();
  const d = data as SetData;
  return (
    <div className={clsx('rg-node rg-set', `is-${d.setKind}`, selected && 'is-sel', ctx.errors.has(id) && 'is-error')} data-testid={`rg-set-${d.setId}`}>
      {d.setKind === 'policy' && <Handle type="target" position={Position.Left} id="in" className="rg-h" />}
      <div className="rg-set-kind">
        {t(d.setKind === 'policy' ? 'policy.policySet' : 'policy.condSet')}
        {ctx.direct === id && <span className="rg-tag">{t('policy.directEntryBadge')}</span>}
      </div>
      <div className="rg-set-name rg-ellip">{d.name || t('rules.graph.unnamed')}</div>
      <Handle type="source" position={Position.Bottom} id="out" className="rg-h" />
      {d.setKind === 'condition' && (
        <Handle type="source" position={Position.Right} id="ref" className="rg-h rg-h-ref" title={t('rules.graph.refPort')} />
      )}
      <Err id={id} />
    </div>
  );
});

export const RuleNode = memo(({ id, data, selected }: NodeProps<GNode>) => {
  const { t } = useTranslation();
  const ctx = useCtx();
  const d = data as RuleData;
  const idx = ctx.a.index.get(id);
  const cond = describeCond(d.when, t, ctx.names);
  const tail = ctx.a.tail.get(id);
  return (
    <div className={clsx('rg-node rg-rule', !d.enabled && 'is-off', !idx && 'is-orphan', selected && 'is-sel', ctx.errors.has(id) && 'is-error')}
      data-testid={`rg-rule-${d.ruleId}`} title={[d.name, cond].filter(Boolean).join('\n')}>
      <Handle type="target" position={Position.Top} id="in" className="rg-h" />
      <Handle type="target" position={Position.Left} id="cond" className="rg-h rg-h-cond" />
      <div className="rg-rule-head">
        {idx && <span className="rg-idx">{idx}</span>}
        <span className="rg-ellip">{d.name || cond}</span>
      </div>
      {d.name && <div className="rg-rule-cond rg-ellip">{cond}</div>}
      <span className="rg-port-label is-match">{t('rules.graph.match')}</span>
      <span className="rg-port-label is-else">{tail ? t(`rules.graph.tail.${tail}`) : t('rules.graph.else')}</span>
      <Handle type="source" position={Position.Right} id="match" className="rg-h rg-h-match" />
      <Handle type="source" position={Position.Bottom} id="else" className="rg-h rg-h-else" />
      <Err id={id} />
    </div>
  );
});

export const EndNode = memo(({ id, data, selected }: NodeProps<GNode>) => {
  const { t } = useTranslation();
  const ctx = useCtx();
  const e = (data as EndData).end;
  const text = describeTerminal(e, t, ctx.sets);
  const mode = e.type === 'exit' && e.mode ? t(e.mode === 'quality' ? 'exit.modeStability' : 'exit.modeSpeed') : '';
  const cls = e.type === 'result' ? (e.result ? 'is-true' : 'is-false') : `is-${e.type}`;
  return (
    <div className={clsx('rg-node rg-end', cls, selected && 'is-sel', ctx.errors.has(id) && 'is-error')} data-testid="rg-end" title={text}>
      <Handle type="target" position={Position.Left} id="in" className="rg-h" />
      <span className="rg-ellip">{text}</span>
      {mode && <span className="rg-end-mode">{mode}</span>}
      <Err id={id} />
    </div>
  );
});

export const SourceNode = memo(({ id, data, selected }: NodeProps<GNode>) => {
  const { t } = useTranslation();
  const ctx = useCtx();
  const d = data as SrcData;
  const locked = d.src === 'wireguard' || d.src === 'vpn';
  const lines = d.src === 'direct' ? [] : d.members.map((m) => m.name);
  return (
    <div className={clsx('rg-node rg-src', `is-${d.src}`, locked && 'is-locked', selected && 'is-sel', ctx.errors.has(id) && 'is-error')} data-testid={`rg-src-${d.src}`}>
      <div className="rg-src-head">
        {d.src === 'direct' ? t('policy.directEntryBadge') : t(`policy.refKind.${d.src}`)}
        {lines.length > 1 && <span className="rg-count">{lines.length}</span>}
      </div>
      {d.src === 'direct'
        ? <div className="rg-src-line rg-ellip">{t('rules.topo.noExit')}</div>
        : lines.length === 0
          ? <div className="rg-src-line is-empty">{t('rules.graph.noMembers')}</div>
          : [...lines.slice(0, 3), ...(lines.length > 3 ? [`+${lines.length - 3}`] : [])].map((n, i) => (
            <div key={i} className="rg-src-line rg-ellip">{n}</div>
          ))}
      <Handle type="source" position={Position.Right} id="out" className="rg-h" />
    </div>
  );
});

export const nodeTypes = { set: SetNode, rule: RuleNode, end: EndNode, source: SourceNode };
