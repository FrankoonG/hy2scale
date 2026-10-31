import { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { getGeoStatus, lookupGeo, type GeoInfo } from '@/api/storage';

/*
 * Geo information of IP addresses, from this node's local GeoIP databases.
 * Lookups of one language are batched (every IP asked for within a short
 * window goes out in one request) and cached for the page's lifetime.
 */

const BATCH_MS = 30;
const MAX_BATCH = 512;

interface LangCache {
  data: Map<string, GeoInfo>;
  pending: Set<string>;
  waiting: Set<string>;
  timer: number | null;
  subs: Set<() => void>;
}
const caches = new Map<string, LangCache>();

function cacheOf(lang: string): LangCache {
  let c = caches.get(lang);
  if (!c) caches.set(lang, (c = { data: new Map(), pending: new Set(), waiting: new Set(), timer: null, subs: new Set() }));
  return c;
}

function flush(lang: string) {
  const c = cacheOf(lang);
  c.timer = null;
  const ips = [...c.waiting].slice(0, MAX_BATCH);
  ips.forEach((ip) => { c.waiting.delete(ip); c.pending.add(ip); });
  if (c.waiting.size) c.timer = window.setTimeout(() => flush(lang), BATCH_MS);
  lookupGeo(ips, lang).then((r) => {
    for (const ip of ips) c.data.set(ip, r.results?.[ip] ?? {});
  }, () => {
    for (const ip of ips) c.data.set(ip, {});
  }).finally(() => {
    ips.forEach((ip) => c.pending.delete(ip));
    c.subs.forEach((f) => f());
  });
}

function request(ip: string, lang: string) {
  const c = cacheOf(lang);
  if (c.data.has(ip) || c.pending.has(ip) || c.waiting.has(ip)) return;
  c.waiting.add(ip);
  if (c.timer === null) c.timer = window.setTimeout(() => flush(lang), BATCH_MS);
}

/** Drops cached answers (after a database was uploaded or removed). */
export function clearGeoCache() {
  caches.forEach((c) => { c.data.clear(); c.subs.forEach((f) => f()); });
}

export function useGeoStatus() {
  return useQuery({ queryKey: ['geoStatus'], queryFn: getGeoStatus, staleTime: 5 * 60_000, retry: false });
}

/** API language of the UI language (zh-CN, ko-KR … → zh, ko, en). */
export const geoLang = (uiLang: string) => (uiLang.startsWith('zh') ? 'zh' : uiLang.startsWith('ko') ? 'ko' : 'en');

const plainIP = (s: string) => s.replace(/^\[|\]$/g, '');

/** Geo information of `ip` (undefined while unknown or without a database). */
export function useGeo(ip: string | undefined, uiLang: string): GeoInfo | undefined {
  const { data: status } = useGeoStatus();
  const lang = geoLang(uiLang);
  const key = ip ? plainIP(ip) : '';
  const enabled = !!key && !!status?.available;
  const [, bump] = useState(0);
  useEffect(() => {
    if (!enabled) return;
    const c = cacheOf(lang);
    const f = () => bump((n) => n + 1);
    c.subs.add(f);
    request(key, lang);
    return () => { c.subs.delete(f); };
  }, [enabled, key, lang]);
  return enabled ? cacheOf(lang).data.get(key) : undefined;
}

/** Geo information of several IPs at once (absent entries: not known yet). */
export function useGeoMany(ips: string[], uiLang: string): Map<string, GeoInfo> {
  const { data: status } = useGeoStatus();
  const lang = geoLang(uiLang);
  const keys = [...new Set(ips.filter(Boolean).map(plainIP))].sort();
  const sig = keys.join(',');
  const enabled = keys.length > 0 && !!status?.available;
  const [, bump] = useState(0);
  useEffect(() => {
    if (!enabled) return;
    const c = cacheOf(lang);
    const f = () => bump((n) => n + 1);
    c.subs.add(f);
    keys.forEach((k) => request(k, lang));
    return () => { c.subs.delete(f); };
    // keys is derived from sig
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, sig, lang]);
  const out = new Map<string, GeoInfo>();
  if (!enabled) return out;
  const data = cacheOf(lang).data;
  for (const k of keys) { const g = data.get(k); if (g) out.set(k, g); }
  return out;
}

/** Strips the port of "ip:port" / "[v6]:port"; returns the bare address. */
export function hostOf(addr: string | undefined): string {
  if (!addr) return '';
  const m = addr.match(/^\[([^\]]+)\](?::\d+)?$/);
  if (m) return m[1];
  const i = addr.lastIndexOf(':');
  return i > 0 && addr.indexOf(':') === i ? addr.slice(0, i) : addr;
}

/** True for an IPv4 / IPv6 literal (not a host name). */
export const isIPLiteral = (s: string) => /^\d{1,3}(\.\d{1,3}){3}$/.test(s) || (s.includes(':') && /^[0-9a-fA-F:.]+$/.test(s));
