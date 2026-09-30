import { outcomeOf } from '@/domain/presentation';
import type { TraceSummary } from '@/types/api';

/**
 * One card per distinct example (same question and answer), in the order the tape first shows
 * them, each opening its newest finished run. A run the gateway is still checking is left out: it
 * has no answer yet, so it would show up as an extra, empty example while the tape plays.
 */
export function distinctExamples(traces: readonly TraceSummary[]): TraceSummary[] {
  const firstSeen = new Map<string, number>();
  const newest = new Map<string, TraceSummary>();
  for (const t of traces) {
    if (outcomeOf(t) === 'checking') continue;
    const key = `${t.question_preview ?? ''}\u0000${t.response_preview ?? ''}`;
    const at = Date.parse(t.created_at);
    firstSeen.set(key, Math.min(firstSeen.get(key) ?? Number.POSITIVE_INFINITY, at));
    const prev = newest.get(key);
    if (!prev || at > Date.parse(prev.created_at)) newest.set(key, t);
  }
  return [...newest.entries()].sort(([a], [b]) => (firstSeen.get(a) ?? 0) - (firstSeen.get(b) ?? 0)).map(([, t]) => t);
}
