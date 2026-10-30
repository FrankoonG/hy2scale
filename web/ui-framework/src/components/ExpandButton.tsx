import { createContext, useCallback, useContext, useEffect, useRef, useState, type MouseEvent, type ReactNode } from 'react';
import clsx from 'clsx';
import type { IconComponent } from '../icons';
import { IconChevronDown } from '../icons';
import { useDismiss } from '../hooks/useDismiss';

/** Set by ActionBar while its children render inside the narrow overflow menu. */
export const ActionBarMenuContext = createContext(false);

export interface ExpandAction {
  key: string;
  label: ReactNode;
  icon?: IconComponent;
  onClick: (e: MouseEvent<HTMLButtonElement>) => void;
  disabled?: boolean;
  testId?: string;
}

export interface ExpandButtonProps {
  label: ReactNode;
  icon?: IconComponent;
  variant?: 'default' | 'primary' | 'success' | 'danger';
  /** The main action. */
  onClick: (e: MouseEvent<HTMLButtonElement>) => void;
  /** Secondary actions revealed on hover, long press or the caret. */
  actions: ExpandAction[];
  disabled?: boolean;
  title?: string;
  testId?: string;
  /** Accessible name of the caret that opens the secondary actions. */
  moreLabel?: string;
}

const HOVER_OPEN_MS = 180;
const HOVER_CLOSE_MS = 220;
const LONG_PRESS_MS = 450;

/**
 * A button with secondary actions. On wide layouts the secondary actions
 * drop down under the button while the pointer rests on it (mouse), on a
 * long press (touch / pen / mouse hold) or from the caret (keyboard,
 * click). A plain click runs the main action. Inside ActionBar's narrow
 * overflow menu it renders flat: the main action, then its secondary
 * actions indented beneath it.
 */
export function ExpandButton({
  label, icon: Icon, variant = 'default', onClick, actions, disabled, title, testId, moreLabel,
}: ExpandButtonProps) {
  const inMenu = useContext(ActionBarMenuContext);
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLSpanElement>(null);
  const hoverTimer = useRef(0);
  const pressTimer = useRef(0);
  const longPressed = useRef(false);

  const clearTimers = () => { window.clearTimeout(hoverTimer.current); window.clearTimeout(pressTimer.current); };
  useEffect(() => clearTimers, []);
  useDismiss(rootRef, () => setOpen(false), { enabled: open });

  const later = useCallback((v: boolean, ms: number) => {
    window.clearTimeout(hoverTimer.current);
    hoverTimer.current = window.setTimeout(() => setOpen(v), ms);
  }, []);

  if (inMenu) {
    return (
      <div className="hy-expand-flat">
        <button type="button" className={clsx('hy-btn sm', variant !== 'default' && variant)} disabled={disabled}
          title={title} data-testid={testId} onClick={onClick}>
          {Icon && <Icon size={14} />}{label}
        </button>
        {actions.map((a) => (
          <button key={a.key} type="button" className="hy-btn sm hy-expand-sub" disabled={a.disabled || disabled}
            data-testid={a.testId} onClick={a.onClick}>
            {a.icon && <a.icon size={14} />}{a.label}
          </button>
        ))}
      </div>
    );
  }

  const mouse = (e: { pointerType: string }) => e.pointerType === 'mouse';

  return (
    <span
      ref={rootRef}
      className={clsx('hy-expand', open && 'open')}
      onPointerEnter={(e) => { if (mouse(e) && !disabled) later(true, HOVER_OPEN_MS); }}
      onPointerLeave={(e) => { if (mouse(e)) later(false, HOVER_CLOSE_MS); }}
      onKeyDown={(e) => { if (e.key === 'Escape' && open) { e.stopPropagation(); setOpen(false); } }}
    >
      <span className={clsx('hy-expand-btn hy-btn sm', variant !== 'default' && variant, disabled && 'disabled')}>
        <button
          type="button"
          className="hy-expand-main"
          disabled={disabled}
          title={title}
          data-testid={testId}
          onPointerDown={(e) => {
            if (e.button !== 0) return;
            longPressed.current = false;
            window.clearTimeout(pressTimer.current);
            pressTimer.current = window.setTimeout(() => { longPressed.current = true; setOpen(true); }, LONG_PRESS_MS);
          }}
          onPointerUp={() => window.clearTimeout(pressTimer.current)}
          onPointerCancel={() => window.clearTimeout(pressTimer.current)}
          onContextMenu={(e) => { if (longPressed.current) e.preventDefault(); }}
          onClick={(e) => {
            // A long press only opens the actions.
            if (longPressed.current) { longPressed.current = false; return; }
            setOpen(false);
            onClick(e);
          }}
        >
          {Icon && <Icon size={14} />}{label}
        </button>
        <button
          type="button"
          className="hy-expand-caret"
          disabled={disabled}
          aria-haspopup="menu"
          aria-expanded={open}
          aria-label={moreLabel ?? 'More'}
          title={moreLabel}
          onClick={() => { window.clearTimeout(hoverTimer.current); setOpen((v) => !v); }}
        >
          <IconChevronDown size={12} strokeWidth={2.5} />
        </button>
      </span>
      {open && (
        <span className="hy-expand-panel" role="menu">
          {actions.map((a) => (
            <button key={a.key} type="button" role="menuitem" className="hy-expand-item" disabled={a.disabled}
              data-testid={a.testId}
              onClick={(e) => { setOpen(false); a.onClick(e); }}>
              {a.icon && <a.icon size={14} />}<span>{a.label}</span>
            </button>
          ))}
        </span>
      )}
    </span>
  );
}
