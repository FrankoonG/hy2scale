import { type ReactNode } from 'react';
import clsx from 'clsx';

export interface AppLayoutProps {
  sidebar: ReactNode;
  topbar: ReactNode;
  children: ReactNode;
  /** The page fills the content area edge to edge (topology views). */
  full?: boolean;
  /** The sidebar is the narrow icon rail; the main area follows its width. */
  sidebarCollapsed?: boolean;
}

export function AppLayout({ sidebar, topbar, children, full, sidebarCollapsed }: AppLayoutProps) {
  return (
    <div className={clsx('hy-layout', sidebarCollapsed && 'sb-collapsed')}>
      {sidebar}
      <div className="hy-main">
        {topbar}
        <div className={full ? 'hy-content full' : 'hy-content'}>{children}</div>
      </div>
    </div>
  );
}
