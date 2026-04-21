import { create } from 'zustand';
function buildExitPaths(topo, selfId) {
    const paths = [];
    function walk(nodes, prefix) {
        for (const n of nodes) {
            if (n.is_self) {
                // Self's children get selfId prefix
                if (n.children) {
                    for (const c of n.children) {
                        const childPath = selfId ? `${selfId}/${c.name}` : c.name;
                        paths.push(childPath);
                        if (c.children)
                            walkChildren(c.children, childPath);
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
    function walkChildren(nodes, prefix) {
        for (const c of nodes) {
            const p = `${prefix}/${c.name}`;
            paths.push(p);
            if (c.children)
                walkChildren(c.children, p);
        }
    }
    walk(topo, '');
    return paths;
}
function extractPeers(topo) {
    const connected = new Set();
    const disabled = new Set(); // root-level disabled names
    const disabledPaths = new Set(); // qualified paths (backend convention, no self prefix)
    function walk(nodes, parent, isChild) {
        for (const n of nodes) {
            if (n.is_self) {
                connected.add(n.name);
                // Self's children: walk with empty parent (matches backend self-strip convention)
                if (n.children)
                    walk(n.children, '', true);
                continue;
            }
            const qp = parent ? parent + '/' + n.name : n.name;
            if (n.connected || isChild)
                connected.add(n.name);
            if (n.disabled) {
                if (parent === '') {
                    // Root-level disable (root outbound or direct inbound under self)
                    // Use bare name — matches both cfg.Clients and cfg.Peers single-key entries
                    disabled.add(n.name);
                }
                else {
                    // Deeper sub-row disable — qualified path
                    disabledPaths.add(qp);
                }
            }
            if (n.children)
                walk(n.children, qp, true);
        }
    }
    walk(topo, '', false);
    return { connected, disabled, disabledPaths };
}
export const useNodeStore = create((set, get) => ({
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
            const name = key.includes('/') ? key.split('/').pop() : key;
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
