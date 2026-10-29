import { create } from 'zustand';

/**
 * Pages that have both a list and a topology view. The top bar title of such
 * a page is the switch; the topology view takes the whole content area.
 */
export type PageView = 'list' | 'topo';
export const TOPO_PAGES = ['nodes', 'interfaces'] as const;
export type TopoPage = (typeof TOPO_PAGES)[number];

const KEY = (page: string) => `hy-view:${page}`;

// Nodes used to persist under its own key; honour it once.
function initial(page: TopoPage): PageView {
  try {
    const v = localStorage.getItem(KEY(page));
    if (v === 'list' || v === 'topo') return v;
    if (page === 'nodes') {
      const old = localStorage.getItem('scale:nodes-view');
      if (old === 'list') return 'list';
      if (old === 'graph') return 'topo';
    }
  } catch { /* storage unavailable */ }
  // Nodes has always opened on its topology.
  return page === 'nodes' ? 'topo' : 'list';
}

interface PageViewState {
  views: Record<TopoPage, PageView>;
  setView: (page: TopoPage, v: PageView) => void;
  toggle: (page: TopoPage) => void;
}

export const usePageViewStore = create<PageViewState>((set, get) => ({
  views: { nodes: initial('nodes'), interfaces: initial('interfaces') },
  setView: (page, v) => {
    try { localStorage.setItem(KEY(page), v); } catch { /* storage unavailable */ }
    set({ views: { ...get().views, [page]: v } });
  },
  toggle: (page) => get().setView(page, get().views[page] === 'topo' ? 'list' : 'topo'),
}));

// Interfaces: show non-NIC, non-sub-interface links (bridges, veth, lo, …).
const HIDDEN_KEY = 'hy-iface-show-hidden';
interface IfaceOptsState { showHidden: boolean; setShowHidden: (v: boolean) => void }
export const useIfaceOpts = create<IfaceOptsState>((set) => ({
  showHidden: (() => { try { return localStorage.getItem(HIDDEN_KEY) === '1'; } catch { return false; } })(),
  setShowHidden: (v) => {
    try { localStorage.setItem(HIDDEN_KEY, v ? '1' : '0'); } catch { /* storage unavailable */ }
    set({ showHidden: v });
  },
}));

export function isTopoPage(page: string): page is TopoPage {
  return (TOPO_PAGES as readonly string[]).includes(page);
}

/** [current view, setter] for one page. */
export function usePageView(page: TopoPage): [PageView, (v: PageView) => void] {
  const v = usePageViewStore((s) => s.views[page]);
  const setView = usePageViewStore((s) => s.setView);
  return [v, (nv: PageView) => setView(page, nv)];
}
