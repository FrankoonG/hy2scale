import { useRef, useState, type ReactNode } from 'react';
import clsx from 'clsx';

export interface FileDropZoneProps {
  // Picked file (controlled). Pass null to render the empty hint.
  file: File | null;
  onFileSelected: (file: File) => void;
  // Comma-joined accept list passed straight to the hidden <input>.
  // Default: any file. Provide e.g. ".tar.gz,.tgz" or "image/*".
  accept?: string;
  // Validate the picked file before bubbling up. Return true to accept.
  // If you reject, raise your own toast — this component just no-ops.
  validate?: (file: File) => boolean;
  // Hint copy when no file is picked. Single line, no marketing prose
  // (per project tone rules — short, factual).
  emptyHint: ReactNode;
  // Smaller secondary hint (e.g. "Accepted: .tar.gz, .tgz"). Optional.
  emptyAcceptHint?: ReactNode;
  // Hint when a file IS picked, telling the user how to swap it.
  // Defaults to "Click or drop another file to replace".
  replaceHint?: ReactNode;
  disabled?: boolean;
  className?: string;
}

// FileDropZone — a styled drop target with click-to-pick fallback.
//
// Visuals are owned by `.hy-dropzone` in components.css using literal
// mid-grey hex colors so Dark Reader's brightness-preserving inversion
// keeps the dashed border quiet in both modes (var-driven near-white
// borders bounce back near-white in dark mode, which lights up the
// dotted outline against a dark surface — the bug that lived in the
// upgrade modal's first iteration).
export function FileDropZone({
  file,
  onFileSelected,
  accept,
  validate,
  emptyHint,
  emptyAcceptHint,
  replaceHint = 'Click or drop another file to replace',
  disabled = false,
  className,
}: FileDropZoneProps) {
  const [dragOver, setDragOver] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  const accepted = (f: File) => (validate ? validate(f) : true);

  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setDragOver(false);
    if (disabled) return;
    const f = e.dataTransfer.files?.[0];
    if (f && accepted(f)) onFileSelected(f);
  };

  return (
    <div
      className={clsx('hy-dropzone', dragOver && 'is-drag-over', disabled && 'is-disabled', className)}
      onDrop={handleDrop}
      onDragOver={(e) => { e.preventDefault(); e.stopPropagation(); if (!disabled) setDragOver(true); }}
      onDragEnter={(e) => { e.preventDefault(); e.stopPropagation(); if (!disabled) setDragOver(true); }}
      onDragLeave={(e) => { e.preventDefault(); e.stopPropagation(); setDragOver(false); }}
      onClick={() => { if (!disabled) inputRef.current?.click(); }}
      role="button"
      tabIndex={disabled ? -1 : 0}
      onKeyDown={(e) => {
        if (disabled) return;
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          inputRef.current?.click();
        }
      }}
    >
      {file ? (
        <>
          <div className="hy-dropzone-name">{file.name}</div>
          <div className="hy-dropzone-meta">{fmtBytes(file.size)}</div>
          <div className="hy-dropzone-replace">{replaceHint}</div>
        </>
      ) : (
        <>
          <div className="hy-dropzone-hint">{emptyHint}</div>
          {emptyAcceptHint && <div className="hy-dropzone-meta">{emptyAcceptHint}</div>}
        </>
      )}
      <input
        ref={inputRef}
        type="file"
        accept={accept}
        style={{ display: 'none' }}
        onChange={(e) => {
          const f = e.target.files?.[0];
          if (f && accepted(f)) onFileSelected(f);
          e.target.value = '';
        }}
        disabled={disabled}
      />
    </div>
  );
}
FileDropZone.displayName = 'FileDropZone';

function fmtBytes(n: number): string {
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  return (n / 1024 / 1024).toFixed(1) + ' MB';
}
