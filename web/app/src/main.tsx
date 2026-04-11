import React from 'react';
import ReactDOM from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider, ConfirmProvider, setImperativeToast, useToast } from '@hy2scale/ui';
import './fonts.css';
import '@hy2scale/ui/css';
import { setTokenGetter } from '@/api/client';
import { getToken } from '@/hooks/useAuth';
import '@/i18n';
import App from './App';

// Wire up token getter for API client
setTokenGetter(getToken);

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
