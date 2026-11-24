import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import { Button, Input, SegmentedControl, Badge, useConfirm } from '@hy2scale/ui';
import { api as request } from '@/api/client';
import { fmtBytes } from '@/hooks/useFormat';

/*
 * Remote access (remote node dialog, second tab). The list of targets lives
 * on the remote node (every node managing it sees the same list); opening a
 * target is up to this node: web, a reverse proxy under this node's address
 * opened by a one-use link; port, a port of this node (tcp, udp or both) for
 * this browser's address only. Opened targets end with the remote sign-in.
 */

export interface RemoteAccessTarget {
  id: string;
  mode: 'web' | 'port';
  target: string;
  tcp: boolean;
  udp: boolean;
  created: number;
}

export interface RemoteAccessEntry {
  id: string;
  chain: string[];
  mode: 'web' | 'port';
  proto: string;
  target_id?: string;
  target: string;
  port?: number;
  visitor: string;
  created: number;
  expires: number;
  active: number;
  up: number;
  down: number;
  firewall: boolean;
  host_network: boolean;
}

type RFetch = (path: string, init?: RequestInit, token?: string) => Promise<Response>;

interface Props {
  chain: string[];
  auth: { token: string; exp: number } | null;
  /** A request to the remote node's API (through this node's proxy). */
  rfetch: RFetch;
}

const POLL = 5000;
const proxied = !!(window as any).__PROXY__;

export function closeRemoteAccess(chain: string[]) {
  if (proxied) return;
  request(`/remote-access?chain=${encodeURIComponent(chain.join('/'))}`, { method: 'DELETE' }).catch(() => {});
}

export function renewRemoteAccess(chain: string[], auth: { token: string; exp: number }) {
  if (proxied) return;
  request('/remote-access/renew', { method: 'POST', body: JSON.stringify({ chain, token: auth.token, expires: auth.exp }) }).catch(() => {});
}

const protoLabel = (t: { mode: string; tcp: boolean; udp: boolean }, web: string) =>
  t.mode === 'web' ? web : [t.tcp && 'TCP', t.udp && 'UDP'].filter(Boolean).join('+');

