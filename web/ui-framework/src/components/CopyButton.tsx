import { useState, useCallback, type ReactNode } from 'react';
import clsx from 'clsx';

export interface CopyButtonProps {
  text: string;
  children?: ReactNode;
  className?: string;
}

export function CopyButton({ text, children, className }: CopyButtonProps) {
  const [copied, setCopied] = useState(false);

  const handleCopy = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch { /* noop */ }
  }, [text]);

  return (
    <button className={clsx('hy-copy-btn', copied && 'copied', className)} onClick={handleCopy}>
      {copied ? '✓' : '📋'}
      {children}
    </button>
  );
}
