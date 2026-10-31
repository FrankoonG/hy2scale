import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient, type UseQueryResult } from '@tanstack/react-query';
import {
  Card, Button, Input, Toggle, FormGroup, FormGrid, FormStack, Badge, IconButton, IconWandSparkles, useToast,
} from '@hy2scale/ui';
import * as api from '@/api';
import type { WireGuardConfig } from '@/api';
import { useServerFormInit } from '@/hooks/useFormInit';

/** WireGuard server: listen port, addresses, key pair, MTU. */
export default function WgServerCard({ wgQ, onAddress }: {
  wgQ: UseQueryResult<WireGuardConfig>;
  /** Current (possibly unsaved) server addresses, for new-peer suggestions. */
  onAddress: (v4: string, v6: string) => void;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();
  const wg = wgQ.data;

  const [enabled, setEnabled] = useState(false);
  const [listenPort, setListenPort] = useState('');
  const [address, setAddressRaw] = useState('');
  const [address6, setAddress6Raw] = useState('');
  const [privKey, setPrivKey] = useState('');
  const [pubKey, setPubKey] = useState('');
  const [mtu, setMtu] = useState('1420');
  const [saving, setSaving] = useState(false);
  const setAddress = (v: string) => { setAddressRaw(v); onAddress(v, address6); };
  const setAddress6 = (v: string) => { setAddress6Raw(v); onAddress(address, v); };

  // Server fields seed once and after their own save; peer add/edit/delete
  // also refetches ['wireguard'] and must not revert unsaved server edits.
  const resync = useServerFormInit(wgQ, (d) => {
    setEnabled(d.enabled);
    setListenPort(String(d.listen_port || ''));
    setAddressRaw(d.address || '');
    setAddress6Raw(d.address6 || '');
    onAddress(d.address || '', d.address6 || '');
    setPrivKey(d.private_key || '');
    setPubKey(d.public_key || '');
    setMtu(String(d.mtu || 1420));
  });

  const handleSave = async () => {
    setSaving(true);
    try {
      await api.updateWireGuard({
        enabled, listen_port: parseInt(listenPort) || 0,
        address, address6: address6.trim(), private_key: privKey, mtu: parseInt(mtu) || 1420,
      } as any);
      toast.success(t('wg.saved'));
      resync();
      queryClient.invalidateQueries({ queryKey: ['wireguard'] });
    } catch (e: any) { toast.error(String(e.message || e)); }
    finally { setSaving(false); }
  };

  const generateKey = async () => {
    try {
      const { private_key, public_key } = await api.generateWGKey();
      setPrivKey(private_key);
      setPubKey(public_key);
    } catch (e: any) { toast.error(String(e.message || e)); }
  };

  return (
    <Card fill={2} title={<>
      {t('wg.title')}
      {wg?.running && <> <span className="status-dot-text">●</span> <Badge variant="green">{wg.connected ? t('wg.connectedStatus', { count: wg.connected }) : t('wg.runningStatus')}</Badge></>}
    </>}>
      <FormStack maxWidth={500}>
        <FormGrid>
          <FormGroup label={t('wg.port')}>
            <Input type="number" value={listenPort} onChange={(e) => setListenPort(e.target.value)} placeholder="51820" />
          </FormGroup>
          <FormGroup label={t('app.enabled')}>
            <Toggle offset checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          </FormGroup>
        </FormGrid>
        <FormGrid>
          <FormGroup label={t('wg.address')}>
            <Input value={address} onChange={(e) => setAddress(e.target.value)} placeholder="10.0.0.1/24" />
          </FormGroup>
          <FormGroup label={t('wg.address6')}>
            <Input value={address6} onChange={(e) => setAddress6(e.target.value)} placeholder="fd00:9::1/64" />
          </FormGroup>
        </FormGrid>
        <FormGroup label={t('wg.privKey')}>
          <div className="input-with-action">
            <Input className="mono small" value={privKey} onChange={(e) => setPrivKey(e.target.value)} />
            <IconButton icon={IconWandSparkles} label={t('wg.generateKey')} variant="circle" onClick={generateKey} />
          </div>
        </FormGroup>
        <FormGroup label={t('wg.pubKey')}>
          <Input className="mono small text-muted" value={pubKey} readOnly />
        </FormGroup>
        <FormGroup label={t('wg.mtu')}>
          <Input type="number" value={mtu} onChange={(e) => setMtu(e.target.value)} />
        </FormGroup>
        <Button variant="primary" className="align-start" onClick={handleSave} loading={saving}>{t('app.save')}</Button>
      </FormStack>
    </Card>
  );
}
