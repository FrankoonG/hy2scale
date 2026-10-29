import { useRef, useState, type CSSProperties, type ReactNode } from 'react';
import clsx from 'clsx';
import { motion, AnimatePresence } from 'framer-motion';
import { useDismiss } from '../hooks/useDismiss';
import { IconCheck, IconChevronRight, type IconComponent } from '../icons';

export interface MenuProps {
  open: boolean;
  children: ReactNode;
  /** Placement / size classes of the popup (it is `position:absolute`). */
  className?: string;
  style?: CSSProperties;
}

/** Animated dropdown panel. Place it inside a `position:relative` anchor. */
export function Menu({ open, children, className, style }: MenuProps) {
  return (
    <AnimatePresence>
      {open && (
        <motion.div
          className={clsx('hy-dropdown hy-menu', className)}
          style={style}
          role="menu"
          initial={{ opacity: 0, y: -8 }}
          animate={{ opacity: 1, y: 0 }}
          exit={{ opacity: 0, y: -8 }}
          transition={{ duration: 0.15, ease: 'easeOut' }}
        >
          {children}
        </motion.div>
      )}
    </AnimatePresence>
  );
}

export interface MenuItemProps {
  icon?: IconComponent;
  children: ReactNode;
  /** Secondary text at the right end (e.g. the current value). */
  meta?: ReactNode;
  danger?: boolean;
  disabled?: boolean;
  onClick?: () => void;
  testId?: string;
}

export function MenuItem({ icon: Icon, children, meta, danger, disabled, onClick, testId }: MenuItemProps) {
  return (
    <button type="button" role="menuitem" className={clsx('hy-dropdown-item', danger && 'danger')}
      disabled={disabled} onClick={onClick} data-testid={testId}>
      {Icon && <Icon />}
      <span>{children}</span>
      {meta !== undefined && <span className="hy-dd-meta">{meta}</span>}
    </button>
  );
}

export interface MenuRadioItemProps {
  checked: boolean;
  children: ReactNode;
  onClick: () => void;
  testId?: string;
}

/** One choice of a set; the current one carries a check mark. */
export function MenuRadioItem({ checked, children, onClick, testId }: MenuRadioItemProps) {
  return (
    <button type="button" role="menuitemradio" aria-checked={checked}
      className={clsx('hy-dropdown-item', checked && 'current')} onClick={onClick} data-testid={testId}>
      <span className="hy-dd-check">{checked && <IconCheck />}</span>
      <span>{children}</span>
    </button>
  );
}

export interface MenuSubProps {
  icon?: IconComponent;
  label: ReactNode;
  meta?: ReactNode;
  open: boolean;
  onToggle: () => void;
  children: ReactNode;
  testId?: string;
}

/** A row that expands its choices in place (accordion). */
export function MenuSub({ icon: Icon, label, meta, open, onToggle, children, testId }: MenuSubProps) {
  return (
    <>
      <button type="button" className="hy-dropdown-item" aria-expanded={open} onClick={onToggle} data-testid={testId}>
        {Icon && <Icon />}
        <span>{label}</span>
        {meta !== undefined && <span className="hy-dd-meta">{meta}</span>}
        <IconChevronRight className="hy-dd-caret" size={14} />
      </button>
      {open && <div className="hy-dd-sub" role="group">{children}</div>}
    </>
  );
}

export function MenuSeparator() {
  return <div className="hy-dd-divider" role="separator" />;
}

export interface MenuCheckProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  children: ReactNode;
  /** Small count at the right end. */
  count?: ReactNode;
  testId?: string;
}

/** A checkbox row (view options inside an action menu). */
export function MenuCheck({ checked, onChange, children, count, testId }: MenuCheckProps) {
  return (
    <label className="hy-menu-check" data-testid={testId}>
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span>{children}</span>
      {count !== undefined && <span className="hy-menu-check-count">{count}</span>}
    </label>
  );
}

export interface DropdownItem {
  key: string;
  label: ReactNode;
  /** Set for a choice list: the current item gets a check mark. */
  checked?: boolean;
  danger?: boolean;
  onClick: () => void;
}

export interface DropdownMenuProps {
  /** The trigger; receives the open state. */
  trigger: (open: boolean) => ReactNode;
  items: DropdownItem[];
  className?: string;
}

/** A trigger with a dropdown list; closes on choice, outside press or Escape. */
export function DropdownMenu({ trigger, items, className }: DropdownMenuProps) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, () => setOpen(false), { enabled: open });
  const pick = (fn: () => void) => () => { setOpen(false); fn(); };
  return (
    <div ref={ref} className="hy-dropdown-anchor" onClick={(e) => { if (!(e.target as HTMLElement).closest('.hy-menu')) setOpen((v) => !v); }}>
      {trigger(open)}
      <Menu open={open} className={clsx('hy-dropdown-below', className)}>
        {items.map((it) => (it.checked !== undefined
          ? <MenuRadioItem key={it.key} checked={it.checked} onClick={pick(it.onClick)}>{it.label}</MenuRadioItem>
          : <MenuItem key={it.key} danger={it.danger} onClick={pick(it.onClick)}>{it.label}</MenuItem>))}
      </Menu>
    </div>
  );
}
