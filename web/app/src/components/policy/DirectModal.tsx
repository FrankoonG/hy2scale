import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import clsx from 'clsx';
import {
  Badge, Button, FormGroup, FormGrid, Hint, Input, Modal, SegmentedControl, Select, Textarea, Toggle, MultiSelect, useToast,
} from '@hy2scale/ui';
import {
  getPolicyCapabilities, getPolicyInterfaces, getPolicyStatus, updatePolicySettings,
  type Avail, type OnStop, type PolicyConfig, type PolicySystem,
} from '@/api/policy';
import { useDialogInit } from '@/hooks/useFormInit';
import { parseValues } from './shared';

const DEFAULT_SYSTEM: PolicySystem = {
  local: { enabled: false, dns: true, on_stop: 'open' },
  forward: { enabled: false, interfaces: [], sources: [], dns: true, on_stop: 'open' },
};

/** "Direct": the policy set direct traffic enters, and system interception. */
export default function DirectModal({ open, onClose, animateFrom, cfg }: {
  open: boolean; onClose: () => void; animateFrom?: { x: number; y: number }; cfg?: PolicyConfig;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const qc = useQueryClient();
  const { data: caps } = useQuery({ queryKey: ['policyCaps'], queryFn: getPolicyCapabilities, enabled: open, retry: false });
  const { data: ifaces = [] } = useQuery({ queryKey: ['policyIfaces'], queryFn: getPolicyInterfaces, enabled: open, retry: false });
  const { data: status } = useQuery({ queryKey: ['policyStatus'], queryFn: getPolicyStatus, enabled: open, retry: false, refetchInterval: open ? 5000 : false });

  const [direct, setDirect] = useState('');
  const [depth, setDepth] = useState('16');
  const [sys, setSys] = useState<PolicySystem>(DEFAULT_SYSTEM);
  const [sources, setSources] = useState('');
  const [saving, setSaving] = useState(false);

  useDialogInit(open, 'direct', () => {
    setDirect(cfg?.direct || '');
    setDepth(String(cfg?.max_depth || 16));
    const s = cfg?.system || DEFAULT_SYSTEM;
    setSys({ local: { ...DEFAULT_SYSTEM.local, ...s.local }, forward: { ...DEFAULT_SYSTEM.forward, ...s.forward } });
    setSources((s.forward?.sources || []).join('\n'));
  });

  const local = caps?.system.local;
  const forward = caps?.system.forward;
  const setLocal = (p: Partial<PolicySystem['local']>) => setSys((s) => ({ ...s, local: { ...s.local, ...p } }));
  const setForward = (p: Partial<PolicySystem['forward']>) => setSys((s) => ({ ...s, forward: { ...s.forward, ...p } }));

  const save = async () => {
    setSaving(true);
    try {
      await updatePolicySettings({
        direct,
        max_depth: Math.min(64, Math.max(2, Number(depth) || 16)),
        system: { ...sys, forward: { ...sys.forward, sources: parseValues(sources) } },
      });
      await qc.invalidateQueries({ queryKey: ['policy'] });
      qc.invalidateQueries({ queryKey: ['policyStatus'] });
      toast.success(t('policy.saved'));
      onClose();
    } catch (e: any) {
      toast.error(String(e?.message || e));
    } finally {
      setSaving(false);
    }
  };

  const off = (a?: Avail) => a && !a.available;
  const why = (code?: string) => (code ? t(`policy.reason.${code}`, { defaultValue: code }) : '');
  const reason = (a?: Avail) => (a && !a.available ? <div className="direct-reason">{why(a.reason)}</div> : null);
  const active = (on?: { active: boolean; error?: string }) => on && (
    on.error ? <Badge variant="red">{why(on.error)}</Badge> : on.active ? <Badge variant="green">{t('policy.active')}</Badge> : <Badge variant="muted">{t('policy.inactive')}</Badge>
  );
  const stopChoice = (value: OnStop, onChange: (v: OnStop) => void) => (
    <SegmentedControl<OnStop>
      items={[{ key: 'open', label: t('policy.stopOpen') }, { key: 'block', label: t('policy.stopBlock') }]}
      value={value} onChange={onChange} />
  );

  return (
    <Modal open={open} onClose={onClose} animateFrom={animateFrom} wide title={t('policy.direct')}
      footer={<><span className="spacer" /><Button onClick={onClose}>{t('app.cancel')}</Button><Button variant="primary" onClick={save} loading={saving} data-testid="direct-save">{t('app.save')}</Button></>}>
      <div className="direct" data-testid="direct-modal">
        <FormGrid>
          <FormGroup label={t('policy.directEntry')} hint={t('policy.directEntryHint')}>
            <div data-testid="direct-entry">
              <Select value={direct} onChange={(e) => setDirect(e.target.value)}
                options={[{ value: '', label: t('policy.term.default') }, ...(cfg?.policies || []).map((p) => ({ value: p.id, label: p.name }))]} />
            </div>
          </FormGroup>
          <FormGroup label={t('policy.maxDepth')}>
            <Input type="number" min={2} max={64} value={depth} onChange={(e) => setDepth(e.target.value)} />
          </FormGroup>
        </FormGrid>

        <section className={clsx('direct-sec', off(local) && 'locked')} data-testid="direct-local">
          <div className="direct-sec-head">
            <span className="direct-sec-title">{t('policy.layerLocal')}</span>
            {active(status?.local)}
            <span className="spacer" />
            <Toggle checked={sys.local.enabled} disabled={off(local)} onChange={(e) => setLocal({ enabled: e.target.checked })} data-testid="direct-local-on" />
          </div>
          <Hint>{t('policy.layerLocalHint')}</Hint>
          {reason(local)}
          {sys.local.enabled && (
            <div className="direct-opts">
              <Toggle size="sm" checked={sys.local.dns} disabled={off(caps?.dns_proxy)} onChange={(e) => setLocal({ dns: e.target.checked })} label={t('policy.dnsProxy')} />
              <div className="direct-opt"><span>{t('policy.onStop')}</span>{stopChoice(sys.local.on_stop, (v) => setLocal({ on_stop: v }))}</div>
            </div>
          )}
        </section>

        <section className={clsx('direct-sec', off(forward) && 'locked')} data-testid="direct-forward">
          <div className="direct-sec-head">
            <span className="direct-sec-title">{t('policy.layerForward')}</span>
            {active(status?.forward)}
            <span className="spacer" />
            <Toggle checked={sys.forward.enabled} disabled={off(forward)} onChange={(e) => setForward({ enabled: e.target.checked })} data-testid="direct-forward-on" />
          </div>
          <Hint>{t('policy.layerForwardHint')}</Hint>
          {reason(forward)}
          {forward?.available && forward.ip_forward === false && <div className="direct-reason">{t('policy.ipForwardOff')}</div>}
          {sys.forward.enabled && (
            <div className="direct-opts">
              <div className="direct-opt-col">
                <span className="direct-opt-label">{t('policy.ifaces')}</span>
                {ifaces.length === 0 ? <span className="muted">{t('policy.noIfaces')}</span> : (
                  <MultiSelect testId="direct-ifaces"
                    options={ifaces.map((i) => ({ value: i.name, label: i.name, detail: [i.type, ...i.addrs].join(' · '), disabled: !i.up && !sys.forward.interfaces.includes(i.name) }))}
                    value={sys.forward.interfaces}
                    onChange={(v) => setForward({ interfaces: v })}
                    placeholder={t('policy.pickIfaces')} />                )}
              </div>
              <FormGroup label={t('policy.sources')} hint={t('policy.sourcesHint')}>
                <Textarea rows={2} value={sources} onChange={(e) => setSources(e.target.value)} placeholder="192.168.1.0/24" />
              </FormGroup>
              <Toggle size="sm" checked={sys.forward.dns} disabled={off(caps?.dns_proxy)} onChange={(e) => setForward({ dns: e.target.checked })} label={t('policy.dnsProxy')} />
              <div className="direct-opt"><span>{t('policy.onStop')}</span>{stopChoice(sys.forward.on_stop, (v) => setForward({ on_stop: v }))}</div>
            </div>
          )}
        </section>
        {caps && !caps.heartbeat.available && (sys.local.enabled || sys.forward.enabled) && (
          <div className="direct-reason">{t('policy.noHeartbeat', { reason: why(caps.heartbeat.reason) })}</div>
        )}
      </div>
    </Modal>
  );
}