export default function RemoteAccess({ chain, auth, rfetch }: Props) {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const key = chain.join('/');
  const [targets, setTargets] = useState<RemoteAccessTarget[] | null>(null);
  const [tooOld, setTooOld] = useState(false);
  const [open, setOpen] = useState<RemoteAccessEntry[]>([]);
  const [mode, setMode] = useState<'web' | 'port'>('web');
  const [tcp, setTcp] = useState(true);
  const [udp, setUdp] = useState(false);
  const [target, setTarget] = useState('');
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    if (!auth) return;
    try {
      const r = await rfetch('/remote-access/targets', {}, auth.token);
      if (r.status === 404 || r.status === 405) { setTooOld(true); setTargets([]); }
      else if (r.ok) { setTooOld(false); setTargets(await r.json()); }
    } catch { /* keep the last list */ }
    try {
      const all = await request<RemoteAccessEntry[]>('/remote-access');
      setOpen((all || []).filter((e) => e.chain.join('/') === key));
    } catch { /* keep the last list */ }
  }, [auth?.token, key]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (proxied || !auth) return;
    load();
    const id = window.setInterval(load, POLL);
    return () => window.clearInterval(id);
  }, [load, !!auth]); // eslint-disable-line react-hooks/exhaustive-deps

  if (proxied) return <div className="remote-muted">{t('remote.accessProxied')}</div>;
  if (tooOld) return <div className="remote-warn" data-testid="ra-too-old">{t('remote.accessTooOld')}</div>;

  const fail = (err: any) => setError(String(err?.message || err).trim());

  const add = async () => {
    if (!auth || !target.trim()) return;
    setBusy('add');
    setError(null);
    try {
      const r = await rfetch('/remote-access/targets', {
        method: 'POST', body: JSON.stringify({ mode, target: target.trim(), tcp: mode === 'web' || tcp, udp: mode === 'port' && udp }),
      }, auth.token);
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      setTarget('');
      await load();
    } catch (err) { fail(err); } finally { setBusy(null); }
  };

  const visit = async (e: RemoteAccessEntry) => {
    // the tab opens at once (a popup blocker lets a click's own window through)
    const w = window.open('about:blank', '_blank');
    try {
      const r = await request<{ url: string }>(`/remote-access/${e.id}/open`, { method: 'POST' });
      if (w) w.location.href = r.url; else window.open(r.url, '_blank');
    } catch (err) { w?.close(); fail(err); }
  };

  const start = async (x: RemoteAccessTarget) => {
    if (!auth) return;
    setBusy(x.id);
    setError(null);
    try {
      const e = await request<RemoteAccessEntry>('/remote-access', {
        method: 'POST', body: JSON.stringify({ chain, token: auth.token, expires: auth.exp, target_id: x.id }),
      });
      await load();
      if (e.mode === 'web') visit(e);
    } catch (err) { fail(err); } finally { setBusy(null); }
  };

  const stop = async (e: RemoteAccessEntry) => {
    try { await request(`/remote-access/${e.id}`, { method: 'DELETE' }); } catch { /* gone already */ }
    load();
  };

  const remove = async (x: RemoteAccessTarget) => {
    if (!auth) return;
    if (!await confirm({
      title: t('remote.accessDelete'), message: t('remote.accessDeleteConfirm', { target: x.target }),
      confirmText: t('app.delete'), danger: true,
    })) return;
    try {
      await request(`/remote-access?chain=${encodeURIComponent(key)}&target_id=${encodeURIComponent(x.id)}`, { method: 'DELETE' }).catch(() => {});
      const r = await rfetch(`/remote-access/targets/${encodeURIComponent(x.id)}`, { method: 'DELETE' }, auth.token);
      if (!r.ok && r.status !== 404) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
    } catch (err) { fail(err); }
    load();
  };

  const host = window.location.hostname.includes(':') ? `[${window.location.hostname}]` : window.location.hostname;
  const toggleProto = (p: 'tcp' | 'udp') => {
    // at least one stays on
    if (p === 'tcp') { if (!tcp || udp) setTcp(!tcp); } else if (!udp || tcp) setUdp(!udp);
  };

  return (
    <div className="ra" data-testid="remote-access">
      <form className="ra-form" onSubmit={(ev) => { ev.preventDefault(); add(); }}>
        <SegmentedControl
          items={[
            { key: 'web', label: t('remote.accessWeb'), testId: 'ra-mode-web' },
            { key: 'port', label: t('remote.accessPort'), testId: 'ra-mode-port' },
          ]}
          value={mode}
          onChange={(k) => setMode(k as 'web' | 'port')}
        />
        <Input
          className="ra-target mono"
          value={target}
          onChange={(ev) => setTarget(ev.target.value)}
          placeholder={mode === 'web' ? 'http://192.168.1.1' : '192.168.1.10:22'}
          aria-label={t('remote.accessTarget')}
          data-testid="ra-target"
        />
        {mode === 'port' && (
          <div className="hy-seg" role="group" aria-label={t('remote.accessProto')}>
            <button type="button" className={clsx(tcp && 'on')} aria-pressed={tcp} onClick={() => toggleProto('tcp')} data-testid="ra-proto-tcp">TCP</button>
            <button type="button" className={clsx(udp && 'on')} aria-pressed={udp} onClick={() => toggleProto('udp')} data-testid="ra-proto-udp">UDP</button>
          </div>
        )}
        <Button type="submit" variant="primary" loading={busy === 'add'} disabled={!auth || !target.trim()} data-testid="ra-add">
          {t('remote.accessAdd')}
        </Button>
      </form>
      {error && <div className="remote-error" data-testid="ra-error">{error}</div>}
      {targets !== null && targets.length === 0 ? (
        <div className="remote-muted" data-testid="ra-empty">{t('remote.accessEmpty')}</div>
      ) : (
        <div className="ra-list" data-testid="ra-list">
          {(targets || []).map((x) => {
            const e = open.find((o) => o.target_id === x.id);
            return (
              <div className={clsx('ra-item', !e && 'off')} key={x.id} data-testid="ra-item" data-open={e ? '1' : '0'}>
                <div className="ra-main">
                  <Badge variant={e ? 'blue' : 'muted'}>{protoLabel(x, t('remote.accessWeb'))}</Badge>
                  <span className="mono ra-dst" title={x.target}>{x.target}</span>
                  <span className="spacer" />
                  {e ? (
                    <>
                      {e.mode === 'web' && <Button size="sm" variant="primary" onClick={() => visit(e)} data-testid="ra-visit">{t('remote.accessVisit')}</Button>}
                      {e.mode === 'port' && x.tcp && (
                        <Button size="sm" variant="primary" onClick={() => window.open(`http://${host}:${e.port}/`, '_blank')} data-testid="ra-visit-port">{t('remote.accessVisit')}</Button>
                      )}
                      <Button size="sm" onClick={() => stop(e)} data-testid="ra-stop">{t('remote.accessStop')}</Button>
                    </>
                  ) : (
                    <Button size="sm" loading={busy === x.id} onClick={() => start(x)} data-testid="ra-start">{t('remote.accessStart')}</Button>
                  )}
                  <Button size="sm" variant="ghost" onClick={() => remove(x)} data-testid="ra-delete">{t('app.delete')}</Button>
                </div>
                {e && (
                  <div className="ra-meta">
                    {e.mode === 'port' && (
                      <>
                        <span className="mono ra-entry" data-testid="ra-entry">{host}:{e.port}</span>
                        <span className="remote-muted">{t('remote.accessOnly', { ip: e.visitor })}</span>
                        {e.firewall && <Badge variant="green">{t('remote.accessFirewall')}</Badge>}
                        {!e.host_network && <span className="remote-warn">{t('remote.accessMapPort')}</span>}
                      </>
                    )}
                    <span className="remote-muted">{t('remote.accessActive', { n: e.active })}</span>
                    <span className="remote-muted mono">↑{fmtBytes(e.up)} ↓{fmtBytes(e.down)}</span>
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
