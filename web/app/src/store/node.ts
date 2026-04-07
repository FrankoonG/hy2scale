import { create } from 'zustand';
import type { NodeConfig, TopologyNode } from '@/api';

interface NodeState {
  node: NodeConfig | null;
  topology: TopologyNode[];
  exitPaths: string[];
  syncingNodes: Map<string, { enabled: boolean }>;
  connectedPeers: Set<string>;
  disabledPeers: Set<string>;

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
  const disabled = new Set<string>();

  function walk(nodes: TopologyNode[], isChild = false) {
    for (const n of nodes) {
      // Root nodes have explicit `connected` field; children are reachable if they appear in the tree
      if (n.connected || n.is_self || isChild) connected.add(n.name);
      if (n.disabled) disabled.add(n.name);
      if (n.children) walk(n.children, true);
    }
  }
  walk(topo);
  return { connected, disabled };
}

export const useNodeStore = create<NodeState>((set, get) => ({
  node: null,
  topology: [],
  exitPaths: [],
  syncingNodes: new Map(),
  connectedPeers: new Set(),
  disabledPeers: new Set(),

  setNode: (node) => set({ node }),

  setTopology: (topo) => {
    const { node, syncingNodes } = get();
    const exitPaths = buildExitPaths(topo, node?.node_id);
    const { connected, disabled } = extractPeers(topo);

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

    set({ topology: topo, exitPaths, connectedPeers: connected, disabledPeers: disabled });
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
