/**
 * Layout surfaces. Elevation is lightness, not shadow (DESIGN.md), so a card is a surface with a
 * hairline. Section is a titled region of a page: its heading is an h2 (the page title in the top
 * bar is the only h1). Card titles default to h3, under a section.
 */
import { Collapsible } from '@base-ui/react/collapsible';
import { ChevronRight } from 'lucide-react';
import { useId, type HTMLAttributes, type ReactNode } from 'react';
import { cn } from '@/lib/cn';

export function Card({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('rounded-lg border border-border bg-surface text-fg', className)} {...props} />;
}

export interface CardHeaderProps {
  title: ReactNode;
  description?: ReactNode;
  /** Buttons or links on the right. */
  actions?: ReactNode;
  /** Heading level (default 3: a card sits under a page section). */
  level?: 2 | 3 | 4;
  id?: string;
  className?: string;
}

export function CardHeader({ title, description, actions, level = 3, id, className }: CardHeaderProps) {
  const Heading = `h${level}` as const;
  return (
    <div className={cn('flex items-start justify-between gap-3 px-4 pt-4', className)}>
      <div className="min-w-0 space-y-0.5">
        <Heading id={id} className="text-sm font-semibold text-fg">
          {title}
        </Heading>
        {description ? <p className="text-sm text-fg-muted">{description}</p> : null}
      </div>
      {actions ? <div className="flex shrink-0 items-center gap-2">{actions}</div> : null}
    </div>
  );
}

export function CardContent({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('p-4', className)} {...props} />;
}

export function CardFooter({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cn('flex flex-wrap items-center justify-end gap-2 border-t border-border px-4 py-3', className)} {...props} />
  );
}

export interface SectionProps {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
  className?: string;
  /** Collapse to its heading (a disclosure), for "Details" and "Performance". */
  collapsible?: boolean;
  defaultOpen?: boolean;
}

export function Section({ title, description, actions, children, className, collapsible = false, defaultOpen = false }: SectionProps) {
  const headingId = useId();
  if (collapsible) {
    // Disclosure pattern: the h2 wraps the toggle button, so the heading stays navigable.
    return (
      <Collapsible.Root defaultOpen={defaultOpen} render={<section aria-labelledby={headingId} />} className={cn('space-y-3', className)}>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0 space-y-0.5">
            <h2 id={headingId} className="text-base font-semibold text-fg">
              <Collapsible.Trigger className="group inline-flex items-center gap-1.5 rounded-sm text-left hover:text-fg">
                <ChevronRight aria-hidden="true" className="size-4 shrink-0 text-fg-muted transition-transform group-data-panel-open:rotate-90" />
                {title}
              </Collapsible.Trigger>
            </h2>
            {description ? <p className="pl-5.5 text-sm text-fg-muted">{description}</p> : null}
          </div>
          {actions ? <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
        </div>
        <Collapsible.Panel>{children}</Collapsible.Panel>
      </Collapsible.Root>
    );
  }
  return (
    <section aria-labelledby={headingId} className={cn('space-y-3', className)}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-0.5">
          <h2 id={headingId} className="text-base font-semibold text-fg">
            {title}
          </h2>
          {description ? <p className="text-sm text-fg-muted">{description}</p> : null}
        </div>
        {actions ? <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
      </div>
      {children}
    </section>
  );
}
