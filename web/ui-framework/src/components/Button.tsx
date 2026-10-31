import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import type { IconComponent } from '../icons';

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'default' | 'primary' | 'success' | 'danger' | 'ghost';
  size?: 'sm' | 'md';
  icon?: ReactNode;
  loading?: boolean;
  fullWidth?: boolean;
}

export function Button({
  variant = 'default',
  size = 'md',
  icon,
  loading,
  fullWidth,
  disabled,
  children,
  className,
  ...props
}: ButtonProps) {
  return (
    <button
      className={clsx('hy-btn', variant !== 'default' && variant, size === 'sm' && 'sm', fullWidth && 'full-width', className)}
      disabled={disabled || loading}
      {...props}
    >
      {loading ? <span className="hy-spinner sm" /> : icon}
      {children}
    </button>
  );
}

export interface IconButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'children'> {
  icon: IconComponent;
  /** Accessible name; also the hover title. */
  label: string;
  /**
   * ghost: bare icon, tinted on hover (default) · subtle: ghost dimmed until
   * hover (row actions) · circle: round bordered (inline "generate" next to
   * an input) · outline: square bordered (toolbar).
   */
  variant?: 'ghost' | 'subtle' | 'circle' | 'outline';
  tone?: 'default' | 'danger';
  /** Pressed / open state (outline). */
  active?: boolean;
  iconSize?: number;
}

/** A button that is only an icon. */
export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(
  ({ icon: Icon, label, variant = 'ghost', tone = 'default', active, iconSize = 16, className, type = 'button', ...props }, ref) => (
    <button
      ref={ref}
      type={type}
      className={clsx('hy-icon-btn', variant !== 'ghost' && variant, tone === 'danger' && 'tone-danger', active && 'active', className)}
      title={label}
      aria-label={label}
      {...props}
    >
      <Icon size={iconSize} />
    </button>
  ),
);
IconButton.displayName = 'IconButton';
