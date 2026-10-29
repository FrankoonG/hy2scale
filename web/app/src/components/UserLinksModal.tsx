import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { QRCodeCanvas } from 'qrcode.react';
import { Modal, Button, Tooltip, FieldLabel, FormStack, useToast } from '@hy2scale/ui';
import type { UserConfig } from '@/api';
import * as api from '@/api';
import { useNodeStore } from '@/store/node';
import { DETAIL_PROXIES, type ProxyContext } from '@/config/proxyRegistry';
import { copyToClipboard } from '@/utils/clipboard';

interface Props {
  open: boolean;
  onClose: () => void;
  user: UserConfig | null;
  animateFrom?: { x: number; y: number };
}

/** Username click: the user's share links, each with a QR code on hover. */
export default function UserLinksModal({ open, onClose, user, animateFrom }: Props) {
  const { t } = useTranslation();
  const toast = useToast();
  const node = useNodeStore((s) => s.node);

  const { data: proxies = [] } = useQuery({ queryKey: ['proxies'], queryFn: api.getProxies, enabled: open });
  const { data: ssConfig } = useQuery({ queryKey: ['ss'], queryFn: api.getSS, enabled: open });

  if (!user) return null;

  const host = window.location.hostname || 'localhost';
  const serverPort = node?.server?.listen?.replace(/.*:/, '') || '5565';

  const ctx: ProxyContext = { serverPort, ssConfig, proxies };

  const links = DETAIL_PROXIES.map((def) => {
    const port = def.getPort(ctx);
    const pw = (def.authType === 'password' && user.proxy_passwords?.[def.key]) || user.password;
    const url = def.buildUrl({ host, port, username: user.username, password: pw }, ctx);
    return { label: def.label, url };
  });

  const handleCopy = (url: string) => {
    copyToClipboard(url).then((ok) => { if (ok) toast.success(t('app.copied')); });
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={user.username}
      animateFrom={animateFrom}
      footer={<Button onClick={onClose}>{t('app.close')}</Button>}
    >
      <FormStack gap={12}>
        {links.map((link) => (
          <div key={link.label}>
            <FieldLabel>{link.label}</FieldLabel>
            <Tooltip content={
              <QRCodeCanvas value={link.url} size={220} bgColor="#ffffff" fgColor="#000000" level="M" includeMargin />
            }>
              <div className="hy-link-box" onClick={() => handleCopy(link.url)}>
                {link.url}
              </div>
            </Tooltip>
          </div>
        ))}
      </FormStack>
    </Modal>
  );
}
