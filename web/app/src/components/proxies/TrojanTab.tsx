import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Input, Select, Toggle, FormGroup, FormStack, FormRow, Hint, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import { useServerFormInit } from '@/hooks/useFormInit';

const DEFAULT_LISTEN = '0.0.0.0:443';

/** Trojan server: enable, listen address, TLS certificate, fallback address. */
export default function TrojanTab() {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();

  const trojanQ = useQuery({ queryKey: ['trojan'], queryFn: api.getTrojan });
  const { data: certsRaw } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts });
  const certs = certsRaw ?? [];

  const [enabled, setEnabled] = useState(false);
  const [listen, setListen] = useState(DEFAULT_LISTEN);
  const [certId, setCertId] = useState('');
  const [fallback, setFallback] = useState('');
  const [loading, setLoading] = useState(false);

  // Seed once from the first fetch; refetches must not revert unsaved edits.
  const resync = useServerFormInit(trojanQ, (d) => {
    if (d) {
      setEnabled(d.enabled);
      setListen(d.listen || DEFAULT_LISTEN);
      setCertId(d.cert_id || 'default');
      setFallback(d.fallback || '');
    }
  });

  const handleSave = async () => {
    if (!certs.some((c) => c.id === certId && !!c.key_file)) { toast.error(t('trojan.certRequired')); return; }
    setLoading(true);
    try {
      await api.updateTrojan({ listen: listen.trim(), enabled, cert_id: certId, fallback: fallback.trim() });
      toast.success(t('trojan.saved'));
      resync();
      queryClient.invalidateQueries({ queryKey: ['trojan'] });
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  const certOptions = certs.filter((c) => !!c.key_file).map((c) => ({ value: c.id, label: `${c.name} (${c.subject})` }));
  if (!certOptions.some((o) => o.value === certId)) certOptions.unshift({ value: certId, label: '—' });

  return (
    <Card fill={1} title={t('trojan.title')}>
      <FormStack maxWidth={400}>
        <FormRow>
          <FormGroup label={t('app.enabled')}>
            <Toggle offset checked={enabled} onChange={(e) => setEnabled(e.target.checked)} data-testid="trojan-enabled" />
          </FormGroup>
          <FormGroup label={t('trojan.listen')} required>
            <Input value={listen} onChange={(e) => setListen(e.target.value)} placeholder={DEFAULT_LISTEN} data-testid="trojan-listen" />
          </FormGroup>
        </FormRow>
        <FormGroup label={t('trojan.cert')} required testId="trojan-cert">
          <Select value={certId} onChange={(e) => setCertId(e.target.value)} options={certOptions} />
        </FormGroup>
        <FormGroup label={t('trojan.fallback')} hint={t('trojan.fallbackHint')}>
          <Input value={fallback} onChange={(e) => setFallback(e.target.value)} placeholder="127.0.0.1:80" data-testid="trojan-fallback" />
        </FormGroup>
        <Hint>{t('trojan.desc')}</Hint>
        <Button variant="primary" className="align-start" onClick={handleSave} loading={loading} data-testid="trojan-save">{t('app.save')}</Button>
      </FormStack>
    </Card>
  );
}
