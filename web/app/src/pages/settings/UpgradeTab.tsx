import { useTranslation } from 'react-i18next';
import { Card, Button, Badge, Input, Toggle, FormGroup, FormStack, Hint, useModalState } from '@hy2scale/ui';
import type { UISettings } from '@/api';
import UpgradeUploadModal from '@/components/UpgradeUploadModal';
import { UpdateButton, useSystemArch, useUpgradeStore } from '@/components/upgrade';
import type { SystemForm } from './forms';

/** Settings → Upgrade: version, online update, package upload, automatic update. */
export default function UpgradeTab({ sys, settings }: { sys: SystemForm; settings?: UISettings }) {
  const { t } = useTranslation();
  const { data: archInfo } = useSystemArch();
  const st = useUpgradeStore((s) => s.status);
  const upload = useModalState();

  return (
    <>
      <Card title={t('settings.upgradeTitle')}>
        <FormStack>
          {archInfo && (
            <div className="button-row wrap">
              <span className="upgrade-label">{t('settings.currentArch')}:</span>
              <Badge>{archInfo.os}/{archInfo.arch}</Badge>
              <span className="upgrade-label">{t('settings.currentVersion')}:</span>
              <Badge>{archInfo.version}</Badge>
            </div>
          )}
          {(archInfo?.upgradable ?? archInfo?.in_docker) ? (
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

      <AutoUpdateCard sys={sys} settings={settings} />

      <UpgradeUploadModal {...upload.modalProps} />
    </>
  );
}

/** Daily automatic update: schedule, peer fallback, last run. */
function AutoUpdateCard({ sys, settings }: { sys: SystemForm; settings?: UISettings }) {
  const { t } = useTranslation();
  const { v, set } = sys;
  // Unknown until the backend says so: only an explicit false disables.
  const supported = settings?.auto_update_supported !== false;
  const last = settings?.auto_update_last;
  const lastAt = last?.at ? new Date(last.at) : null;
  const lastLine = last && [
    t('settings.autoUpdateLast', { time: lastAt && !isNaN(lastAt.getTime()) ? lastAt.toLocaleString() : last.at }),
    t(`settings.autoUpdateResult.${last.result}`, { defaultValue: last.result }),
    last.version,
    last.source && t('settings.autoUpdateFrom', { source: last.source }),
  ].filter(Boolean).join(' · ');

  return (
    <Card fill={1} title={t('settings.autoUpdate')}>
      <FormStack maxWidth={480} gap={18}>
        {!supported && <Hint>{t('settings.autoUpdateUnsupported')}</Hint>}
        <FormGroup label={t('app.enabled')}>
          <Toggle checked={v.auto_update_enabled} disabled={!supported}
            onChange={(e) => set('auto_update_enabled', e.target.checked)} data-testid="auto-update" />
        </FormGroup>
        {v.auto_update_enabled && (
          <>
            <FormGroup label={t('settings.autoUpdateTime')} hint={t('settings.autoUpdateTimeDesc')}>
              <Input type="time" value={v.auto_update_time} disabled={!supported}
                onChange={(e) => set('auto_update_time', e.target.value)} data-testid="auto-update-time" />
            </FormGroup>
            <FormGroup label={t('settings.autoUpdatePeers')} hint={t('settings.autoUpdatePeersDesc')}>
              <Toggle checked={v.auto_update_from_peers} disabled={!supported}
                onChange={(e) => set('auto_update_from_peers', e.target.checked)} data-testid="auto-update-peers" />
            </FormGroup>
          </>
        )}
        {lastLine && (
          <div data-testid="auto-update-last">
            <div className="upgrade-latest">{lastLine}</div>
            {last.result === 'failed' && last.error && <div className="upgrade-error">{last.error}</div>}
          </div>
        )}
        <Button variant="primary" className="align-start" disabled={!supported}
          onClick={() => sys.save('update')} loading={sys.saving === 'update'} data-testid="auto-update-save">
          {t('app.save')}
        </Button>
      </FormStack>
    </Card>
  );
}
