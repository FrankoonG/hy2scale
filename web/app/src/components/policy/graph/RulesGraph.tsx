import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  ReactFlow, ReactFlowProvider, Background, BackgroundVariant, MiniMap, useReactFlow, applyNodeChanges,
  type Connection, type Edge, type EdgeChange, type FinalConnectionState, type NodeChange,
} from '@xyflow/react';
import '@xyflow/react/dist/base.css';
import clsx from 'clsx';
import {
  Button, useConfirm, useToast,
  IconScan, IconZoomIn, IconZoomOut, IconUndo, IconRedo, IconLayoutGrid, IconPlus,
} from '@hy2scale/ui';
import * as api from '@/api';
import { getPolicy, getPolicyCapabilities, type End, type PolicyConfig } from '@/api/policy';
import { useTheme } from '@/theme';
import { newId } from '../shared';
import {
  NEW_SET, addRef, analyze, ancestors, compile, condRefs, dropRef, edge, endKey, endNodeId, index, kindAt, setNodeId,
  type Analysis, type GData, type GNode, type Graph, type Member,
} from './model';
import { autoLayout } from './layout';
import { useDraft, savePositions, clearPositions } from './draft';
import { pending, saveGraph, SaveError } from './save';
import { GraphCtx, nodeTypes, type NodeCtx } from './nodes';
import Inspector from './Inspector';

type AddKind = 'policy' | 'condition' | 'rule' | 'exit' | 'iface' | 'deny' | 'default' | 'true' | 'false' | 'user' | 'proxy';
const GROUPS: { key: string; items: AddKind[] }[] = [
  { key: 'sets', items: ['policy', 'condition'] },
  { key: 'flow', items: ['rule'] },
  { key: 'ends', items: ['exit', 'iface', 'deny', 'default', 'true', 'false'] },
  { key: 'sources', items: ['user', 'proxy'] },
];
const isEndKind = (k: AddKind) => ['exit', 'iface', 'deny', 'default', 'true', 'false'].includes(k);
const endOf = (k: AddKind): End => {
  switch (k) {
    case 'exit': return { type: 'exit', paths: [''], mode: '' };
    case 'iface': return { type: 'iface', iface: '' };
    case 'deny': return { type: 'deny', reject: true };
    case 'true': return { type: 'result', result: true };
    case 'false': return { type: 'result', result: false };
    default: return { type: 'default' };
  }
};

interface Pending { node: string; handle: string }
interface Menu { x: number; y: number; flow: { x: number; y: number }; from?: Pending }

/** Whether a link from (source, handle) to (target, handle) is allowed. */
function validLink(g: Graph, a: Analysis, c: { source: string; sourceHandle?: string | null; target: string; targetHandle?: string | null }): boolean {
  const { byId } = index(g);
  const s = byId.get(c.source);
  const t = byId.get(c.target);
  if (!s || !t || s.id === t.id) return false;
  const sh = c.sourceHandle || 'out';
  const th = c.targetHandle || 'in';
  const endOk = (n: GNode, k: string | undefined) => n.data.kind === 'end' && (!k || (n.data.end.type === 'result') === (k === 'condition'));
  if (sh === 'ref') {
    return s.data.kind === 'set' && t.data.kind === 'rule' && th === 'cond'
      && !condRefs(t.data.when).includes(s.data.setId) && a.owner.get(t.id) !== s.id;
  }
  if (th === 'cond') return false;
  if (s.data.kind === 'source') return t.data.kind === 'set' && t.data.setKind === 'policy';
  if (s.data.kind === 'set') {
    if (t.data.kind === 'rule') return !ancestors(g, s.id).has(t.id);
    return endOk(t, s.data.setKind);
  }
  if (s.data.kind !== 'rule') return false;
  const k = kindAt(g, a, s.id);
  if (t.data.kind === 'end') return endOk(t, k);
  if (t.data.kind === 'set') return sh === 'match' && t.data.setKind === 'policy' && k !== 'condition' && a.owner.get(s.id) !== t.id;
  if (t.data.kind === 'rule') return !ancestors(g, s.id).has(t.id);
  return false;
}

