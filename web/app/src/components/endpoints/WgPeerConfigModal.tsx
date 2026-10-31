import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Modal, Button } from '@hy2scale/ui';
import { downloadBlob } from '@/utils/download';

export interface WgPeerConfig {
  name: string;
  /** wg-quick .conf text. */
  text: string;
  /** QR image of the config (object URL owned by this dialog), '' if none. */
  qr: string;
}

interface Props {
  open: boolean;
  onClose: () => void;
  animateFrom?: { x: number; y: number };
  config: WgPeerConfig | undefined;
}

/** A peer's client config: QR code, text, download. */
export default function WgPeerConfigModal({ open, onClose, animateFrom, config }: Props) {
  const { t } = useTranslation();

  // Release each QR object URL once the next config replaces it (or on unmount).
  const [qr, setQr] = useState('');
  useEffect(() => {
    const url = config?.qr || '';
    setQr(url);
    return () => { if (url) URL.revokeObjectURL(url); };
  }, [config?.qr]);

  const download = () => {
    if (config) downloadBlob(new Blob([config.text], { type: 'text/plain' }), `${config.name}.conf`);
  };

  return (
    <Modal
      open={open} onClose={onClose}
      title={config?.name}
      animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onClose}>{t('app.close')}</Button>
          <Button variant="primary" onClick={download}>{t('wg.downloadConf')}</Button>
        </>
      }
    >
      {config?.text && (
        <>
          {qr && (
            <div className="wg-qr">
              <img src={qr} alt={t('wg.qrCode')} />
            </div>
          )}
          <pre className="hy-code-block">{config.text}</pre>
        </>
      )}
    </Modal>
  );
}
