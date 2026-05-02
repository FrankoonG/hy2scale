import { useCallback, useEffect, useRef, useState, type RefObject } from 'react';
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

  // Items are the per-row form state. The earlier assumption — that
  // the consuming Modal unmounts on close, giving a fresh mount with
  // the right initial value — was wrong: the Modal stays mounted and
  // toggles open=false, so this component's items would persist across
  // user switches in UserModal (and rule switches in RulesPage). The
  // sync-from-value effect below resyncs items whenever the parent
  // hands us a different value than the one we last emitted — which
  // is exactly the "operator opened a different row" case.
  const [items, setItems] = useState<PathItem[]>(() => toItems(value.paths.length > 0 ? value.paths : ['']));
  // addPath/removePath need to drive mode transitions alongside items.
  // Stash the desired mode here and let the post-commit effect apply it.
  const pendingModeOverrideRef = useRef<ExitPathValue['mode'] | null>(null);
  const lastEmittedRef = useRef<string>(JSON.stringify({
    paths: value.paths.length > 0 ? value.paths : [''],
    mode: value.mode,
  }));

  const mode = value.mode;

  // External-value sync: if `value` differs from what we last emitted,
  // the parent assigned a new value (e.g. UserModal swapped editing
  // target). Pull it into items so the rendered rows reflect the new
  // user. lastEmittedRef is updated here too so the emit-effect that
  // fires immediately after setItems sees a matching signature and
  // short-circuits — no echo back to the parent, no loop.
  useEffect(() => {
    const incomingPaths = value.paths.length > 0 ? value.paths : [''];
    const incomingSig = JSON.stringify({ paths: incomingPaths, mode: value.mode });
    if (incomingSig !== lastEmittedRef.current) {
      lastEmittedRef.current = incomingSig;
      setItems(toItems(incomingPaths));
    }
  }, [value]);

  // Emit AFTER commit, never inside a setItems updater. Mirrors the TargetList
  // fix — calling parent setState from inside an updater races with concurrent
  // re-renders (zustand topology polls) and forces remounts that drop focus.
  useEffect(() => {
    const paths = items.map((it) => it.value);
    const override = pendingModeOverrideRef.current;
    pendingModeOverrideRef.current = null;
    let m: ExitPathValue['mode'] = override !== null ? override : mode;
    const filled = paths.filter(Boolean);
    if (m && filled.length <= 1 && filled.length === 0) m = '';
    if (m === 'aggregate' && filled.length > 1) {
      const targets = new Set(filled.map((p) => p.split('/').pop()));
      if (targets.size > 1) m = 'quality';
    }
    const sig = JSON.stringify({ paths, mode: m });
    if (sig !== lastEmittedRef.current) {
      lastEmittedRef.current = sig;
      onChange({ paths, mode: m });
    }
  }, [items, mode, onChange]);

  const updateItem = useCallback((id: number, val: string) => {
    setItems((prev) => prev.map((it) => it.id === id ? { ...it, value: val } : it));
  }, []);

  const addPath = useCallback(() => {
    setItems((prev) => {
      pendingModeOverrideRef.current = prev.length === 0 ? '' : (mode || 'quality');
      return [...prev, { id: nextId++, value: '' }];
    });
  }, [mode]);

  const removePath = useCallback((id: number) => {
    setItems((prev) => {
      const next = prev.filter((it) => it.id !== id);
      pendingModeOverrideRef.current = next.length <= 1 ? '' : mode;
      return next;
    });
  }, [mode]);

  const handleReorder = useCallback((newItems: PathItem[]) => {
    setItems(newItems);
  }, []);

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

  // Aggregate requires all paths to reach the same final exit node
  let aggregateDisabled = singleOnly;
  if (hasMultiple && !aggregateDisabled) {
    const targets = new Set(filledItems.map((it) => it.value.split('/').pop()));
    if (targets.size > 1) aggregateDisabled = true;
  }

  return (
    <FormGroup label={label || t('users.exitVia')}>
      {/* Mode selection */}
      <div className="exit-mode-options">
        <label className="exit-mode-opt">
          <input type="radio" name="exitMode" checked={mode === '' || !mode} onChange={() => setMode('')} disabled={hasMultiple} />
          {t('exit.modeNone')}
        </label>
        <label className="exit-mode-opt">
          <input type="radio" name="exitMode" checked={mode === 'quality'} onChange={() => setMode('quality')} disabled={singleOnly} />
          {t('exit.modeStability')}
        </label>
        <label className="exit-mode-opt">
          <input type="radio" name="exitMode" checked={mode === 'aggregate'} onChange={() => setMode('aggregate')} disabled={aggregateDisabled} />
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
