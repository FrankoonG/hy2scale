import { useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card, Button, FormStack, Hint, useConfirm, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import { getBasePath } from '@/api/client';
import { downloadBlob } from '@/utils/download';

/** Restart the node, then reload once it answers again. */
function useRestart() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const [restarting, setRestarting] = useState(false);
  const restart = async () => {
    const ok = await confirm({
      title: t('settings.restart'),
      message: t('settings.restartConfirm'),
      danger: true, confirmText: t('settings.restart'), cancelText: t('app.cancel'),
    });
    if (!ok) return;
    setRestarting(true);
    try {
      await api.restartNode();
    } catch (e: any) {
      toast.error(String(e.message || e));
      setRestarting(false);
      return;
    }
    toast.info(t('settings.restarting'));
    // Reload once the node answers again (seen down, or after 5 s if the
    // restart was quicker than the first poll).
    const t0 = Date.now();
    let wentDown = false;
    while (Date.now() - t0 < 120_000) {
      await new Promise((r) => setTimeout(r, 1000));
      let up = false;
      try {
        up = (await fetch(getBasePath() + '/api/build-id', { cache: 'no-store' })).ok;
      } catch { /* down */ }
      if (!up) wentDown = true;
      else if (wentDown || Date.now() - t0 > 5000) {
        window.location.reload();
        return;
      }
    }
    toast.error(t('settings.restartTimeout'));
    setRestarting(false);
  };
  return { restart, restarting };
}

/** Settings → Maintenance: backup / restore and restart. */
export default function MaintenanceTab() {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const { restart, restarting } = useRestart();
  const restoreRef = useRef<HTMLInputElement>(null);

  const handleBackup = async () => {
    try {
      downloadBlob(await api.downloadBackup(), 'hy2scale-backup.tar');
    } catch (e: any) { toast.error(String(e.message || e)); }
  };

  const handleRestore = async (file: File) => {
    const ok = await confirm({
      title: t('settings.backup'), message: t('settings.restoreConfirm'),
      danger: true, confirmText: t('app.confirm'), cancelText: t('app.cancel'),
    });
    if (!ok) return;
    try {
      toast.info(t('settings.restoreUploading'));
      await api.uploadRestore(file);
      toast.success(t('settings.restoreComplete'));
      setTimeout(() => window.location.reload(), 3000);
    } catch (e: any) { toast.error(t('settings.restoreFailed') + ': ' + String(e.message || e)); }
  };

  return (
    <>
      <Card title={t('settings.backup')}>
        <FormStack>
          <Hint>{t('settings.backupDesc')}</Hint>
          <div className="button-row wrap">
            <Button onClick={handleBackup}>{t('settings.downloadBackup')}</Button>
            <Button variant="danger" onClick={() => restoreRef.current?.click()}>{t('settings.restoreFromFile')}</Button>
            <input
              ref={restoreRef}
              type="file"
              accept=".tar"
              hidden
              onChange={(e) => {
                const file = e.target.files?.[0];
                if (file) handleRestore(file);
                e.target.value = '';
              }}
            />
          </div>
        </FormStack>
      </Card>

      <Card fill={1} title={t('settings.restart')}>
        <FormStack>
          <Hint>{t('settings.restartDesc')}</Hint>
          <Button variant="danger" className="align-start" onClick={restart} loading={restarting}>{t('settings.restart')}</Button>
        </FormStack>
      </Card>
    </>
  );
}
