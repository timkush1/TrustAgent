/**
 * The Overview hero chart, "Requests by outcome" (SPEC §3.3), beyond what the shared chart draws.
 */
import type { OverviewStats } from '@/types/api';
import { bucketDescription } from './overviewModel';

/**
 * The hero chart's description: its bucket and window, or "over the replayed traffic" for the
 * static build. Would flag and Would block are series of their own, so the legend says it all.
 */
export function heroDescription(stats: Pick<OverviewStats, 'bucket' | 'window'>, replay: boolean): string {
  return replay ? `${bucketDescription(stats).split(',')[0]}, over the replayed traffic` : bucketDescription(stats);
}
