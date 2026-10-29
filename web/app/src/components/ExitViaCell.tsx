import { useTranslation } from 'react-i18next';
import { Badge, Tooltip } from '@hy2scale/ui';
import { useNodeStore } from '@/store/node';

interface ExitViaCellProps {
  exitVia: string;
  exitPaths?: string[];
  exitMode?: string;
}

/** Sort key for an exit column: the path the cell shows first. */
export function exitViaSortValue(exitVia?: string, exitPaths?: string[]): string | null {
  if (!exitVia) return null;
  return exitPaths && exitPaths.length > 0 ? exitPaths[0] : exitVia;
}

export function ExitViaCell({ exitVia, exitPaths, exitMode }: ExitViaCellProps) {
  const { t } = useTranslation();
  const pathStatus = useNodeStore((s) => s.pathStatus);

  if (!exitVia) return <span className="text-muted">—</span>;

  const paths = exitPaths && exitPaths.length > 0 ? exitPaths : [exitVia];
  const primary = paths[0];

  // The backend decides (same rules as routing): hops before the blocking
  // one are green, the blocking hop and everything after it red. A path not
  // evaluated yet (just saved) stays neutral.
  const renderPath = (path: string) => {
    const hops = path.split('/');
    const st = pathStatus.get(path);
    return (
      <span className="exit-path">
        {hops.map((hop, i) => {
          const blocked = st && !st.ok && i >= st.hop;
          const tone = !st ? 'pending' : blocked ? 'bad' : 'ok';
          const title = st && !st.ok && i === st.hop && st.reason ? t(`exit.reason.${st.reason}`) : undefined;
          return (
            <span key={i}>
              {i > 0 && <span className="exit-path-sep">/</span>}
              <span className={`exit-hop ${tone}`} title={title} data-reason={title ? st?.reason : undefined}>{hop}</span>
            </span>
          );
        })}
      </span>
    );
  };

  const modeBadge = exitMode === 'quality' ? (
    <Badge variant="green">{t('exit.modeQualityShort')}</Badge>
  ) : exitMode === 'aggregate' ? (
    <Badge variant="blue">{t('exit.modeAggregateShort')}</Badge>
  ) : null;

  const extraCount = paths.length - 1;

  return (
    <span className="exit-cell">
      {renderPath(primary)}
      {modeBadge}
      {extraCount > 0 && (
        <Tooltip
          content={
            <div>
              {paths.map((p, i) => (
                <div key={i} className="cell-small">
                  <span className="text-muted">{i === 0 ? t('exit.primary') : t('exit.fallback', { n: i })} </span>
                  {renderPath(p)}
                </div>
              ))}
            </div>
          }
        >
          <Badge variant="muted">+{extraCount}</Badge>
        </Tooltip>
      )}
    </span>
  );
}
