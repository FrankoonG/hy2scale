import { useEffect } from 'react';
import { Routes, Route, Navigate, useNavigate, useLocation } from 'react-router-dom';
import { useAuthStore } from '@/store/auth';
import { signOut } from '@/hooks/authTransition';
import { useQuery } from '@tanstack/react-query';
import { getNode } from '@/api';
import { useNodeStore } from '@/store/node';
import LoginPage from '@/pages/LoginPage';
import MainLayout from '@/components/MainLayout';
import OverviewPage from '@/pages/OverviewPage';
import NodesPage from '@/pages/NodesPage';
import InterfacesPage from '@/pages/InterfacesPage';
import UsersPage from '@/pages/UsersPage';
import ProxiesPage from '@/pages/ProxiesPage';
import EndpointsPage from '@/pages/EndpointsPage';
import RulesPage from '@/pages/RulesPage';
import TLSPage from '@/pages/TLSPage';
import SettingsPage from '@/pages/SettingsPage';
import LogsPage from '@/pages/LogsPage';
import ToolsPage from '@/pages/ToolsPage';
import AuditPage from '@/pages/AuditPage';
import FeatureGate from '@/components/FeatureGate';

function AuthenticatedRoutes() {
  const setNode = useNodeStore((s) => s.setNode);
  const setTopology = useNodeStore((s) => s.setTopology);
  const setPathStatus = useNodeStore((s) => s.setPathStatus);
  const node = useNodeStore((s) => s.node);
  const navigate = useNavigate();

  // Browser tab title — `<self node_id> - HY2 SCALE` once we know who we
  // are; static "HY2 SCALE" until the API call returns.
  useEffect(() => {
    document.title = node?.node_id ? `${node.node_id} - HY2 SCALE` : 'HY2 SCALE';
  }, [node?.node_id]);

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

  // Exit path status (nested gate, disabled, offline) as routing sees it
  useQuery({
    queryKey: ['pathStatus'],
    queryFn: async () => {
      const { getPathStatus } = await import('@/api');
      const list = await getPathStatus();
      setPathStatus(list);
      return list;
    },
    refetchInterval: 3000,
  });

  // Listen for session expired events
  useEffect(() => {
    const handler = () => {
      signOut(navigate);
    };
    window.addEventListener('hy2scale:session-expired', handler);
    return () => window.removeEventListener('hy2scale:session-expired', handler);
  }, [navigate]);

  return (
    <MainLayout>
      <Routes>
        <Route path="/overview" element={<OverviewPage />} />
        <Route path="/nodes" element={<NodesPage />} />
        <Route path="/interfaces" element={<InterfacesPage />} />
        <Route path="/users" element={<UsersPage />} />
        <Route path="/proxies" element={<ProxiesPage />} />
        <Route path="/endpoints" element={<EndpointsPage />} />
        <Route path="/rules" element={<RulesPage />} />
        <Route path="/tls" element={<TLSPage />} />
        <Route path="/tools" element={<ToolsPage />} />
        <Route path="/audit" element={<FeatureGate feature="audit"><AuditPage /></FeatureGate>} />
        <Route path="/logs" element={<FeatureGate feature="logs"><LogsPage /></FeatureGate>} />
        <Route path="/settings" element={<SettingsPage />} />
        <Route path="*" element={<Navigate to="/overview" replace />} />
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
    return <Navigate to={forcePasswordChange ? '/settings' : '/overview'} replace />;
  }

  // Force password change: only allow /settings
  if (forcePasswordChange && location.pathname !== '/settings') {
    return <Navigate to="/settings" replace />;
  }

  return <AuthenticatedRoutes />;
}