/** A link added: an output feeds one input; a rule has one way in. */
function link(g: Graph, source: string, sourceHandle: string, target: string, targetHandle: string): Graph {
  if (sourceHandle === 'ref') {
    const set = g.nodes.find((n) => n.id === source);
    if (set?.data.kind !== 'set') return g;
    const setId = set.data.setId;
    return { ...g, nodes: g.nodes.map((n) => (n.id === target && n.data.kind === 'rule' ? { ...n, data: { ...n.data, when: addRef(n.data.when, setId) } } : n)) };
  }
  const t = g.nodes.find((n) => n.id === target);
  const edges = g.edges.filter((e) => !(e.source === source && e.sourceHandle === sourceHandle)
    && !(t?.data.kind === 'rule' && e.target === target && e.targetHandle === 'in'));
  return { ...g, edges: [...edges, edge(source, sourceHandle, target, targetHandle)] };
}

/** Rule nodes in a chain from start (its match flows and else rungs). */
function chainRules(g: Graph, start: string | undefined, acc = new Set<string>()): Set<string> {
  const { byId, out } = index(g);
  const walk = (id: string | undefined) => {
    while (id) {
      const n = byId.get(id);
      if (!n || n.data.kind !== 'rule' || acc.has(id)) return;
      acc.add(id);
      walk(out(id, 'match')?.target);
      id = out(id, 'else')?.target;
    }
  };
  walk(start);
  return acc;
}

/** Remove nodes / edges. A rule taken out of a chain closes the gap. */
function remove(g: Graph, nodeIds: string[], edgeIds: string[]): Graph {
  let nodes = g.nodes;
  let edges = g.edges.filter((e) => !edgeIds.includes(e.id));
  // derived edges: condition references
  for (const id of edgeIds) {
    const m = /^ref:(.+):(rule:.+)$/.exec(id);
    if (!m) continue;
    nodes = nodes.map((n) => (n.id === m[2] && n.data.kind === 'rule' ? { ...n, data: { ...n.data, when: dropRef(n.data.when, m[1]) } } : n));
  }
  const gone = new Set<string>();
  for (const id of nodeIds) {
    const n = nodes.find((x) => x.id === id);
    if (!n || gone.has(id)) continue;
    const cur: Graph = { nodes, edges };
    const { out } = index(cur);
    if (n.data.kind === 'source' && n.data.src !== 'user' && n.data.src !== 'proxy') continue;
    if (n.data.kind === 'rule') {
      const into = edges.find((e) => e.target === id && e.targetHandle === 'in');
      const next = out(id, 'else');
      chainRules(cur, out(id, 'match')?.target).forEach((x) => gone.add(x));
      if (into && next) edges = [...edges, edge(into.source, into.sourceHandle || 'out', next.target)];
    }
    if (n.data.kind === 'set') {
      chainRules(cur, out(id, 'out')?.target).forEach((x) => gone.add(x));
      const setId = n.data.setId;
      nodes.filter((x) => x.id.startsWith(`end:${setId}:`)).forEach((x) => gone.add(x.id));
      if (n.data.setKind === 'condition') {
        nodes = nodes.map((x) => (x.data.kind === 'rule' && condRefs(x.data.when).includes(setId) ? { ...x, data: { ...x.data, when: dropRef(x.data.when, setId) } } : x));
      }
    }
    gone.add(id);
    edges = edges.filter((e) => !gone.has(e.source) && !gone.has(e.target) && !(e.source === id) && !(e.target === id));
  }
  return { nodes: nodes.filter((n) => !gone.has(n.id)), edges: edges.filter((e) => !gone.has(e.source) && !gone.has(e.target)) };
}

export default function RulesGraph(props: { cfg?: PolicyConfig; actions?: ReactNode; onDirect: (e: React.MouseEvent) => void }) {
  return (
    <ReactFlowProvider>
      <Editor {...props} />
    </ReactFlowProvider>
  );
}

