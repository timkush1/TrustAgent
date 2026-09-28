/**
 * Shares and rates for the Overview and Evaluators pages: "12%", "4.1% (95% CI 2.9–5.6%)", and
 * whether a LabelledRate has enough unbiased samples to be shown at all (SPEC §2.6 LabelledRate,
 * §3.3 "Needs 30 unbiased samples (have 12)"). Rounding is half-up like r2() (SPEC §2.1), never
 * Math.round.
 */
import type { LabelledRate } from '@/types/api';
import { copy, fillTemplate } from './presentation';

const EM_DASH = '—';

function roundTo(x: number, decimals: number): number {
  const f = 10 ** decimals;
  return Math.floor(x * f + 0.5) / f;
}

/**
 * A share in [0, 1] as a percentage: one decimal below 10% ("4.1%"), whole numbers from there
 * ("12%"), "<0.1%" for a small non-zero share, an em dash for null.
 */
export function formatPercent(share: number | null | undefined): string {
  if (share === null || share === undefined || !Number.isFinite(share)) return EM_DASH;
  const pct = share * 100;
  if (pct === 0) return '0%';
  if (pct > 0 && pct < 0.05) return '<0.1%';
  if (Math.abs(pct) < 10) {
    const v = roundTo(pct, 1);
    // 9.96% rounds to 10.0: write it the way the next band would.
    return v >= 10 ? `${roundTo(pct, 0)}%` : `${v.toFixed(1)}%`;
  }
  return `${roundTo(pct, 0)}%`;
}

/** "2.9–5.6%" for a 95% interval of shares. */
export function formatInterval(ci: readonly [number, number] | null | undefined): string | null {
  if (!ci) return null;
  const [lo, hi] = ci;
  return `${formatPercent(lo).replace('%', '')}–${formatPercent(hi)}`;
}

/** The share of `part` in `whole`, or null when there is no whole. */
export function share(part: number, whole: number): number | null {
  return whole > 0 ? part / whole : null;
}

export interface RateReadout {
  /** Enough unbiased samples to show the value. */
  enough: boolean;
  /** "4.1%" when enough, else null. */
  value: string | null;
  /** "95% CI 2.9–5.6%" when the server sent an interval. */
  interval: string | null;
  /** "Needs 30 unbiased samples (have 12)" until n ≥ min_n. */
  needs: string | null;
  /** Progress towards min_n, in [0, 1]. */
  progress: number;
}

/**
 * `needsTemplate` words the "not enough samples" sentence ({min_n}, {n}); the default says the
 * samples are unbiased, which only a uniform sample is (a share among risk-selected audits is not).
 */
export function rateReadout(rate: LabelledRate, needsTemplate: string = copy.overview.hallucination_rate_needs_samples): RateReadout {
  const enough = rate.value !== null && rate.n >= rate.min_n;
  const interval = formatInterval(rate.ci95);
  return {
    enough,
    value: enough ? formatPercent(rate.value) : null,
    interval: enough && interval ? `95% CI ${interval}` : null,
    needs: enough ? null : fillTemplate(needsTemplate, { min_n: rate.min_n, n: rate.n }),
    progress: rate.min_n > 0 ? Math.min(1, Math.max(0, rate.n / rate.min_n)) : 1,
  };
}
