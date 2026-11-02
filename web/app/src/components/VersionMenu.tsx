import { useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { motion, AnimatePresence } from 'framer-motion';
import { Button, popMotion, useDismiss, useDropdownPosition, useModalState } from '@hy2scale/ui';
import VersionBadge from '@/components/VersionBadge';
import UpgradeUploadModal from '@/components/UpgradeUploadModal';
import { UpdateButton, hasUpdate, useSystemArch, useUpgradeFeed, useUpgradeStore } from '@/components/upgrade';

/**
 * This node's version pill (sidebar). Yellow while a newer release is known;
 * a click opens the version / online-update panel.
 */
export default function VersionMenu({ version, limited }: { version: string; limited?: boolean }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  useUpgradeFeed();
  const status = useUpgradeStore((s) => s.status);
  const { data: arch } = useSystemArch();
  const upload = useModalState();
  const [open, setOpen] = useState(false);
  const btn = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const pos = useDropdownPosition(open, btn, 320, 6);
  useDismiss([btn, panel], () => setOpen(false), { enabled: open });
  const update = hasUpdate(status);

  return (
    <>
      <button ref={btn} type="button" className="version-menu-btn" aria-haspopup="dialog" aria-expanded={open}
        data-testid="version-menu" title={update ? t('version.updateAvailable') : undefined}
        onClick={() => setOpen((v) => !v)}>
        <VersionBadge version={version} limited={limited} update={update} selfBuilt={arch?.official === false} />
      </button>
      {createPortal(
        <AnimatePresence>
          {open && pos && (
            <motion.div ref={panel} className="hy-dropdown version-menu" role="dialog" aria-label={t('settings.upgradeTitle')}
              style={{ position: 'fixed', top: pos.top, left: pos.left, transformOrigin: 'top left' }} {...popMotion}>
              <dl className="version-menu-rows">
                <dt>{t('settings.currentVersion')}</dt>
                <dd>v{version}{arch && <span className="version-menu-arch">{arch.os}/{arch.arch}</span>}</dd>
                {arch && typeof arch.official === 'boolean' && (
                  <>
                    <dt>{t('build.origin')}</dt>
                    <dd data-testid="version-build">{arch.official ? t('build.official') : t('build.selfBuilt')}</dd>
                  </>
                )}
                {status?.latest && (
                  <>
                    <dt>{t('settings.latestVersion')}</dt>
                    <dd className={update ? 'version-menu-new' : undefined} data-testid="version-latest">
                      v{status.latest.replace(/^v/, '')}
                      {!update && <span className="version-menu-ok">{t('settings.upToDate')}</span>}
                    </dd>
                  </>
                )}
              </dl>
              {status?.state === 'error' && status.error && <div className="version-menu-error">{status.error}</div>}
              {arch?.in_docker ? (
                <div className="version-menu-actions">
                  <UpdateButton size="sm" />
                  <Button size="sm" variant="ghost" onClick={(e) => { setOpen(false); upload.show(undefined, e.currentTarget); }}>
                    {t('version.uploadPackage')}
                  </Button>
                </div>
              ) : arch && <div className="version-menu-note">{t('version.manualUpgrade')}</div>}
              <button type="button" className="hy-dropdown-item version-menu-link"
                onClick={() => { setOpen(false); navigate('/settings', { state: { settingsTab: 'upgrade' } }); }}>
                {t('version.upgradeSettings')}
              </button>
            </motion.div>
          )}
        </AnimatePresence>,
        document.body,
      )}
      <UpgradeUploadModal {...upload.modalProps} />
    </>
  );
}
