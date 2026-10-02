/**
 * The Overview hero chart, "Requests by outcome" (SPEC §3.3), beyond what the shared chart draws.
 */
import { outcomeView } from '@/domain/presentation';
import type { OverviewStats } from '@/types/api';
import { bucketDescription } from './overviewModel';

/**
 * The hero chart's description. GET /api/overview (#9) counts an observe-only route's would-block
 * answers in its `would_flag` series, so whenever that series has any, the description says what
 * the series named "Would flag" holds: the legend alone would under-state it.
 */
export function heroDescription(stats: Pick<OverviewStats, 'bucket' | 'window' | 'outcomes_series'>, replay: boolean): string {
  const base = replay ? `${bucketDescription(stats).split(',')[0]}, over the replayed traffic` : bucketDescription(stats);
  if (!stats.outcomes_series.some((p) => p.would_flag > 0)) return base;
  return `${base}. ${outcomeView('would_flag').label} also counts answers an observe-only route would have blocked.`;
}
