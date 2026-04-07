import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Input, Toggle, FormGroup, useToast } from '@hy2scale/ui';
import * as api from '@/api';

export default function Socks5Tab() {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();

  const { data: proxies = [] } = useQuery({ queryKey: ['proxies'], queryFn: api.getProxies });

  const socks5 = proxies.find((p) => p.protocol === 'socks5');

  const [enabled, setEnabled] = useState(false);
  const [listen, setListen] = useState('');
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (socks5) {
      setEnabled(socks5.enabled);
      setListen(socks5.listen || '');
    }
  }, [socks5]);

  const handleSave = async () => {
    setLoading(true);
    try {
      if (socks5) {
        await api.updateProxy(socks5.id, { ...socks5, listen, enabled });
      } else {
        await api.createProxy({ protocol: 'socks5', listen, enabled });
      }
      toast.success(t('socks5.saved'));
      queryClient.invalidateQueries({ queryKey: ['proxies'] });
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  return (
    <Card title={t('socks5.title')}>
      <div style={{ maxWidth: 400, display: 'flex', flexDirection: 'column', gap: 14 }}>
        <FormGroup label={t('app.enabled')}>
          <Toggle checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        </FormGroup>
        <FormGroup label={t('socks5.port')} required>
          <Input value={listen} onChange={(e) => setListen(e.target.value)} placeholder=":1080" />
        </FormGroup>
        <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('socks5.desc')}</div>
        <Button variant="primary" onClick={handleSave} loading={loading} style={{ alignSelf: 'flex-start' }}>{t('app.save')}</Button>
      </div>
    </Card>
  );
}
