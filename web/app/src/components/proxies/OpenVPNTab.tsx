import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Badge, Button, Input, Select, Toggle, FormGroup, FormGrid, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import { useServerFormInit } from '@/hooks/useFormInit';
import VPNStatus from './VPNStatus';

export default function OpenVPNTab() {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();

  const ovpnQ = useQuery({ queryKey: ['openvpn'], queryFn: api.getOpenVPN, refetchInterval: 5000 });
  const ovpn = ovpnQ.data;
  const { data: certs = [] } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts });

  const [enabled, setEnabled] = useState(false);
  const [port, setPort] = useState('1194');
  const [pool, setPool] = useState('');
  const [pool6, setPool6] = useState('');
  const [certId, setCertId] = useState('');
  const [mtu, setMtu] = useState('1400');
  const [endpoint, setEndpoint] = useState(window.location.hostname);
  const [loading, setLoading] = useState(false);

  // Seed once from the first fetch; refetches must not revert unsaved edits.
  const resync = useServerFormInit(ovpnQ, (d) => {
    if (d) {
      setEnabled(d.enabled);
      setPort(String(d.port || 1194));
      setPool(d.pool || '');
      setPool6(d.pool6 || '');
      setCertId(d.cert_id || '');
      setMtu(String(d.mtu || 1400));
    }
  });

  const handleSave = async () => {
    if (!pool) { toast.error(t('openvpn.poolRequired')); return; }
    if (!certId) { toast.error(t('openvpn.caRequired')); return; }
    setLoading(true);
    try {
      await api.updateOpenVPN({
        enabled, port: parseInt(port) || 1194, pool, pool6: pool6.trim(), cert_id: certId, mtu: parseInt(mtu) || 1400,
      });
      toast.success(t('openvpn.saved'));
      resync();
      queryClient.invalidateQueries({ queryKey: ['openvpn'] });
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  const downloadProfile = async () => {
    if (!endpoint.trim()) { toast.error(t('openvpn.endpointRequired')); return; }
    try {
      const res = await api.downloadOpenVPNProfile(endpoint.trim());
      const blob = new Blob([await (res as any).text()], { type: 'application/x-openvpn-profile' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url; a.download = `${endpoint.trim().replace(/[[\]:]/g, '_')}.ovpn`; a.click();
      URL.revokeObjectURL(url);
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
        <div style={{ maxWidth: 500, display: 'flex', flexDirection: 'column', gap: 14 }}>
          <FormGroup label={t('app.enabled')}>
            <Toggle checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          </FormGroup>
          <FormGrid>
            <FormGroup label={t('openvpn.port')} required>
              <Input type="number" value={port} onChange={(e) => setPort(e.target.value)} placeholder="1194" />
            </FormGroup>
            <FormGroup label={t('openvpn.ca')} required>
              <Select value={certId} onChange={(e) => setCertId(e.target.value)} options={caOptions} />
            </FormGroup>
            <FormGroup label={t('openvpn.pool')} required>
              <Input value={pool} onChange={(e) => setPool(e.target.value)} placeholder="10.8.0.1/24" />
            </FormGroup>
            <FormGroup label={t('openvpn.pool6')}>
              <Input value={pool6} onChange={(e) => setPool6(e.target.value)} placeholder="fd00:8::1/64" />
            </FormGroup>
            <FormGroup label={t('openvpn.mtu')}>
              <Input type="number" value={mtu} onChange={(e) => setMtu(e.target.value)} placeholder="1400" />
            </FormGroup>
          </FormGrid>
          <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('openvpn.desc')}</div>
          <Button variant="primary" onClick={handleSave} loading={loading} style={{ alignSelf: 'flex-start' }}>{t('app.save')}</Button>
        </div>
      </Card>
      <Card title={t('openvpn.profile')}>
        <div style={{ maxWidth: 500, display: 'flex', gap: 10, alignItems: 'flex-end' }}>
          <div style={{ flex: 1 }}>
            <FormGroup label={t('openvpn.endpoint')}>
              <Input value={endpoint} onChange={(e) => setEndpoint(e.target.value)} placeholder="vpn.example.com" />
            </FormGroup>
          </div>
          <Button onClick={downloadProfile} disabled={!ovpn?.cert_id}>{t('openvpn.download')}</Button>
        </div>
        {!ovpn?.cert_id && <div style={{ marginTop: 8 }}><Badge variant="orange">{t('openvpn.saveFirst')}</Badge></div>}
      </Card>
    </>
  );
}
