/**
 * The one outcome per trace (ui/outcomes.v1.json) as a pill: icon + label + tone + shape, so no
 * outcome relies on color alone (DESIGN.md "Outcome encoding"). Drawn by the shared <Badge>.
 */
import { Badge } from '@/components/ui/badge';
import { outcomeView, type OutcomeId, type OutcomeView } from '@/domain/presentation';

export interface OutcomePillProps {
  outcome: OutcomeId | OutcomeView;
  size?: 'sm' | 'md';
  className?: string;
}

export function OutcomePill({ outcome, size = 'md', className }: OutcomePillProps) {
  const view = typeof outcome === 'string' ? outcomeView(outcome) : outcome;
  return (
    <Badge
      data-outcome={view.id}
      tone={view.tone}
      emphasis={view.emphasis}
      size={size}
      icon={view.icon}
      spin={view.id === 'checking'}
      className={className}
    >
      {view.label}
    </Badge>
  );
}
