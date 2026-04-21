import { Modal } from './Modal';
import { Button } from './Button';
import { createContext, useContext, useCallback, useEffect, useRef, useState, type ReactNode } from 'react';

export interface ConfirmDialogProps {
  open: boolean;
  title: string;
  message: ReactNode;
  confirmText?: string;
  cancelText?: string;
  danger?: boolean;
  animateFrom?: { x: number; y: number };
  onConfirm: () => void;
  onCancel: () => void;
}

export function ConfirmDialog({
  open, title, message, confirmText = 'Confirm', cancelText = 'Cancel',
  danger, animateFrom, onConfirm, onCancel,
}: ConfirmDialogProps) {
  return (
    <Modal
      open={open}
      onClose={onCancel}
      title={title}
      animateFrom={animateFrom}
      footer={
        <>
          <Button onClick={onCancel}>{cancelText}</Button>
          <Button variant={danger ? 'danger' : 'primary'} onClick={onConfirm}>{confirmText}</Button>
        </>
      }
    >
      <div className="hy-confirm-body">{message}</div>
    </Modal>
  );
}

// Imperative confirm hook
interface ConfirmOptions {
  title: string;
  message: ReactNode;
  confirmText?: string;
  cancelText?: string;
  danger?: boolean;
  /** Pixel origin from which the confirm dialog animates in. When omitted,
   *  the ConfirmProvider falls back to the last recorded pointerdown
   *  position — so existing callers that fire from a click handler get
   *  correct button-anchored animation without having to thread the event. */
  animateFrom?: { x: number; y: number };
}

interface ConfirmContextValue {
  confirm: (opts: ConfirmOptions) => Promise<boolean>;
}

const ConfirmContext = createContext<ConfirmContextValue | null>(null);

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<(ConfirmOptions & { resolve: (v: boolean) => void; origin?: { x: number; y: number } }) | null>(null);
  // Separate open flag: toggled to false on resolve so the ConfirmDialog
  // stays mounted long enough for the Modal's AnimatePresence to play
  // the exit animation. Content (state) is cleared only after the exit
  // finishes so the last-shown title/message/origin aren't lost mid-fade.
  const [open, setOpen] = useState(false);

  // Track the last pointerdown coordinates at the window level. Since
  // confirm() is typically invoked synchronously inside a click handler,
  // the ref's value at call time equals the click that triggered the
  // confirm — giving every existing caller a button-anchored animation
  // without needing to thread MouseEvent objects through.
  const lastPointerPosRef = useRef<{ x: number; y: number } | null>(null);
  useEffect(() => {
    const h = (ev: PointerEvent) => { lastPointerPosRef.current = { x: ev.clientX, y: ev.clientY }; };
    window.addEventListener('pointerdown', h, true);
    return () => window.removeEventListener('pointerdown', h, true);
  }, []);

  const closeTimerRef = useRef<number | null>(null);
  const confirm = useCallback((opts: ConfirmOptions): Promise<boolean> => {
    return new Promise((resolve) => {
      if (closeTimerRef.current) { window.clearTimeout(closeTimerRef.current); closeTimerRef.current = null; }
      const origin = opts.animateFrom || lastPointerPosRef.current || undefined;
      setState({ ...opts, resolve, origin });
      setOpen(true);
    });
  }, []);

  const handleResult = (result: boolean) => {
    state?.resolve(result);
    setOpen(false);
    // Clear state after the Modal's exit animation completes (Modal
    // uses 0.47s default, bumped by a small safety margin). Until then
    // the ConfirmDialog keeps rendering the last title/message so the
    // close animation has something to fade away.
    if (closeTimerRef.current) window.clearTimeout(closeTimerRef.current);
    closeTimerRef.current = window.setTimeout(() => {
      setState(null);
      closeTimerRef.current = null;
    }, 550);
  };

  return (
    <ConfirmContext.Provider value={{ confirm }}>
      {children}
      {state && (
        <ConfirmDialog
          open={open}
          title={state.title}
          message={state.message}
          confirmText={state.confirmText}
          cancelText={state.cancelText}
          danger={state.danger}
          animateFrom={state.origin}
          onConfirm={() => handleResult(true)}
          onCancel={() => handleResult(false)}
        />
      )}
    </ConfirmContext.Provider>
  );
}

export function useConfirm() {
  const ctx = useContext(ConfirmContext);
  if (!ctx) throw new Error('useConfirm must be used within ConfirmProvider');
  return ctx.confirm;
}
