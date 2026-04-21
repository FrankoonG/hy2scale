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

  return (
    <MainLayout>
      <Routes>
        <Route path="/nodes" element={<NodesPage />} />
        <Route path="/users" element={<UsersPage />} />
        <Route path="/proxies" element={<ProxiesPage />} />
        <Route path="/rules" element={<RulesPage />} />
        <Route path="/tls" element={<TLSPage />} />
        <Route path="/settings" element={<SettingsPage />} />
        <Route path="*" element={<Navigate to="/nodes" replace />} />
      </Routes>
    </MainLayout>
  );
}

export default function App() {
  const token = useAuthStore((s) => s.token);
  const forcePasswordChange = useAuthStore((s) => s.forcePasswordChange);
  const location = useLocation();

  if (!token) {
    if (location.pathname !== '/login') {
      return <Navigate to="/login" replace />;
    }
    return <LoginPage />;
  }

  if (location.pathname === '/login') {
    return <Navigate to={forcePasswordChange ? '/settings' : '/nodes'} replace />;
  }

  // Force password change: only allow /settings
  if (forcePasswordChange && location.pathname !== '/settings') {
    return <Navigate to="/settings" replace />;
  }

  return <AuthenticatedRoutes />;
}
