import { useNodeStore } from '@/store/node';

/**
 * Returns the list of reachable exit paths from the topology,
 * plus helpers for rendering exit path hops with connectivity colors.
 */
export function useExitPaths() {
  const exitPaths = useNodeStore((s) => s.exitPaths);
  const connectedPeers = useNodeStore((s) => s.connectedPeers);
  const disabledPeers = useNodeStore((s) => s.disabledPeers);
  const disabledPaths = useNodeStore((s) => s.disabledPaths);

  /** Check if a single bare hop name is reachable (used for simple contexts). */
  function isHopReachable(name: string): boolean {
    return connectedPeers.has(name) && !disabledPeers.has(name);
  }

  /**
   * Check if an exit path hop at a given qualified position is reachable.
   * `qualifiedPath` is the full path from the start up to and including this hop
   * (e.g. for path "A/B/C", hop "B" has qualifiedPath "A/B").
   * Returns false if the hop name is a disabled root, OR if any prefix of the
   * qualified path is disabled in the backend Peers map.
   */
  function isReachableAt(qualifiedPath: string): boolean {
    const parts = qualifiedPath.split('/');
    const hop = parts[parts.length - 1];
    if (disabledPeers.has(hop)) return false;
    for (let i = 1; i <= parts.length; i++) {
      const prefix = parts.slice(0, i).join('/');
      if (disabledPaths.has(prefix)) return false;
    }
    return connectedPeers.has(hop);
  }

  return { exitPaths, isHopReachable, isReachableAt };
}
