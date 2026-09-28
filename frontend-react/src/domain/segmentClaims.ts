/**
 * Inline claim marks for the answer text (SPEC §3.3 "Trace detail" 2, left; DESIGN.md §3).
 *
 * Claim spans are UTF-16 code units, half-open (SPEC §2.1), which are JavaScript string indices,
 * so they slice the answer directly. The answer is cut at every span edge; each run records the
 * claims covering it and is drawn with the most severe of their verdicts. Claims without a usable
 * span (the judge paraphrased them, or the span does not fit this text) are returned separately
 * and listed as "not located in text". NOT_CHECKED claims are never marked (DESIGN.md §3), so they
 * appear in neither list; the Claims section still shows them.
 */
import type { ClaimVerdict, ClaimView } from '@/types/api';
import { CLAIM_VERDICT_ORDER, claimVerdictView } from './presentation';

export interface AnswerSegment {
  /** UTF-16 offsets into the answer, half-open. */
  start: number;
  end: number;
  text: string;
  /** The claims covering this run, most severe first; empty for plain text. */
  claims: ClaimView[];
  /** The verdict the run is drawn with (its most severe claim); null for plain text. */
  verdict: ClaimVerdict | null;
  /** A covering claim was located approximately (span method `fuzzy`). */
  fuzzy: boolean;
}

export interface SegmentedAnswer {
  segments: AnswerSegment[];
  /** Markable claims that could not be placed in this text, in claim order. */
  unlocated: ClaimView[];
}

const severity = (v: ClaimVerdict) => {
  const i = CLAIM_VERDICT_ORDER.indexOf(v);
  return i < 0 ? CLAIM_VERDICT_ORDER.length : i;
};

const isHigh = (c: number) => c >= 0xd800 && c <= 0xdbff;
const isLow = (c: number) => c >= 0xdc00 && c <= 0xdfff;

/** Moves an offset that falls inside a surrogate pair to the pair's start (toward) or end. */
function snap(text: string, offset: number, toward: 'start' | 'end'): number {
  if (offset <= 0 || offset >= text.length) return offset;
  if (isLow(text.charCodeAt(offset)) && isHigh(text.charCodeAt(offset - 1))) {
    return toward === 'start' ? offset - 1 : offset + 1;
  }
  return offset;
}

/** The span of `claim` inside `text`, or null when it has none that fits. */
function usableSpan(text: string, claim: ClaimView): { start: number; end: number } | null {
  const s = claim.span;
  if (!s || !Number.isInteger(s.start) || !Number.isInteger(s.end)) return null;
  if (s.start < 0 || s.end > text.length || s.start >= s.end) return null;
  return { start: snap(text, s.start, 'start'), end: snap(text, s.end, 'end') };
}

/**
 * Splits `text` into plain and marked runs for `claims`. Pass `located: false` when the spans do
 * not refer to this text (for example, only a preview of the answer is known): every markable
 * claim is then returned as unlocated and the text stays plain.
 */
export function segmentClaims(
  text: string,
  claims: readonly ClaimView[],
  { located = true }: { located?: boolean } = {},
): SegmentedAnswer {
  const ordered = [...claims].sort((a, b) => a.index - b.index);
  const placed: { claim: ClaimView; start: number; end: number }[] = [];
  const unlocated: ClaimView[] = [];
  for (const claim of ordered) {
    if (claimVerdictView(claim.status).mark === null) continue;
    const span = located ? usableSpan(text, claim) : null;
    if (span) placed.push({ claim, ...span });
    else unlocated.push(claim);
  }

  const cuts = new Set<number>([0, text.length]);
  for (const p of placed) cuts.add(p.start).add(p.end);
  const edges = [...cuts].sort((a, b) => a - b);

  const segments: AnswerSegment[] = [];
  for (let i = 0; i + 1 < edges.length; i++) {
    const start = edges[i];
    const end = edges[i + 1];
    const covering = placed
      .filter((p) => p.start <= start && end <= p.end)
      .sort((a, b) => severity(a.claim.status) - severity(b.claim.status) || a.claim.index - b.claim.index);
    const covered = covering.map((p) => p.claim);
    segments.push({
      start,
      end,
      text: text.slice(start, end),
      claims: covered,
      verdict: covered[0]?.status ?? null,
      fuzzy: covered.some((c) => c.span?.method === 'fuzzy'),
    });
  }
  return { segments, unlocated };
}
