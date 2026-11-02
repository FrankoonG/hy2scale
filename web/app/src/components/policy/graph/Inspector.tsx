import { useTranslation } from 'react-i18next';
import { Button, FormGroup, Input, MultiSelect, Toggle, IconX } from '@hy2scale/ui';
import type { End } from '@/api/policy';
import CondEditor, { type CondContext } from '../CondEditor';
import TerminalEditor from '../TerminalEditor';
import type { GData, GNode, Member } from './model';

/**
 * The selected node's settings, beside the canvas. Edits apply to the
 * draft at once; the canvas' save writes them.
 */
export default function Inspector({ node, cond, shared, members, ifaceAvailable, onData, onMembers, onDelete, onClose, onDirect }: {
  node: GNode;
  cond: CondContext;
  /** edges into an end (it is shared when more than one) */
  shared: number;
  /** all users / proxies a group may take */
  members: { user: Member[]; proxy: Member[] };
  ifaceAvailable: boolean;
  onData: (patch: Partial<GData>, key?: string) => void;
  onMembers: (ids: string[]) => void;
  onDelete?: () => void;
  onClose: () => void;
  onDirect: (e: React.MouseEvent) => void;
}) {
  const { t } = useTranslation();
  const d = node.data;
  const title = d.kind === 'set' ? t(d.setKind === 'policy' ? 'policy.policySet' : 'policy.condSet')
    : d.kind === 'rule' ? t('rules.graph.rule')
      : d.kind === 'end' ? t('rules.graph.end')
        : d.src === 'direct' ? t('policy.directEntryBadge') : t(`policy.refKind.${d.src}`);

  return (
    <div className="rg-inspector" data-testid="rg-inspector" onKeyDown={(e) => e.stopPropagation()}>
      <div className="rg-inspector-head">
        <span>{title}</span>
        <button type="button" className="rg-inspector-x" onClick={onClose} aria-label={t('app.close')}><IconX size={14} /></button>
      </div>
      <div className="rg-inspector-body">
        {d.kind === 'set' && (
          <FormGroup label={t('policy.name')}>
            <Input value={d.name} autoFocus={!d.name} data-testid="rg-set-name"
              onChange={(e) => onData({ name: e.target.value }, `name:${node.id}`)} />
          </FormGroup>
        )}

        {d.kind === 'rule' && (
          <>
            <FormGroup label={t('policy.ruleName')}>
              <Input value={d.name || ''} data-testid="rg-rule-name"
                onChange={(e) => onData({ name: e.target.value || undefined }, `name:${node.id}`)} />
            </FormGroup>
            <Toggle label={t('rules.graph.enabled')} checked={d.enabled} data-testid="rg-rule-enabled"
              onChange={(e) => onData({ enabled: e.target.checked })} />
            <FormGroup label={t('policy.when')}>
              <CondEditor value={d.when} ctx={cond} onChange={(c) => onData({ when: c }, `when:${node.id}`)} />
            </FormGroup>
          </>
        )}

        {d.kind === 'end' && (
          <>
            <TerminalEditor value={d.end} kind={d.end.type === 'result' ? 'condition' : 'policy'} policies={[]}
              ifaceAvailable={ifaceAvailable}
              onChange={(v) => v && v.type !== 'flow' && v.type !== 'call' && onData({ end: v as End }, `end:${node.id}`)} />
            {shared > 1 && <div className="rg-inspector-note">{t('rules.graph.sharedEnd', { n: shared })}</div>}
          </>
        )}

        {d.kind === 'source' && (d.src === 'user' || d.src === 'proxy') && (
          <FormGroup label={t('rules.graph.members')}>
            <MultiSelect testId="rg-members"
              options={members[d.src].map((m) => ({ value: m.id, label: m.name }))}
              value={d.members.map((m) => m.id)}
              onChange={onMembers} />
          </FormGroup>
        )}
        {d.kind === 'source' && (d.src === 'wireguard' || d.src === 'vpn') && (
          <>
            <div className="rg-inspector-list">{d.members.map((m) => <div key={m.id}>{m.name}</div>)}</div>
            <div className="rg-inspector-note">{t('rules.graph.lockedSource')}</div>
          </>
        )}
        {d.kind === 'source' && d.src === 'direct' && (
          <Button onClick={onDirect}>{t('policy.direct')}</Button>
        )}
      </div>
      {onDelete && (
        <div className="rg-inspector-foot">
          <Button variant="danger" onClick={onDelete} data-testid="rg-delete">{t('app.delete')}</Button>
        </div>
      )}
    </div>
  );
}
