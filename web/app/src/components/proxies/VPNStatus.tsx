import { useTranslation } from 'react-i18next';
import { Badge } from '@hy2scale/ui';

/** Running / connected-clients badge for the userspace VPN servers. */
export default function VPNStatus({ running, connected }: { running?: boolean; connected?: number }) {
  const { t } = useTranslation();
  if (!running) return null;
  return (
    <Badge variant="green">{connected ? t('vpn.connectedCount', { count: connected }) : t('vpn.running')}</Badge>
  );
}
