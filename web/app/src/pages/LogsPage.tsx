import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import { Card, Button, Input, Select, SegmentedControl, MenuCheck, Pager, useStoredState, boolCodec, type SegmentedItem } from '@hy2scale/ui';
import * as api from '@/api';
import type { LogEntry } from '@/api';
import { useNodeStore } from '@/store/node';
import ResponsiveActions from '@/components/ResponsiveActions';
import { downloadBlob } from '@/utils/download';

const MAX = 5000;
const POLL_MS = 2000;
type Level = 'all' | 'warn' | 'error';

const WRAP_KEY = 'hy-logs-wrap';
const SIZE_KEY = 'hy-logs-page-size';
const PAGE_SIZES = [100, 200, 500, 1000];

const pad = (n: number) => String(n).padStart(2, '0');
const fmtTime = (ms: number) => { const d = new Date(ms); return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`; };
const fmtDay = (ms: number) => { const d = new Date(ms); return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`; };
const dayOf = (ms: number) => new Date(ms).toDateString();

// The "[tag] " prefix is shown as its own column.
const body = (e: LogEntry) => (e.src && e.msg.startsWith(`[${e.src}]`) ? e.msg.slice(e.src.length + 2).trimStart() : e.msg);

function highlight(text: string, q: string): ReactNode {
  if (!q) return text;
  const lower = text.toLowerCase();
  const out: ReactNode[] = [];
  let i = 0;
  for (;;) {
    const j = lower.indexOf(q, i);
    if (j < 0) break;
    if (j > i) out.push(text.slice(i, j));
    out.push(<mark key={j}>{text.slice(j, j + q.length)}</mark>);
    i = j + q.length;
  }
  out.push(text.slice(i));
  return out;
}

