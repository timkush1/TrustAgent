/**
 * The setup checklist at the top of Overview (SPEC §3.3): what is connected and what is still to
 * do, each open item with its next step and a real link to the fix. A finished checklist folds to
 * one line so it stops taking space.
 */
import { Link } from '@tanstack/react-router';
import { ArrowRight, Circle, CircleCheck } from 'lucide-react';
import { Section } from '@/components/ui/card';
import { formatCount } from '@/domain/format';
import { cn } from '@/lib/cn';
import type { ChecklistItem } from './overviewModel';

function FixLink({ item }: { item: ChecklistItem }) {
  const fix = item.fix;
  if (!fix) return null;
  const className = 'inline-flex items-center gap-0.5 text-xs font-medium text-fg underline underline-offset-2';
  const label = item.id === 'evidence' ? 'See why not' : item.id === 'first_request' ? 'Open the Playground' : 'Open Settings';
  if (fix.to === '/app/settings/$section') {
    return (
      <Link to="/app/settings/$section" params={{ section: fix.section }} className={className}>
        {label}
        <ArrowRight aria-hidden="true" className="size-3" />
      </Link>
    );
  }
  if (fix.to === '/app/overview') {
    return (
      <a href="#evidence-coverage" className={className}>
        {label}
        <ArrowRight aria-hidden="true" className="size-3" />
      </a>
    );
  }
  return (
    <Link to={fix.to} className={className}>
      {label}
      <ArrowRight aria-hidden="true" className="size-3" />
    </Link>
  );
}

export function SetupChecklist({ items }: { items: readonly ChecklistItem[] }) {
  const done = items.filter((i) => i.done).length;
  const complete = done === items.length;
  return (
    <Section
      collapsible
      defaultOpen={!complete}
      title={complete ? 'Setup complete' : 'Setup checklist'}
      description={`${formatCount(done)} of ${formatCount(items.length)} done`}
      className="rounded-lg border border-border bg-surface px-4 py-3"
    >
      <ol className="grid gap-x-6 gap-y-2 pl-5.5 sm:grid-cols-2">
        {items.map((item) => (
          <li key={item.id} data-done={item.done || undefined} className="flex items-start gap-2">
            {item.done ? (
              <CircleCheck aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-success" />
            ) : (
              <Circle aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-fg-subtle" />
            )}
            <div className="min-w-0 space-y-0.5">
              <p className={cn('text-sm', item.done ? 'text-fg-muted' : 'font-medium text-fg')}>
                {item.label}{' '}
                <span className="sr-only">{item.done ? '(done)' : '(to do)'}</span>
              </p>
              {item.done ? null : (
                <p className="flex flex-wrap items-center gap-x-2 text-xs text-fg-muted">
                  <span>{item.hint}</span>
                  <FixLink item={item} />
                </p>
              )}
            </div>
          </li>
        ))}
      </ol>
    </Section>
  );
}
