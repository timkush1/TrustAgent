/**
 * Today's spend against a daily cap (SPEC §3.3 Budgets): a neutral meter (a budget is not a
 * verdict, so the fill is the neutral ink), with the line where observe and flag routes stop on the
 * fast-check budget (the enforcing reserve, 75% by default). The words under it carry the numbers
 * and the state, so nothing depends on the picture.
 *
 * The spend is labelled Estimate: /api/status sums the ledger's real calls without saying how many
 * were priced from tokens rather than reported by the provider, so it claims no more than the
 * weaker of the two.
 */
import { CircleAlert } from 'lucide-react';
import { pct01 } from '@/components/charts/scale';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { formatUsd } from '@/domain/format';
import { formatPercent } from '@/domain/rates';
import type { Spend } from './budgetsModel';

export interface SpendBarProps {
  /** "Fast-check budget". */
  label: string;
  /** The cap in USD per day; 0 means no cap. */
  cap: number;
  spend: Spend | null;
  /** Share of the cap where observe and flag routes stop (1 − reserve); omit for no marker. */
  observeLine?: number;
}

/** Where today's spend comes from (the badge's basis). */
export const SPEND_BASIS =
  'Today’s real calls from the cost ledger: the provider’s reported cost where it reports one, otherwise their tokens × the price table, as the cap counts them. Simulated checks never count.';

export function SpendBar({ label, cap, spend, observeLine }: SpendBarProps) {
  const badge = <EvidenceBadge label="estimate" basis={SPEND_BASIS} subject={`${label}: spent today`} />;
  if (!spend) {
    return <p className="text-xs text-fg-muted">Today’s spend is unknown: this proxy does not report it.</p>;
  }
  if (cap <= 0) {
    return (
      <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-fg-muted">
        <span>
          No cap: <span className="font-medium text-fg tabular">{formatUsd(spend.spent)}</span> spent today (UTC).
        </span>
        {badge}
      </p>
    );
  }
  const share = spend.spent / cap;
  const valueText = `${formatUsd(spend.spent)} of ${formatUsd(cap)} spent today (${formatPercent(share)})`;
  const state = spend.exhausted
    ? 'Used up for today: resets at midnight UTC.'
    : spend.reserveOnly
      ? 'Observe and flag routes have used their share; only routes that block or fall back are checked.'
      : null;
  return (
    <div className="space-y-1">
      <div
        role="meter"
        aria-label={`${label}: spent today`}
        aria-valuemin={0}
        aria-valuemax={cap}
        aria-valuenow={Math.min(spend.spent, cap)}
        aria-valuetext={valueText}
        className="relative h-2 w-full rounded-full bg-surface-sunken"
      >
        <span className="absolute inset-y-0 left-0 rounded-full bg-fg-muted" style={{ width: pct01(share) }} />
        {observeLine !== undefined ? (
          <span
            data-marker="observe-line"
            aria-hidden="true"
            className="absolute -top-1 -bottom-1 w-0.5 -translate-x-1/2 rounded-full bg-fg"
            style={{ left: pct01(observeLine) }}
          />
        ) : null}
      </div>
      <p className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-xs text-fg-muted">
        <span className="flex flex-wrap items-center gap-2">
          <span className="tabular">{valueText}</span>
          {badge}
        </span>
        {observeLine !== undefined ? (
          <span>
            Observe and flag stop at {formatPercent(observeLine)} ({formatUsd(cap * observeLine)})
          </span>
        ) : null}
      </p>
      {state ? (
        <p className="flex items-start gap-1 text-xs text-fg">
          <CircleAlert aria-hidden="true" className="mt-px size-3.5 shrink-0 text-tone-warning" />
          {state}
        </p>
      ) : null}
    </div>
  );
}
