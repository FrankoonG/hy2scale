import { useEffect, useMemo, useState } from 'react';
import { getBasePath } from '@/api/client';
import type { TopologyNode } from '@/api/types';

/**
 * Remote management sessions (this node's /remote/ proxy to another node).
 * A session lives in sessionStorage for REMOTE_TTL and is never refreshed;
 * it comes from the remote's relay trust (a password-free token) or from a
 * password login in the remote dialog. A node with a live session is
 * "accessible": its UI and API are one click away.
 */
export const REMOTE_TTL = 30 * 60 * 1000;
export type RemoteVia = 'trust' | 'password';
export interface RemoteAuth { token: string; via: RemoteVia; exp: number }

const EVENT = 'hy-remote-auth';
const key = (chain: string[]) => `hy-remote:${chain.join('/')}`;

export function readRemoteAuth(chain: string[]): RemoteAuth | null {
  try {
    const a = JSON.parse(sessionStorage.getItem(key(chain)) || 'null') as RemoteAuth | null;
    return a && a.token && a.exp > Date.now() ? a : null;
  } catch { return null; }
}

export function writeRemoteAuth(chain: string[], a: RemoteAuth | null) {
  try {
    if (a) sessionStorage.setItem(key(chain), JSON.stringify(a));
    else sessionStorage.removeItem(key(chain));
  } catch { /* storage unavailable */ }
  window.dispatchEvent(new Event(EVENT));
}

/** Base of the remote node's web UI behind this node's proxy. */
export const remoteBase = (chain: string[]) => `${getBasePath()}/remote/${chain.map(encodeURIComponent).join('/')}`;

/** A request to the remote node's API through this node's proxy. */
export function remoteFetch(chain: string[], path: string, init: RequestInit = {}, token?: string) {
  const headers: Record<string, string> = { ...(init.headers as Record<string, string> || {}) };
  if (token) headers.Authorization = `Bearer ${token}`;
  if (typeof init.body === 'string') headers['Content-Type'] = 'application/json';
  return fetch(`${remoteBase(chain)}/api${path}`, { ...init, headers, credentials: 'same-origin', cache: 'no-store' });
}

/** The remote trusts this node's relay: a password-free session (stored). */
export async function signInByTrust(chain: string[]): Promise<RemoteAuth | null> {
  try {
    const r = await remoteFetch(chain, '/relay-passthrough-token', { method: 'POST' });
    if (!r.ok) return null;
    const d = await r.json();
    if (!d?.token) return null;
    const a: RemoteAuth = { token: d.token, via: 'trust', exp: Date.now() + REMOTE_TTL };
    writeRemoteAuth(chain, a);
    return a;
  } catch { return null; }
}

/**
 * The manageable nodes of a topology and the chain to each (its shortest
 * one): online, not disabled, a hysteria2 node of ours with management
 * (not native, not reached by node access by user, compatible), behind
 * nested-enabled hops only. Keyed by node name.
 */
export function remoteChains(topology: TopologyNode[]): Map<string, string[]> {
  const out = new Map<string, string[]>();
  const self = topology.find((n) => n.is_self);
  let level: { n: TopologyNode; chain: string[] }[] =
    [...(self?.children || []), ...topology.filter((n) => !n.is_self)].map((n) => ({ n, chain: [n.name] }));
  const selfName = self?.name;
  while (level.length) {
    const next: typeof level = [];
    for (const { n, chain } of level) {
      if (!n.connected || n.disabled || n.native || n.restricted || n.invisible || n.incompatible) continue;
      if (n.name === selfName) continue;
      if (!out.has(n.name)) out.set(n.name, chain);
      if (n.nested) for (const c of n.children || []) if (!chain.includes(c.name)) next.push({ n: c, chain: [...chain, c.name] });
    }
    level = next;
  }
  return out;
}

const PROBE_AGAIN = 5 * 60 * 1000; // a refused trust probe is retried after this
const probed = new Map<string, number>();
const inflight = new Set<string>();

/**
 * The node names (of `chains`) with a live remote session. Nodes without one
 * are probed for relay trust (a few at a time, a refusal remembered for a
 * while); sessions created in the remote dialog show at once.
 */
export function useRemoteAccess(chains: Map<string, string[]>, enabled = true): Set<string> {
  const [tick, setTick] = useState(0);
  const sig = useMemo(() => [...chains.entries()].map(([n, c]) => `${n}=${c.join('/')}`).sort().join('|'), [chains]);

  useEffect(() => {
    const bump = () => setTick((t) => t + 1);
    window.addEventListener(EVENT, bump);
    const t = setInterval(bump, 30_000); // expiries
    return () => { window.removeEventListener(EVENT, bump); clearInterval(t); };
  }, []);

  useEffect(() => {
    if (!enabled) return;
    const todo = [...chains.values()].filter((c) => {
      const k = c.join('/');
      return !readRemoteAuth(c) && !inflight.has(k) && Date.now() - (probed.get(k) || 0) > PROBE_AGAIN;
    });
    let i = 0;
    const worker = async () => {
      while (i < todo.length) {
        const c = todo[i++];
        const k = c.join('/');
        inflight.add(k);
        probed.set(k, Date.now());
        await signInByTrust(c);
        inflight.delete(k);
      }
    };
    for (let w = 0; w < 3; w++) void worker();
  }, [sig, enabled, tick]); // eslint-disable-line react-hooks/exhaustive-deps

  return useMemo(() => {
    const s = new Set<string>();
    for (const [name, c] of chains) if (readRemoteAuth(c)) s.add(name);
    // a session opened over another chain to the same node counts too
    for (const c of liveChains()) if (chains.has(c[c.length - 1])) s.add(c[c.length - 1]);
    return s;
  }, [sig, tick]); // eslint-disable-line react-hooks/exhaustive-deps
}

/** Chains with a live session, any route. */
export function liveChains(): string[][] {
  const out: string[][] = [];
  try {
    for (let i = 0; i < sessionStorage.length; i++) {
      const k = sessionStorage.key(i) || '';
      if (!k.startsWith('hy-remote:')) continue;
      const c = k.slice('hy-remote:'.length).split('/').filter(Boolean);
      if (c.length && readRemoteAuth(c)) out.push(c);
    }
  } catch { /* storage unavailable */ }
  return out;
}

/** A live session to the node named `name` (its chain and token), if any. */
export function sessionFor(name: string, preferred?: string[]): { chain: string[]; auth: RemoteAuth } | null {
  if (preferred) {
    const a = readRemoteAuth(preferred);
    if (a) return { chain: preferred, auth: a };
  }
  for (const c of liveChains()) {
    if (c[c.length - 1] === name) {
      const a = readRemoteAuth(c);
      if (a) return { chain: c, auth: a };
    }
  }
  return null;
}
