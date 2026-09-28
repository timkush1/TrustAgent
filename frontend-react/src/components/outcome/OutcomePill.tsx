/**
 * The one outcome per trace (ui/outcomes.v1.json) as a pill: icon + label + tone + shape, so no
 * outcome relies on color alone (DESIGN.md "Outcome encoding").
 */
import type { CSSProperties } from 'react';
import { outcomeView, toneVar, type OutcomeId, type OutcomeView } from '@/domain/presentation';
import { cn } from '@/lib/cn';

const emphasisClass: Record<OutcomeView['emphasis'], string> = {
  solid: 'border-transparent bg-(--pill) text-fg-inverse',
  tint: 'border-[color-mix(in_oklab,var(--pill)_40%,transparent)] bg-[color-mix(in_oklab,var(--pill)_var(--tint),transparent)] text-(--pill)',
  outline: 'border-dashed border-(--pill) bg-transparent text-(--pill)',
  quiet: 'border-border bg-surface-sunken text-fg-muted',
};

export interface OutcomePillProps {
  outcome: OutcomeId | OutcomeView;
  size?: 'sm' | 'md';
  className?: string;
}

export function OutcomePill({ outcome, size = 'md', className }: OutcomePillProps) {
  const view = typeof outcome === 'string' ? outcomeView(outcome) : outcome;
  const Icon = view.icon;
  return (
    <span
      data-outcome={view.id}
      style={{ '--pill': `var(${toneVar(view.tone)})` } as CSSProperties}
      className={cn(
        'inline-flex max-w-full items-center gap-1 rounded-full border font-medium whitespace-nowrap',
        size === 'sm' ? 'h-5 px-1.5 text-2xs' : 'h-6 px-2 text-xs',
        emphasisClass[view.emphasis],
        className,
      )}
    >
      <Icon aria-hidden="true" className={cn('shrink-0', size === 'sm' ? 'size-3' : 'size-3.5', view.id === 'checking' && 'animate-spin')} />
      <span className="truncate">{view.label}</span>
    </span>
  );
}
