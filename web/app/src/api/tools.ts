import { api, authToken, getBasePath } from './client';

/** Network tools run by this node (Tools page). */

export interface ToolAvail { available: boolean; reason?: string }
export interface ToolsInfo { ping: ToolAvail; mtr: ToolAvail; speed: ToolAvail; terminal: ToolAvail }

export const getToolsInfo = () => api<ToolsInfo>('/tools/info');

export type PingLine =
  | { type: 'start'; host: string; ip: string; proto: string }
  | { type: 'reply'; seq: number; ip: string; rtt_ms: number; ttl?: number; bytes?: number }
  | { type: 'timeout'; seq: number; error?: string }
  | { type: 'error'; error: string }
  | { type: 'done'; sent: number; recv: number; loss: number; min_ms: number; avg_ms: number; max_ms: number; mdev_ms: number };

export interface MtrHop {
  ttl: number; ip: string; name?: string;
  sent: number; recv: number; loss: number;
  last_ms: number; avg_ms: number; best_ms: number; worst_ms: number; stdev_ms: number;
}
export type MtrLine =
  | { type: 'start'; host: string; ip: string }
  | { type: 'round'; round: number; hops: MtrHop[] }
  | { type: 'error'; error: string }
  | { type: 'done' };

/** One path of an aggregate speed test. */
export interface SpeedPath { path: string; bytes: number; mbps: number; error?: string }

export type SpeedLine =
  | { type: 'start'; mode: string; target?: string; dir?: string; exit_mode?: string; paths?: string[] }
  | { type: 'path'; path: string }
  | { type: 'progress'; elapsed_ms: number; bytes: number; mbps: number; cur_mbps?: number; paths?: SpeedPath[] }
  | { type: 'error'; error: string }
  | { type: 'done'; bytes: number; seconds: number; mbps: number; paths?: SpeedPath[] };

/**
 * Streams an NDJSON tool endpoint: `onLine` per object, until the stream
 * ends or `signal` aborts (an abort resolves quietly).
 */
export async function streamTool<T>(path: string, params: Record<string, string | number | boolean | undefined>, onLine: (l: T) => void, signal: AbortSignal): Promise<void> {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== '') q.set(k, String(v));
  let res: Response;
  try {
    res = await api<Response>(`${path}?${q}`, { signal, cache: 'no-store' });
  } catch (e: any) {
    if (signal.aborted) return;
    throw e;
  }
  const reader = res.body!.getReader();
  const dec = new TextDecoder();
  let buf = '';
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      let i;
      while ((i = buf.indexOf('\n')) >= 0) {
        const line = buf.slice(0, i).trim();
        buf = buf.slice(i + 1);
        if (line) onLine(JSON.parse(line) as T);
      }
    }
    if (buf.trim()) onLine(JSON.parse(buf) as T);
  } catch (e) {
    if (signal.aborted) return;
    throw e;
  }
}

/** WebSocket URL of the terminal. */
export function terminalUrl(cols: number, rows: number): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const q = new URLSearchParams({ cols: String(cols), rows: String(rows) });
  const tok = authToken();
  if (tok) q.set('token', tok);
  return `${proto}//${location.host}${getBasePath()}/api/tools/terminal?${q}`;
}
