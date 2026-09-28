/**
 * Claim verdict (or the fast check's verdict) as icon + label in its verdict hue. The underline
 * style that marks the same verdict in answer text is shown on the label, so the badge doubles as
 * a legend entry (color-vision safe: hue, icon, label and underline all differ).
 */
import type { CSSProperties } from 'react';
import type { ClaimVerdict, GateVerdict } from '@/types/api';
import { claimVerdictView, gateVerdictView, type VerdictView } from '@/domain/presentation';
import { cn } from '@/lib/cn';

type Props = ({ verdict: ClaimVerdict; gate?: never } | { gate: GateVerdict; verdict?: never }) & {
  /** Underline the label with the verdict's mark style. */
  legend?: boolean;
  className?: string;
};

export function VerdictBadge({ verdict, gate, legend = false, className }: Props) {
  const view: VerdictView = gate !== undefined ? gateVerdictView(gate) : claimVerdictView(verdict);
  const Icon = view.icon;
  return (
    <span
      title={view.description}
      style={{ '--vc': `var(${view.colorVar})` } as CSSProperties}
      className={cn('inline-flex items-center gap-1 text-xs font-medium text-(--vc)', className)}
    >
      <Icon aria-hidden="true" className="size-3.5 shrink-0" />
      <span
        className={cn(legend && view.underline !== 'none' && 'underline decoration-(--vc) underline-offset-3')}
        style={legend && view.underline !== 'none' ? { textDecorationStyle: view.underline } : undefined}
      >
        {view.label}
      </span>
    </span>
  );
}
