import { forwardRef, type InputHTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';

export interface InputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'prefix' | 'suffix'> {
  error?: boolean;
  prefix?: ReactNode;
  suffix?: ReactNode;
  wrapClassName?: string;
}

export const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ error, prefix, suffix, wrapClassName, className, ...props }, ref) => {
    // If prefix/suffix, wrap in a flex container
    if (prefix || suffix) {
      return (
        <div className={clsx('hy-input-wrap', wrapClassName)} style={{ display: 'flex', alignItems: 'center', gap: 0 }}>
          {prefix && <span className="prefix">{prefix}</span>}
          <input ref={ref} className={clsx('hy-input', error && 'error', className)} style={{ border: 'none', flex: 1, borderRadius: 0 }} {...props} />
          {suffix && <span className="suffix">{suffix}</span>}
        </div>
      );
    }
    return (
      <input ref={ref} className={clsx('hy-input', error && 'error', className)} {...props} />
    );
  }
);
Input.displayName = 'Input';