function Editor({ cfg, actions, onDirect }: { cfg?: PolicyConfig; actions?: ReactNode; onDirect: (e: React.MouseEvent) => void }) {
  const { t } = useTranslation();
  const toast = useToast();
  const confirm = useConfirm();
  const qc = useQueryClient();
  const { theme } = useTheme();
  const rf = useReactFlow();
  const wrapRef = useRef<HTMLDivElement>(null);
  const { graph: g, base, dirty, past, future, set, commit, mark, undo, redo, reset } = useDraft();
  const [edgeSel, setEdgeSel] = useState<Set<string>>(new Set());
  const [menu, setMenu] = useState<Menu | null>(null);
  const [saving, setSaving] = useState(false);

  const { data: users = [] } = useQuery({ queryKey: ['users'], queryFn: api.getUsers, staleTime: 30000 });
  const { data: proxies = [] } = useQuery({ queryKey: ['proxies'], queryFn: api.getProxies, staleTime: 30000 });
  const { data: caps } = useQuery({ queryKey: ['policyCaps'], queryFn: getPolicyCapabilities, retry: false });

  // the server's sets, unless there is unsaved work
  useEffect(() => {
    if (cfg && (!g || !dirty) && cfg !== base) reset(cfg);
  }, [cfg]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!dirty) return;
    const h = (e: BeforeUnloadEvent) => { e.preventDefault(); };
    window.addEventListener('beforeunload', h);
    return () => window.removeEventListener('beforeunload', h);
  }, [dirty]);

  const a = useMemo(() => (g ? analyze(g) : null), [g]);
  const compiled = useMemo(() => (g ? compile(g) : null), [g]);
  const errors = useMemo(() => new Map((compiled?.errors || []).map((e) => [e.node, t(`rules.graph.err.${e.code}`)])), [compiled, t]);
  const changes = useMemo(() => (g && base && dirty ? pending(g, base) : 0), [g, base, dirty]);

  const userMembers: Member[] = useMemo(() => users.map((u) => ({ id: u.id, name: u.username })), [users]);
  const proxyMembers: Member[] = useMemo(() => proxies.map((p) => ({ id: p.id, name: p.id })), [proxies]);
  const ctx: NodeCtx | null = useMemo(() => (g && a ? {
    a, errors,
    direct: g.edges.find((e) => e.source === 'src:direct')?.target || '',
    names: {
      cond: new Map(g.nodes.flatMap((n) => (n.data.kind === 'set' ? [[n.data.setId, n.data.name] as [string, string]] : []))),
      user: new Map(userMembers.map((u) => [u.id, u.name])),
    },
    sets: new Map(g.nodes.flatMap((n) => (n.data.kind === 'set' ? [[n.data.setId, n.data.name] as [string, string]] : []))),
  } : null), [g, a, errors, userMembers]);

  // ---- edges as drawn: stored ones, condition references, continues ----
  const rfEdges: Edge[] = useMemo(() => {
    if (!g || !a) return [];
    const { byId } = index(g);
    const cls = (e: Edge) => {
      const s = byId.get(e.source);
      if (s?.data.kind === 'source') return 'is-src';
      if (e.sourceHandle === 'match') return byId.get(e.target)?.data.kind === 'set' ? 'is-call' : 'is-match';
      if (e.sourceHandle === 'else') return 'is-else';
      return 'is-start';
    };
    const out: Edge[] = g.edges.map((e) => ({ ...e, className: cls(e), selected: edgeSel.has(e.id), zIndex: 0 }));
    for (const n of g.nodes) {
      if (n.data.kind !== 'rule') continue;
      for (const ref of new Set(condRefs(n.data.when))) {
        const src = setNodeId(ref);
        if (!byId.has(src)) continue;
        const id = `ref:${ref}:${n.id}`;
        out.push({ id, source: src, sourceHandle: 'ref', target: n.id, targetHandle: 'cond', className: 'is-ref', selected: edgeSel.has(id) });
      }
    }
    for (const e of a.ghosts) out.push({ ...e, className: 'is-ghost', selectable: false, focusable: false });
    return out;
  }, [g, a, edgeSel]);

  // ---- canvas callbacks ----
  const onNodesChange = useCallback((ch: NodeChange<GNode>[]) => {
    const cur = useDraft.getState().graph;
    if (!cur) return;
    const keep = ch.filter((c) => c.type !== 'remove' && c.type !== 'add' && c.type !== 'replace');
    if (keep.length) set({ ...cur, nodes: applyNodeChanges(keep, cur.nodes) });
  }, [set]);
  const onEdgesChange = useCallback((ch: EdgeChange[]) => {
    setEdgeSel((s) => {
      let next = s;
      for (const c of ch) {
        if (c.type !== 'select') continue;
        if (next === s) next = new Set(s);
        if (c.selected) next.add(c.id); else next.delete(c.id);
      }
      return next;
    });
  }, []);
  const isValid = useCallback((c: Edge | Connection) => (g && a ? validLink(g, a, c) : false), [g, a]);
  const onConnect = useCallback((c: Connection) => {
    const cur = useDraft.getState().graph;
    if (!cur || !c.sourceHandle) return;
    commit(link(cur, c.source, c.sourceHandle, c.target, c.targetHandle || 'in'));
  }, [commit]);
  const onConnectEnd = useCallback((ev: MouseEvent | TouchEvent, st: FinalConnectionState) => {
    if (st.isValid || st.toNode || !st.fromNode || !st.fromHandle || st.fromHandle.type !== 'source') return;
    const p = 'changedTouches' in ev ? ev.changedTouches[0] : ev;
    const box = wrapRef.current!.getBoundingClientRect();
    setMenu({ x: p.clientX - box.left, y: p.clientY - box.top, flow: rf.screenToFlowPosition({ x: p.clientX, y: p.clientY }),
      from: { node: st.fromNode.id, handle: st.fromHandle.id || 'out' } });
  }, [rf]);
  const onPaneMenu = useCallback((ev: MouseEvent | React.MouseEvent) => {
    ev.preventDefault();
    const box = wrapRef.current!.getBoundingClientRect();
    setMenu({ x: ev.clientX - box.left, y: ev.clientY - box.top, flow: rf.screenToFlowPosition({ x: ev.clientX, y: ev.clientY }) });
  }, [rf]);

  // ---- add ----
  const menuItems = (from?: Pending): AddKind[] => {
    if (!g || !a) return [];
    const all = GROUPS.flatMap((x) => x.items);
    if (!from) return all;
    const n = g.nodes.find((x) => x.id === from.node);
    if (!n) return [];
    if (n.data.kind === 'source') return ['policy'];
    if (from.handle === 'ref') return ['rule'];
    const k = n.data.kind === 'set' ? n.data.setKind : kindAt(g, a, n.id);
    const ends: AddKind[] = k === 'condition' ? ['true', 'false'] : k === 'policy' ? ['exit', 'iface', 'deny', 'default'] : ['exit', 'iface', 'deny', 'default', 'true', 'false'];
    return ['rule', ...ends, ...(from.handle === 'match' && k !== 'condition' ? ['policy' as AddKind] : [])];
  };

  const add = (k: AddKind, at: { x: number; y: number }, from?: Pending) => {
    const cur = useDraft.getState().graph;
    if (!cur || !a) return;
    let node: GNode | undefined;
    let id = '';
    let targetHandle = 'in';
    const count = (sk: string) => cur.nodes.filter((n) => n.data.kind === 'set' && n.data.setKind === sk).length + 1;
    if (k === 'policy' || k === 'condition') {
      const setId = NEW_SET + newId();
      id = setNodeId(setId);
      node = { id, type: 'set', position: at, data: { kind: 'set', setKind: k, setId, name: t(k === 'policy' ? 'rules.graph.newPolicyName' : 'rules.graph.newConditionName', { n: count(k) }) } };
    } else if (k === 'rule') {
      const ruleId = newId();
      id = `rule:${ruleId}`;
      node = { id, type: 'rule', position: at, data: { kind: 'rule', ruleId, enabled: true } };
      if (from?.handle === 'ref') targetHandle = 'cond';
    } else if (k === 'user' || k === 'proxy') {
      id = `src:${k}:${newId()}`;
      node = { id, type: 'source', position: at, data: { kind: 'source', src: k, members: [] } };
    } else if (isEndKind(k)) {
      const end = endOf(k);
      // fixed ends are shared: link the existing one
      const owner = from ? (cur.nodes.find((n) => n.id === from.node)?.data.kind === 'set' ? from.node : a.owner.get(from.node)) : undefined;
      const ownerSet = cur.nodes.find((n) => n.id === owner);
      const setId = ownerSet?.data.kind === 'set' ? ownerSet.data.setId : `new${newId()}`;
      const shareable = end.type === 'deny' || end.type === 'default' || end.type === 'result';
      const existing = shareable ? cur.nodes.find((n) => n.id === endNodeId(end, setId)) : undefined;
      if (existing && from) {
        commit(link(cur, from.node, from.handle, existing.id, 'in'));
        return;
      }
      id = shareable && !existing ? endNodeId(end, setId) : `end:${endKey(end)}~${newId()}`;
      node = { id, type: 'end', position: at, data: { kind: 'end', end } };
    }
    if (!node) return;
    let next: Graph = { nodes: [...cur.nodes.map((n) => ({ ...n, selected: false })), { ...node, selected: true }], edges: cur.edges };
    if (from) next = link(next, from.node, from.handle, id, targetHandle);
    commit(next);
    setEdgeSel(new Set());
  };

  // ---- delete / keys ----
  const deleteSelection = useCallback(() => {
    const cur = useDraft.getState().graph;
    if (!cur) return;
    const nodes = cur.nodes.filter((n) => n.selected).map((n) => n.id);
    const edges = [...edgeSel].filter((id) => !id.startsWith('ghost:'));
    if (!nodes.length && !edges.length) return;
    commit(remove(cur, nodes, edges));
    setEdgeSel(new Set());
  }, [edgeSel, commit]);

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null;
      if (el && (el.closest('input, textarea, select, [contenteditable="true"], .hy-modal, .cm-editor') )) return;
      if (!wrapRef.current?.isConnected) return;
      const mod = e.ctrlKey || e.metaKey;
      if (e.key === 'Delete' || e.key === 'Backspace') { e.preventDefault(); deleteSelection(); }
      else if (mod && e.key.toLowerCase() === 'z' && !e.shiftKey) { e.preventDefault(); undo(); }
      else if (mod && (e.key.toLowerCase() === 'y' || (e.key.toLowerCase() === 'z' && e.shiftKey))) { e.preventDefault(); redo(); }
      else if (mod && e.key.toLowerCase() === 's') { e.preventDefault(); void save(); }
      else if (e.key === 'Escape') {
        if (menu) setMenu(null);
        else {
          const cur = useDraft.getState().graph;
          if (cur?.nodes.some((n) => n.selected)) set({ ...cur, nodes: cur.nodes.map((n) => (n.selected ? { ...n, selected: false } : n)) });
          setEdgeSel(new Set());
        }
      }
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  });

  // ---- save / discard / layout ----
  const save = async () => {
    const cur = useDraft.getState().graph;
    if (!cur || !base || !compiled || saving || !dirty) return;
    if (compiled.errors.length) {
      toast.error(t('rules.graph.saveBlocked', { n: compiled.errors.length }));
      const first = cur.nodes.find((n) => n.id === compiled.errors[0].node);
      if (first) rf.setCenter(first.position.x + 120, first.position.y + 30, { zoom: Math.max(rf.getZoom(), 0.9), duration: 300 });
      return;
    }
    if (compiled.orphans.length && !await confirm({ title: t('app.save'), message: t('rules.graph.orphans', { n: compiled.orphans.length }), confirmText: t('app.save') })) return;
    setSaving(true);
    try {
      const done = await saveGraph(cur, base);
      savePositions(done);
      await Promise.all([qc.invalidateQueries({ queryKey: ['policy'] }), qc.invalidateQueries({ queryKey: ['users'] }), qc.invalidateQueries({ queryKey: ['proxies'] })]);
      const fresh = await qc.fetchQuery({ queryKey: ['policy'], queryFn: getPolicy });
      reset(fresh);
      toast.success(t('policy.saved'));
    } catch (e: any) {
      if (e instanceof SaveError) {
        set(e.graph);
        toast.error(e.step.name ? `${e.step.name}: ${e.message}` : e.message);
      } else toast.error(String(e?.message || e));
    } finally {
      setSaving(false);
    }
  };
  const discard = async () => {
    if (!cfg) return;
    if (!await confirm({ title: t('rules.graph.discard'), message: t('rules.graph.discardConfirm'), danger: true, confirmText: t('rules.graph.discard') })) return;
    reset(cfg);
  };
  const relayout = () => {
    const cur = useDraft.getState().graph;
    if (!cur) return;
    mark();
    const p = autoLayout(cur);
    const next = { ...cur, nodes: cur.nodes.map((n) => ({ ...n, position: p.get(n.id) || n.position })) };
    set(next);
    clearPositions();
    savePositions(next);
    setTimeout(() => rf.fitView({ padding: 0.15, maxZoom: 1, duration: 300 }), 50);
  };

  // ---- inspector ----
  const selected = g?.nodes.filter((n) => n.selected) || [];
  const one = selected.length === 1 ? selected[0] : null;
  const patchNode = (id: string, patch: Partial<GData>, key?: string) => {
    const cur = useDraft.getState().graph;
    if (!cur) return;
    commit({ ...cur, nodes: cur.nodes.map((n) => (n.id === id ? { ...n, data: { ...n.data, ...patch } as GData } : n)) }, key);
  };
  const setMembers = (id: string, ids: string[]) => {
    const cur = useDraft.getState().graph;
    if (!cur) return;
    const node = cur.nodes.find((n) => n.id === id);
    if (node?.data.kind !== 'source') return;
    const src = node.data.src;
    const all = src === 'user' ? userMembers : proxyMembers;
    const pick = new Set(ids);
    // a member belongs to one group: taking it here drops it elsewhere
    commit({
      ...cur,
      nodes: cur.nodes.map((n) => {
        if (n.data.kind !== 'source' || n.data.src !== src) return n;
        if (n.id === id) return { ...n, data: { ...n.data, members: all.filter((m) => pick.has(m.id)) } };
        return { ...n, data: { ...n.data, members: n.data.members.filter((m) => !pick.has(m.id)) } };
      }),
    });
  };
  const condCtx = useMemo(() => ({
    conditions: (g?.nodes || []).flatMap((n) => (n.data.kind === 'set' && n.data.setKind === 'condition' && (!one || a?.owner.get(one.id) !== n.id) ? [{ id: n.data.setId, name: n.data.name }] : [])),
    users: userMembers.map((u) => ({ id: u.id, name: u.name })),
  }), [g, one, a, userMembers]);

  if (!g || !ctx) return <div ref={wrapRef} className="rg-wrap"><div className="iface-topo-empty">{t('app.loading')}</div></div>;
  const items = menu ? menuItems(menu.from) : [];

  return (
    <GraphCtx.Provider value={ctx}>
      <div ref={wrapRef} className={clsx('rg-wrap', theme === 'dark' && 'is-dark')} data-testid="rules-graph">
        <ReactFlow<GNode, Edge>
          nodes={g.nodes} edges={rfEdges} nodeTypes={nodeTypes}
          onNodesChange={onNodesChange} onEdgesChange={onEdgesChange}
          onConnect={onConnect} onConnectEnd={onConnectEnd} isValidConnection={isValid}
          onNodeDragStart={() => mark()} onNodeDragStop={() => { const cur = useDraft.getState().graph; if (cur) savePositions(cur); }}
          onPaneContextMenu={onPaneMenu} onPaneClick={() => setMenu(null)}
          deleteKeyCode={null} fitView fitViewOptions={{ padding: 0.15, maxZoom: 1 }} minZoom={0.15} maxZoom={2}
          colorMode={theme} proOptions={{ hideAttribution: true }}
          defaultEdgeOptions={{ interactionWidth: 14 }}
          connectionRadius={28}
        >
          <Background variant={BackgroundVariant.Dots} gap={22} size={1} />
          <MiniMap pannable zoomable position="bottom-left" className="rg-minimap" nodeBorderRadius={6} />
        </ReactFlow>

        <div className="iface-topo-topbar rg-topbar">
          <div className="iface-topo-tools">
            <button type="button" className="iface-topo-tool" onClick={() => rf.fitView({ padding: 0.15, maxZoom: 1, duration: 300 })} title={t('iface.topo.fit')} data-testid="rg-fit"><IconScan size={14} /></button>
            <button type="button" className="iface-topo-tool" onClick={() => rf.zoomOut({ duration: 200 })} title={t('iface.topo.zoomOut')}><IconZoomOut size={14} /></button>
            <button type="button" className="iface-topo-tool" onClick={() => rf.zoomIn({ duration: 200 })} title={t('iface.topo.zoomIn')}><IconZoomIn size={14} /></button>
            <span className="rg-sep" />
            <button type="button" className="iface-topo-tool" onClick={relayout} title={t('rules.graph.autoLayout')} data-testid="rg-layout"><IconLayoutGrid size={14} /></button>
            <button type="button" className="iface-topo-tool" onClick={undo} disabled={!past.length} title={t('rules.graph.undo')} data-testid="rg-undo"><IconUndo size={14} /></button>
            <button type="button" className="iface-topo-tool" onClick={redo} disabled={!future.length} title={t('rules.graph.redo')} data-testid="rg-redo"><IconRedo size={14} /></button>
            <span className="rg-sep" />
            <button type="button" className="iface-topo-tool" title={t('rules.graph.add')} data-testid="rg-add"
              onClick={(e) => {
                const box = wrapRef.current!.getBoundingClientRect();
                const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
                setMenu({ x: r.left - box.left, y: r.bottom - box.top + 6, flow: rf.screenToFlowPosition({ x: box.left + box.width / 2, y: box.top + box.height / 2 }) });
              }}><IconPlus size={14} /></button>
          </div>
          <div className="button-row">
            {actions}
            {dirty && <Button onClick={discard} data-testid="rg-discard">{t('rules.graph.discard')}</Button>}
            <Button variant="primary" onClick={() => void save()} disabled={!dirty || saving} data-testid="rg-save">
              {t('app.save')}{changes > 0 && <span className="rg-badge">{changes}</span>}
            </Button>
          </div>
        </div>

        {menu && items.length > 0 && (
          <div className="rg-menu" style={{ left: menu.x, top: menu.y }} data-testid="rg-menu" onContextMenu={(e) => e.preventDefault()}>
            {GROUPS.map((grp) => {
              const list = grp.items.filter((k) => items.includes(k));
              if (!list.length) return null;
              return (
                <div key={grp.key} className="rg-menu-group">
                  <div className="rg-menu-title">{t(`rules.graph.group.${grp.key}`)}</div>
                  {list.map((k) => (
                    <button key={k} type="button" className="rg-menu-item" data-testid={`rg-add-${k}`}
                      onClick={() => { add(k, menu.flow, menu.from); setMenu(null); }}>
                      {t(`rules.graph.kind.${k}`)}
                    </button>
                  ))}
                </div>
              );
            })}
          </div>
        )}

        {one && (
          <Inspector key={one.id} node={one} cond={condCtx}
            shared={g.edges.filter((e) => e.target === one.id).length}
            members={{ user: userMembers, proxy: proxyMembers }}
            ifaceAvailable={!!caps?.iface_terminal.available}
            onData={(p, key) => patchNode(one.id, p, key)}
            onMembers={(ids) => setMembers(one.id, ids)}
            onDelete={one.data.kind === 'source' && one.data.src !== 'user' && one.data.src !== 'proxy' ? undefined : () => { commit(remove(g, [one.id], [])); }}
            onClose={() => set({ ...g, nodes: g.nodes.map((n) => ({ ...n, selected: false })) })}
            onDirect={onDirect} />
        )}
      </div>
    </GraphCtx.Provider>
  );
}
