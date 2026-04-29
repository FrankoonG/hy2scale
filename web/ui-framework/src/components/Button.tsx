import { type ButtonHTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';

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

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  icon: ReactNode;
  variant?: 'default' | 'danger';
  tooltip?: string;
}

export function IconButton({ icon, variant = 'default', tooltip, className, ...props }: IconButtonProps) {
  return (
    <button
      className={clsx('hy-icon-btn', variant !== 'default' && variant, className)}
      title={tooltip}
      {...props}
    >
      {icon}
    </button>
  );
}
