import { type ReactNode } from 'react';

export interface EmptyStateProps {
  icon?: ReactNode;
  message: ReactNode;
  action?: ReactNode;
}

export function EmptyState({ icon, message, action }: EmptyStateProps) {
  return (
    <div className="hy-empty">
      {icon}
      <div dangerouslySetInnerHTML={typeof message === 'string' ? { __html: message } : undefined}>
        {typeof message !== 'string' ? message : undefined}
      </div>
      {action}
    </div>
  );
}
