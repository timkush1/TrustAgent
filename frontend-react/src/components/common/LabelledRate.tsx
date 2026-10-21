/**
 * A rate that is only honest with enough unbiased samples (SPEC §2.6 LabelledRate): the value with
 * its 95% interval, sample size and value label once n ≥ min_n; before that, "Needs 30 unbiased
 * samples (have 12)" with a progress meter instead of a number that would mislead. Where the rate
 * cannot be collected at all (a replay picked its audits by risk), the sentence says why and no
 * meter is drawn, since nothing is being collected.
 */
import { formatCount } from '@/domain/format';
import { rateReadout } from '@/domain/rates';
import { cn } from '@/lib/cn';
import type { LabelledRate } from '@/types/api';
import { EvidenceBadge } from './EvidenceBadge';

export interface LabelledRateViewProps {
  rate: LabelledRate;
  /** What the rate measures ("Hallucination rate"); names the meter and the badge. */
  subject: string;
  /** Show the server's basis sentence under the value. */
  showBasis?: boolean;
  /**
   * The sentence below `min_n` ({min_n}, {n}); defaults to "Needs 30 unbiased samples (have 12)",
   * which fits only a uniform sample.
   */
  needsTemplate?: string;
  /**
   * The rate does not apply here (a replay has no uniform sample): show the `needsTemplate` sentence
   * and the badge only, with no progress meter that would read as still collecting.
   */
  notApplicable?: boolean;
  size?: 'md' | 'lg';
  className?: string;
}

export function LabelledRateView({
  rate,
  subject,
  showBasis = false,
  needsTemplate,
  notApplicable = false,
  size = 'md',
  className,
}: LabelledRateViewProps) {
  const r = rateReadout(rate, needsTemplate);
  if (!r.enough) {
    return (
      <div className={cn('space-y-1.5', className)}>
        <div className="flex flex-wrap items-center gap-2">
          <p className="text-sm text-fg">{r.needs}</p>
          <EvidenceBadge label={rate.label} basis={rate.basis} subject={subject} />
        </div>
        {notApplicable ? null : (
          <div
            role="meter"
            aria-label={`${subject}: unbiased samples collected`}
            aria-valuemin={0}
            aria-valuemax={rate.min_n}
            aria-valuenow={Math.min(rate.n, rate.min_n)}
            aria-valuetext={`${formatCount(rate.n)} of ${formatCount(rate.min_n)} samples`}
            className="h-1.5 w-full max-w-60 overflow-hidden rounded-full bg-surface-sunken ring-1 ring-border ring-inset"
          >
            <div className="h-full rounded-full bg-fg-muted" style={{ width: `${r.progress * 100}%` }} />
          </div>
        )}
        {showBasis && rate.basis ? <p className="text-xs text-fg-muted">{rate.basis}</p> : null}
      </div>
    );
  }
  return (
    <div className={cn('space-y-0.5', className)}>
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <span className={cn('font-semibold text-fg tabular', size === 'lg' ? 'text-2xl' : 'text-xl')}>{r.value}</span>
        {r.interval ? <span className="text-xs text-fg-muted tabular">{r.interval}</span> : null}
        <span className="text-xs text-fg-muted tabular">n = {formatCount(rate.n)}</span>
        <EvidenceBadge label={rate.label} basis={rate.basis} subject={subject} className="self-center" />
      </div>
      {showBasis && rate.basis ? <p className="text-xs text-fg-muted">{rate.basis}</p> : null}
    </div>
  );
}
