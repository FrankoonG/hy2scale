import { create } from 'zustand';
import type { NodeConfig, TopologyNode } from '@/api';

interface NodeState {
  node: NodeConfig | null;
  topology: TopologyNode[];
  exitPaths: string[];
  syncingNodes: Map<string, { enabled: boolean }>;
  connectedPeers: Set<string>;
  disabledPeers: Set<string>;           // root-level disabled names (client disables)
  disabledPaths: Set<string>;           // qualified paths disabled (sub-row disables), backend convention

  setNode: (node: NodeConfig) => void;
  setTopology: (topo: TopologyNode[]) => void;
  setSyncing: (qualifiedPath: string, enabled: boolean) => void;
  clearSyncing: (qualifiedPath: string) => void;
}

function buildExitPaths(topo: TopologyNode[], selfId?: string): string[] {
  const paths: string[] = [];

  function walk(nodes: TopologyNode[], prefix: string) {
    for (const n of nodes) {
      if (n.is_self) {
        // Self's children get selfId prefix
        if (n.children) {
          for (const c of n.children) {
            const childPath = selfId ? `${selfId}/${c.name}` : c.name;
            paths.push(childPath);
            if (c.children) walkChildren(c.children, childPath);
          }
        }
        continue;
      }
      paths.push(prefix ? `${prefix}/${n.name}` : n.name);
      if (n.children) {
        walkChildren(n.children, prefix ? `${prefix}/${n.name}` : n.name);
      }
    }
  }

  function walkChildren(nodes: TopologyNode[], prefix: string) {
    for (const c of nodes) {
      const p = `${prefix}/${c.name}`;
      paths.push(p);
      if (c.children) walkChildren(c.children, p);
    }
  }

  walk(topo, '');
  return paths;
}

function extractPeers(topo: TopologyNode[]) {
  const connected = new Set<string>();
  const disabled = new Set<string>();           // root-level disabled names
  const disabledPaths = new Set<string>();      // qualified paths (backend convention, no self prefix)

  function walk(nodes: TopologyNode[], parent: string, isChild: boolean) {
    for (const n of nodes) {
      if (n.is_self) {
        connected.add(n.name);
        // Self's children: walk with empty parent (matches backend self-strip convention)
        if (n.children) walk(n.children, '', true);
        continue;
      }
      const qp = parent ? parent + '/' + n.name : n.name;
      if (n.connected || isChild) connected.add(n.name);
      if (n.disabled) {
        if (parent === '') {
          // Root-level disable (root outbound or direct inbound under self)
          // Use bare name — matches both cfg.Clients and cfg.Peers single-key entries
          disabled.add(n.name);
        } else {
          // Deeper sub-row disable — qualified path
          disabledPaths.add(qp);
        }
      }
      if (n.children) walk(n.children, qp, true);
    }
  }
  walk(topo, '', false);
  return { connected, disabled, disabledPaths };
}

export const useNodeStore = create<NodeState>((set, get) => ({
  node: null,
  topology: [],
  exitPaths: [],
  syncingNodes: new Map(),
  connectedPeers: new Set(),
  disabledPeers: new Set(),
  disabledPaths: new Set(),

  setNode: (node) => set({ node }),

  setTopology: (topo) => {
    const { node, syncingNodes } = get();
    const exitPaths = buildExitPaths(topo, node?.node_id);
    const { connected, disabled, disabledPaths } = extractPeers(topo);

    // Clear syncing for nodes whose state has settled
    const newSyncing = new Map(syncingNodes);
    for (const [key, val] of newSyncing) {
      const name = key.includes('/') ? key.split('/').pop()! : key;
      const isDisabled = disabled.has(name);
      // If the backend state now matches what we requested, clear syncing
      if (val.enabled === !isDisabled || val.enabled === connected.has(name)) {
        // State settled — but we keep it one cycle to ensure topology has refreshed
      }
    }

    set({ topology: topo, exitPaths, connectedPeers: connected, disabledPeers: disabled, disabledPaths });
  },

  setSyncing: (qualifiedPath, enabled) => {
    const map = new Map(get().syncingNodes);
    map.set(qualifiedPath, { enabled });
    set({ syncingNodes: map });
  },

  clearSyncing: (qualifiedPath) => {
    const map = new Map(get().syncingNodes);
    map.delete(qualifiedPath);
    set({ syncingNodes: map });
  },
}));
