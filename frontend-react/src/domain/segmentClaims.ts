/**
 * Inline marks for the answer text (SPEC §3.3 "Trace detail" 2, left; DESIGN.md §3): the deep
 * audit's claims and the instant checks' detector findings.
 *
 * Claim spans and detector findings are UTF-16 code units, half-open (SPEC §2.1, §2.10), which are
 * JavaScript string indices, so they slice the answer directly. The answer is cut at every span
 * edge; each run records the claims and the findings covering it, so the two may overlap or nest
 * freely. A run is drawn with the most severe of its claims' verdicts. Claims without a usable span
 * (the judge paraphrased them, or the span does not fit this text) are returned separately and
 * listed as "not located in text". NOT_CHECKED claims are never marked (DESIGN.md §3), so they
 * appear in neither list; the Claims section still shows them. A finding that does not fit the text
 * (or a masked secret, which has no span) is simply not marked: the outcome card lists every reason.
 */
import type { ClaimVerdict, ClaimView } from '@/types/api';
import { CLAIM_VERDICT_ORDER, claimVerdictView } from './presentation';

/** One detector finding to mark (DetectorResult.findings of a fired detector). */
export interface MarkFinding {
  /** The detector that found it (a DetectorId, e.g. `urls_not_in_evidence`). */
  detector: string;
  /** UTF-16 offsets into the answer, half-open. */
  start: number;
  end: number;
}

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
  /** The detector findings covering this run, in the order given; empty when none. */
  findings: MarkFinding[];
}

export interface SegmentedAnswer {
  segments: AnswerSegment[];
  /** Markable claims that could not be placed in this text, in claim order. */
  unlocated: ClaimView[];
}

export interface SegmentOptions {
  /** False when the claim spans do not refer to this text (only a preview is known). */
  located?: boolean;
  /** Detector findings on this same text. */
  findings?: readonly MarkFinding[];
}

/**
 * Consecutive runs covered by the same claims, so one claim mark (one link, one icon) holds every
 * piece of its text even where findings cut it. `claims` is empty for the plain stretches between.
 */
export interface ClaimRun {
  start: number;
  end: number;
  text: string;
  claims: ClaimView[];
  verdict: ClaimVerdict | null;
  fuzzy: boolean;
  /** The segments of the run, each with its own findings. */
  pieces: AnswerSegment[];
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

/** `span` snapped inside `text`, or null when it does not fit. */
function fit(text: string, span: { start: number; end: number } | null | undefined): { start: number; end: number } | null {
  if (!span || !Number.isInteger(span.start) || !Number.isInteger(span.end)) return null;
  if (span.start < 0 || span.end > text.length || span.start >= span.end) return null;
  return { start: snap(text, span.start, 'start'), end: snap(text, span.end, 'end') };
}

/**
 * Splits `text` into plain and marked runs for `claims` and detector `findings`. Pass
 * `located: false` when the claim spans do not refer to this text (for example, only a preview of
 * the answer is known): every markable claim is then returned as unlocated.
 */
export function segmentClaims(
  text: string,
  claims: readonly ClaimView[],
  { located = true, findings = [] }: SegmentOptions = {},
): SegmentedAnswer {
  const ordered = [...claims].sort((a, b) => a.index - b.index);
  const placed: { claim: ClaimView; start: number; end: number }[] = [];
  const unlocated: ClaimView[] = [];
  for (const claim of ordered) {
    if (claimVerdictView(claim.status).mark === null) continue;
    const span = located ? fit(text, claim.span) : null;
    if (span) placed.push({ claim, ...span });
    else unlocated.push(claim);
  }
  const found: MarkFinding[] = [];
  for (const f of findings) {
    const span = fit(text, f);
    if (!span || found.some((g) => g.detector === f.detector && g.start === span.start && g.end === span.end)) continue;
    found.push({ detector: f.detector, ...span });
  }

  const cuts = new Set<number>([0, text.length]);
  for (const p of placed) cuts.add(p.start).add(p.end);
  for (const f of found) cuts.add(f.start).add(f.end);
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
      findings: found.filter((f) => f.start <= start && end <= f.end),
    });
  }
  return { segments, unlocated };
}

const claimKey = (s: AnswerSegment) => s.claims.map((c) => c.index).join(',');

/** Groups consecutive segments covered by the same claims (see ClaimRun). */
export function claimRuns(segments: readonly AnswerSegment[]): ClaimRun[] {
  const runs: ClaimRun[] = [];
  for (const seg of segments) {
    const last = runs[runs.length - 1];
    if (last && claimKey(last.pieces[0]) === claimKey(seg)) {
      last.pieces.push(seg);
      last.end = seg.end;
      last.text += seg.text;
      continue;
    }
    runs.push({ start: seg.start, end: seg.end, text: seg.text, claims: seg.claims, verdict: seg.verdict, fuzzy: seg.fuzzy, pieces: [seg] });
  }
  return runs;
}
