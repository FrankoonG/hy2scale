import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useCallback, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Reorder, useDragControls } from 'framer-motion';
import { Autocomplete, FormGroup, GripIcon } from '@hy2scale/ui';
import { useExitPaths } from '@/hooks/useExitPaths';
import { useNodeStore } from '@/store/node';
import clsx from 'clsx';
let nextId = 1;
function toItems(paths) {
    return paths.map((v) => ({ id: nextId++, value: v }));
}
export function ExitPathList({ value, onChange, label }) {
    const { t } = useTranslation();
    const { exitPaths } = useExitPaths();
    // Stable ID items — sync from value.paths on mount/reset
    const [items, setItems] = useState(() => toItems(value.paths.length > 0 ? value.paths : ['']));
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
    const emitChange = useCallback((newItems, newMode) => {
        const paths = newItems.map((it) => it.value);
        onChange({ paths, mode: (newMode ?? mode) });
    }, [onChange, mode]);
    const updateItem = useCallback((id, val) => {
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
    const removePath = useCallback((id) => {
        setItems((prev) => {
            const next = prev.filter((it) => it.id !== id);
            const newMode = next.length <= 1 ? '' : mode;
            emitChange(next, newMode);
            return next;
        });
    }, [emitChange, mode]);
    const handleReorder = useCallback((newItems) => {
        setItems(newItems);
        emitChange(newItems);
    }, [emitChange]);
    const setMode = useCallback((m) => {
        onChange({ paths: items.map((it) => it.value), mode: m });
    }, [items, onChange]);
    const listRef = useRef(null);
    const topology = useNodeStore((s) => s.topology);
    const filledItems = items.filter((it) => it.value);
    const hasMultiple = filledItems.length > 1;
    // Check if single exit target has multiple addrs (enables quality/aggregate)
    let targetHasMultiAddr = false;
    if (filledItems.length === 1) {
        const targetName = filledItems[0].value.split('/').pop() || '';
        const findNode = (nodes) => {
            for (const n of nodes) {
                if (n.name === targetName)
                    return n;
                if (n.children) {
                    const r = findNode(n.children);
                    if (r)
                        return r;
                }
            }
            return null;
        };
        const node = findNode(topology);
        if (node && node.addrs && node.addrs.length > 1)
            targetHasMultiAddr = true;
    }
    const singleOnly = !hasMultiple && !targetHasMultiAddr && filledItems.length <= 1;
    return (_jsxs(FormGroup, { label: label || t('users.exitVia'), children: [_jsxs("div", { className: clsx('exit-mode-options', singleOnly && !hasMultiple && 'exit-mode-disabled'), children: [_jsxs("label", { className: "exit-mode-opt", children: [_jsx("input", { type: "radio", name: "exitMode", checked: mode === '' || !mode, onChange: () => setMode(''), disabled: hasMultiple }), t('exit.modeNone')] }), _jsxs("label", { className: "exit-mode-opt", children: [_jsx("input", { type: "radio", name: "exitMode", checked: mode === 'quality', onChange: () => setMode('quality'), disabled: singleOnly }), t('exit.modeStability')] }), _jsxs("label", { className: "exit-mode-opt", children: [_jsx("input", { type: "radio", name: "exitMode", checked: mode === 'aggregate', onChange: () => setMode('aggregate'), disabled: singleOnly }), t('exit.modeSpeed')] })] }), _jsx(Reorder.Group, { ref: listRef, axis: "y", values: items, onReorder: handleReorder, className: "addr-list", style: { listStyle: 'none', padding: 0, margin: 0 }, children: items.map((item) => (_jsx(PathRow, { item: item, canDrag: items.length > 1, canRemove: items.length > 1, constraintsRef: listRef, exitPaths: exitPaths, placeholder: t('users.exitViaHint'), deleteTitle: t('app.delete'), onUpdate: (val) => updateItem(item.id, val), onRemove: () => removePath(item.id) }, item.id))) }), _jsx("div", { className: "addr-add-row", onClick: addPath, children: t('exit.addPath') })] }));
}
function PathRow({ item, canDrag, canRemove, constraintsRef, exitPaths, placeholder, deleteTitle, onUpdate, onRemove }) {
    const controls = useDragControls();
    const [dragging, setDragging] = useState(false);
    return (_jsxs(Reorder.Item, { value: item, dragListener: false, dragControls: controls, dragConstraints: constraintsRef, dragElastic: 0.1, onDragStart: () => { setDragging(true); document.body.classList.add('dragging-active'); }, onDragEnd: () => { setDragging(false); document.body.classList.remove('dragging-active'); }, className: clsx('addr-row', dragging && 'dragging'), style: { listStyle: 'none' }, children: [canDrag && (_jsx("div", { className: "addr-drag", onPointerDown: (e) => controls.start(e), children: _jsx(GripIcon, {}) })), _jsx(Autocomplete, { options: exitPaths, value: item.value, onChange: onUpdate, placeholder: placeholder }), canRemove && (_jsx("button", { type: "button", className: "addr-del", onClick: onRemove, title: deleteTitle, children: "\u00D7" }))] }));
}
/** Extract exit_via (first path) and exit_paths/exit_mode for API */
export function exitPathToApi(val) {
    const filtered = val.paths.filter(Boolean);
    return {
        exit_via: filtered[0] || '',
        exit_paths: filtered.length > 1 ? filtered : undefined,
        exit_mode: val.mode || undefined,
    };
}
/** Parse API response into ExitPathValue */
export function apiToExitPath(exitVia, exitPaths, exitMode) {
    const paths = exitPaths && exitPaths.length > 0 ? exitPaths : (exitVia ? [exitVia] : ['']);
    return { paths, mode: exitMode || '' };
}
