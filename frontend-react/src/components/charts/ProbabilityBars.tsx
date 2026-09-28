/**
 * How the fast check split its answer across a question's options (the "Show exact Jev request"
 * glass box): one thin bar per option from a common baseline, with its probability written at the
 * end, so every value is readable without hovering. The most likely option is marked in words.
 *
 * Color is spent only on options that count toward the risk (`tone`, set by the caller from the
 * evaluator's risk weights or the matching verdict); every other option is the neutral ink.
 * It is a list, not a picture: screen readers read each option with its probability.
 */
import { formatRisk } from '@/domain/format';
import { cn } from '@/lib/cn';
import { seriesColor, type ChartTone } from './palette';
import { pct01 } from './scale';

export interface ProbabilityOption {
  key: string;
  /** Plain words for the option ("Declines or deflects"). */
  label: string;
  /** Probability in [0, 1]. */
  p: number;
  /** Tone of an option that counts toward the risk; omit for the neutral ink. */
  tone?: ChartTone;
}

export interface ProbabilityBarsProps {
  /** What was asked, as a short name ("Refusal"). Names the list. */
  label: string;
  options: readonly ProbabilityOption[];
  /** The option the check settled on (`top`); defaults to the most likely one. */
  top?: string | null;
  /** Order as given (the evaluator's option order) or most likely first. */
  sort?: 'given' | 'desc';
  /** How a probability is written (default "0.82"). */
  format?: (p: number) => string;
  className?: string;
}

export function ProbabilityBars({ label, options, top, sort = 'given', format = formatRisk, className }: ProbabilityBarsProps) {
  const ordered = sort === 'desc' ? [...options].sort((a, b) => b.p - a.p || a.key.localeCompare(b.key)) : options;
  const topKey =
    top === undefined
      ? options.reduce<ProbabilityOption | null>((best, o) => (best === null || o.p > best.p ? o : best), null)?.key
      : top;
  return (
    <ul aria-label={label} className={cn('space-y-1.5', className)}>
      {ordered.map((o) => {
        const isTop = o.key === topKey;
        return (
          <li
            key={o.key}
            data-option={o.key}
            className="grid grid-cols-[minmax(6rem,12rem)_minmax(3rem,1fr)_3rem] items-center gap-x-3 text-xs"
          >
            <span className={cn('min-w-0 truncate', isTop ? 'font-medium text-fg' : 'text-fg-muted')} title={o.label}>
              {o.label}
              {isTop ? <span className="sr-only"> (most likely)</span> : null}
            </span>
            <span aria-hidden="true" className="relative h-2 rounded-r-[4px] bg-surface-sunken">
              <span
                className="absolute inset-y-0 left-0 rounded-r-[4px]"
                style={{ width: pct01(o.p), backgroundColor: seriesColor({ tone: o.tone ?? 'ink', shade: 'solid' }) }}
              />
            </span>
            <span className={cn('text-right tabular', isTop ? 'font-medium text-fg' : 'text-fg-muted')}>
              <span className="sr-only">: </span>
              {format(o.p)}
            </span>
          </li>
        );
      })}
    </ul>
  );
}
