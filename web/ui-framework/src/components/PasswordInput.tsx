import { forwardRef, useState, type InputHTMLAttributes } from 'react';
import clsx from 'clsx';

export interface PasswordInputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'type'> {
  showToggle?: boolean;
  error?: boolean;
}

export const PasswordInput = forwardRef<HTMLInputElement, PasswordInputProps>(
  ({ showToggle = true, error, className, ...props }, ref) => {
    const [visible, setVisible] = useState(false);
    return (
      <div className="hy-pw-wrap">
        <input
          ref={ref}
          type={visible ? 'text' : 'password'}
          className={clsx('hy-input', error && 'error', className)}
          {...props}
        />
        {showToggle && (
          <button
            type="button"
            className={clsx('hy-pw-eye', visible && 'visible')}
            onClick={() => setVisible(!visible)}
            tabIndex={-1}
          >
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
          </button>
        )}
      </div>
    );
  }
);
PasswordInput.displayName = 'PasswordInput';