export default function LogsPage() {
  const { t } = useTranslation();
  const nodeName = useNodeStore((s) => s.node?.name || s.node?.node_id || 'hy2scale');

  const [entries, setEntries] = useState<LogEntry[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [failed, setFailed] = useState('');
  const lastRef = useRef(0);
  const [level, setLevel] = useState<Level>('all');
  const [src, setSrc] = useState('');
  const [query, setQuery] = useState('');
  const [wrap, setWrap] = useStoredState(WRAP_KEY, true, boolCodec);
  const [refreshing, setRefreshing] = useState(false);
  const viewRef = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  const [follow, setFollow] = useState(true);
  const [unseen, setUnseen] = useState(0);
  const [pageSize, setPageSize] = useStoredState(SIZE_KEY, 200);
  // null = the newest page, which follows new lines
  const [page, setPage] = useState<number | null>(null);

  // Poll for lines newer than the last one seen.
  useEffect(() => {
    let stop = false;
    let timer: ReturnType<typeof setTimeout>;
    const tick = async () => {
      try {
        const r = await api.getLogs(lastRef.current);
        if (stop) return;
        if (r.last < lastRef.current) {
          // The process restarted: start over.
          lastRef.current = 0;
          setEntries([]);
        } else {
          if (r.entries.length) {
            setEntries((prev) => {
              const next = prev.concat(r.entries);
              return next.length > MAX ? next.slice(next.length - MAX) : next;
            });
            if (!atBottom.current) setUnseen((n) => n + r.entries.length);
          }
          lastRef.current = Math.max(lastRef.current, r.last);
        }
        setLoaded(true);
        setFailed('');
      } catch (e: any) {
        if (!stop) setFailed(String(e?.message || e));
      }
      if (!stop) timer = setTimeout(tick, POLL_MS);
    };
    tick();
    return () => { stop = true; clearTimeout(timer); };
  }, []);

  const live = entries;

  // Reload the whole buffer from the node.
  const refresh = async () => {
    setRefreshing(true);
    try {
      const r = await api.getLogs(0);
      lastRef.current = r.last;
      setEntries(r.entries.length > MAX ? r.entries.slice(r.entries.length - MAX) : r.entries);
      setFailed('');
      setLoaded(true);
      setUnseen(0);
    } catch (e: any) {
      setFailed(String(e?.message || e));
    } finally {
      setRefreshing(false);
    }
  };
  const counts = useMemo(() => {
    let warn = 0, error = 0;
    for (const e of live) { if (e.level === 'error') error++; else if (e.level === 'warn') warn++; }
    return { all: live.length, warn: warn + error, error };
  }, [live]);
  const sources = useMemo(() => [...new Set(live.map((e) => e.src).filter(Boolean))].sort(), [live]);
  const q = query.trim().toLowerCase();
  const shown = useMemo(() => live.filter((e) =>
    (level === 'all' || (level === 'warn' ? e.level !== 'info' : e.level === 'error'))
    && (!src || e.src === src)
    && (!q || e.msg.toLowerCase().includes(q))), [live, level, src, q]);

  // Pages run oldest to newest; the newest page is shown by default.
  const size = PAGE_SIZES.includes(pageSize) ? pageSize : 200;
  const pages = Math.max(1, Math.ceil(shown.length / size));
  const curPage = page === null ? pages : Math.min(page, pages);
  const onLastPage = curPage === pages;
  const pageRows = shown.slice((curPage - 1) * size, curPage * size);
  // a new filter starts again on the newest lines
  useEffect(() => { setPage(null); }, [level, src, q, size]);
  const goPage = (p: number) => {
    setPage(p >= pages ? null : p);
    atBottom.current = p >= pages;
    setFollow(p >= pages);
    if (p >= pages) setUnseen(0);
  };

  // Stay pinned to the newest line unless the user scrolled up.
  useLayoutEffect(() => {
    const el = viewRef.current;
    if (el && follow && onLastPage) el.scrollTop = el.scrollHeight;
  }, [pageRows, follow, wrap, onLastPage]);
  // another page opens at its top
  useLayoutEffect(() => {
    const el = viewRef.current;
    if (el && !onLastPage) el.scrollTop = 0;
  }, [curPage, onLastPage]);

  const onScroll = () => {
    const el = viewRef.current;
    if (!el) return;
    const bottom = onLastPage && el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    atBottom.current = bottom;
    if (bottom !== follow) setFollow(bottom);
    if (bottom) setUnseen(0);
  };
  const toLatest = () => {
    setPage(null);
    const el = viewRef.current;
    if (el) el.scrollTop = el.scrollHeight;
    atBottom.current = true;
    setFollow(true);
    setUnseen(0);
  };

  const download = () => {
    const text = shown.map((e) => `${fmtDay(e.ts)} ${fmtTime(e.ts)} ${e.msg}`).join('\n') + '\n';
    const d = new Date();
    const stamp = `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`;
    downloadBlob(new Blob([text], { type: 'text/plain;charset=utf-8' }), `${nodeName}-${stamp}.log`);
  };

  const levelTabs: SegmentedItem<Level>[] = [
    { key: 'all', label: t('logs.all'), count: counts.all, testId: 'logs-level-all' },
    { key: 'warn', label: t('logs.warn'), count: counts.warn, tone: 'warn', testId: 'logs-level-warn' },
    { key: 'error', label: t('logs.error'), count: counts.error, tone: 'danger', testId: 'logs-level-error' },
  ];

  const truncatedHead = live.length > 0 && live[0].seq > 1;

  let prevDay = '';
  const rows: ReactNode[] = [];
  for (const e of pageRows) {
    const day = dayOf(e.ts);
    if (day !== prevDay) {
      rows.push(<div key={`d${e.seq}`} className="log-day">{fmtDay(e.ts)}</div>);
      prevDay = day;
    }
    rows.push(
      <div key={e.seq} className={clsx('log-row', e.level)}>
        <span className="log-time" title={`${fmtDay(e.ts)} ${fmtTime(e.ts)}`}>{fmtTime(e.ts)}</span>
        <span className={clsx('log-lvl', e.level)}>{t(`logs.lvl.${e.level}`)}</span>
        <span className="log-src">{e.src}</span>
        <span className="log-msg">{highlight(body(e), q)}</span>
      </div>,
    );
  }

  return (
    <div className="hy-page logs-page">
      <Card
        className="logs-card"
        title={t('logs.title')}
        count={shown.length}
        noPadding
        actions={
          <ResponsiveActions
            more={
              <MenuCheck checked={wrap} onChange={setWrap} testId="logs-wrap">{t('logs.wrap')}</MenuCheck>
            }
          >
            <SegmentedControl items={levelTabs} value={level} onChange={setLevel} />
            <Select
              className="logs-src-select"
              value={src}
              onChange={(e) => setSrc(e.target.value)}
              options={[{ value: '', label: t('logs.allSources') }, ...sources.map((s) => ({ value: s, label: s }))]}
            />
            <Input className="logs-search" value={query} placeholder={t('logs.search')} onChange={(e) => setQuery(e.target.value)} data-testid="logs-search" />
            <Button size="sm" onClick={refresh} loading={refreshing} data-testid="logs-refresh">{t('logs.refresh')}</Button>
            <Button size="sm" onClick={download} disabled={!shown.length} data-testid="logs-download">{t('logs.download')}</Button>
          </ResponsiveActions>
        }
      >
        <div className="logs-body">
          <div ref={viewRef} className={clsx('logs-view', wrap && 'wrap')} onScroll={onScroll} data-testid="logs-view">
            {truncatedHead && curPage === 1 && <div className="log-note">{t('logs.olderDropped')}</div>}
            {failed && <div className="log-note bad">{t('logs.loadFailed')}: {failed}</div>}
            {!loaded && !failed && <div className="log-note">{t('app.loading')}</div>}
            {loaded && !shown.length && <div className="log-note">{t('logs.empty')}</div>}
            {rows}
          </div>
          {(!follow || !onLastPage) && (
            <button type="button" className="logs-latest" onClick={toLatest} data-testid="logs-latest">
              {unseen > 0 ? t('logs.newLines', { count: unseen }) : t('logs.latest')}
            </button>
          )}
        </div>
        <Pager total={shown.length} size={size} onSize={setPageSize} sizes={PAGE_SIZES}
          pager={{ page: curPage, pages, setPage: goPage }} />
      </Card>
    </div>
  );
}
