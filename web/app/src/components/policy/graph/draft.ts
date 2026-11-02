import { create } from 'zustand';
import type { PolicyConfig } from '@/api/policy';
import { autoLayout } from './layout';
import { canon, fromConfig, type Graph } from './model';

/**
 * The graph being edited. Kept outside the view so switching to the list
 * and back keeps unsaved work; positions are remembered per node.
 */
const POS_KEY = 'hy-rules-graph-pos';
const HISTORY = 100;

type Pos = Record<string, [number, number]>;
function readPos(): Pos {
  try { return JSON.parse(localStorage.getItem(POS_KEY) || '{}') || {}; } catch { return {}; }
}
export function savePositions(g: Graph) {
  const p = readPos();
  for (const n of g.nodes) p[canon(n, g)] = [Math.round(n.position.x), Math.round(n.position.y)];
  try { localStorage.setItem(POS_KEY, JSON.stringify(p)); } catch { /* storage blocked */ }
}
export function clearPositions() {
  try { localStorage.removeItem(POS_KEY); } catch { /* storage blocked */ }
}

/** The sets as a graph, at remembered positions (or laid out). */
export function load(cfg: PolicyConfig, relayout = false): Graph {
  const g = fromConfig(cfg);
  const auto = autoLayout(g);
  const stored = relayout ? {} : readPos();
  return {
    edges: g.edges,
    nodes: g.nodes.map((n) => {
      const s = stored[canon(n, g)];
      const p = s ? { x: s[0], y: s[1] } : auto.get(n.id) || { x: 0, y: 0 };
      return { ...n, position: p };
    }),
  };
}

interface DraftState {
  graph: Graph | null;
  /** the config the graph was loaded from / last saved against */
  base: PolicyConfig | null;
  dirty: boolean;
  past: Graph[];
  future: Graph[];
  /** replace without history (selection, drags in progress) */
  set: (g: Graph) => void;
  /** an edit: remembered for undo, marks the draft changed; edits with
   *  the same key in a row (typing in one field) undo as one */
  commit: (g: Graph, key?: string) => void;
  lastKey: string;
  /** remember the current graph before a change applied with set() */
  mark: () => void;
  undo: () => void;
  redo: () => void;
  reset: (cfg: PolicyConfig, g?: Graph) => void;
}

export const useDraft = create<DraftState>((set, get) => ({
  graph: null, base: null, dirty: false, past: [], future: [], lastKey: '',
  set: (g) => set({ graph: g }),
  commit: (g, key = '') => {
    const { graph: cur, past, lastKey } = get();
    const merge = key !== '' && key === lastKey;
    set({ graph: g, dirty: true, past: cur && !merge ? [...past, cur].slice(-HISTORY) : past, future: [], lastKey: key });
  },
  mark: () => {
    const cur = get().graph;
    if (cur) set({ past: [...get().past, cur].slice(-HISTORY), future: [], lastKey: '' });
  },
  undo: () => {
    const { past, graph, future } = get();
    if (!past.length || !graph) return;
    set({ graph: past[past.length - 1], past: past.slice(0, -1), future: [graph, ...future], dirty: true, lastKey: '' });
  },
  redo: () => {
    const { past, graph, future } = get();
    if (!future.length || !graph) return;
    set({ graph: future[0], future: future.slice(1), past: [...past, graph], dirty: true, lastKey: '' });
  },
  reset: (cfg, g) => set({ graph: g ?? load(cfg), base: cfg, dirty: false, past: [], future: [], lastKey: '' }),
}));
