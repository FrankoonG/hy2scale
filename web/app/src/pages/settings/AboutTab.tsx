import { useTranslation } from 'react-i18next';
import { Card } from '@hy2scale/ui';
import LicensePanel from './LicensePanel';

/** Settings → About: licence and bundled components. */
export default function AboutTab() {
  const { t } = useTranslation();
  return (
    <Card fill={1} title={t('settings.about')}>
      <LicensePanel />
    </Card>
  );
}
