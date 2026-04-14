export interface ChipProps {
  label: string;
  onDelete?: () => void;
  className?: string;
}

export function Chip({ label, onDelete, className }: ChipProps) {
  return (
    <span className={`hy-chip ${className || ''}`}>
      {label}
      {onDelete && <span className="close" onClick={onDelete}>×</span>}
    </span>
  );
}
