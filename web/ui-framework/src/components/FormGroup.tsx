import { type CSSProperties, type ReactNode } from 'react';
import clsx from 'clsx';

export interface FormGroupProps {
  label?: string;
  required?: boolean;
  /** One short line under the control saying what the field is. */
  hint?: ReactNode;
  error?: string;
  children: ReactNode;
  className?: string;
  fullWidth?: boolean;
  testId?: string;
}

export function FormGroup({ label, required, hint, error, children, className, fullWidth, testId }: FormGroupProps) {
  return (
    <div className={clsx('hy-form-group', fullWidth && 'full-width', className)} data-testid={testId}>
      {label && (
        <label className="hy-form-label">
          {label}
          {required && <span className="required">*</span>}
        </label>
      )}
      {children}
      {hint && <div className="hy-form-hint">{hint}</div>}
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

export interface FormStackProps {
  children: ReactNode;
  /** Max width in px (form cards keep a readable line length). */
  maxWidth?: number;
  /** Vertical gap in px (default 14). */
  gap?: number;
  className?: string;
  style?: CSSProperties;
}

/** Fields stacked vertically. */
export function FormStack({ children, maxWidth, gap = 14, className, style }: FormStackProps) {
  return (
    <div className={clsx('hy-form-stack', className)} style={{ maxWidth, gap, ...style }}>
      {children}
    </div>
  );
}

/** Fields side by side: the first keeps its own width, the rest share the remainder. */
export function FormRow({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={clsx('hy-form-row', className)}>{children}</div>;
}

/** A short muted line of text inside a form or card. */
export function Hint({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={clsx('hy-hint', className)}>{children}</div>;
}
