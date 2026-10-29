import { type ReactNode, useState } from 'react';
import { useNavigate, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { AppLayout, Sidebar, Topbar, useMediaQuery } from '@hy2scale/ui';
import { useAuthStore } from '@/store/auth';
import { useNodeStore } from '@/store/node';
import UserMenu from '@/components/UserMenu';
import { usePageViewStore, isTopoPage } from '@/store/pageView';

// SVG icons for sidebar
const icons = {
  logs: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/><path d="M14 2v6h6M8 13h8M8 17h5"/></svg>,
  overview: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><rect x="3" y="3" width="7" height="9" rx="1.5"/><rect x="14" y="3" width="7" height="5" rx="1.5"/><rect x="14" y="12" width="7" height="9" rx="1.5"/><rect x="3" y="16" width="7" height="5" rx="1.5"/></svg>,
  nodes: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="12" cy="5" r="3"/><circle cx="5" cy="19" r="3"/><circle cx="19" cy="19" r="3"/><path d="M12 8v4m-4.5 3.5L10 12m4 0l2.5 3.5"/></svg>,
  interfaces: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 16v-5h2V9h6v2h2v5z"/><path d="M10 16v-2M12 16v-2M14 16v-2"/></svg>,
  users: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M17 21v-2a4 4 0 00-4-4H5a4 4 0 00-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 00-3-3.87m-4-12a4 4 0 010 7.75"/></svg>,
  proxies: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg>,
  endpoints: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M12 22v-5"/><path d="M9 8V2M15 8V2"/><path d="M18 8v4a6 6 0 01-12 0V8z"/></svg>,
  rules: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M22 3H2l8 9.46V19l4 2v-8.54L22 3z"/></svg>,
  tls: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0110 0v4"/></svg>,
  settings: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 01-2.83 2.83l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-4 0v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 010-4h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 012.83-2.83l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 014 0v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 010 4h-.09a1.65 1.65 0 00-1.51 1z"/></svg>,
};

// Shown beside a switchable page title: the view a click switches to.
const viewIcons = {
  list: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><line x1="8" y1="6" x2="21" y2="6"/><line x1="8" y1="12" x2="21" y2="12"/><line x1="8" y1="18" x2="21" y2="18"/><circle cx="4" cy="6" r="1"/><circle cx="4" cy="12" r="1"/><circle cx="4" cy="18" r="1"/></svg>,
  topo: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="6" cy="6" r="2.5"/><circle cx="18" cy="6" r="2.5"/><circle cx="12" cy="18" r="2.5"/><path d="M7.5 8l3.5 8M16.5 8L13 16M8.5 6h7"/></svg>,
};

// Sidebar groups. Logs and Settings are pinned to the bottom, above the collapse toggle.
const navGroups: { key: string; label?: string; items: string[] }[] = [
  { key: 'main', items: ['overview'] },
  { key: 'network', label: 'nav.groupNetwork', items: ['nodes', 'interfaces', 'rules'] },
  { key: 'access', label: 'nav.groupAccess', items: ['users', 'proxies', 'endpoints', 'tls'] },
];
const bottomNav = ['logs', 'settings'];
const navPaths: Record<string, string> = {
  overview: '/overview', nodes: '/nodes', interfaces: '/interfaces', rules: '/rules',
  users: '/users', proxies: '/proxies', endpoints: '/endpoints', tls: '/tls', logs: '/logs', settings: '/settings',
};
const navKeys = Object.keys(navPaths);

const COLLAPSE_KEY = 'hy-sidebar-collapsed';
function initialCollapsed(): boolean {
  try { return localStorage.getItem(COLLAPSE_KEY) === '1'; } catch { return false; }
}

export default function MainLayout({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const node = useNodeStore((s) => s.node);
  const isMobile = useMediaQuery('(max-width: 768px)');
  const [mobileOpen, setMobileOpen] = useState(false);
  const [collapsedPref, setCollapsedPref] = useState(initialCollapsed);
  const collapsed = !isMobile && collapsedPref;
  const toggleCollapsed = () => {
    setCollapsedPref((v) => {
      const next = !v;
      try { localStorage.setItem(COLLAPSE_KEY, next ? '1' : '0'); } catch { /* storage unavailable */ }
      return next;
    });
  };

  const forcePasswordChange = useAuthStore((s) => s.forcePasswordChange);
  const activePage = navKeys.find((k) => location.pathname.startsWith(navPaths[k])) || 'overview';
  const views = usePageViewStore((s) => s.views);
  const toggleView = usePageViewStore((s) => s.toggle);
  const switchable = isTopoPage(activePage);
  const view = switchable ? views[activePage] : 'list';
  const pageTitle = switchable ? (
    <button
      type="button"
      className="hy-view-switch"
      data-testid="view-switch"
      title={t(view === 'topo' ? 'app.viewList' : 'app.viewTopo')}
      onClick={() => toggleView(activePage)}
    >
      {t(`nav.${activePage}` as any)}
      {view === 'topo' ? viewIcons.list : viewIcons.topo}
    </button>
  ) : t(`nav.${activePage}` as any);

  const navItem = (key: string) => ({
    key,
    label: t(`nav.${key}` as any),
    icon: icons[key as keyof typeof icons],
    disabled: forcePasswordChange && key !== 'settings',
  });
  const sidebarSections = navGroups.map((g) => ({
    key: g.key,
    label: g.label ? t(g.label as any) : undefined,
    items: g.items.map(navItem),
  }));

  // Version badge matching old frontend style (green/orange/red + mode text)
  const versionBadge = node?.version ? (
    <span className={`version-badge${node.limited ? ' limited' : node.compat ? ' compat' : ''}`} style={{ fontSize: 10 }}>
      v{node.version}{node.limited ? ' Limited' : node.compat ? ' Compat' : ''}
    </span>
  ) : null;

  return (
    <AppLayout
      full={view === 'topo'}
      sidebarCollapsed={collapsed}
      sidebar={
        <Sidebar
          sections={sidebarSections}
          bottomItems={bottomNav.map(navItem)}
          activeKey={activePage}
          onSelect={(key) => { if (navPaths[key]) navigate(navPaths[key]); }}
          mobile={isMobile && mobileOpen}
          onClose={() => setMobileOpen(false)}
          collapsed={collapsed}
          onToggleCollapse={isMobile ? undefined : toggleCollapsed}
          toggleLabel={t(collapsed ? 'app.expandNav' : 'app.collapseNav')}
          logo={
            <>
              <div className="logo"><img src="./logo.svg" alt="" className="logo-img" /></div>
              <div>
                <div style={{ fontSize: 14, fontWeight: 700, letterSpacing: '-.3px', whiteSpace: 'nowrap' }}>HY2 SCALE</div>
                {versionBadge}
              </div>
            </>
          }
        />
      }
      topbar={
        <Topbar
          title={pageTitle}
          onMenuClick={isMobile ? () => setMobileOpen(true) : undefined}
        >
          <div className="spacer" />
          {node?.name && <span className="node-badge">{node.name}</span>}
          <UserMenu />
        </Topbar>
      }
    >
      <div className="page-enter" key={activePage}>
        {children}
      </div>
    </AppLayout>
  );
}
