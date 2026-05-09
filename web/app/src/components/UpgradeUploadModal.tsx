import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Modal, Button, useConfirm, useToast } from '@hy2scale/ui';
import * as api from '@/api';

interface Props {
  open: boolean;
  onClose: () => void;
  animateFrom?: { x: number; y: number };
}

// UpgradeUploadModal replaces the previous "click button → native file
// picker" upload flow with a styled drop-zone + click-to-pick combo,
// matching NodeImportModal's modal shape. Keeps the existing
// confirm-before-upload safety dialog and the existing api.uploadUpgrade
// transport.
export default function UpgradeUploadModal({ open, onClose, animateFrom }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();

  const [file, setFile] = useState<File | null>(null);
  const [dragOver, setDragOver] = useState(false);
  const [uploading, setUploading] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!open) {
      setFile(null);
      setDragOver(false);
      setUploading(false);
    }
  }, [open]);

  const accept = ['.tar.gz', '.tgz'];
  const looksValid = (name: string) => accept.some((s) => name.toLowerCase().endsWith(s));

  const setOrReject = (f: File) => {
    if (!looksValid(f.name)) {
      toast.error(t('settings.upgradeFailed') + ': expected .tar.gz');
      return;
    }
    setFile(f);
  };

  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setDragOver(false);
    const f = e.dataTransfer.files?.[0];
    if (f) setOrReject(f);
  };

  const handleDragOver = (e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setDragOver(true);
  };

  const handleDragLeave = (e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setDragOver(false);
  };

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

  const fmtBytes = (n: number) => {
    if (n < 1024) return n + ' B';
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
    return (n / 1024 / 1024).toFixed(1) + ' MB';
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
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        <div
          onDrop={handleDrop}
          onDragOver={handleDragOver}
          onDragEnter={handleDragOver}
          onDragLeave={handleDragLeave}
          onClick={() => inputRef.current?.click()}
          style={{
            border: `2px dashed ${dragOver ? 'var(--primary, #3b82f6)' : 'var(--border)'}`,
            borderRadius: 8,
            padding: '28px 20px',
            background: dragOver ? 'var(--bg-hover, rgba(59,130,246,.06))' : 'var(--bg-secondary)',
            textAlign: 'center',
            cursor: uploading ? 'not-allowed' : 'pointer',
            transition: 'border-color .15s, background .15s',
            userSelect: 'none',
          }}
        >
          {file ? (
            <>
              <div style={{ fontSize: 14, fontFamily: 'var(--mono)', wordBreak: 'break-all' }}>{file.name}</div>
              <div style={{ fontSize: 12, color: 'var(--text-muted)', marginTop: 4 }}>{fmtBytes(file.size)}</div>
              <div style={{ fontSize: 12, color: 'var(--text-muted)', marginTop: 8 }}>{t('settings.uploadReplace')}</div>
            </>
          ) : (
            <>
              <div style={{ fontSize: 14 }}>{t('settings.uploadDropHint')}</div>
              <div style={{ fontSize: 12, color: 'var(--text-muted)', marginTop: 4 }}>{t('settings.uploadAccept')}</div>
            </>
          )}
          <input
            ref={inputRef}
            type="file"
            accept=".tar.gz,.tgz"
            style={{ display: 'none' }}
            onChange={(e) => {
              const f = e.target.files?.[0];
              if (f) setOrReject(f);
              e.target.value = '';
            }}
            disabled={uploading}
          />
        </div>
      </div>
    </Modal>
  );
}
