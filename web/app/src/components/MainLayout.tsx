import { type ReactNode, useState } from 'react';
import { useNavigate, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { AppLayout, Sidebar, Topbar, Button, useMediaQuery } from '@hy2scale/ui';
import { useAuthStore } from '@/store/auth';
import { useNodeStore } from '@/store/node';
import LanguageSwitcher from '@/components/LanguageSwitcher';

// SVG icons for sidebar
const icons = {
  nodes: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="12" cy="5" r="3"/><circle cx="5" cy="19" r="3"/><circle cx="19" cy="19" r="3"/><path d="M12 8v4m-4.5 3.5L10 12m4 0l2.5 3.5"/></svg>,
  users: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M17 21v-2a4 4 0 00-4-4H5a4 4 0 00-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 00-3-3.87m-4-12a4 4 0 010 7.75"/></svg>,
  proxies: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg>,
  rules: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M22 3H2l8 9.46V19l4 2v-8.54L22 3z"/></svg>,
  tls: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0110 0v4"/></svg>,
  settings: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 01-2.83 2.83l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-4 0v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 010-4h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 012.83-2.83l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 014 0v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 010 4h-.09a1.65 1.65 0 00-1.51 1z"/></svg>,
};

const logoutIcon = <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></svg>;

const navItems = [
  { key: 'nodes', path: '/nodes', icon: icons.nodes },
  { key: 'users', path: '/users', icon: icons.users },
  { key: 'proxies', path: '/proxies', icon: icons.proxies },
  { key: 'rules', path: '/rules', icon: icons.rules },
  { key: 'tls', path: '/tls', icon: icons.tls },
  { key: 'settings', path: '/settings', icon: icons.settings },
];

export default function MainLayout({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const logout = useAuthStore((s) => s.logout);
  const node = useNodeStore((s) => s.node);
  const isMobile = useMediaQuery('(max-width: 768px)');
  const [mobileOpen, setMobileOpen] = useState(false);

  const forcePasswordChange = useAuthStore((s) => s.forcePasswordChange);
  const activePage = navItems.find((n) => location.pathname.startsWith(n.path))?.key || 'nodes';
  const pageTitle = t(`nav.${activePage}` as any);

  const sidebarItems = navItems.map((n) => ({
    key: n.key,
    label: t(`nav.${n.key}` as any),
    icon: n.icon,
    disabled: forcePasswordChange && n.key !== 'settings',
  }));

  // Version badge matching old frontend style (green/orange/red + mode text)
  const versionBadge = node?.version ? (
    <span className={`version-badge${node.limited ? ' limited' : node.compat ? ' compat' : ''}`} style={{ fontSize: 10 }}>
      v{node.version}{node.limited ? ' Limited' : node.compat ? ' Compat' : ''}
    </span>
  ) : null;

  return (
    <AppLayout
      sidebar={
        <Sidebar
          items={sidebarItems}
          activeKey={activePage}
          onSelect={(key) => {
            const item = navItems.find((n) => n.key === key);
            if (item) navigate(item.path);
          }}
          mobile={isMobile && mobileOpen}
          onClose={() => setMobileOpen(false)}
          logo={
            <>
              <div className="logo"><img src="./logo.svg" alt="" className="logo-img" /></div>
              <div>
                <div style={{ fontSize: 14, fontWeight: 700, letterSpacing: '-.3px', whiteSpace: 'nowrap' }}>HY2 SCALE</div>
                {versionBadge}
              </div>
            </>
          }
          footer={
            <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
              <button
                className="hy-sidebar-item"
                onClick={() => { if (!forcePasswordChange) { logout(); navigate('/login'); } }}
                style={{ width: '100%', ...(forcePasswordChange ? { opacity: 0.35, pointerEvents: 'none' as const } : {}) }}
              >
                {logoutIcon}
                {t('app.logout')}
              </button>
            </div>
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
          <LanguageSwitcher />
        </Topbar>
      }
    >
      <div className="page-enter" key={activePage}>
        {children}
      </div>
    </AppLayout>
  );
}
