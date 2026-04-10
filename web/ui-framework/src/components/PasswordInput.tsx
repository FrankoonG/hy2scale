import { forwardRef, useState, type InputHTMLAttributes } from 'react';
import clsx from 'clsx';

function generatePassword(len = 24): string {
  const chars = 'abcdefghijklmnopqrstuvwxyz0123456789';
  const arr = new Uint8Array(len);
  crypto.getRandomValues(arr);
  return Array.from(arr, (b) => chars[b % chars.length]).join('');
}

export interface PasswordInputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'type'> {
  showToggle?: boolean;
  error?: boolean;
  /** When provided, shows a random-generate button. Callback receives the generated password. */
  onGenerate?: (password: string) => void;
}

export const PasswordInput = forwardRef<HTMLInputElement, PasswordInputProps>(
  ({ showToggle = true, error, className, onGenerate, ...props }, ref) => {
    const [visible, setVisible] = useState(false);
    const hasGenerate = !!onGenerate;

    const handleGenerate = () => {
      const pw = generatePassword();
      setVisible(true);
      onGenerate?.(pw);
    };

    return (
      <div className={clsx('hy-pw-wrap', hasGenerate && 'hy-pw-wrap--gen')}>
        <input
          ref={ref}
          type={visible ? 'text' : 'password'}
          className={clsx('hy-input', error && 'error', className)}
          {...props}
        />
        {hasGenerate && (
          <button
            type="button"
            className="hy-pw-btn hy-pw-gen"
            onClick={handleGenerate}
            tabIndex={-1}
            title="Generate"
          >
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><polyline points="16 3 21 3 21 8"/><line x1="4" y1="20" x2="21" y2="3"/><polyline points="21 16 21 21 16 21"/><line x1="15" y1="15" x2="21" y2="21"/><line x1="4" y1="4" x2="9" y2="9"/></svg>
          </button>
        )}
        {showToggle && (
          <button
            type="button"
            className={clsx('hy-pw-btn hy-pw-eye', visible && 'visible')}
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
