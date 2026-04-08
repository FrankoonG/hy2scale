import { useCallback, useRef, useState, type RefObject } from 'react';
import { useTranslation } from 'react-i18next';
import { Reorder, useDragControls } from 'framer-motion';
import { Autocomplete, FormGroup, GripIcon } from '@hy2scale/ui';
import { useExitPaths } from '@/hooks/useExitPaths';
import { useNodeStore } from '@/store/node';
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

  const listRef = useRef<HTMLUListElement>(null);
  const topology = useNodeStore((s) => s.topology);
  const filledItems = items.filter((it) => it.value);
  const hasMultiple = filledItems.length > 1;

  // Check if single exit target has multiple addrs (enables quality/aggregate)
  let targetHasMultiAddr = false;
  if (filledItems.length === 1) {
    const targetName = filledItems[0].value.split('/').pop() || '';
    const findNode = (nodes: any[]): any => {
      for (const n of nodes) {
        if (n.name === targetName) return n;
        if (n.children) { const r = findNode(n.children); if (r) return r; }
      }
      return null;
    };
    const node = findNode(topology);
    if (node && node.addrs && node.addrs.length > 1) targetHasMultiAddr = true;
  }
  const singleOnly = !hasMultiple && !targetHasMultiAddr && filledItems.length <= 1;

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
        ref={listRef}
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
            constraintsRef={listRef}
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
  constraintsRef: RefObject<HTMLElement | null>;
  exitPaths: string[];
  placeholder: string;
  deleteTitle: string;
  onUpdate: (val: string) => void;
  onRemove: () => void;
}

function PathRow({ item, canDrag, canRemove, constraintsRef, exitPaths, placeholder, deleteTitle, onUpdate, onRemove }: PathRowProps) {
  const controls = useDragControls();
  const [dragging, setDragging] = useState(false);

  return (
    <Reorder.Item
      value={item}
      dragListener={false}
      dragControls={controls}
      dragConstraints={constraintsRef}
      dragElastic={0.1}
      onDragStart={() => { setDragging(true); document.body.classList.add('dragging-active'); }}
      onDragEnd={() => { setDragging(false); document.body.classList.remove('dragging-active'); }}
      className={clsx('addr-row', dragging && 'dragging')}
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
