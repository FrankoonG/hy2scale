import { useCallback, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import clsx from 'clsx';
import { IconScan, IconZoomIn, IconZoomOut } from '@hy2scale/ui';
import * as api from '@/api';
import type { PolicyConfig, PolicySet } from '@/api/policy';
import { usePanZoom, type View } from '@/hooks/usePanZoom';
import { describeCond, describeTerminal } from './shared';
import { layoutRules, HEAD_H, ROW_H, SRC_HEAD, SRC_LINE, type Edge } from './rulesGraph';

const K_MIN = 0.25;
const K_MAX = 2.5;
const TOP_RESERVE = 56;
const PAD = 40;

const curve = (e: Edge) => {
  if (e.vertical) return `M${e.from.x},${e.from.y} L${e.to.x},${e.to.y}`;
  const mx = (e.from.x + e.to.x) / 2;
  return `M${e.from.x},${e.from.y} C${mx},${e.from.y} ${mx},${e.to.y} ${e.to.x},${e.to.y}`;
};

/**
 * Rules as a flow, left to right: sources → policy sets (a row per rule) →
 * hand-overs to other sets / exit groups, interfaces, deny, default route.
 * Read-only; a set opens its dialog.
 */
export default function RulesTopoView({ cfg, onEdit, actions }: {
  cfg: PolicyConfig | undefined;
  onEdit: (set: PolicySet, from: { x: number; y: number }) => void;
  actions?: ReactNode;
}) {
  const { t } = useTranslation();
  const { data: users = [] } = useQuery({ queryKey: ['users'], queryFn: api.getUsers, staleTime: 30000 });
  const names = useMemo(() => ({
    cond: new Map((cfg?.conditions || []).map((c) => [c.id, c.name])),
    user: new Map(users.map((u) => [u.id, u.username])),
  }), [cfg, users]);
  const policyNames = useMemo(() => new Map((cfg?.policies || []).map((p) => [p.id, p.name])), [cfg]);

  const localLabel = useMemo(() => {
    const out: string[] = [];
    if (cfg?.system.local.enabled) out.push(t('policy.kind.local'));
    if (cfg?.system.forward.enabled) out.push(t('policy.kind.forward'));
    out.push(t('rules.topo.noExit'));
    return out;
  }, [cfg, t]);
  const L = useMemo(() => (cfg ? layoutRules(cfg, localLabel) : null), [cfg, localLabel]);

  // ---- viewport ----
  const wrapRef = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });
  const userMoved = useRef(false);
  const { view, setView, animateTo, zoomBy, consumeClick, clampK } = usePanZoom({
    ref: wrapRef, minK: K_MIN, maxK: K_MAX,
    isOverlay: (el) => !!el.closest('[data-overlay]'),
    onUserMove: () => { userMoved.current = true; },
  });
  useLayoutEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const measure = () => setSize({ w: el.clientWidth, h: el.clientHeight });
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const fit = useCallback((): View | null => {
    if (!L || !size.w || !size.h || !L.w) return null;
    const k = clampK(Math.min(1.1, (size.w - PAD * 2) / L.w, (size.h - TOP_RESERVE - PAD) / L.h));
    return { x: (size.w - L.w * k) / 2, y: TOP_RESERVE + Math.max(0, (size.h - TOP_RESERVE - PAD - L.h * k) / 2), k };
  }, [L, size.w, size.h, clampK]);
  useLayoutEffect(() => {
    if (userMoved.current) return;
    const v = fit();
    if (v) setView(v);
  }, [fit, setView]);
  const resetView = () => { userMoved.current = false; const v = fit(); if (v) animateTo(v); };

  // ---- highlight: a hovered card / row / source lights its edges ----
  const [hot, setHot] = useState<{ kind: 'card' | 'row' | 'src' | 'end'; id: string } | null>(null);
  const edgeHot = (e: Edge) => {
    if (!hot) return false;
    switch (hot.kind) {
      case 'row': return e.row === hot.id;
      case 'card': return e.target === hot.id || (e.row || '').startsWith(hot.id + ':');
      case 'src': return e.key === `e:${hot.id}`;
      case 'end': return e.target === hot.id;
    }
  };

  const srcLabel = (kind: string) => (kind === 'direct' ? t('policy.directEntryBadge') : t(`policy.refKind.${kind}`));
  const endText = (end: Parameters<typeof describeTerminal>[0]) => {
    const s = describeTerminal(end, t, policyNames);
    if (end.type === 'exit' && end.mode) return `${s} · ${t(end.mode === 'quality' ? 'exit.modeStability' : 'exit.modeSpeed')}`;
    return s;
  };

  const empty = !L || (!L.cards.length);
  return (
    <div ref={wrapRef} className="iface-topo rules-topo" data-testid="rules-topo"
      onClick={() => { if (!consumeClick()) setHot(null); }}>
      {empty ? (
        <div className="iface-topo-empty">{cfg ? t('policy.noSets') : t('app.loading')}</div>
      ) : (
        <div className="iface-topo-stage" style={{ width: L!.w, height: L!.h, transform: `translate(${view.x}px,${view.y}px) scale(${view.k})` }}>
          <svg className="iface-topo-svg" width={L!.w} height={L!.h} aria-hidden="true">
            {L!.edges.filter((e) => !edgeHot(e)).map((e) => (
              <path key={e.key} d={curve(e)} className={clsx('rules-topo-edge', `is-${e.kind}`)} />
            ))}
            {L!.edges.filter(edgeHot).map((e) => (
              <path key={e.key} d={curve(e)} className={clsx('rules-topo-edge', `is-${e.kind}`, 'is-hot')} />
            ))}
          </svg>

          {L!.sources.map((s) => (
            <div key={s.key} className={clsx('rules-topo-src', `is-${s.kind}`)} data-testid={`rules-src-${s.kind}`}
              style={{ left: s.box.x, top: s.box.y, width: s.box.w, height: s.box.h }}
              onMouseEnter={() => setHot({ kind: 'src', id: s.key })} onMouseLeave={() => setHot(null)}>
              <div className="rules-topo-src-head" style={{ height: SRC_HEAD }}>{srcLabel(s.kind)}{s.names.length > 1 && <span className="rules-topo-count">{s.names.length}</span>}</div>
              {(s.kind === 'direct' ? s.extra || [] : [...s.names.slice(0, 3), ...(s.names.length > 3 ? [`+${s.names.length - 3}`] : [])]).map((n, i) => (
                <div key={i} className="rules-topo-src-line iface-topo-ellip" style={{ height: SRC_LINE }}>{n}</div>
              ))}
            </div>
          ))}

          {L!.cards.map((c) => (
            <div key={c.set.id} className={clsx('rules-topo-card', hot?.kind === 'card' && hot.id === c.set.id && 'is-sel')}
              data-testid={`rules-card-${c.set.id}`}
              style={{ left: c.box.x, top: c.box.y, width: c.box.w, height: c.box.h }}
              onMouseEnter={() => setHot({ kind: 'card', id: c.set.id })} onMouseLeave={() => setHot(null)}>
              <button type="button" className="rules-topo-card-head" style={{ height: HEAD_H }} data-overlay=""
                onClick={(e) => { e.stopPropagation(); onEdit(c.set, { x: e.clientX, y: e.clientY }); }}>
                <span className="iface-topo-ellip">{c.set.name}</span>
                {cfg?.direct === c.set.id && <span className="iface-tag">{t('policy.directEntryBadge')}</span>}
              </button>
              {c.rows.map((r) => {
                const id = `${c.set.id}:${r.key}`;
                const cond = r.kind === 'default' ? t('rules.topo.otherwise') : describeCond(r.when, t, names);
                return (
                  <div key={r.key} className={clsx('rules-topo-row', `is-${r.kind}`, !r.enabled && 'is-off', r.flow && 'is-flow', hot?.id === id && 'is-hot')}
                    style={{ height: ROW_H, paddingLeft: 8 + r.depth * 14 }}
                    title={[r.name, cond, r.end ? endText(r.end) : ''].filter(Boolean).join('\n')}
                    onMouseEnter={(e) => { e.stopPropagation(); setHot({ kind: 'row', id }); }}
                    onMouseLeave={() => setHot({ kind: 'card', id: c.set.id })}>
                    {r.index && <span className="rules-topo-idx">{r.index}</span>}
                    <span className="rules-topo-cond iface-topo-ellip">{r.name || cond}</span>
                    {r.flow && <span className="rules-topo-flowmark">{t('policy.termKind.flow')}</span>}
                  </div>
                );
              })}
            </div>
          ))}

          {L!.ends.map((e) => (
            <div key={e.key} className={clsx('rules-topo-end', `is-${e.kind}`)}
              data-testid={`rules-end-${e.kind}`}
              style={{ left: e.box.x, top: e.box.y, width: e.box.w, height: e.box.h }}
              onMouseEnter={() => setHot({ kind: 'end', id: e.key })} onMouseLeave={() => setHot(null)}
              title={endText(e.end)}>
              <span className="iface-topo-ellip">{endText(e.end)}</span>
            </div>
          ))}
        </div>
      )}

      <div className="iface-topo-topbar" data-overlay="">
        <div className="iface-topo-tools">
          <button type="button" className="iface-topo-tool" onClick={resetView} title={t('iface.topo.fit')} data-testid="rules-topo-fit">
            <IconScan size={14} />
          </button>
          <button type="button" className="iface-topo-tool" onClick={() => zoomBy(1 / 1.2)} title={t('iface.topo.zoomOut')}>
            <IconZoomOut size={14} />
          </button>
          <button type="button" className="iface-topo-tool" onClick={() => zoomBy(1.2)} title={t('iface.topo.zoomIn')}>
            <IconZoomIn size={14} />
          </button>
        </div>
        {actions}
      </div>
    </div>
  );
}
