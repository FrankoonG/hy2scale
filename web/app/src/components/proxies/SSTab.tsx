import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Input, Select, Toggle, FormGroup, FormGrid, useToast } from '@hy2scale/ui';
import * as api from '@/api';

export default function SSTab() {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();

  const { data: ss } = useQuery({ queryKey: ['ss'], queryFn: api.getSS });

  const [enabled, setEnabled] = useState(false);
  const [listen, setListen] = useState('');
  const [method, setMethod] = useState('aes-256-gcm');
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (ss) {
      setEnabled(ss.enabled);
      setListen(ss.listen || '');
      setMethod(ss.method || 'aes-256-gcm');
    }
  }, [ss]);

  const handleSave = async () => {
    setLoading(true);
    try {
      await api.updateSS({ listen, enabled, method });
      toast.success(t('ss.saved'));
      queryClient.invalidateQueries({ queryKey: ['ss'] });
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  return (
    <Card title={t('ss.title')}>
      <div style={{ maxWidth: 400, display: 'flex', flexDirection: 'column', gap: 14 }}>
        <FormGroup label={t('app.enabled')}>
          <Toggle checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        </FormGroup>
        <FormGrid>
          <FormGroup label={t('ss.port')} required>
            <Input value={listen} onChange={(e) => setListen(e.target.value)} placeholder=":8388" />
          </FormGroup>
          <FormGroup label={t('ss.method')}>
            <Select
              value={method}
              onChange={(e) => setMethod(e.target.value)}
              options={[
                { value: 'aes-128-gcm', label: 'AES-128-GCM' },
                { value: 'aes-256-gcm', label: 'AES-256-GCM' },
                { value: 'chacha20-ietf-poly1305', label: 'ChaCha20-Poly1305' },
              ]}
            />
          </FormGroup>
        </FormGrid>
        <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('ss.desc')}</div>
        <Button variant="primary" onClick={handleSave} loading={loading} style={{ alignSelf: 'flex-start' }}>{t('app.save')}</Button>
      </div>
    </Card>
  );
}
