import React from 'react';
import ReactDOM from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider, ConfirmProvider, setImperativeToast, useToast } from '@hy2scale/ui';
import '@hy2scale/ui/css';
import { setTokenGetter } from '@/api/client';
import { getToken } from '@/hooks/useAuth';
import '@/i18n';
import App from './App';

// Wire up token getter for API client
setTokenGetter(getToken);

// Long-lived tabs: poll /api/build-id and reload if the server has been
// rebuilt/redeployed since the bundle currently running was served. The
// loaded build-id is injected into index.html by the server as
// window.__BUILD_ID__; a mismatch means our cached JS is stale.
(() => {
  const loaded = (window as any).__BUILD_ID__ as string | undefined;
  if (!loaded) return;
  const base = (window as any).__BASE__ || '';
  let reloading = false;
  const check = async () => {
    if (reloading) return;
    try {
      const r = await fetch(base + '/api/build-id', { cache: 'no-store' });
      if (!r.ok) return;
      const data = await r.json();
      if (data?.build_id && data.build_id !== loaded) {
        reloading = true;
        // Hard-reload with the root path so the browser fetches a fresh
        // index.html and follows it to the new bundle hash. Small delay
        // so any in-flight user action (mid-drag, PUT) can complete.
        setTimeout(() => window.location.reload(), 300);
      }
    } catch { /* network blip; next tick retries */ }
  };
  // First check after 10 s so initial mount isn't racing anything else,
  // then every 60 s.
  setTimeout(check, 10_000);
  setInterval(check, 60_000);
  // Also check when the tab regains focus — the common case after the
  // user comes back from switching apps while we deployed.
  window.addEventListener('focus', check);
})();

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      refetchOnWindowFocus: false,
      staleTime: 2000,
    },
  },
});

// Bridge imperative toast from inside React tree
function ToastBridge() {
  const toast = useToast();
  React.useEffect(() => { setImperativeToast(toast); }, [toast]);
  return null;
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter basename={(window as any).__BASE__ || ''}>
        <ToastProvider>
          <ConfirmProvider>
            <ToastBridge />
            <App />
          </ConfirmProvider>
        </ToastProvider>
      </BrowserRouter>
    </QueryClientProvider>
  </React.StrictMode>,
);
