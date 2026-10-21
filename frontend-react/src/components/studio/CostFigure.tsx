/**
 * A cost with its honest label (DESIGN.md §8, SPEC §7.4): "≈ $0.005" with an Estimate badge, a
 * Simulated $0 for a mock, a Vendor-reported figure, or "Cost unknown" when there is nothing to
 * stand on. The badge's popover says how the figure was produced.
 */
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { formatUsd } from '@/domain/format';
import { cn } from '@/lib/cn';
import type { ValueLabel } from '@/types/api';

export interface CostFigureProps {
  usd: number | null;
  label: ValueLabel;
  basis?: string;
  /** What the figure is ("Cost of this test"), for the badge's name and popover title. */
  subject: string;
  /** Write "≈" before an estimate (default true for estimates). */
  approximate?: boolean;
  className?: string;
}

export function CostFigure({ usd, label, basis, subject, approximate, className }: CostFigureProps) {
  const approx = approximate ?? label === 'estimate';
  return (
    <span className={cn('inline-flex flex-wrap items-center gap-1.5', className)}>
      <span className="tabular text-fg">{usd === null ? 'Cost unknown' : `${approx && usd > 0 ? '≈ ' : ''}${formatUsd(usd)}`}</span>
      <EvidenceBadge label={label} basis={basis} subject={subject} />
    </span>
  );
}

