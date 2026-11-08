import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Badge, Button, Card } from '@hy2scale/ui';
import type { Terminal } from '@xterm/xterm';
import type { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { terminalUrl, type ToolAvail } from '@/api/tools';
import { copyToClipboard } from '@/utils/clipboard';
import { Unavailable } from './common';

type State = 'idle' | 'connecting' | 'open' | 'closed' | 'error';

const THEME = {
  background: '#0f1115', foreground: '#d7dae0', cursor: '#d7dae0', selectionBackground: '#3a4150',
  black: '#1c1f26', red: '#e06c75', green: '#98c379', yellow: '#e5c07b', blue: '#61afef', magenta: '#c678dd', cyan: '#56b6c2', white: '#d7dae0',
  brightBlack: '#5c6370', brightRed: '#ef8b93', brightGreen: '#b5e08f', brightYellow: '#f0d197', brightBlue: '#8cc4f5', brightMagenta: '#d89ce8', brightCyan: '#7ccbd4', brightWhite: '#ffffff',
};

/** A shell on this node over a WebSocket; connects when first shown. */
export default function TerminalTab({ avail, active }: { avail?: ToolAvail; active: boolean }) {
  const { t } = useTranslation();
  const host = useRef<HTMLDivElement>(null);
  const term = useRef<Terminal | null>(null);
  const fit = useRef<FitAddon | null>(null);
  const ws = useRef<WebSocket | null>(null);
  const [state, setState] = useState<State>('idle');
  const [exitCode, setExitCode] = useState<number | null>(null);
  const started = useRef(false);

  const sendResize = useCallback(() => {
    const tm = term.current;
    const s = ws.current;
    if (tm && s && s.readyState === WebSocket.OPEN) s.send(JSON.stringify({ type: 'resize', cols: tm.cols, rows: tm.rows }));
  }, []);

  const connect = useCallback(async () => {
    if (!host.current) return;
    if (!term.current) {
      const [{ Terminal }, { FitAddon }] = await Promise.all([import('@xterm/xterm'), import('@xterm/addon-fit')]);
      const tm = new Terminal({
        cursorBlink: true, fontSize: 13, scrollback: 5000, theme: THEME,
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
      });
      const f = new FitAddon();
      tm.loadAddon(f);
      tm.open(host.current);
      f.fit();
      // Clipboard keys: Ctrl/Cmd+C copies a selection (without one, Ctrl+C
      // goes to the shell as ^C); Ctrl/Cmd+V pastes (the browser's paste
      // event reaches the terminal); Cmd+A or Ctrl+Shift+A selects all
      // (plain Ctrl+A stays the shell's line start).
      tm.attachCustomKeyEventHandler((e) => {
        if (e.type !== 'keydown' || e.altKey || !(e.ctrlKey || e.metaKey)) return true;
        const k = e.key.toLowerCase();
        if (k === 'c' && tm.hasSelection()) {
          void copyToClipboard(tm.getSelection()).then(() => tm.focus());
          tm.clearSelection();
          e.preventDefault();
          return false;
        }
        if (k === 'v') return false;
        if (k === 'a' && (e.metaKey || e.shiftKey)) {
          tm.selectAll();
          e.preventDefault();
          return false;
        }
        return true;
      });
      tm.onData((d) => { const s = ws.current; if (s && s.readyState === WebSocket.OPEN) s.send(new TextEncoder().encode(d)); });
      tm.onResize(() => sendResize());
      term.current = tm;
      fit.current = f;
    }
    const tm = term.current!;
    ws.current?.close();
    setExitCode(null);
    setState('connecting');
    const s = new WebSocket(terminalUrl(tm.cols, tm.rows));
    s.binaryType = 'arraybuffer';
    ws.current = s;
    s.onopen = () => { if (ws.current === s) { setState('open'); tm.focus(); sendResize(); } };
    s.onmessage = (e) => {
      if (typeof e.data === 'string') {
        try {
          const m = JSON.parse(e.data);
          if (m.type === 'exit') setExitCode(typeof m.code === 'number' ? m.code : null);
        } catch { /* not a control frame */ }
        return;
      }
      tm.write(new Uint8Array(e.data as ArrayBuffer));
    };
    s.onerror = () => { if (ws.current === s) setState('error'); };
    s.onclose = () => { if (ws.current === s) { ws.current = null; setState((st) => (st === 'error' ? st : 'closed')); } };
  }, [sendResize]);

  const disconnect = () => { ws.current?.close(); };

  // Connect when first shown; refit whenever shown or resized.
  useEffect(() => {
    if (!active || avail?.available === false) return;
    if (!started.current) { started.current = true; connect(); }
    else requestAnimationFrame(() => fit.current?.fit());
  }, [active, avail?.available, connect]);
  useEffect(() => {
    const el = host.current;
    if (!el) return;
    const ro = new ResizeObserver(() => { if (el.offsetWidth > 0) fit.current?.fit(); });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  useEffect(() => () => { ws.current?.close(); term.current?.dispose(); }, []);

  const badge = state === 'open'
    ? <Badge variant="green">{t('tools.terminalConnected')}</Badge>
    : state === 'connecting'
      ? <Badge variant="muted">{t('tools.terminalConnecting')}</Badge>
      : state === 'error'
        ? <Badge variant="red">{t('tools.terminalError')}</Badge>
        : <Badge variant="muted">{exitCode !== null ? t('tools.terminalExited', { code: exitCode }) : t('tools.terminalDisconnected')}</Badge>;

  return (
    <div className="tool-tab tool-tab-fill" data-testid="tool-terminal">
      <Card
        className="tool-term-card"
        title={<span data-testid="terminal-state">{badge}</span>}
        actions={state === 'open' || state === 'connecting'
          ? <Button size="sm" onClick={disconnect} data-testid="terminal-disconnect">{t('tools.terminalDisconnect')}</Button>
          : <Button size="sm" variant="primary" onClick={connect} disabled={avail?.available === false} data-testid="terminal-connect">{t('tools.terminalConnect')}</Button>}
        noPadding
      >
        <Unavailable avail={avail} />
        <div className="tool-term" ref={host} data-testid="terminal-screen" />
      </Card>
    </div>
  );
}
