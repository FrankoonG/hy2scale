import { useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Reorder, useDragControls } from 'framer-motion';
import { Autocomplete, FormGroup } from '@hy2scale/ui';
import { useExitPaths } from '@/hooks/useExitPaths';
import clsx from 'clsx';

export interface ExitPathValue {
  paths: string[];
  mode: '' | 'quality' | 'aggregate';
}

interface ExitPathListProps {
  value: ExitPathValue;
  onChange: (val: ExitPathValue) => void;
  label?: string;
}

const GripIcon = () => (
  <svg viewBox="0 0 24 24" fill="currentColor">
    <circle cx="9" cy="5" r="1.5"/><circle cx="15" cy="5" r="1.5"/>
    <circle cx="9" cy="12" r="1.5"/><circle cx="15" cy="12" r="1.5"/>
    <circle cx="9" cy="19" r="1.5"/><circle cx="15" cy="19" r="1.5"/>
  </svg>
);

export function ExitPathList({ value, onChange, label }: ExitPathListProps) {
  const { t } = useTranslation();
  const { exitPaths } = useExitPaths();

  const paths = value.paths.length > 0 ? value.paths : [''];
  const mode = value.mode;

  const updatePath = useCallback((index: number, val: string) => {
    const newPaths = [...paths];
    newPaths[index] = val;
    onChange({ paths: newPaths, mode: value.mode });
  }, [paths, value.mode, onChange]);

  const addPath = useCallback(() => {
    onChange({ paths: [...paths, ''], mode: paths.length === 0 ? '' : (value.mode || 'quality') });
  }, [paths, value.mode, onChange]);

  const removePath = useCallback((index: number) => {
    const newPaths = paths.filter((_, i) => i !== index);
    const newMode = newPaths.length <= 1 ? '' : value.mode;
    onChange({ paths: newPaths, mode: newMode });
  }, [paths, value.mode, onChange]);

  const setMode = useCallback((m: '' | 'quality' | 'aggregate') => {
    onChange({ paths: value.paths, mode: m });
  }, [value.paths, onChange]);

  const handleReorder = useCallback((newPaths: string[]) => {
    onChange({ paths: newPaths, mode: value.mode });
  }, [value.mode, onChange]);

  const hasMultiple = paths.filter(Boolean).length > 1;
  const singleOnly = !hasMultiple && paths.filter(Boolean).length <= 1;

  return (
    <FormGroup label={label || t('users.exitVia')}>
      {/* Mode selection */}
      <div className={clsx('exit-mode-options', singleOnly && !hasMultiple && 'exit-mode-disabled')}>
        <label className="exit-mode-opt">
          <input type="radio" name="exitMode" checked={mode === '' || !mode} onChange={() => setMode('')} disabled={hasMultiple} />
          {t('exit.modeNone')}
        </label>
        <label className="exit-mode-opt">
          <input type="radio" name="exitMode" checked={mode === 'quality'} onChange={() => setMode('quality')} disabled={singleOnly} />
          {t('exit.modeStability')}
        </label>
        <label className="exit-mode-opt">
          <input type="radio" name="exitMode" checked={mode === 'aggregate'} onChange={() => setMode('aggregate')} disabled={singleOnly} />
          {t('exit.modeSpeed')}
        </label>
      </div>

      {/* Path inputs */}
      <Reorder.Group axis="y" values={paths} onReorder={handleReorder} className="addr-list" style={{ listStyle: 'none', padding: 0, margin: 0 }}>
        {paths.map((p, i) => (
          <PathRow
            key={`path-${i}-${paths.length}`}
            path={p}
            index={i}
            canRemove={paths.length > 1}
            canDrag={paths.length > 1}
            exitPaths={exitPaths}
            placeholder={t('users.exitViaHint')}
            deleteTitle={t('app.delete')}
            onUpdate={(v) => updatePath(i, v)}
            onRemove={() => removePath(i)}
          />
        ))}
      </Reorder.Group>
      <div className="addr-add-row" onClick={addPath}>
        {t('exit.addPath')}
      </div>
    </FormGroup>
  );
}

interface PathRowProps {
  path: string;
  index: number;
  canRemove: boolean;
  canDrag: boolean;
  exitPaths: string[];
  placeholder: string;
  deleteTitle: string;
  onUpdate: (val: string) => void;
  onRemove: () => void;
}

function PathRow({ path, canRemove, canDrag, exitPaths, placeholder, deleteTitle, onUpdate, onRemove }: PathRowProps) {
  const controls = useDragControls();

  return (
    <Reorder.Item
      value={path}
      dragListener={false}
      dragControls={controls}
      className="addr-row"
      style={{ listStyle: 'none' }}
    >
      {canDrag && (
        <div className="addr-drag" onPointerDown={(e) => controls.start(e)}>
          <GripIcon />
        </div>
      )}
      <Autocomplete
        options={exitPaths}
        value={path}
        onChange={onUpdate}
        placeholder={placeholder}
      />
      {canRemove && (
        <button type="button" className="addr-del" onClick={onRemove} title={deleteTitle}>
          ×
        </button>
      )}
    </Reorder.Item>
  );
}

/** Extract exit_via (first path) and exit_paths/exit_mode for API */
export function exitPathToApi(val: ExitPathValue) {
  const filtered = val.paths.filter(Boolean);
  return {
    exit_via: filtered[0] || '',
    exit_paths: filtered.length > 1 ? filtered : undefined,
    exit_mode: val.mode || undefined,
  };
}

/** Parse API response into ExitPathValue */
export function apiToExitPath(exitVia?: string, exitPaths?: string[], exitMode?: string): ExitPathValue {
  const paths = exitPaths && exitPaths.length > 0 ? exitPaths : (exitVia ? [exitVia] : ['']);
  return { paths, mode: (exitMode as ExitPathValue['mode']) || '' };
}
