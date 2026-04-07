import { useCallback, useRef, useState } from 'react';
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

interface PathItem {
  id: number;
  value: string;
}

const GripIcon = () => (
  <svg viewBox="0 0 24 24" fill="currentColor">
    <circle cx="9" cy="5" r="1.5"/><circle cx="15" cy="5" r="1.5"/>
    <circle cx="9" cy="12" r="1.5"/><circle cx="15" cy="12" r="1.5"/>
    <circle cx="9" cy="19" r="1.5"/><circle cx="15" cy="19" r="1.5"/>
  </svg>
);

let nextId = 1;
function toItems(paths: string[]): PathItem[] {
  return paths.map((v) => ({ id: nextId++, value: v }));
}

export function ExitPathList({ value, onChange, label }: ExitPathListProps) {
  const { t } = useTranslation();
  const { exitPaths } = useExitPaths();

  // Stable ID items — sync from value.paths on mount/reset
  const [items, setItems] = useState<PathItem[]>(() => toItems(value.paths.length > 0 ? value.paths : ['']));
  const prevPathsRef = useRef(value.paths);

  // Sync items when external paths change (e.g. modal open with new data)
  if (value.paths !== prevPathsRef.current) {
    const extPaths = value.paths.length > 0 ? value.paths : [''];
    // Only reset if the actual content changed (not from our own reorder)
    const curValues = items.map((it) => it.value);
    if (JSON.stringify(extPaths) !== JSON.stringify(curValues)) {
      setItems(toItems(extPaths));
    }
    prevPathsRef.current = value.paths;
  }

  const mode = value.mode;

  const emitChange = useCallback((newItems: PathItem[], newMode?: string) => {
    const paths = newItems.map((it) => it.value);
    onChange({ paths, mode: (newMode ?? mode) as ExitPathValue['mode'] });
  }, [onChange, mode]);

  const updateItem = useCallback((id: number, val: string) => {
    setItems((prev) => {
      const next = prev.map((it) => it.id === id ? { ...it, value: val } : it);
      emitChange(next);
      return next;
    });
  }, [emitChange]);

  const addPath = useCallback(() => {
    setItems((prev) => {
      const next = [...prev, { id: nextId++, value: '' }];
      const newMode = prev.length === 0 ? '' : (mode || 'quality');
      emitChange(next, newMode);
      return next;
    });
  }, [emitChange, mode]);

  const removePath = useCallback((id: number) => {
    setItems((prev) => {
      const next = prev.filter((it) => it.id !== id);
      const newMode = next.length <= 1 ? '' : mode;
      emitChange(next, newMode);
      return next;
    });
  }, [emitChange, mode]);

  const handleReorder = useCallback((newItems: PathItem[]) => {
    setItems(newItems);
    emitChange(newItems);
  }, [emitChange]);

  const setMode = useCallback((m: '' | 'quality' | 'aggregate') => {
    onChange({ paths: items.map((it) => it.value), mode: m });
  }, [items, onChange]);

  const hasMultiple = items.filter((it) => it.value).length > 1;
  const singleOnly = !hasMultiple && items.filter((it) => it.value).length <= 1;

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
      <Reorder.Group
        axis="y"
        values={items}
        onReorder={handleReorder}
        className="addr-list"
        style={{ listStyle: 'none', padding: 0, margin: 0 }}
      >
        {items.map((item) => (
          <PathRow
            key={item.id}
            item={item}
            canDrag={items.length > 1}
            canRemove={items.length > 1}
            exitPaths={exitPaths}
            placeholder={t('users.exitViaHint')}
            deleteTitle={t('app.delete')}
            onUpdate={(val) => updateItem(item.id, val)}
            onRemove={() => removePath(item.id)}
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
  item: PathItem;
  canDrag: boolean;
  canRemove: boolean;
  exitPaths: string[];
  placeholder: string;
  deleteTitle: string;
  onUpdate: (val: string) => void;
  onRemove: () => void;
}

function PathRow({ item, canDrag, canRemove, exitPaths, placeholder, deleteTitle, onUpdate, onRemove }: PathRowProps) {
  const controls = useDragControls();

  return (
    <Reorder.Item
      value={item}
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
        value={item.value}
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
