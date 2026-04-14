import { type ReactNode } from 'react';

export interface AppLayoutProps {
  sidebar: ReactNode;
  topbar: ReactNode;
  children: ReactNode;
}

export function AppLayout({ sidebar, topbar, children }: AppLayoutProps) {
  return (
    <div className="hy-layout">
      {sidebar}
      <div className="hy-main">
        {topbar}
        <div className="hy-content">{children}</div>
      </div>
    </div>
  );
}
