import { QRCodeCanvas } from 'qrcode.react';
import { Tooltip } from '@hy2scale/ui';

/** A link to copy: click copies it, hover shows its QR code. */
export function LinkBox({ url, onCopy }: { url: string; onCopy: (url: string) => void }) {
  return (
    <Tooltip content={<QRCodeCanvas value={url} size={220} bgColor="#ffffff" fgColor="#000000" level="M" includeMargin />}>
      <div className="hy-link-box" onClick={() => onCopy(url)}>{url}</div>
    </Tooltip>
  );
}
