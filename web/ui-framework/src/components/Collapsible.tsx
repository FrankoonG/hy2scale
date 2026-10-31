import { type ReactNode } from 'react';
import clsx from 'clsx';
import { IconChevronRight } from '../icons';

export interface SectionDividerProps {
  children: ReactNode;
  className?: string;
}

/** Small uppercase heading with a rule above it, splitting a form into parts. */
export function SectionDivider({ children, className }: SectionDividerProps) {
  return <div className={clsx('section-divider', className)}>{children}</div>;
}

export interface CollapsibleProps {
  title: ReactNode;
  open: boolean;
  onToggle: (open: boolean) => void;
  children: ReactNode;
  testId?: string;
}

/**
 * A SectionDivider that shows / hides the content below it. The content is
 * rendered as siblings (no wrapper), so the parent's gap still applies.
 */
export function Collapsible({ title, open, onToggle, children, testId }: CollapsibleProps) {
  return (
    <>
      <div
        className="section-divider collapsible"
        role="button"
        tabIndex={0}
        aria-expanded={open}
        data-testid={testId}
        onClick={() => onToggle(!open)}
        onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onToggle(!open); } }}
      >
        <IconChevronRight className="section-caret" size={12} strokeWidth={2.5} />
        {title}
      </div>
      {open && children}
    </>
  );
}
