import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card, Input, SegmentedControl } from '@hy2scale/ui';
import { streamTool, type PingLine, type ToolAvail } from '@/api/tools';
import { Field, RunButton, RttBars, Stats, Unavailable, ms, useToolRun } from './common';

type Proto = 'icmp' | 'tcp';
type Row = { seq: number; ip?: string; rtt?: number; ttl?: number; bytes?: number; err?: string };
type Done = Extract<PingLine, { type: 'done' }>;

export default function PingTab({ avail }: { avail?: ToolAvail }) {
  const { t } = useTranslation();
  const run = useToolRun();
  const [host, setHost] = useState('');
  const [proto, setProto] = useState<Proto>('icmp');
  const [port, setPort] = useState('443');
  const [count, setCount] = useState('10');
  const [target, setTarget] = useState<{ host: string; ip: string } | null>(null);
  const [rows, setRows] = useState<Row[]>([]);
  const [done, setDone] = useState<Done | null>(null);

  const go = () => {
    const h = host.trim();
    if (!h || run.running) return;
    setRows([]);
    setDone(null);
    setTarget(null);
    run.start((signal) => streamTool<PingLine>('/tools/ping', {
      host: h, proto, count: Number(count) || 4, port: proto === 'tcp' ? Number(port) || 0 : undefined,
    }, (l) => {
      if (l.type === 'start') setTarget({ host: l.host, ip: l.ip });
      else if (l.type === 'reply') setRows((r) => [...r, { seq: l.seq, ip: l.ip, rtt: l.rtt_ms, ttl: l.ttl, bytes: l.bytes }]);
      else if (l.type === 'timeout') setRows((r) => [...r, { seq: l.seq, err: l.error }]);
      else if (l.type === 'done') setDone(l);
      else if (l.type === 'error') run.setError(l.error);
    }, signal));
  };

  // Live figures until the summary arrives.
  const live = useMemo(() => {
    const rtts = rows.filter((r) => r.rtt !== undefined).map((r) => r.rtt!);
    const avg = rtts.length ? rtts.reduce((a, b) => a + b, 0) / rtts.length : undefined;
    return {
      sent: rows.length, recv: rtts.length,
      loss: rows.length ? ((rows.length - rtts.length) / rows.length) * 100 : 0,
      min: rtts.length ? Math.min(...rtts) : undefined, avg, max: rtts.length ? Math.max(...rtts) : undefined,
    };
  }, [rows]);
  const s = done
    ? { sent: done.sent, recv: done.recv, loss: done.loss, min: done.min_ms, avg: done.avg_ms, max: done.max_ms }
    : live;

  return (
    <div className="tool-tab" data-testid="tool-ping">
      <Card>
        <form className="tool-bar" onSubmit={(e) => { e.preventDefault(); go(); }}>
          <Field label={t('tools.target')} grow>
            <Input value={host} onChange={(e) => setHost(e.target.value)} placeholder={t('tools.targetPh')} data-testid="ping-host" />
          </Field>
          <Field label={t('tools.protocol')}>
            <SegmentedControl<Proto> items={[{ key: 'icmp', label: 'ICMP' }, { key: 'tcp', label: 'TCP' }]} value={proto} onChange={setProto} />
          </Field>
          {proto === 'tcp' && (
            <Field label={t('tools.port')}>
              <Input className="tool-num" type="number" min={1} max={65535} value={port} onChange={(e) => setPort(e.target.value)} />
            </Field>
          )}
          <Field label={t('tools.count')}>
            <Input className="tool-num" type="number" min={1} max={1000} value={count} onChange={(e) => setCount(e.target.value)} />
          </Field>
          <RunButton running={run.running} disabled={!host.trim() || avail?.available === false} onStop={run.stop} testId="ping" />
        </form>
        <Unavailable avail={avail} />
        {run.error && <div className="tool-note error" data-testid="ping-error">{run.error}</div>}
      </Card>

      {(rows.length > 0 || target) && (
        <Card title={target ? (target.host === target.ip ? target.ip : t('tools.resolved', { host: target.host, ip: target.ip })) : undefined}>
          <Stats items={[
            [t('tools.sent'), s.sent],
            [t('tools.received'), s.recv],
            [t('tools.loss'), `${s.loss.toFixed(s.loss % 1 ? 1 : 0)}%`],
            [t('tools.minAvgMax'), `${ms(s.min)} / ${ms(s.avg)} / ${ms(s.max)} ms`],
            ...(done ? [[t('tools.jitter'), `${ms(done.mdev_ms)} ms`] as [string, string]] : []),
          ]} />
          <RttBars values={rows.map((r) => r.rtt ?? null)} />
          <div className="tool-lines" data-testid="ping-lines">
            {rows.slice(-200).map((r) => (
              <div key={r.seq} className={r.rtt === undefined ? 'bad' : undefined}>
                <span className="mono">#{r.seq}</span>
                {r.rtt === undefined
                  ? <span>{r.err || t('tools.timeout')}</span>
                  : <>
                    <span className="mono">{r.ip}</span>
                    <span className="mono">{ms(r.rtt)} ms</span>
                    {r.ttl !== undefined && <span className="mono muted">ttl={r.ttl}</span>}
                  </>}
              </div>
            ))}
          </div>
        </Card>
      )}
    </div>
  );
}
