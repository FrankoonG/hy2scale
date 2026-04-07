import { type ReactNode } from 'react';
import clsx from 'clsx';

export interface FormGroupProps {
  label?: string;
  required?: boolean;
  error?: string;
  children: ReactNode;
  className?: string;
  fullWidth?: boolean;
}

export function FormGroup({ label, required, error, children, className, fullWidth }: FormGroupProps) {
  return (
    <div className={clsx('hy-form-group', fullWidth && 'full-width', className)}>
      {label && (
        <label className="hy-form-label">
          {label}
          {required && <span className="required">*</span>}
        </label>
      )}
      {children}
      {error && <span className="hy-form-error">{error}</span>}
    </div>
  );
}

export interface FormGridProps {
  columns?: 1 | 2;
  children: ReactNode;
  className?: string;
}

export function FormGrid({ columns = 2, children, className }: FormGridProps) {
  return (
    <div className={clsx('hy-form-grid', columns === 1 && 'cols-1', className)}>
      {children}
    </div>
  );
}
