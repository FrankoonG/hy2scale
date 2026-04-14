import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect } from 'react';
import { Routes, Route, Navigate, useNavigate, useLocation } from 'react-router-dom';
import { useAuthStore } from '@/store/auth';
import { useQuery } from '@tanstack/react-query';
import { getNode } from '@/api';
import { useNodeStore } from '@/store/node';
import LoginPage from '@/pages/LoginPage';
import MainLayout from '@/components/MainLayout';
import NodesPage from '@/pages/NodesPage';
import UsersPage from '@/pages/UsersPage';
import ProxiesPage from '@/pages/ProxiesPage';
import RulesPage from '@/pages/RulesPage';
import TLSPage from '@/pages/TLSPage';
import SettingsPage from '@/pages/SettingsPage';
function AuthenticatedRoutes() {
    const setNode = useNodeStore((s) => s.setNode);
    const setTopology = useNodeStore((s) => s.setTopology);
    const navigate = useNavigate();
    // Fetch node info once on mount
    useQuery({
        queryKey: ['node'],
        queryFn: async () => {
            const node = await getNode();
            setNode(node);
            return node;
        },
        staleTime: 30000,
    });
    // Fetch topology globally so peer status colors work on all pages
    useQuery({
        queryKey: ['topology'],
        queryFn: async () => {
            const { getTopology } = await import('@/api');
            const topo = await getTopology();
            setTopology(topo);
            return topo;
        },
        refetchInterval: 5000,
    });
    // Listen for session expired events
    useEffect(() => {
        const handler = () => {
            useAuthStore.getState().logout();
            navigate('/login', { replace: true });
        };
        window.addEventListener('hy2scale:session-expired', handler);
        return () => window.removeEventListener('hy2scale:session-expired', handler);
    }, [navigate]);
    return (_jsx(MainLayout, { children: _jsxs(Routes, { children: [_jsx(Route, { path: "/nodes", element: _jsx(NodesPage, {}) }), _jsx(Route, { path: "/users", element: _jsx(UsersPage, {}) }), _jsx(Route, { path: "/proxies", element: _jsx(ProxiesPage, {}) }), _jsx(Route, { path: "/rules", element: _jsx(RulesPage, {}) }), _jsx(Route, { path: "/tls", element: _jsx(TLSPage, {}) }), _jsx(Route, { path: "/settings", element: _jsx(SettingsPage, {}) }), _jsx(Route, { path: "*", element: _jsx(Navigate, { to: "/nodes", replace: true }) })] }) }));
}
export default function App() {
    const token = useAuthStore((s) => s.token);
    const location = useLocation();
    if (!token) {
        if (location.pathname !== '/login') {
            return _jsx(Navigate, { to: "/login", replace: true });
        }
        return _jsx(LoginPage, {});
    }
    if (location.pathname === '/login') {
        return _jsx(Navigate, { to: "/nodes", replace: true });
    }
    return _jsx(AuthenticatedRoutes, {});
}
