import { useCallback, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { AnimatePresence, motion } from 'framer-motion';
import clsx from 'clsx';
import {
  Button, FormGroup, Input, Modal, useDismiss, IconCheck, IconChevronDown, IconChevronRight, IconLayers,
} from '@hy2scale/ui';

export interface CanvasMenuEntry { key: string; label: string }

export interface CanvasMenuProps {
  presets: CanvasMenuEntry[];
  customs: CanvasMenuEntry[];
  activeKey: string;
  activeLabel: string;
  isCustom: boolean;
  onSelect: (key: string) => void;
  onCopy: () => void;
  onImport: () => void;
  onExport: () => void;
  onRename: () => void;
  onReset: () => void;
  onDelete: () => void;
}

/** The menu body, shared by the toolbar dropdown and the ⋯ submenu. */
function MenuItems(p: CanvasMenuProps & { close: () => void }) {
  const { t } = useTranslation();
  const act = (fn: () => void) => () => { p.close(); fn(); };
  const item = (e: CanvasMenuEntry) => (
    <button
      key={e.key}
      type="button"
      role="menuitemradio"
      aria-checked={e.key === p.activeKey}
      className={clsx('hy-dropdown-item', e.key === p.activeKey && 'current')}
      data-testid={`canvas-item-${e.key}`}
      onClick={act(() => p.onSelect(e.key))}
    >
      <span className="hy-dd-check">{e.key === p.activeKey && <IconCheck size={14} />}</span>
      <span className="hy-topo-canvas-name">{e.label}</span>
    </button>
  );
  return (
    <>
      <div className="hy-topo-canvas-sec">{t('nodes.canvas.presets')}</div>
      {p.presets.map(item)}
      <div className="hy-topo-canvas-sec">{t('nodes.canvas.custom')}</div>
      {p.customs.length ? p.customs.map(item) : <div className="hy-topo-canvas-empty">{t('nodes.canvas.none')}</div>}
      <div className="hy-dd-divider" />
      <button type="button" role="menuitem" className="hy-dropdown-item" data-testid="canvas-copy" onClick={act(p.onCopy)}>{t('nodes.canvas.copy')}</button>
      <button type="button" role="menuitem" className="hy-dropdown-item" data-testid="canvas-import" onClick={act(p.onImport)}>{t('nodes.canvas.import')}</button>
      <button type="button" role="menuitem" className="hy-dropdown-item" data-testid="canvas-export" onClick={act(p.onExport)}>{t('nodes.canvas.export')}</button>
      {p.isCustom && (
        <>
          <div className="hy-dd-divider" />
          <button type="button" role="menuitem" className="hy-dropdown-item" data-testid="canvas-rename" onClick={act(p.onRename)}>{t('nodes.canvas.rename')}</button>
          <button type="button" role="menuitem" className="hy-dropdown-item" data-testid="canvas-reset" onClick={act(p.onReset)}>{t('nodes.canvas.reset')}</button>
          <button type="button" role="menuitem" className="hy-dropdown-item danger" data-testid="canvas-delete" onClick={act(p.onDelete)}>{t('nodes.canvas.delete')}</button>
        </>
      )}
    </>
  );
}

/** Toolbar button with a dropdown of every canvas and the canvas actions. */
export function CanvasMenuButton(p: CanvasMenuProps) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const close = useCallback(() => setOpen(false), []);
  useDismiss(ref, close, { enabled: open });
  return (
    <div ref={ref} className="hy-topo-canvas">
      <button
        type="button"
        className={clsx('iface-topo-tool hy-topo-canvas-btn', open && 'open')}
        aria-haspopup="menu"
        aria-expanded={open}
        title={t('nodes.canvas.title')}
        data-testid="canvas-menu"
        onClick={() => setOpen((v) => !v)}
      >
        <IconLayers size={14} />
        <span className="hy-topo-canvas-label">{p.activeLabel}</span>
        <IconChevronDown className="hy-topo-canvas-caret" size={12} />
      </button>
      <AnimatePresence>
        {open && (
          <motion.div
            className="hy-dropdown hy-topo-canvas-menu"
            role="menu"
            initial={{ opacity: 0, y: -6 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -6 }}
            transition={{ duration: 0.15, ease: 'easeOut' }}
          >
            <MenuItems {...p} close={close} />
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  );
}

/** The same menu as an inline-expanding submenu inside the ⋯ menu. */
export function CanvasSubmenu(p: CanvasMenuProps & { onDone: () => void }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  return (
    <div className="hy-topo-canvas-inline">
      <button
        type="button"
        className="hy-dropdown-item"
        aria-expanded={open}
        data-testid="canvas-submenu"
        onClick={() => setOpen((v) => !v)}
      >
        <IconLayers size={14} />
        <span>{t('nodes.canvas.title')}</span>
        <span className="hy-dd-meta">{p.activeLabel}</span>
        <IconChevronRight className="hy-dd-caret" size={14} />
      </button>
      {open && (
        <div className="hy-dd-sub" role="group">
          <MenuItems {...p} close={() => { setOpen(false); p.onDone(); }} />
        </div>
      )}
    </div>
  );
}

/** Small modal asking for a canvas name; resolves null when cancelled. */
export function useNamePrompt(): [ReactNode, (title: string, initial: string) => Promise<string | null>] {
  const { t } = useTranslation();
  const [state, setState] = useState<{ title: string; value: string } | null>(null);
  const resolver = useRef<((v: string | null) => void) | null>(null);
  const ask = useCallback((title: string, initial: string) => new Promise<string | null>((res) => {
    resolver.current = res;
    setState({ title, value: initial });
  }), []);
  const finish = (v: string | null) => {
    resolver.current?.(v);
    resolver.current = null;
    setState(null);
  };
  const value = state?.value ?? '';
  const ok = value.trim().length > 0 && [...value.trim()].length <= 64;
  const el = (
    <Modal
      open={!!state}
      onClose={() => finish(null)}
      title={state?.title}
      footer={
        <>
          <Button onClick={() => finish(null)}>{t('app.cancel')}</Button>
          <Button variant="primary" disabled={!ok} data-testid="canvas-name-ok" onClick={() => finish(value.trim())}>{t('app.confirm')}</Button>
        </>
      }
    >
      <form onSubmit={(e) => { e.preventDefault(); if (ok) finish(value.trim()); }}>
        <FormGroup label={t('nodes.canvas.name')}>
          <Input
            autoFocus
            maxLength={64}
            value={value}
            data-testid="canvas-name-input"
            onChange={(e) => setState((s) => (s ? { ...s, value: e.target.value } : s))}
          />
        </FormGroup>
      </form>
    </Modal>
  );
  return [el, ask];
}
