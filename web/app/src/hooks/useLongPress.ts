import { useCallback, useRef } from 'react';
import * as React from 'react';

export interface UseLongPressOptions {
  delayMs?: number;
  onLongPress: () => void;
  onShortClick?: () => void;
}

export interface LongPressHandlers {
  onMouseDown: (e: React.MouseEvent) => void;
  onMouseUp: (e: React.MouseEvent) => void;
  onMouseLeave: () => void;
  onTouchStart: (e: React.TouchEvent) => void;
  onTouchEnd: (e: React.TouchEvent) => void;
  onTouchCancel: () => void;
  onContextMenu: (e: React.MouseEvent) => void;
  onClick: (e: React.MouseEvent) => void;
}

// useLongPress returns a unified handler set that distinguishes a
// long press (>= delayMs) from a short click. The long-press fires once
// from the held timer; the short-click fires from the synthetic onClick
// only when the press was released BEFORE the timer fired. This avoids
// double-firing when the underlying button has its own onClick.
export function useLongPress({ delayMs = 600, onLongPress, onShortClick }: UseLongPressOptions): LongPressHandlers {
  const timerRef = useRef<number | null>(null);
  const triggeredRef = useRef(false);

  const start = useCallback(() => {
    triggeredRef.current = false;
    if (timerRef.current) window.clearTimeout(timerRef.current);
    timerRef.current = window.setTimeout(() => {
      triggeredRef.current = true;
      timerRef.current = null;
      onLongPress();
    }, delayMs);
  }, [delayMs, onLongPress]);

  const cancel = useCallback(() => {
    if (timerRef.current) {
      window.clearTimeout(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  const onMouseDown = useCallback((_e: React.MouseEvent) => start(), [start]);
  const onMouseUp = useCallback((_e: React.MouseEvent) => cancel(), [cancel]);
  const onMouseLeave = useCallback(() => cancel(), [cancel]);
  const onTouchStart = useCallback((_e: React.TouchEvent) => start(), [start]);
  const onTouchEnd = useCallback((_e: React.TouchEvent) => cancel(), [cancel]);
  const onTouchCancel = useCallback(() => cancel(), [cancel]);
  // Long-press on touch devices and right-click on desktop browsers can
  // both surface a contextmenu — suppress it so the button stays interactive.
  const onContextMenu = useCallback((e: React.MouseEvent) => e.preventDefault(), []);

  const onClick = useCallback((e: React.MouseEvent) => {
    if (triggeredRef.current) {
      // Long-press already handled the gesture; swallow the click so the
      // host button's default action doesn't run a second time.
      e.preventDefault();
      e.stopPropagation();
      triggeredRef.current = false;
      return;
    }
    onShortClick?.();
  }, [onShortClick]);

  return { onMouseDown, onMouseUp, onMouseLeave, onTouchStart, onTouchEnd, onTouchCancel, onContextMenu, onClick };
}
