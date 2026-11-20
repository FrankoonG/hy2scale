import { useQuery } from '@tanstack/react-query';
import { getShareHost } from '@/api/links';

/**
 * The host clients reach this node by: the public host setting, else the
 * detected public address, else the host this page was opened through.
 */
export function useShareHost(): string {
  const q = useQuery({ queryKey: ['shareHost'], queryFn: getShareHost, staleTime: 30_000 });
  return q.data?.host || window.location.hostname;
}
