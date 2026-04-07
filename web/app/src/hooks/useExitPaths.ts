import { useNodeStore } from '@/store/node';

/**
 * Returns the list of reachable exit paths from the topology,
 * plus helpers for rendering exit path hops with connectivity colors.
 */
export function useExitPaths() {
  const exitPaths = useNodeStore((s) => s.exitPaths);
  const connectedPeers = useNodeStore((s) => s.connectedPeers);
  const disabledPeers = useNodeStore((s) => s.disabledPeers);

  /** Check if a single hop is reachable */
  function isHopReachable(name: string): boolean {
    return connectedPeers.has(name) && !disabledPeers.has(name);
  }

  return { exitPaths, isHopReachable };
}
