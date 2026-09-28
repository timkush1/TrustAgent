/**
 * The top of a page's content, under the top bar. The top bar already shows the page title as the
 * only h1 (DESIGN.md layout), so this does not repeat it: it states the question the page answers
 * and holds the page-wide actions on the right, with an optional row below for tabs or filters
 * (one filter row, above everything it scopes). A detail page may add a `title` for the object it
 * shows (a trace id, an evaluator name); it renders as an h2.
 */
import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

export interface PageHeaderProps {
  /** The object on a detail page ("Trace 7f3a…"). List pages leave it to the top bar. */
  title?: ReactNode;
  /** One sentence: what the page answers or what the viewer can do here. */
  description?: ReactNode;
  /** Page-wide actions (primary last, at the right edge). */
  actions?: ReactNode;
  /** A row under the header: tabs or the filter bar. */
  children?: ReactNode;
  className?: string;
}

export function PageHeader({ title, description, actions, children, className }: PageHeaderProps) {
  return (
    <header className={cn('mb-5 space-y-3', className)}>
      {title || description || actions ? (
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0 space-y-1">
            {title ? <h2 className="truncate text-xl font-semibold text-fg">{title}</h2> : null}
            {description ? <p className="max-w-prose text-sm text-fg-muted">{description}</p> : null}
          </div>
          {actions ? <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
        </div>
      ) : null}
      {children}
    </header>
  );
}
