import { QueryClient } from '@tanstack/react-query';

/** The app's query cache (module-level so sign-out can clear it). */
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      refetchOnWindowFocus: false,
      staleTime: 2000,
    },
  },
});
