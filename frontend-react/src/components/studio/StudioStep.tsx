/**
 * One numbered step of Studio (SPEC §7.3): a titled region with an anchor the steps list links to.
 * The number is decoration; screen readers hear "Step 2: Review the check".
 */
import { useId, type ReactNode } from 'react';
import { cn } from '@/lib/cn';

export interface StudioStepProps {
  n: number;
  /** Anchor id ("step-review"). */
  id: string;
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
  className?: string;
}

export function StudioStep({ n, id, title, description, actions, children, className }: StudioStepProps) {
  const headingId = useId();
  return (
    <section id={id} aria-labelledby={headingId} className={cn('scroll-mt-16 space-y-4 rounded-lg border border-border bg-surface p-4 sm:p-5', className)}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-0.5">
          <h2 id={headingId} className="flex items-center gap-2 text-base font-semibold text-fg">
            <span aria-hidden="true" className="tabular inline-flex size-6 shrink-0 items-center justify-center rounded-full border border-border-strong text-xs">
              {n}
            </span>
            <span className="sr-only">Step {n}:</span> {title}
          </h2>
          {description ? <p className="text-sm text-fg-muted">{description}</p> : null}
        </div>
        {actions ? <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
      </div>
      {children}
    </section>
  );
}
