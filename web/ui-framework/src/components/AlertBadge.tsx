import { type ReactNode } from 'react';
import { Tooltip } from './Tooltip';

export interface AlertBadgeProps {
  /** Tooltip content shown on hover. If omitted, no tooltip wrapper. */
  tooltip?: ReactNode;
  /** Icon size in px (default 14). */
  size?: number;
  /** Stroke color (default var(--red)). */
  color?: string;
}

/**
 * Inline warning icon (circled exclamation mark).
 * Designed to sit next to text inside an `inline-flex` container.
 * Wrap the parent with `display:inline-flex; align-items:center; gap:6px`.
 */
export function AlertBadge({ tooltip, size = 14, color = 'var(--red)' }: AlertBadgeProps) {
  const icon = (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke={color}
      strokeWidth="2.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      style={{ flexShrink: 0, cursor: tooltip ? 'help' : undefined }}
    >
      <circle cx="12" cy="12" r="10" />
      <line x1="12" y1="8" x2="12" y2="12" />
      <line x1="12" y1="16" x2="12.01" y2="16" />
    </svg>
  );

  if (!tooltip) return icon;
  return <Tooltip content={tooltip}>{icon}</Tooltip>;
}
