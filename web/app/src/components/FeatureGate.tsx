import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Button, Card } from '@hy2scale/ui';
import { getStorageSettings } from '@/api/storage';

export type StoredFeature = 'logs' | 'audit';

/**
 * Whether this node keeps logs / audit records (Settings → Storage). A node
 * without the storage settings (older version) always keeps logs and has
 * no audit.
 */
export function useStoredFeatures() {
  const q = useQuery({ queryKey: ['storageSettings'], queryFn: getStorageSettings, retry: false, staleTime: 30_000 });
  if (q.isError) return { loaded: true, logs: true, audit: false };
  return { loaded: !!q.data, logs: !!q.data?.logs.enabled, audit: !!q.data?.audit.enabled };
}

/** The page of a stored feature, or a notice while the feature is off. */
export default function FeatureGate({ feature, children }: { feature: StoredFeature; children: ReactNode }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const f = useStoredFeatures();
  if (!f.loaded) return null;
  if (f[feature]) return <>{children}</>;
  return (
    <div className="hy-page">
      <Card>
        <div className="feature-off" data-testid={`feature-off-${feature}`}>
          <div className="feature-off-title">{t(`storage.off.${feature}`)}</div>
          <Button variant="primary" onClick={() => navigate('/settings', { state: { settingsTab: 'storage' } })}>
            {t('storage.openSettings')}
          </Button>
        </div>
      </Card>
    </div>
  );
}
