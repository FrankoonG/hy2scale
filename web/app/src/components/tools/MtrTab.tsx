import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card, Input, Toggle } from '@hy2scale/ui';
import { streamTool, type MtrHop, type MtrLine, type ToolAvail } from '@/api/tools';
import { Field, RunButton, Unavailable, ms, useToolRun } from './common';

export default function MtrTab({ avail }: { avail?: ToolAvail }) {
  const { t } = useTranslation();
  const run = useToolRun();
  const [host, setHost] = useState('');
  const [count, setCount] = useState('10');
  const [dns, setDns] = useState(true);
  const [target, setTarget] = useState<{ host: string; ip: string } | null>(null);
  const [hops, setHops] = useState<MtrHop[]>([]);
  const [round, setRound] = useState(0);

  const go = () => {
    const h = host.trim();
    if (!h || run.running) return;
    setHops([]);
    setRound(0);
    setTarget(null);
    run.start((signal) => streamTool<MtrLine>('/tools/mtr', { host: h, count: Number(count) || 10, dns: dns ? 1 : 0 }, (l) => {
      if (l.type === 'start') setTarget({ host: l.host, ip: l.ip });
      else if (l.type === 'round') { setHops(l.hops); setRound(l.round); }
      else if (l.type === 'error') run.setError(l.error);
    }, signal));
  };

  return (
    <div className="tool-tab" data-testid="tool-mtr">
      <Card>
        <form className="tool-bar" onSubmit={(e) => { e.preventDefault(); go(); }}>
          <Field label={t('tools.target')} grow>
            <Input value={host} onChange={(e) => setHost(e.target.value)} placeholder={t('tools.targetPh')} data-testid="mtr-host" />
          </Field>
          <Field label={t('tools.rounds')}>
            <Input className="tool-num" type="number" min={1} max={1000} value={count} onChange={(e) => setCount(e.target.value)} />
          </Field>
          <Field label={t('tools.resolveNames')}>
            <span className="tool-toggle"><Toggle checked={dns} onChange={(e) => setDns(e.target.checked)} /></span>
          </Field>
          <RunButton running={run.running} disabled={!host.trim() || avail?.available === false} onStop={run.stop} testId="mtr" />
        </form>
        <Unavailable avail={avail} />
        {run.error && <div className="tool-note error" data-testid="mtr-error">{run.error}</div>}
      </Card>

      {(hops.length > 0 || target) && (
        <Card
          title={target ? (target.host === target.ip ? target.ip : t('tools.resolved', { host: target.host, ip: target.ip })) : undefined}
          actions={round > 0 ? <span className="muted">{t('tools.round', { n: round })}</span> : undefined}
          noPadding
        >
          <div className="tool-table-wrap">
            <table className="tool-table" data-testid="mtr-table">
              <thead>
                <tr>
                  <th>#</th>
                  <th className="left">{t('tools.host')}</th>
                  <th>{t('tools.loss')}</th>
                  <th>{t('tools.sent')}</th>
                  <th>{t('tools.last')}</th>
                  <th>{t('tools.avg')}</th>
                  <th>{t('tools.best')}</th>
                  <th>{t('tools.worst')}</th>
                  <th>{t('tools.stdev')}</th>
                </tr>
              </thead>
              <tbody>
                {hops.map((h) => {
                  const none = !h.ip;
                  return (
                    <tr key={h.ttl} className={none ? 'muted' : undefined}>
                      <td>{h.ttl}</td>
                      <td className="left">
                        {none ? '???' : (
                          <>
                            {h.name && h.name !== h.ip && <span>{h.name} </span>}
                            <span className={h.name && h.name !== h.ip ? 'mono muted' : 'mono'}>{h.ip}</span>
                          </>
                        )}
                      </td>
                      <td className={h.loss > 0 && !none ? 'bad' : undefined}>{none ? '—' : `${h.loss.toFixed(h.loss % 1 ? 1 : 0)}%`}</td>
                      <td>{h.sent}</td>
                      <td>{none ? '—' : ms(h.last_ms)}</td>
                      <td>{none ? '—' : ms(h.avg_ms)}</td>
                      <td>{none ? '—' : ms(h.best_ms)}</td>
                      <td>{none ? '—' : ms(h.worst_ms)}</td>
                      <td>{none ? '—' : ms(h.stdev_ms)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </div>
  );
}
