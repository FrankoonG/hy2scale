import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Button, FormGrid, FormGroup, Input, Modal, SegmentedControl, Select } from '@hy2scale/ui';
import { testPolicy, type PolicyConfig, type PolicyTestResult } from '@/api/policy';
import * as api from '@/api';
import { SOURCE_KINDS, describeTerminal } from './shared';

const USER_KINDS = new Set(['socks5', 'http', 'ss', 'hy2', 'ikev2', 'l2tp', 'openvpn', 'access']);

/** Dry run: which rules a connection would pass through, and where it ends. */
export default function TestModal({ open, onClose, animateFrom, cfg }: {
  open: boolean; onClose: () => void; animateFrom?: { x: number; y: number }; cfg?: PolicyConfig;
}) {
  const { t } = useTranslation();
  const { data: users = [] } = useQuery({ queryKey: ['users'], queryFn: api.getUsers, enabled: open });
  const [start, setStart] = useState('');
  const [kind, setKind] = useState('socks5');
  const [user, setUser] = useState('');
  const [srcIp, setSrcIp] = useState('');
  const [iface, setIface] = useState('');
  const [node, setNode] = useState('');
  const [host, setHost] = useState('');
  const [port, setPort] = useState('443');
  const [proto, setProto] = useState<'tcp' | 'udp'>('tcp');
  const [res, setRes] = useState<PolicyTestResult | null>(null);
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);

  const run = async (e?: React.FormEvent) => {
    e?.preventDefault();
    setBusy(true);
    setErr('');
    try {
      const isIP = /^[\d.]+$|:/.test(host);
      setRes(await testPolicy({
        start: start || undefined,
        source: {
          kind, user: USER_KINDS.has(kind) ? user || undefined : undefined, ip: srcIp || undefined,
          iface: kind === 'forward' ? iface || undefined : undefined, node: kind === 'relay' ? node || undefined : undefined,
        },
        dest: { host: isIP ? undefined : host || undefined, ip: isIP ? host : undefined, port: Number(port) || 0, proto },
      }));
    } catch (ex: any) {
      setRes(null);
      setErr(String(ex?.message || ex));
    } finally {
      setBusy(false);
    }
  };

  const policyNames = new Map((cfg?.policies || []).map((p) => [p.id, p.name]));
  // The deciding rule by its name, else its position ("Rule 2.1"); a
  // default end as "Otherwise" (a nested flow's: "Rule 2 · Otherwise").
  const ruleLabel = (h: PolicyTestResult['chain'][number]) => {
    const pos = (h.path || []).join('.');
    if (h.otherwise) return pos ? `${t('policy.ruleN', { n: pos })} · ${t('policy.otherwise')}` : t('policy.otherwise');
    return h.rule_name || t('policy.ruleN', { n: pos || h.rule });
  };
  return (
    <Modal open={open} onClose={onClose} animateFrom={animateFrom} wide title={t('policy.test')}
      footer={<><span className="spacer" /><Button onClick={onClose}>{t('app.close')}</Button></>}>
      <form className="ptest" onSubmit={run} data-testid="test-modal">
        <FormGrid>
          <FormGroup label={t('policy.testStart')}>
            <Select value={start} onChange={(e) => setStart(e.target.value)}
              options={[{ value: '', label: t('policy.testStartAuto') }, { value: 'direct', label: t('policy.direct') },
                ...(cfg?.policies || []).map((p) => ({ value: p.id, label: p.name }))]} />
          </FormGroup>
          <FormGroup label={t('policy.field.src_kind')}>
            <Select value={kind} onChange={(e) => setKind(e.target.value)}
              options={SOURCE_KINDS.map((k) => ({ value: k, label: t(`policy.kind.${k}`) }))} />
          </FormGroup>
          {USER_KINDS.has(kind) && (
            <FormGroup label={t('policy.field.user')}>
              <Select value={user} onChange={(e) => setUser(e.target.value)}
                options={[{ value: '', label: '—' }, ...users.map((u) => ({ value: u.id, label: u.username }))]} />
            </FormGroup>
          )}
          <FormGroup label={t('policy.field.src_ip')}>
            <Input value={srcIp} onChange={(e) => setSrcIp(e.target.value)} placeholder="192.168.1.20" />
          </FormGroup>
          {kind === 'forward' && (
            <FormGroup label={t('policy.field.in_iface')}>
              <Input value={iface} onChange={(e) => setIface(e.target.value)} placeholder="eth1" />
            </FormGroup>
          )}
          {kind === 'relay' && (
            <FormGroup label={t('policy.field.src_node')}>
              <Input value={node} onChange={(e) => setNode(e.target.value)} />
            </FormGroup>
          )}
          <FormGroup label={t('policy.testDest')}>
            <Input value={host} onChange={(e) => setHost(e.target.value)} placeholder="example.com / 1.1.1.1" data-testid="test-host" />
          </FormGroup>
          <FormGroup label={t('policy.field.dst_port')}>
            <div className="ptest-port">
              <Input type="number" value={port} onChange={(e) => setPort(e.target.value)} />
              <SegmentedControl<'tcp' | 'udp'> items={[{ key: 'tcp', label: 'TCP' }, { key: 'udp', label: 'UDP' }]} value={proto} onChange={setProto} />
            </div>
          </FormGroup>
        </FormGrid>
        <Button type="submit" variant="primary" className="align-start" loading={busy} disabled={!host} data-testid="test-run">{t('policy.testRun')}</Button>
        {err && <div className="pset-error">{err}</div>}
        {res && (
          <div className="ptest-result" data-testid="test-result">
            <div className="ptest-chain">
              {res.chain.length === 0 && <span className="muted">{t('policy.testNoPolicy')}</span>}
              {res.chain.map((h, i) => (
                <div key={i} className="ptest-hop">
                  <span className="ptest-depth">{h.depth}</span>
                  <b>{h.policy_name || policyNames.get(h.policy) || h.policy}</b>
                  <span className="muted">· {ruleLabel(h)}</span>
                </div>
              ))}
            </div>
            <div className="ptest-term" data-testid="test-terminal">
              → {describeTerminal(res.terminal, t, policyNames)}{res.reason && <span className="muted"> ({res.reason})</span>}
            </div>
            {res.fields && Object.keys(res.fields).length > 0 && (
              <div className="ptest-fields muted">
                {Object.entries(res.fields).map(([k, v]) => <span key={k}>{t(`policy.field.${k}`, { defaultValue: k })}: {v}</span>)}
              </div>
            )}
          </div>
        )}
      </form>
    </Modal>
  );
}
