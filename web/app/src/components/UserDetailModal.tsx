import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { QRCodeCanvas } from 'qrcode.react';
import { Modal, Button, Tooltip, useToast } from '@hy2scale/ui';
import type { UserConfig } from '@/api';
import * as api from '@/api';
import { useNodeStore } from '@/store/node';
import { DETAIL_PROXIES, type ProxyContext } from '@/config/proxyRegistry';

interface Props {
  open: boolean;
  onClose: () => void;
  user: UserConfig | null;
  animateFrom?: { x: number; y: number };
}

function copyText(text: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    return navigator.clipboard.writeText(text);
  }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.select();
  document.execCommand('copy');
  document.body.removeChild(ta);
  return Promise.resolve();
}

export default function UserDetailModal({ open, onClose, user, animateFrom }: Props) {
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
    copyText(url).then(() => toast.success(t('app.copied'))).catch(() => {});
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={user.username}
      animateFrom={animateFrom}
      footer={<Button onClick={onClose}>{t('app.close')}</Button>}
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        {links.map((link) => (
          <div key={link.label}>
            <div style={{ fontSize: 11, fontWeight: 600, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '.5px', marginBottom: 4 }}>
              {link.label}
            </div>
            <Tooltip content={
              <QRCodeCanvas value={link.url} size={220} bgColor="#ffffff" fgColor="#000000" level="M" includeMargin />
            }>
              <div className="hy-link-box" onClick={() => handleCopy(link.url)}>
                {link.url}
              </div>
            </Tooltip>
          </div>
        ))}
      </div>
    </Modal>
  );
}
