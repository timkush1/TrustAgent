import { outcomeOf } from '@/domain/presentation';
import type { TraceSummary } from '@/types/api';

/**
 * The example cards of the static Playground: one per outcome the tape shows (passed, flagged,
 * blocked, …), so the cards are a short tour of what the gateway does rather than every request of
 * the recording (Traffic lists those). Each outcome takes the first distinct example (same question
 * and answer) the tape showed with it, cards come in the order the tape first showed their
 * example, and each opens its example's newest finished run. A run the gateway is still checking is
 * left out: it has no answer yet, so it would show up as an extra, empty example while the tape
 * plays.
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
  const shown = new Set<string>();
  return [...newest.entries()]
    .sort(([a], [b]) => (firstSeen.get(a) ?? 0) - (firstSeen.get(b) ?? 0))
    .map(([, t]) => t)
    .filter((t) => {
      const outcome = outcomeOf(t);
      if (shown.has(outcome)) return false;
      shown.add(outcome);
      return true;
    });
}
