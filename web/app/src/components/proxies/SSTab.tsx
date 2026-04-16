import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Input, Select, Toggle, FormGroup, useToast } from '@hy2scale/ui';
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
        <div style={{ display: 'flex', gap: 14, alignItems: 'flex-start' }}>
          <div style={{ flex: '0 0 auto' }}>
            <FormGroup label={t('app.enabled')}>
              <div style={{ paddingTop: 6 }}>
                <Toggle checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
              </div>
            </FormGroup>
          </div>
          <div style={{ flex: 1, minWidth: 0 }}>
            <FormGroup label={t('ss.listen')} required>
              <Input value={listen} onChange={(e) => setListen(e.target.value)} placeholder="0.0.0.0:8388" />
            </FormGroup>
          </div>
        </div>
        <FormGroup label={t('ss.method')}>
          <Select
            value={method}
            onChange={(e) => setMethod(e.target.value)}
            options={[
              { value: 'aes-128-gcm', label: 'aes-128-gcm' },
              { value: 'aes-256-gcm', label: 'aes-256-gcm' },
              { value: 'chacha20-ietf-poly1305', label: 'chacha20-ietf-poly1305' },
              { value: '2022-blake3-aes-128-gcm', label: '2022-blake3-aes-128-gcm' },
              { value: '2022-blake3-aes-256-gcm', label: '2022-blake3-aes-256-gcm' },
              { value: 'none', label: 'none (no encryption)' },
            ]}
          />
        </FormGroup>
        <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('ss.desc')}</div>
        <Button variant="primary" onClick={handleSave} loading={loading} style={{ alignSelf: 'flex-start' }}>{t('app.save')}</Button>
      </div>
    </Card>
  );
}
