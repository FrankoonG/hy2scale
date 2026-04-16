import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Input, Toggle, Select, FormGroup, useToast } from '@hy2scale/ui';
import * as api from '@/api';

export default function HttpTab() {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();

  const { data: proxies = [] } = useQuery({ queryKey: ['proxies'], queryFn: api.getProxies });
  const { data: certs = [] } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts });

  const http = proxies.find((p) => p.protocol === 'http');

  const [enabled, setEnabled] = useState(false);
  const [listen, setListen] = useState('');
  const [tlsCert, setTlsCert] = useState('');
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (http) {
      setEnabled(http.enabled);
      setListen(http.listen || '');
      setTlsCert(http.tls_cert || '');
    }
  }, [http]);

  const handleSave = async () => {
    setLoading(true);
    try {
      const data = { protocol: 'http' as const, listen, enabled, tls_cert: tlsCert || undefined };
      if (http) {
        await api.updateProxy(http.id, { ...http, ...data });
      } else {
        await api.createProxy(data);
      }
      toast.success(t('http.saved'));
      queryClient.invalidateQueries({ queryKey: ['proxies'] });
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  const tlsOptions = [
    { value: '', label: t('proxies.noTLS') },
    ...(certs || []).filter((c) => !!c.key_file).map((c) => ({ value: c.id, label: `${c.name} (${c.subject})` })),
  ];

  return (
    <Card title={t('http.title')}>
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
            <FormGroup label={t('http.listen')} required>
              <Input value={listen} onChange={(e) => setListen(e.target.value)} placeholder="0.0.0.0:8080" />
            </FormGroup>
          </div>
        </div>
        <FormGroup label={t('proxies.tlsCert')}>
          <Select value={tlsCert} onChange={(e) => setTlsCert(e.target.value)} options={tlsOptions} />
        </FormGroup>
        <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('http.desc')}</div>
        <Button variant="primary" onClick={handleSave} loading={loading} style={{ alignSelf: 'flex-start' }}>{t('app.save')}</Button>
      </div>
    </Card>
  );
}
