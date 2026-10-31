import { useTranslation } from 'react-i18next';
import { Card, Button, Input, Toggle, FormGroup, FormStack, SectionDivider } from '@hy2scale/ui';
import type { SystemForm } from './forms';

type ResolverNum = 'dns_resolver_cache_ttl' | 'dns_resolver_negative_ttl' | 'dns_resolver_cache_size' | 'dns_resolver_query_timeout_ms';

/** Settings → DNS: upstream servers and the built-in resolver. */
export default function DnsTab({ sys }: { sys: SystemForm }) {
  const { t } = useTranslation();
  const { v, set } = sys;
  const num = (k: ResolverNum, min: number, max: number, suffix?: string) => (
    <Input type="number" min={min} max={max} suffix={suffix} value={v[k]} onChange={(e) => set(k, e.target.value)} />
  );
  return (
    <Card fill={1} title={t('settings.dnsTab')}>
      <FormStack maxWidth={480} gap={18}>
        <FormGroup label={t('settings.dns')}>
          <Input value={v.dns} onChange={(e) => set('dns', e.target.value)} placeholder="8.8.8.8,1.1.1.1" />
        </FormGroup>

        <SectionDivider>{t('settings.dnsResolver')}</SectionDivider>
        <FormGroup label={t('settings.dnsResolverEnabled')} hint={t('settings.dnsResolverDesc')}>
          <Toggle checked={v.dns_resolver_enabled} onChange={(e) => set('dns_resolver_enabled', e.target.checked)} />
        </FormGroup>
        <FormGroup label={t('settings.dnsResolverCacheTtl')}>{num('dns_resolver_cache_ttl', 0, 86400, 's')}</FormGroup>
        <FormGroup label={t('settings.dnsResolverNegativeTtl')}>{num('dns_resolver_negative_ttl', 0, 3600, 's')}</FormGroup>
        <FormGroup label={t('settings.dnsResolverCacheSize')}>{num('dns_resolver_cache_size', 1, 65536)}</FormGroup>
        <FormGroup label={t('settings.dnsResolverQueryTimeout')}>{num('dns_resolver_query_timeout_ms', 100, 30000, 'ms')}</FormGroup>

        <Button variant="primary" className="align-start" onClick={() => sys.save('dns')} loading={sys.saving === 'dns'}>{t('app.save')}</Button>
      </FormStack>
    </Card>
  );
}
