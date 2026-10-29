import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Card, Button, Input, Toggle, Select, FormGroup, FormStack, FormRow, Hint, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import { useServerFormInit } from '@/hooks/useFormInit';

/** Per-protocol bits: i18n namespace and the default listen address. */
const PROTOCOLS = {
  socks5: { ns: 'socks5', listen: '0.0.0.0:1080' },
  http: { ns: 'http', listen: '0.0.0.0:8080' },
} as const;

/**
 * SOCKS5 / HTTP proxy: enable, listen address, optional TLS certificate.
 * The listen address defaults to the protocol's standard port so a fresh
 * install is submittable (an empty listen is rejected by POST and would
 * persist an unrunnable proxy on update); the saved value replaces it once
 * /api/proxies resolves.
 */
export default function ListenProxyTab({ protocol }: { protocol: keyof typeof PROTOCOLS }) {
  const { t } = useTranslation();
  const toast = useToast();
  const queryClient = useQueryClient();
  const { ns, listen: defaultListen } = PROTOCOLS[protocol];

  // The server returns literal null on a fresh install: coalesce.
  const proxiesQ = useQuery({ queryKey: ['proxies'], queryFn: api.getProxies });
  const { data: certsRaw } = useQuery({ queryKey: ['certs'], queryFn: api.getCerts });
  const certs = certsRaw ?? [];
  const current = (proxiesQ.data ?? []).find((p) => p.protocol === protocol);

  const [enabled, setEnabled] = useState(false);
  const [listen, setListen] = useState<string>(defaultListen);
  const [tlsCert, setTlsCert] = useState('');
  const [loading, setLoading] = useState(false);

  // Seed once from the first fetch; refetches must not revert unsaved edits.
  const resync = useServerFormInit(proxiesQ, (d) => {
    const p = (d ?? []).find((x) => x.protocol === protocol);
    if (p) {
      setEnabled(p.enabled);
      setListen(p.listen || defaultListen);
      setTlsCert(p.tls_cert || '');
    }
  });

  const handleSave = async () => {
    setLoading(true);
    try {
      const data = { id: current?.id ?? protocol, protocol, listen, enabled, tls_cert: tlsCert || undefined };
      if (current) await api.updateProxy(current.id, { ...current, ...data });
      else await api.createProxy(data);
      toast.success(t(`${ns}.saved`));
      resync();
      queryClient.invalidateQueries({ queryKey: ['proxies'] });
    } catch (e: any) {
      toast.error(String(e.message || e));
    } finally {
      setLoading(false);
    }
  };

  const tlsOptions = [
    { value: '', label: t('proxies.noTLS') },
    ...certs.filter((c) => !!c.key_file).map((c) => ({ value: c.id, label: `${c.name} (${c.subject})` })),
  ];

  return (
    <Card fill={1} title={t(`${ns}.title`)}>
      <FormStack maxWidth={400}>
        <FormRow>
          <FormGroup label={t('app.enabled')}>
            <Toggle offset checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          </FormGroup>
          <FormGroup label={t(`${ns}.listen`)} required>
            <Input value={listen} onChange={(e) => setListen(e.target.value)} placeholder={defaultListen} />
          </FormGroup>
        </FormRow>
        <FormGroup label={t('proxies.tlsCert')}>
          <Select value={tlsCert} onChange={(e) => setTlsCert(e.target.value)} options={tlsOptions} />
        </FormGroup>
        <Hint>{t(`${ns}.desc`)}</Hint>
        <Button variant="primary" className="align-start" onClick={handleSave} loading={loading}>{t('app.save')}</Button>
      </FormStack>
    </Card>
  );
}
