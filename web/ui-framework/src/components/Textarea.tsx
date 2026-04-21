import { forwardRef, type TextareaHTMLAttributes } from 'react';
import clsx from 'clsx';

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  monospace?: boolean;
}

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(
  ({ monospace, className, ...props }, ref) => {
    return (
      <textarea
        ref={ref}
        className={clsx('hy-textarea', monospace && 'mono', className)}
        {...props}
      />
    );
  }
);
Textarea.displayName = 'Textarea';
