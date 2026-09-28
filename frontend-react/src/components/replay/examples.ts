import type { TraceSummary } from '@/types/api';

/** The newest recorded run of each distinct example (same question and answer). */
export function distinctExamples(traces: readonly TraceSummary[]): TraceSummary[] {
  const seen = new Map<string, TraceSummary>();
  for (const t of traces) {
    const key = `${t.question_preview ?? ''}\u0000${t.response_preview ?? ''}`;
    const prev = seen.get(key);
    if (!prev || Date.parse(t.created_at) > Date.parse(prev.created_at)) seen.set(key, t);
  }
  return [...seen.values()].sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at));
}
