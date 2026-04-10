import { useLayoutEffect, useState, type RefObject } from 'react';

export interface DropdownPos {
  top: number;
  left: number;
  width: number;
  flip: boolean; // true = dropdown opens upward
}

/**
 * Calculates fixed position for a portal dropdown, flipping above
 * the trigger when there isn't enough space below.
 *
 * @param open     whether the dropdown is open
 * @param anchorRef ref to the trigger element
 * @param maxHeight maximum dropdown height (px) — used to decide if flip is needed
 * @param gap      space between trigger and dropdown (px, default 4)
 */
export function useDropdownPosition(
  open: boolean,
  anchorRef: RefObject<HTMLElement | null>,
  maxHeight: number,
  gap = 4,
) {
  const [pos, setPos] = useState<DropdownPos | null>(null);

  useLayoutEffect(() => {
    if (!open || !anchorRef.current) { setPos(null); return; }

    const update = () => {
      const rect = anchorRef.current!.getBoundingClientRect();
      const spaceBelow = window.innerHeight - rect.bottom - gap;
      const flip = spaceBelow < maxHeight && rect.top - gap > spaceBelow;
      setPos({
        top: flip ? rect.top - gap : rect.bottom + gap,
        left: rect.left,
        width: rect.width,
        flip,
      });
    };

    update();
    window.addEventListener('scroll', update, true);
    window.addEventListener('resize', update);
    return () => {
      window.removeEventListener('scroll', update, true);
      window.removeEventListener('resize', update);
    };
  }, [open, anchorRef, maxHeight, gap]);

  return pos;
}
