import { Badge, Tooltip } from '@hy2scale/ui';
import { useExitPaths } from '@/hooks/useExitPaths';

interface ExitViaCellProps {
  exitVia: string;
  exitPaths?: string[];
  exitMode?: string;
}

export function ExitViaCell({ exitVia, exitPaths, exitMode }: ExitViaCellProps) {
  const { isReachableAt } = useExitPaths();

  if (!exitVia) return <span style={{ color: 'var(--text-muted)' }}>—</span>;

  const paths = exitPaths && exitPaths.length > 0 ? exitPaths : [exitVia];
  const primary = paths[0];

  const renderPath = (path: string) => {
    const hops = path.split('/');
    let unreachable = false;
    return (
      <span style={{ fontFamily: 'var(--mono)', fontSize: 12 }}>
        {hops.map((hop, i) => {
          // Build qualified prefix for this hop position
          const qp = hops.slice(0, i + 1).join('/');
          if (!unreachable && !isReachableAt(qp)) unreachable = true;
          const color = unreachable ? 'var(--red)' : 'var(--green)';
          return (
            <span key={i}>
              {i > 0 && <span style={{ color: 'var(--text-muted)', margin: '0 2px' }}>/</span>}
              <span style={{ color, fontWeight: 600 }}>{hop}</span>
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
