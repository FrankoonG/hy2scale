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

  if (!exitVia) return <span style={{ color: 'var(--text-muted)' }}>—</span>;

  const paths = exitPaths && exitPaths.length > 0 ? exitPaths : [exitVia];
  const primary = paths[0];

  // The backend decides (same rules as routing): hops before the blocking
  // one are green, the blocking hop and everything after it red. A path not
  // evaluated yet (just saved) stays neutral.
  const renderPath = (path: string) => {
    const hops = path.split('/');
    const st = pathStatus.get(path);
    return (
      <span style={{ fontFamily: 'var(--mono)', fontSize: 12 }}>
        {hops.map((hop, i) => {
          const blocked = st && !st.ok && i >= st.hop;
          const color = !st ? 'var(--text-muted)' : blocked ? 'var(--red)' : 'var(--green)';
          const title = st && !st.ok && i === st.hop && st.reason ? t(`exit.reason.${st.reason}`) : undefined;
          return (
            <span key={i}>
              {i > 0 && <span style={{ color: 'var(--text-muted)', margin: '0 2px' }}>/</span>}
              <span style={{ color, fontWeight: 600 }} title={title} data-reason={title ? st?.reason : undefined}>{hop}</span>
            </span>
          );
        })}
      </span>
    );
  };

  const modeBadge = exitMode === 'quality' ? (
    <Badge variant="green" className="ml-1">Q</Badge>
  ) : exitMode === 'aggregate' ? (
    <Badge variant="blue" className="ml-1">A</Badge>
  ) : null;

  const extraCount = paths.length - 1;

  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
      {renderPath(primary)}
      {modeBadge}
      {extraCount > 0 && (
        <Tooltip
          content={
            <div>
              {paths.map((p, i) => (
                <div key={i} style={{ fontSize: 12 }}>
                  <span style={{ color: 'var(--text-muted)' }}>{i === 0 ? 'primary: ' : `fallback ${i}: `}</span>
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
