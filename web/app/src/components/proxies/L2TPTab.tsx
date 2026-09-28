import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Input, Toggle, FormGroup, FormGrid, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import { useServerFormInit } from '@/hooks/useFormInit';

export default function L2TPTab({ limited }: { limited?: boolean }) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();

  const l2tpQ = useQuery({ queryKey: ['l2tp'], queryFn: api.getL2TP });
  const l2tp = l2tpQ.data;

  const [enabled, setEnabled] = useState(false);
  const [listen, setListen] = useState('');
  const [pool, setPool] = useState('');
  const [psk, setPsk] = useState('');
  const [mtu, setMtu] = useState('1400');
  const [loading, setLoading] = useState(false);

  // Seed once from the first fetch; refetches must not revert unsaved edits.
  const resync = useServerFormInit(l2tpQ, (d) => {
    const l2tp = d;
    if (l2tp) {
      setEnabled(l2tp.enabled);
      setListen(l2tp.listen || '');
      setPool(l2tp.pool || '');
      setPsk(l2tp.psk || '');
      setMtu(String(l2tp.mtu || 1400));
    }
  });

  const handleSave = async () => {
    if (!listen || !pool || !psk) {
      toast.error(t('l2tp.portPoolPskRequired'));
      return;
    }
    setLoading(true);
    try {
      await api.updateL2TP({ listen, enabled, pool, psk, mtu: parseInt(mtu) || 1400 });
      toast.success(t('l2tp.saved'));
      resync();
      queryClient.invalidateQueries({ queryKey: ['l2tp'] });
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  const isLimited = limited || (l2tp && !l2tp.capable);

  return (
    <>
      {isLimited && (
        <div className="hy-warn-banner">
          {/* When the backend gives a specific reason
              (e.g. "/dev/ppp present but blocked by cgroup — add
              --device-cgroup-rule=…"), show that verbatim. The static
              i18n strings are misleading once NET_ADMIN/host network
              are already granted but a different gate (cgroup, kernel
              module) is still blocking. */}
          {(l2tp as any)?.reason
            ? (l2tp as any).reason
            : l2tp?.host_network === false ? t('l2tp.warnHostNetwork') : t('l2tp.warnText')}
        </div>
      )}
    <Card fill={1} title={t('l2tp.title')}>
      <div style={{ maxWidth: 500, display: 'flex', flexDirection: 'column', gap: 14, ...(isLimited ? { opacity: 0.4, pointerEvents: 'none' as const } : {}) }}>
        <FormGroup label={t('app.enabled')}>
          <Toggle checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        </FormGroup>
        <FormGrid>
          <FormGroup label={t('l2tp.port')} required>
            <Input value={listen} onChange={(e) => setListen(e.target.value)} placeholder="1701" />
          </FormGroup>
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
    </>
  );
}
