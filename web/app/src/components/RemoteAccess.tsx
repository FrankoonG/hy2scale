import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, Input, SegmentedControl, Badge } from '@hy2scale/ui';
import { api as request } from '@/api/client';
import { fmtBytes } from '@/hooks/useFormat';

/*
 * Remote access (in the remote node dialog): reach an address from the
 * remote node. Web: a reverse proxy under this node's address, opened by a
 * link with a one-use key. Port: a port of this node forwarded there, for
 * this browser's address only. Entries end with the remote sign-in.
 */

export interface RemoteAccessEntry {
  id: string;
  chain: string[];
  mode: 'web' | 'port';
  proto: 'tcp' | 'udp';
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

interface Props {
  chain: string[];
  auth: { token: string; exp: number } | null;
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

export default function RemoteAccess({ chain, auth }: Props) {
  const { t } = useTranslation();
  const key = chain.join('/');
  const [list, setList] = useState<RemoteAccessEntry[]>([]);
  const [mode, setMode] = useState<'web' | 'port'>('web');
  const [proto, setProto] = useState<'tcp' | 'udp'>('tcp');
  const [target, setTarget] = useState('');
  const [port, setPort] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const all = await request<RemoteAccessEntry[]>('/remote-access');
      setList((all || []).filter((e) => e.chain.join('/') === key));
    } catch { /* keep the last list */ }
  }, [key]);

  useEffect(() => {
    if (proxied || !auth) return;
    load();
    const id = window.setInterval(load, POLL);
    return () => window.clearInterval(id);
  }, [load, !!auth]); // eslint-disable-line react-hooks/exhaustive-deps

  if (proxied) return <div className="remote-muted">{t('remote.accessProxied')}</div>;

  const visit = async (e: RemoteAccessEntry) => {
    // the tab opens at once (a popup blocker lets a click's own window through)
    const w = window.open('about:blank', '_blank');
    try {
      const r = await request<{ url: string }>(`/remote-access/${e.id}/open`, { method: 'POST' });
      if (w) w.location.href = r.url; else window.open(r.url, '_blank');
    } catch (err: any) {
      w?.close();
      setError(String(err?.message || err));
    }
  };

  const create = async () => {
    if (!auth || !target.trim()) return;
    setBusy(true);
    setError(null);
    try {
      const body: any = { chain, token: auth.token, expires: auth.exp, mode, target: target.trim() };
      if (mode === 'port') {
        body.proto = proto;
        if (port.trim()) body.port = Number(port.trim());
      }
      const e = await request<RemoteAccessEntry>('/remote-access', { method: 'POST', body: JSON.stringify(body) });
      setTarget('');
      setPort('');
      await load();
      if (e.mode === 'web') visit(e);
    } catch (err: any) {
      setError(String(err?.message || err).trim());
    } finally {
      setBusy(false);
    }
  };

  const close = async (e: RemoteAccessEntry) => {
    try {
      await request(`/remote-access/${e.id}`, { method: 'DELETE' });
    } catch { /* gone already */ }
    load();
  };

  const host = window.location.hostname.includes(':') ? `[${window.location.hostname}]` : window.location.hostname;
  const portValid = !port.trim() || (/^\d+$/.test(port.trim()) && Number(port) >= 1 && Number(port) <= 65535);

  return (
    <div className="ra" data-testid="remote-access">
      <form className="ra-form" onSubmit={(ev) => { ev.preventDefault(); create(); }}>
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
          <>
            <SegmentedControl
              items={[
                { key: 'tcp', label: 'TCP', testId: 'ra-proto-tcp' },
                { key: 'udp', label: 'UDP', testId: 'ra-proto-udp' },
              ]}
              value={proto}
              onChange={(k) => setProto(k as 'tcp' | 'udp')}
            />
            <Input
              className="ra-port mono"
              value={port}
              onChange={(ev) => setPort(ev.target.value)}
              placeholder={t('remote.accessAutoPort')}
              aria-label={t('remote.accessLocalPort')}
              data-testid="ra-port"
            />
          </>
        )}
        <Button type="submit" variant="primary" loading={busy} disabled={!auth || !target.trim() || !portValid} data-testid="ra-open">
          {t('remote.accessOpen')}
        </Button>
      </form>
      {error && <div className="remote-error" data-testid="ra-error">{error}</div>}
      {list.length === 0 ? (
        <div className="remote-muted" data-testid="ra-empty">{t('remote.accessEmpty')}</div>
      ) : (
        <div className="ra-list" data-testid="ra-list">
          {list.map((e) => (
            <div className="ra-item" key={e.id} data-testid="ra-item">
              <div className="ra-main">
                <Badge variant="muted">{e.mode === 'web' ? t('remote.accessWeb') : e.proto.toUpperCase()}</Badge>
                <span className="mono ra-dst" title={e.target}>{e.target}</span>
                <span className="spacer" />
                {e.mode === 'web' && <Button size="sm" variant="primary" onClick={() => visit(e)} data-testid="ra-visit">{t('remote.accessVisit')}</Button>}
                {e.mode === 'port' && e.proto === 'tcp' && (
                  <Button size="sm" onClick={() => window.open(`http://${host}:${e.port}/`, '_blank')} data-testid="ra-visit-port">{t('remote.accessVisit')}</Button>
                )}
                <Button size="sm" onClick={() => close(e)} data-testid="ra-close">{t('remote.accessClose')}</Button>
              </div>
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
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
