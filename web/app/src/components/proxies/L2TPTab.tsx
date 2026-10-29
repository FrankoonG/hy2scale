import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Input, Toggle, FormGroup, FormGrid, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import { useServerFormInit } from '@/hooks/useFormInit';
import VPNStatus from './VPNStatus';

export default function L2TPTab() {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();

  const l2tpQ = useQuery({ queryKey: ['l2tp'], queryFn: api.getL2TP, refetchInterval: 5000 });
  const l2tp = l2tpQ.data;

  const [enabled, setEnabled] = useState(false);
  const [pool, setPool] = useState('');
  const [psk, setPsk] = useState('');
  const [mtu, setMtu] = useState('1280');
  const [loading, setLoading] = useState(false);

  // Seed once from the first fetch; refetches must not revert unsaved edits.
  const resync = useServerFormInit(l2tpQ, (d) => {
    const l2tp = d;
    if (l2tp) {
      setEnabled(l2tp.enabled);
      setPool(l2tp.pool || '');
      setPsk(l2tp.psk || '');
      setMtu(String(l2tp.mtu || 1280));
    }
  });

  const handleSave = async () => {
    if (!pool || !psk) {
      toast.error(t('l2tp.poolPskRequired'));
      return;
    }
    setLoading(true);
    try {
      await api.updateL2TP({ enabled, pool, psk, mtu: parseInt(mtu) || 1280 });
      toast.success(t('l2tp.saved'));
      resync();
      queryClient.invalidateQueries({ queryKey: ['l2tp'] });
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  return (
    <Card fill={1} title={<>{t('l2tp.title')} <VPNStatus running={l2tp?.running} connected={l2tp?.connected} /></>}>
      <div style={{ maxWidth: 500, display: 'flex', flexDirection: 'column', gap: 14 }}>
        <FormGroup label={t('app.enabled')}>
          <Toggle checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        </FormGroup>
        <FormGrid>
          <FormGroup label={t('l2tp.pool')} required>
            <Input value={pool} onChange={(e) => setPool(e.target.value)} placeholder="10.10.0.0/24" />
          </FormGroup>
          <FormGroup label={t('l2tp.ipsecPsk')} required>
            <Input value={psk} onChange={(e) => setPsk(e.target.value)} />
          </FormGroup>
          <FormGroup label={t('l2tp.mtu')}>
            <Input type="number" value={mtu} onChange={(e) => setMtu(e.target.value)} />
          </FormGroup>
        </FormGrid>
        <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('l2tp.desc')}</div>
        <Button variant="primary" onClick={handleSave} loading={loading} style={{ alignSelf: 'flex-start' }}>{t('app.save')}</Button>
      </div>
    </Card>
  );
}
