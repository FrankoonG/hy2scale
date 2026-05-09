import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Modal, Button, FileDropZone, useConfirm, useToast } from '@hy2scale/ui';
import * as api from '@/api';

interface Props {
  open: boolean;
  onClose: () => void;
  animateFrom?: { x: number; y: number };
}

// Upload-package upgrade modal. Owns the file pick (via the shared
// FileDropZone primitive) + confirm dialog + uploadUpgrade transport
// + post-success page reload. Style is whatever FileDropZone gives us
// — keeps the upgrade dialog visually identical to any other future
// drop-zone surface and keeps Dark Reader handling in one place.
export default function UpgradeUploadModal({ open, onClose, animateFrom }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();

  const [file, setFile] = useState<File | null>(null);
  const [uploading, setUploading] = useState(false);

  useEffect(() => {
    if (!open) {
      setFile(null);
      setUploading(false);
    }
  }, [open]);

  const accept = ['.tar.gz', '.tgz'];
  const looksValid = (name: string) => accept.some((s) => name.toLowerCase().endsWith(s));

  const handleConfirm = async () => {
    if (!file) return;
    const ok = await confirm({
      title: t('settings.upgradeTitle'),
      message: t('settings.upgradeConfirm'),
      danger: true,
      confirmText: t('app.confirm'),
      cancelText: t('app.cancel'),
    });
    if (!ok) return;
    setUploading(true);
    try {
      toast.info(t('settings.upgradeUploading'));
      await api.uploadUpgrade(file);
      toast.success(t('settings.upgradeComplete'));
      onClose();
      setTimeout(() => window.location.reload(), 5000);
    } catch (e: any) {
      toast.error(t('settings.upgradeFailed') + ': ' + String(e.message || e));
    } finally {
      setUploading(false);
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('settings.uploadPackage')}
      animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onClose} disabled={uploading}>{t('app.cancel')}</Button>
          <Button variant="primary" onClick={handleConfirm} loading={uploading} disabled={!file}>
            {t('settings.uploadPackage')}
          </Button>
        </>
      }
    >
      <FileDropZone
        file={file}
        onFileSelected={(f) => setFile(f)}
        accept=".tar.gz,.tgz"
        validate={(f) => {
          if (looksValid(f.name)) return true;
          toast.error(t('settings.upgradeFailed') + ': expected .tar.gz');
          return false;
        }}
        emptyHint={t('settings.uploadDropHint')}
        emptyAcceptHint={t('settings.uploadAccept')}
        replaceHint={t('settings.uploadReplace')}
        disabled={uploading}
      />
    </Modal>
  );
}
