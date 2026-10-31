import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Card, Badge, Button, Input, Select, SegmentedControl, Textarea, Toggle, FormGroup, FormGrid, FormStack, Hint, useConfirm, useToast,
} from '@hy2scale/ui';
import { downloadBlob } from '@/utils/download';
import * as api from '@/api';
import { useServerFormInit } from '@/hooks/useFormInit';
import VPNStatus from './VPNStatus';

type Control = '' | 'tls-crypt' | 'tls-auth';
type Digest = 'SHA1' | 'SHA256';

/** Address / CIDR lists typed as one value each per line or comma. */
const splitList = (s: string) => s.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean);

export default function OpenVPNTab() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const queryClient = useQueryClient();

  const ovpnQ = useQuery({ queryKey: ['openvpn'], queryFn: api.getOpenVPN, refetchInterval: 5000 });
  const ovpn = ovpnQ.data;
  const { data: certs = [] } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts });

  const [enabled, setEnabled] = useState(false);
  const [port, setPort] = useState('1194');
  const [listenIP, setListenIP] = useState('');
  const [pool, setPool] = useState('');
  const [pool6, setPool6] = useState('');
  const [certId, setCertId] = useState('');
  const [mtu, setMtu] = useState('1400');
  const [dns, setDns] = useState('');
  const [control, setControl] = useState<Control>('');
  const [digest, setDigest] = useState<Digest>('SHA1');
  const [shape, setShape] = useState(false);
  const [fullTunnel, setFullTunnel] = useState(true);
  const [tunnelIPv6, setTunnelIPv6] = useState(true);
  const [routes, setRoutes] = useState('');
  const [extra, setExtra] = useState('');
  const [endpoint, setEndpoint] = useState(window.location.hostname);
  const [loading, setLoading] = useState(false);

  // Seed once from the first fetch; refetches must not revert unsaved edits.
  const resync = useServerFormInit(ovpnQ, (d) => {
    if (d) {
      setEnabled(d.enabled);
      setPort(String(d.port || 1194));
      setListenIP(d.listen_ip || '');
      setPool(d.pool || '');
      setPool6(d.pool6 || '');
      setCertId(d.cert_id || '');
      setMtu(String(d.mtu || 1400));
      setDns((d.dns || []).join(', '));
      setControl((d.control || '') as Control);
      setDigest(d.auth === 'SHA256' ? 'SHA256' : 'SHA1');
      setShape(!!d.shape);
      setFullTunnel(d.full_tunnel !== false);
      setTunnelIPv6(d.tunnel_ipv6 !== false);
      setRoutes((d.routes || []).join('\n'));
      setExtra(d.profile_extra || '');
    }
  });

  const save = async (rotateKey = false) => {
    if (!pool) { toast.error(t('openvpn.poolRequired')); return; }
    if (!certId) { toast.error(t('openvpn.caRequired')); return; }
    setLoading(true);
    try {
      await api.updateOpenVPN({
        enabled, port: parseInt(port) || 1194, listen_ip: listenIP.trim(), pool, pool6: pool6.trim(), cert_id: certId,
        mtu: parseInt(mtu) || 1400, dns: splitList(dns), control, auth: control === 'tls-auth' ? digest : '', shape,
        full_tunnel: fullTunnel, tunnel_ipv6: tunnelIPv6, routes: fullTunnel ? [] : splitList(routes), profile_extra: extra.trim(),
        ...(rotateKey ? { rotate_key: true } : {}),
      });
      toast.success(t(rotateKey ? 'openvpn.keyRotated' : 'openvpn.saved'));
      resync();
      queryClient.invalidateQueries({ queryKey: ['openvpn'] });
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  const rotateKey = async () => {
    const ok = await confirm({ title: t('openvpn.rotateKey'), message: t('openvpn.rotateKeyConfirm'), confirmText: t('openvpn.rotateKey'), danger: true });
    if (ok) await save(true);
  };

  const downloadProfile = async () => {
    if (!endpoint.trim()) { toast.error(t('openvpn.endpointRequired')); return; }
    try {
      const res = await api.downloadOpenVPNProfile(endpoint.trim());
      const blob = new Blob([await (res as any).text()], { type: 'application/x-openvpn-profile' });
      downloadBlob(blob, `${endpoint.trim().replace(/[[\]:]/g, '_')}.ovpn`);
    } catch (e: any) {
      toast.error(String(e.message || e));
    }
  };

  const caOptions = [
    { value: '', label: t('ikev2.selectCACert') },
    ...certs.filter((c) => c.is_ca).map((c) => ({ value: c.id, label: c.name || c.id })),
  ];

  return (
    <>
      <Card fill={1} title={<>{t('openvpn.title')} <VPNStatus running={ovpn?.running} connected={ovpn?.connected} /></>}>
        <FormStack maxWidth={560}>
          <FormGroup label={t('app.enabled')}>
            <Toggle checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          </FormGroup>
          <FormGrid>
            <FormGroup label={t('openvpn.port')} required>
              <Input type="number" value={port} onChange={(e) => setPort(e.target.value)} placeholder="1194" data-testid="ovpn-port" />
            </FormGroup>
            <FormGroup label={t('openvpn.listenIP')}>
              <Input value={listenIP} onChange={(e) => setListenIP(e.target.value)} placeholder={t('openvpn.listenAll')} data-testid="ovpn-listen" />
            </FormGroup>
            <FormGroup label={t('openvpn.ca')} required>
              <Select value={certId} onChange={(e) => setCertId(e.target.value)} options={caOptions} />
            </FormGroup>
            <FormGroup label={t('openvpn.mtu')}>
              <Input type="number" value={mtu} onChange={(e) => setMtu(e.target.value)} placeholder="1400" />
            </FormGroup>
            <FormGroup label={t('openvpn.pool')} required>
              <Input value={pool} onChange={(e) => setPool(e.target.value)} placeholder="10.8.0.1/24" />
            </FormGroup>
            <FormGroup label={t('openvpn.pool6')}>
              <Input value={pool6} onChange={(e) => setPool6(e.target.value)} placeholder="fd00:8::1/64" />
            </FormGroup>
          </FormGrid>
          <FormGroup label={t('openvpn.dns')} hint={t('openvpn.dnsHint')}>
            <Input value={dns} onChange={(e) => setDns(e.target.value)} placeholder="1.1.1.1, 2606:4700:4700::1111" data-testid="ovpn-dns" />
          </FormGroup>

          <div className="ovpn-sec">{t('openvpn.security')}</div>
          <FormGroup label={t('openvpn.control')} hint={t('openvpn.controlHint')}>
            <SegmentedControl<Control | 'none'>
              className="ovpn-seg"
              items={[{ key: 'none', label: t('openvpn.controlNone') }, { key: 'tls-crypt', label: 'tls-crypt' }, { key: 'tls-auth', label: 'tls-auth' }]}
              value={control || 'none'}
              onChange={(k) => setControl(k === 'none' ? '' : k)}
            />
          </FormGroup>
          {control === 'tls-auth' && (
            <FormGroup label={t('openvpn.digest')}>
              <SegmentedControl<Digest> className="ovpn-seg" items={[{ key: 'SHA1', label: 'SHA1' }, { key: 'SHA256', label: 'SHA256' }]} value={digest} onChange={setDigest} />
            </FormGroup>
          )}
          {control && ovpn?.has_key && (
            <div className="ovpn-key">
              <Badge variant="green">{t('openvpn.keyReady')}</Badge>
              <Button size="sm" onClick={rotateKey} data-testid="ovpn-rotate">{t('openvpn.rotateKey')}</Button>
            </div>
          )}
          <Toggle checked={shape} onChange={(e) => setShape(e.target.checked)} label={t('openvpn.shape')} />
          <Hint>{t('openvpn.shapeHint')}</Hint>

          <div className="ovpn-sec">{t('openvpn.clientSec')}</div>
          <Toggle checked={fullTunnel} onChange={(e) => setFullTunnel(e.target.checked)} label={t('openvpn.fullTunnel')} />
          {!fullTunnel && (
            <FormGroup label={t('openvpn.routes')} hint={t('openvpn.routesHint')}>
              <Textarea rows={3} value={routes} onChange={(e) => setRoutes(e.target.value)} placeholder={'10.0.0.0/8\n192.168.0.0/16'} data-testid="ovpn-routes" />
            </FormGroup>
          )}
          <Toggle checked={tunnelIPv6} onChange={(e) => setTunnelIPv6(e.target.checked)} label={t('openvpn.tunnelIPv6')} />
          <FormGroup label={t('openvpn.extra')} hint={t('openvpn.extraHint')}>
            <Textarea rows={3} value={extra} onChange={(e) => setExtra(e.target.value)} placeholder={'sndbuf 0\nrcvbuf 0'} className="mono" data-testid="ovpn-extra" />
          </FormGroup>
          <Hint>{t('openvpn.desc')}</Hint>
          <Button variant="primary" className="align-start" onClick={() => save()} loading={loading} data-testid="ovpn-save">{t('app.save')}</Button>
        </FormStack>
      </Card>
      <Card title={t('openvpn.profile')}>
        <div className="field-with-button">
          <FormGroup label={t('openvpn.endpoint')}>
            <Input value={endpoint} onChange={(e) => setEndpoint(e.target.value)} placeholder="vpn.example.com" />
          </FormGroup>
          <Button onClick={downloadProfile} disabled={!ovpn?.cert_id}>{t('openvpn.download')}</Button>
        </div>
        {!ovpn?.cert_id && <div className="hint-gap"><Badge variant="orange">{t('openvpn.saveFirst')}</Badge></div>}
      </Card>
    </>
  );
}
