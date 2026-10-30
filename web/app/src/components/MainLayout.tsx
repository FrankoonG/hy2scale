import { type ReactNode, useLayoutEffect, useState } from 'react';
import { useNavigate, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  AppLayout, Sidebar, Topbar, useMediaQuery, useStoredState, boolCodec, type IconComponent,
  IconLayoutDashboard, IconWaypoints, IconEthernetPort, IconSplit, IconUsers, IconServer, IconPlug,
  IconShieldCheck, IconScrollText, IconSettings, IconList, IconNetwork,
} from '@hy2scale/ui';
import { useAuthStore } from '@/store/auth';
import { useNodeStore } from '@/store/node';
import UserMenu from '@/components/UserMenu';
import VersionMenu from '@/components/VersionMenu';
import { usePageViewStore, isTopoPage } from '@/store/pageView';
import BrandLogo from '@/components/login/BrandLogo';
import { playAppEnter, takeHandoff } from '@/hooks/authTransition';
import { SELF_KEY } from '@/utils/nodeKeys';

// Sidebar icons: one per page, matching what the page manages.
const icons: Record<string, IconComponent> = {
  overview: IconLayoutDashboard,   // dashboard
  nodes: IconWaypoints,            // mesh peers
  interfaces: IconEthernetPort,    // network interfaces
  rules: IconSplit,                // traffic split by IP / domain
  users: IconUsers,
  proxies: IconServer,             // inbound proxy / VPN services
  endpoints: IconPlug,             // WireGuard endpoints
  tls: IconShieldCheck,            // certificates
  logs: IconScrollText,
  settings: IconSettings,
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

export default function MainLayout({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  // arriving from the login page: its divider slides in to the sidebar edge
  useLayoutEffect(() => { const h = takeHandoff('login'); if (h) playAppEnter(h); }, []);
  const navigate = useNavigate();
  const location = useLocation();
  const node = useNodeStore((s) => s.node);
  const isMobile = useMediaQuery('(max-width: 768px)');
  const [mobileOpen, setMobileOpen] = useState(false);
  const [collapsedPref, setCollapsedPref] = useStoredState(COLLAPSE_KEY, false, boolCodec);
  const collapsed = !isMobile && collapsedPref;
  const toggleCollapsed = () => setCollapsedPref((v) => !v);

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
      {view === 'topo' ? <IconList /> : <IconNetwork />}
    </button>
  ) : t(`nav.${activePage}` as any);

  const navItem = (key: string) => {
    const Icon = icons[key];
    return {
      key,
      label: t(`nav.${key}` as any),
      icon: <Icon />,
      disabled: forcePasswordChange && key !== 'settings',
    };
  };
  const sidebarSections = navGroups.map((g) => ({
    key: g.key,
    label: g.label ? t(g.label as any) : undefined,
    items: g.items.map(navItem),
  }));

  const versionBadge = node?.version
    ? <VersionMenu version={node.version} limited={node.limited} />
    : null;

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
              <BrandLogo />
              <div>
                <div className="hy-brand">HY2 SCALE</div>
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
          {node?.name && (
            <button type="button" className="node-badge" title={t('app.selectSelfNode')} data-testid="node-badge"
              onClick={() => navigate('/nodes', { state: { select: SELF_KEY } })}>{node.name}</button>
          )}
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
