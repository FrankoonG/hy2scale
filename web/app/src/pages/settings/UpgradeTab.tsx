import { useTranslation } from 'react-i18next';
import { Card, Button, Badge, FormStack, Hint, useModalState } from '@hy2scale/ui';
import UpgradeUploadModal from '@/components/UpgradeUploadModal';
import { UpdateButton, useSystemArch, useUpgradeStore } from '@/components/upgrade';

/** Settings → Upgrade: version, online update, package upload. */
export default function UpgradeTab() {
  const { t } = useTranslation();
  const { data: archInfo } = useSystemArch();
  const st = useUpgradeStore((s) => s.status);
  const upload = useModalState();

  return (
    <>
      <Card fill={1} title={t('settings.upgradeTitle')}>
        <FormStack>
          {archInfo && (
            <div className="button-row wrap">
              <span className="upgrade-label">{t('settings.currentArch')}:</span>
              <Badge>{archInfo.os}/{archInfo.arch}</Badge>
              <span className="upgrade-label">{t('settings.currentVersion')}:</span>
              <Badge>{archInfo.version}</Badge>
            </div>
          )}
          {archInfo?.in_docker ? (
            <>
              <Hint>{t('settings.upgradeDesc')}</Hint>
              {/* Online update; the package upload below covers offline / private builds. */}
              {st && (
                <div className="button-row wrap">
                  <UpdateButton />
                  {st.latest && (
                    <span className="upgrade-latest">
                      {t('settings.latestVersion')}: <Badge>{st.latest}</Badge>
                      {st.latest === st.current && <span className="upgrade-uptodate">· {t('settings.upToDate')}</span>}
                    </span>
                  )}
                  {st.state === 'error' && st.error && <span className="upgrade-error">{st.error}</span>}
                </div>
              )}
              <div>
                <Button onClick={(e) => upload.show(undefined, e.currentTarget)}>{t('settings.uploadPackage')}</Button>
              </div>
            </>
          ) : (
            <Hint>{t('settings.upgradeNotAvailable')}</Hint>
          )}
        </FormStack>
      </Card>

      <UpgradeUploadModal {...upload.modalProps} />
    </>
  );
}
