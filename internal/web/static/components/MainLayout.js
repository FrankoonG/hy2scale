import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useState } from 'react';
import { useNavigate, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { AppLayout, Sidebar, Topbar, useMediaQuery } from '@hy2scale/ui';
import { useAuthStore } from '@/store/auth';
import { useNodeStore } from '@/store/node';
import LanguageSwitcher from '@/components/LanguageSwitcher';
// SVG icons for sidebar
const icons = {
    nodes: _jsxs("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: "2", children: [_jsx("circle", { cx: "12", cy: "5", r: "3" }), _jsx("circle", { cx: "5", cy: "19", r: "3" }), _jsx("circle", { cx: "19", cy: "19", r: "3" }), _jsx("path", { d: "M12 8v4m-4.5 3.5L10 12m4 0l2.5 3.5" })] }),
    users: _jsxs("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: "2", children: [_jsx("path", { d: "M17 21v-2a4 4 0 00-4-4H5a4 4 0 00-4 4v2" }), _jsx("circle", { cx: "9", cy: "7", r: "4" }), _jsx("path", { d: "M23 21v-2a4 4 0 00-3-3.87m-4-12a4 4 0 010 7.75" })] }),
    proxies: _jsx("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: "2", children: _jsx("path", { d: "M22 12h-4l-3 9L9 3l-3 9H2" }) }),
    rules: _jsx("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: "2", children: _jsx("path", { d: "M22 3H2l8 9.46V19l4 2v-8.54L22 3z" }) }),
    tls: _jsxs("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: "2", children: [_jsx("rect", { x: "3", y: "11", width: "18", height: "11", rx: "2" }), _jsx("path", { d: "M7 11V7a5 5 0 0110 0v4" })] }),
    settings: _jsxs("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: "2", children: [_jsx("circle", { cx: "12", cy: "12", r: "3" }), _jsx("path", { d: "M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 01-2.83 2.83l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-4 0v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 010-4h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 012.83-2.83l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 014 0v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 010 4h-.09a1.65 1.65 0 00-1.51 1z" })] }),
};
const logoutIcon = _jsxs("svg", { viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", strokeWidth: "2", children: [_jsx("path", { d: "M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" }), _jsx("polyline", { points: "16 17 21 12 16 7" }), _jsx("line", { x1: "21", y1: "12", x2: "9", y2: "12" })] });
const navItems = [
    { key: 'nodes', path: '/nodes', icon: icons.nodes },
    { key: 'users', path: '/users', icon: icons.users },
    { key: 'proxies', path: '/proxies', icon: icons.proxies },
    { key: 'rules', path: '/rules', icon: icons.rules },
    { key: 'tls', path: '/tls', icon: icons.tls },
    { key: 'settings', path: '/settings', icon: icons.settings },
];
export default function MainLayout({ children }) {
    const { t } = useTranslation();
    const navigate = useNavigate();
    const location = useLocation();
    const logout = useAuthStore((s) => s.logout);
    const node = useNodeStore((s) => s.node);
    const isMobile = useMediaQuery('(max-width: 768px)');
    const [mobileOpen, setMobileOpen] = useState(false);
    const activePage = navItems.find((n) => location.pathname.startsWith(n.path))?.key || 'nodes';
    const pageTitle = t(`nav.${activePage}`);
    const sidebarItems = navItems.map((n) => ({
        key: n.key,
        label: t(`nav.${n.key}`),
        icon: n.icon,
    }));
    // Version badge matching old frontend style (green/orange/red + mode text)
    const versionBadge = node?.version ? (_jsxs("span", { className: `version-badge${node.limited ? ' limited' : node.compat ? ' compat' : ''}`, style: { fontSize: 10 }, children: ["v", node.version, node.limited ? ' Limited' : node.compat ? ' Compat' : ''] })) : null;
    return (_jsx(AppLayout, { sidebar: _jsx(Sidebar, { items: sidebarItems, activeKey: activePage, onSelect: (key) => {
                const item = navItems.find((n) => n.key === key);
                if (item)
                    navigate(item.path);
            }, mobile: isMobile && mobileOpen, onClose: () => setMobileOpen(false), logo: _jsxs(_Fragment, { children: [_jsx("div", { className: "logo", children: _jsx("img", { src: "./logo.svg", alt: "", className: "logo-img" }) }), _jsxs("div", { children: [_jsx("div", { style: { fontSize: 14, fontWeight: 700, letterSpacing: '-.3px', whiteSpace: 'nowrap' }, children: "HY2 SCALE" }), versionBadge] })] }), footer: _jsx("div", { style: { display: 'flex', flexDirection: 'column', gap: 0 }, children: _jsxs("button", { className: "hy-sidebar-item", onClick: () => { logout(); navigate('/login'); }, style: { width: '100%' }, children: [logoutIcon, t('app.logout')] }) }) }), topbar: _jsxs(Topbar, { title: pageTitle, onMenuClick: isMobile ? () => setMobileOpen(true) : undefined, children: [_jsx("div", { className: "spacer" }), node?.name && _jsx("span", { className: "node-badge", children: node.name }), _jsx(LanguageSwitcher, {})] }), children: _jsx("div", { className: "page-enter", children: children }, activePage) }));
}
