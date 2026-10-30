import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Card, Button, Badge, FormStack, Hint, useConfirm, useModalState, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import UpgradeUploadModal from '@/components/UpgradeUploadModal';
import { versionGreater } from '@/utils/version';

/**
 * Online-update job state (a server-side singleton). While mounted, SSE keeps
 * every tab in sync; each frame is a full snapshot, so a missed one heals on
 * the next. The first visit with no known latest version runs one check.
 */
function useUpgradeStatus(enabled: boolean) {
  const [status, setStatus] = useState<api.UpgradeStatus | null>(null);
  const [checking, setChecking] = useState(false);

  useEffect(() => {
    if (!enabled) return;
    api.getUpgradeStatus().then(setStatus).catch(() => {});
    const base = (window as any).__BASE__ || '';
    const token = sessionStorage.getItem('token:' + base);
    const es = new EventSource(`${base}/api/upgrade/events${token ? '?token=' + encodeURIComponent(token) : ''}`);
    es.onmessage = (e) => { try { setStatus(JSON.parse(e.data)); } catch { /* partial frame */ } };
    return () => es.close();
  }, [enabled]);

  useEffect(() => {
    if (!enabled || !status) return;
    if (status.state === 'idle' && !status.latest && !checking) {
      setChecking(true);
      api.checkUpdate().catch(() => {}).finally(() => setChecking(false));
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, status?.state, status?.latest]);

  return { status, checking, setChecking };
}

/** The online-update button: check → download (progress) → apply. */
function UpdateButton({ status, checking, setChecking }: ReturnType<typeof useUpgradeStatus> & { status: api.UpgradeStatus }) {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const [applying, setApplying] = useState(false);

  const check = async () => {
    setChecking(true);
    try { await api.checkUpdate(); }
    catch (e: any) { toast.error(String(e.message || e)); }
    finally { setChecking(false); }
  };
  const download = async () => {
    try { await api.startUpgradeDownload(); }
    catch (e: any) { toast.error(String(e.message || e)); }
  };
  const apply = async () => {
    const ok = await confirm({
      title: t('settings.applyUpdate'), message: t('settings.upgradeConfirm'),
      danger: true, confirmText: t('app.confirm'), cancelText: t('app.cancel'),
    });
    if (!ok) return;
    setApplying(true);
    try {
      await api.applyUpgrade();
      toast.success(t('settings.upgradeApplying'));
      setTimeout(() => window.location.reload(), 5000);
    } catch (e: any) {
      toast.error(String(e.message || e));
      setApplying(false);
    }
  };

  if (status.state === 'downloading') {
    return <Button variant="primary" disabled>{t('settings.downloadingFmt', { pct: Math.floor(status.progress) })}</Button>;
  }
  if (status.state === 'ready') {
    return <Button variant="success" onClick={apply} loading={applying}>{t('settings.applyUpdate')}</Button>;
  }
  if (status.state === 'applying' || applying) {
    return <Button variant="primary" disabled>{t('settings.applying')}</Button>;
  }
  // Strictly newer only: a GitHub "latest" older than this build is no update.
  if (status.latest && versionGreater(status.latest, status.current)) {
    return <Button variant="primary" onClick={download}>{t('settings.onlineUpdate')}</Button>;
  }
  return <Button onClick={check} loading={checking || status.state === 'checking'}>{t('settings.checkForUpdate')}</Button>;
}

/** Settings → Upgrade: version, online update, package upload. */
export default function UpgradeTab() {
  const { t } = useTranslation();
  const { data: archInfo } = useQuery({ queryKey: ['systemArch'], queryFn: api.getSystemArch });
  const upgrade = useUpgradeStatus(!!archInfo?.in_docker);
  const upload = useModalState();
  const st = upgrade.status;

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
                  <UpdateButton {...upgrade} status={st} />
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
