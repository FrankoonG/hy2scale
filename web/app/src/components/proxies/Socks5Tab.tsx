import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Input, Toggle, Select, FormGroup, useToast } from '@hy2scale/ui';
import * as api from '@/api';

export default function Socks5Tab() {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();

  // `= []` only defaults when data is undefined — the server returns
  // literal null on a fresh install, which would make proxies.find()
  // explode. Coalesce explicitly.
  const { data: proxiesRaw } = useQuery({ queryKey: ['proxies'], queryFn: api.getProxies });
  const { data: certsRaw } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts });
  const proxies = proxiesRaw ?? [];
  const certs = certsRaw ?? [];

  const socks5 = proxies.find((p) => p.protocol === 'socks5');

  // Default to "0.0.0.0:1080" so the form is immediately submittable —
  // an empty `listen` would either get rejected by the backend's
  // "id, listen required" check (POST) or silently persist an unrunnable
  // proxy on the update path. Pre-filling the standard SOCKS5 port also
  // saves the operator from typing it. The useEffect below overwrites
  // this with the saved listen as soon as the GET /api/proxies query
  // resolves, so existing-proxy edits still see the real value.
  const [enabled, setEnabled] = useState(false);
  const [listen, setListen] = useState('0.0.0.0:1080');
  const [tlsCert, setTlsCert] = useState('');
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (socks5) {
      setEnabled(socks5.enabled);
      setListen(socks5.listen || '0.0.0.0:1080');
      setTlsCert(socks5.tls_cert || '');
    }
  }, [socks5]);

  const handleSave = async () => {
    setLoading(true);
    try {
      const data = { protocol: 'socks5' as const, listen, enabled, tls_cert: tlsCert || undefined };
      if (socks5) {
        await api.updateProxy(socks5.id, { ...socks5, ...data });
      } else {
        await api.createProxy(data);
      }
      toast.success(t('socks5.saved'));
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
    <Card fill={1} title={t('socks5.title')}>
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
            <FormGroup label={t('socks5.listen')} required>
              <Input value={listen} onChange={(e) => setListen(e.target.value)} placeholder="0.0.0.0:1080" />
            </FormGroup>
          </div>
        </div>
        <FormGroup label={t('proxies.tlsCert')}>
          <Select value={tlsCert} onChange={(e) => setTlsCert(e.target.value)} options={tlsOptions} />
        </FormGroup>
        <div style={{ fontSize: 12, color: 'var(--text-muted)' }}>{t('socks5.desc')}</div>
        <Button variant="primary" onClick={handleSave} loading={loading} style={{ alignSelf: 'flex-start' }}>{t('app.save')}</Button>
      </div>
    </Card>
  );
}
