import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { create } from 'zustand';
import { Button, useConfirm, useToast } from '@hy2scale/ui';
import * as api from '@/api';
import { liveStream } from '@/api/client';
import { versionGreater } from '@/utils/version';

/**
 * Online-update job state (a server-side singleton), shared by the sidebar
 * version menu and Settings → Upgrade. One SSE feed (`useUpgradeFeed`,
 * mounted by the main layout) keeps it current; each frame is a full
 * snapshot, so a missed one heals on the next.
 */
interface UpgradeState {
  status: api.UpgradeStatus | null;
  checking: boolean;
  setStatus: (s: api.UpgradeStatus) => void;
  setChecking: (v: boolean) => void;
}

export const useUpgradeStore = create<UpgradeState>((set) => ({
  status: null,
  checking: false,
  setStatus: (status) => set({ status }),
  setChecking: (checking) => set({ checking }),
}));

/** Build / platform of this node; online update needs the Docker image. */
export const useSystemArch = () => useQuery({ queryKey: ['systemArch'], queryFn: api.getSystemArch, staleTime: 60_000 });

/** A strictly newer release is known (an older GitHub "latest" is no update). */
export const hasUpdate = (s: api.UpgradeStatus | null) => !!s?.latest && versionGreater(s.latest, s.current);

/** Subscribes to the update job while mounted; the first run with no known
 *  latest version checks once. */
export function useUpgradeFeed() {
  const { data: arch } = useSystemArch();
  const enabled = !!arch?.in_docker;
  const { status, checking, setStatus, setChecking } = useUpgradeStore();

  useEffect(() => {
    if (!enabled) return;
    const fetchStatus = () => { api.getUpgradeStatus().then(setStatus).catch(() => {}); };
    fetchStatus();
    return liveStream('/upgrade/events', setStatus, fetchStatus);
  }, [enabled, setStatus]);

  useEffect(() => {
    if (!enabled || !status) return;
    if (status.state === 'idle' && !status.latest && !checking) {
      setChecking(true);
      api.checkUpdate().catch(() => {}).finally(() => setChecking(false));
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, status?.state, status?.latest]);
}

/** The online-update button: check → download (progress) → apply. */
export function UpdateButton({ size }: { size?: 'sm' }) {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const { status, checking, setChecking } = useUpgradeStore();
  const [applying, setApplying] = useState(false);
  if (!status) return null;

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
    return <Button size={size} variant="primary" disabled>{t('settings.downloadingFmt', { pct: Math.floor(status.progress) })}</Button>;
  }
  if (status.state === 'ready') {
    return <Button size={size} variant="success" onClick={apply} loading={applying}>{t('settings.applyUpdate')}</Button>;
  }
  if (status.state === 'applying' || applying) {
    return <Button size={size} variant="primary" disabled>{t('settings.applying')}</Button>;
  }
  if (hasUpdate(status)) {
    return <Button size={size} variant="primary" onClick={download}>{t('settings.onlineUpdate')}</Button>;
  }
  return <Button size={size} onClick={check} loading={checking || status.state === 'checking'}>{t('settings.checkForUpdate')}</Button>;
}
