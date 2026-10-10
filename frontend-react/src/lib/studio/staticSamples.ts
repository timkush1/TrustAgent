/**
 * Test-bench samples from the replayed recording (D107): the static build's answer to
 * `POST /api/studio/samples`. The gateway reads stored trace bodies (cmd/proxy studio_api.go
 * `sampleOfBody`, internal/store `StudioSampleTraces`); here the bodies come from the tape:
 *
 * - a trace's original answer (attempt 0), its user message, system instructions and rules, and
 *   its trusted documents (an untrusted tool result is never evidence);
 * - `flagged`: the fast check flagged, blocked or fell back, or would have (an observe route
 *   records the action it would have taken); `disagreement`: the decision says the checks
 *   disagreed; `random`: any answer;
 * - at most STATIC_SAMPLE_LIMIT, and each distinct exchange once: the tape repeats its scenarios
 *   round after round, and six copies of one answer test nothing.
 *
 * The recording is one fixed stretch of time, so the request's window does not narrow it.
 */
import type { StudioSample, StudioSamplesRequest, TraceDetail } from '@/types/api';

/** At most this many samples come from the tape per request (D107). */
export const STATIC_SAMPLE_LIMIT = 6;

const FLAGGING = new Set(['flag', 'block', 'fallback']);

/** The trace's attempt-0 decision, if the replay shows one yet. */
function firstDecision(detail: TraceDetail): TraceDetail['decisions'][number] | undefined {
  return detail.decisions.find((d) => d.attempt === 0) ?? detail.decisions[0];
}

/** Whether a trace is one `filter` selects (Go flaggedSQL / disagreementSQL). */
export function matchesSampleFilter(detail: TraceDetail, filter: StudioSamplesRequest['filter']): boolean {
  if (filter === 'random') return true;
  const decision = firstDecision(detail);
  if (filter === 'disagreement') return decision?.disagreement === true;
  const action = decision?.action ?? detail.action;
  const would = decision?.would_action ?? detail.would_action;
  return (action !== null && FLAGGING.has(action)) || (would !== null && would !== undefined && FLAGGING.has(would));
}

/** A trace body as a test-bench sample, or null when the body has no answer (Go sampleOfBody). */
export function sampleOfTrace(detail: TraceDetail): StudioSample | null {
  const b = detail.body;
  const answer = b.response_text;
  if (!b.available || answer === null || answer.trim() === '') return null;
  const sample: StudioSample = { id: detail.trace_id, trace_id: detail.trace_id, assistant_response: answer };
  if (b.user_request) sample.user_request = b.user_request;
  if (b.system_instructions) sample.system_instructions = b.system_instructions;
  if (Object.keys(b.rules ?? {}).length > 0) sample.rules = { ...b.rules };
  const evidence = b.evidence.filter((e) => !e.untrusted && e.text.trim() !== '').map((e) => ({ id: e.id, text: e.text, source: e.source }));
  if (evidence.length > 0) sample.evidence = evidence;
  return sample;
}

/** What makes two samples the same exchange. */
export function sampleKey(s: StudioSample): string {
  return JSON.stringify([s.user_request ?? '', s.assistant_response, s.system_instructions ?? '', (s.evidence ?? []).map((e) => e.text), s.rules ?? {}]);
}

/** The order a filter reads the tape in: newest first, or shuffled for `random` (Fisher-Yates with `random`). */
export function sampleOrder(ids: readonly string[], filter: StudioSamplesRequest['filter'], random: () => number = Math.random): string[] {
  const out = [...ids].reverse();
  if (filter !== 'random') return out;
  for (let i = out.length - 1; i > 0; i -= 1) {
    const j = Math.floor(random() * (i + 1));
    [out[i], out[j]] = [out[j], out[i]];
  }
  return out;
}
