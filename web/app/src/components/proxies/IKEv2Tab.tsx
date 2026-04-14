import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Input, Select, Toggle, FormGroup, FormGrid, useToast } from '@hy2scale/ui';
import { ExitPathList, exitPathToApi, apiToExitPath, type ExitPathValue } from '@/components/ExitPathList';
import * as api from '@/api';

export default function IKEv2Tab({ limited }: { limited?: boolean }) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();

  const { data: ikev2 } = useQuery({ queryKey: ['ikev2'], queryFn: api.getIKEv2 });
  const { data: certs = [] } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts });

  const [enabled, setEnabled] = useState(false);
  const [mode, setMode] = useState<'mschapv2' | 'psk'>('mschapv2');
  const [pool, setPool] = useState('');
  const [certId, setCertId] = useState('');
  const [psk, setPsk] = useState('');
  const [localId, setLocalId] = useState('');
  const [mtu, setMtu] = useState('1400');
  const [exitPath, setExitPath] = useState<ExitPathValue>({ paths: [''], mode: '' });
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (ikev2) {
      setEnabled(ikev2.enabled);
      setMode(ikev2.mode || 'mschapv2');
      setPool(ikev2.pool || '');
      setCertId(ikev2.cert_id || '');
      setPsk(ikev2.psk || '');
      setLocalId(ikev2.local_id || '');
      setMtu(String(ikev2.mtu || 1400));
      setExitPath(apiToExitPath(ikev2.default_exit, ikev2.default_exit_paths, ikev2.default_exit_mode));
    }
  }, [ikev2]);

  const handleSave = async () => {
    if (!pool) { toast.error(t('ikev2.poolRequired')); return; }
    if (mode === 'mschapv2' && !certId) { toast.error(t('ikev2.certRequired')); return; }
    if (mode === 'psk' && !psk) { toast.error(t('ikev2.pskRequired')); return; }

    setLoading(true);
    try {
      const exitData = mode === 'psk' ? exitPathToApi(exitPath) : {};
      await api.updateIKEv2({
        enabled, mode, pool, cert_id: certId, psk, local_id: localId,
        mtu: parseInt(mtu) || 1400,
        default_exit: (exitData as any).exit_via || '',
        default_exit_paths: (exitData as any).exit_paths,
        default_exit_mode: (exitData as any).exit_mode,
      } as any);
      toast.success(t('ikev2.saved'));
      queryClient.invalidateQueries({ queryKey: ['ikev2'] });
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  const isLimited = limited || (ikev2 && !ikev2.capable);

  const certOptions = [
    { value: '', label: t('ikev2.selectCert') },
    ...certs.map((c) => ({ value: c.id, label: c.name || c.id })),
  ];

  return (
    <>
      {isLimited && (
        <div className="hy-warn-banner">
          {ikev2?.host_network === false ? t('ikev2.warnHostNetwork') : t('ikev2.warnText')}
        </div>
      )}
    <Card title={t('ikev2.title')}>
      <div style={{ maxWidth: 450, display: 'flex', flexDirection: 'column', gap: 14, ...(isLimited ? { opacity: 0.4, pointerEvents: 'none' as const } : {}) }}>
        <FormGrid>
          <FormGroup label={t('ikev2.mode')}>
            <Select
              value={mode}
              onChange={(e) => setMode(e.target.value as any)}
              options={[
                { value: 'mschapv2', label: t('ikev2.modeMschapv2') },
                { value: 'psk', label: t('ikev2.modePsk') },
              ]}
            />
          </FormGroup>
          <FormGroup label={t('app.enabled')}>
            <Toggle checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          </FormGroup>
        </FormGrid>

        <FormGroup label={t('ikev2.localId')}>
          <Input value={localId} onChange={(e) => setLocalId(e.target.value)} placeholder="node ID" />
        </FormGroup>

        <FormGroup label={t('ikev2.pool')} required>
          <Input value={pool} onChange={(e) => setPool(e.target.value)} placeholder="192.168.26.1/24" />
        </FormGroup>

        <FormGroup label={t('ikev2.mtu')}>
          <Input type="number" value={mtu} onChange={(e) => setMtu(e.target.value)} placeholder="1400" />
        </FormGroup>

        {mode === 'mschapv2' ? (
          <>
            <FormGroup label={t('ikev2.cert')} required>
              <Select value={certId} onChange={(e) => setCertId(e.target.value)} options={certOptions} />
            </FormGroup>
            <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('ikev2.certDesc')}</div>
          </>
        ) : (
          <>
            <FormGroup label={t('ikev2.psk')} required>
              <Input value={psk} onChange={(e) => setPsk(e.target.value)} />
            </FormGroup>
            <ExitPathList value={exitPath} onChange={setExitPath} label={t('ikev2.defaultExit')} />
            <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('ikev2.pskDesc')}</div>
          </>
        )}

        <Button variant="primary" onClick={handleSave} loading={loading} style={{ alignSelf: 'flex-start' }}>{t('app.save')}</Button>
      </div>
    </Card>
    </>
  );
}
