import { useTranslation } from 'react-i18next';
import { Card, Button, Input, FormGroup, FormStack, Hint } from '@hy2scale/ui';
import type { SystemForm } from './forms';

type NestedKey = 'max_nested_depth' | 'max_response_nodes' | 'max_cache_entries' | 'max_response_bytes' | 'max_fetch_fan_out';

/** Settings → Nested discovery: limits of the nested peer cache. */
export default function NestedTab({ sys }: { sys: SystemForm }) {
  const { t } = useTranslation();
  const { v, set } = sys;
  const num = (k: NestedKey, min: number, max: number, suffix?: string) => (
    <Input type="number" min={min} max={max} suffix={suffix} value={v[k]} onChange={(e) => set(k, e.target.value)} />
  );
  return (
    <Card fill={1} title={t('settings.nestedLimits')}>
      <FormStack maxWidth={480} gap={18}>
        <Hint className="hint-block">{t('settings.nestedLimitsDesc')}</Hint>
        <FormGroup label={t('settings.maxNestedDepth')}>{num('max_nested_depth', 1, 10)}</FormGroup>
        <FormGroup label={t('settings.maxResponseNodes')}>{num('max_response_nodes', 1, 65536)}</FormGroup>
        <FormGroup label={t('settings.maxCacheEntries')}>{num('max_cache_entries', 1, 100000)}</FormGroup>
        <FormGroup label={t('settings.maxResponseBytes')}>{num('max_response_bytes', 1024, 16777216, 'B')}</FormGroup>
        <FormGroup label={t('settings.maxFetchFanOut')}>{num('max_fetch_fan_out', 1, 64)}</FormGroup>
        <div className="button-row">
          <Button variant="primary" onClick={() => sys.save('nested')} loading={sys.saving === 'nested'}>{t('app.save')}</Button>
          <Button variant="ghost" onClick={sys.restoreNestedDefaults}>{t('settings.restoreDefaults')}</Button>
        </div>
      </FormStack>
    </Card>
  );
}
