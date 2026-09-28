/**
 * normalize(answer, question) and the risk signal (SPEC §2.9). Port of
 * backend-go/internal/decision/normalize.go; pinned by contracts/vectors/normalize.
 */
import type { RawAnswer } from '@/lib/contracts/types.gen';
import { clamp01, EPS } from './floats';
import { specOptions, type Spec } from './spec';
import { has, own, record, sortedUTF16 } from './text';

export type NormalizeError = 'missing_answer' | 'unexpected_option' | 'invalid_distribution' | 'probabilities_missing';
export type ConfidenceSource = 'vendor' | 'fallback_top_prob' | 'choice_only';

/** The result of normalize(). With an error, every other field is empty. */
export interface Normalized {
  error: NormalizeError | null;
  /** Renormalized option (choice) or level (score) probabilities, every option present; null for noul. */
  probabilities: Record<string, number> | null;
  /** Risk in [0,1] before calibration (high = risky). */
  riskRaw: number;
  /** Most likely option or level; null for noul. */
  top: string | null;
  /** The answer's own confidence, for display only. */
  vendorConfidence: number | null;
  /** Where the displayed confidence comes from; null for noul without a vendor confidence. */
  confidenceSource: ConfidenceSource | null;
  /** Non-fatal observations (sum_out_of_range), for metrics. */
  anomalies: 'sum_out_of_range'[];
}

/** A raw answer as it may arrive in JSON: absent and null members mean the same. */
export type RawAnswerInput = { [K in keyof RawAnswer]?: RawAnswer[K] | null };

const failed = (error: NormalizeError): Normalized => ({
  error,
  probabilities: null,
  riskRaw: 0,
  top: null,
  vendorConfidence: null,
  confidenceSource: null,
  anomalies: [],
});

/** A finite number, or null (Go decodes JSON null into a nil pointer). */
function optNumber(v: unknown): number | null {
  return typeof v === 'number' ? v : null;
}

/**
 * Turns a raw answer into a risk signal. A null answer is missing_answer. The fractional score and
 * the answer's type field are never used.
 */
export function normalize(answer: RawAnswerInput | null | undefined, spec: Spec): Normalized {
  if (answer === null || answer === undefined) return failed('missing_answer');
  switch (spec.question.type) {
    case 'noul':
      return normalizeNoul(answer, spec);
    case 'choice':
      return normalizeChoice(answer, spec);
    case 'score':
      return normalizeScore(answer, spec);
  }
  return failed('missing_answer');
}

function normalizeNoul(a: RawAnswerInput, s: Spec): Normalized {
  const noul = optNumber(a.noul);
  if (noul === null || Number.isNaN(noul)) return failed('missing_answer');
  const p = clamp01(noul);
  const confidence = optNumber(a.confidence);
  return {
    error: null,
    probabilities: null,
    riskRaw: s.signal.invert ? 1 - p : p,
    top: null,
    vendorConfidence: confidence,
    confidenceSource: confidence !== null ? 'vendor' : null,
    anomalies: [],
  };
}

/** The probabilities member as a map; a null member value decodes as 0, as in Go. */
function probabilityMap(v: unknown): Map<string, number> | null {
  if (v === null || v === undefined || typeof v !== 'object' || Array.isArray(v)) return null;
  const m = new Map<string, number>();
  for (const [k, p] of Object.entries(v as Record<string, unknown>)) m.set(k, p === null ? 0 : typeof p === 'number' ? p : NaN);
  return m;
}

function normalizeChoice(a: RawAnswerInput, s: Spec): Normalized {
  const options = specOptions(s);
  let probs = probabilityMap(a.probabilities);
  let choiceOnly = false;
  if (probs === null) {
    const choice = typeof a.choice === 'string' ? a.choice : '';
    if (choice === '') return failed('missing_answer');
    // A choice-only answer; a choice outside the options fails as unexpected_option below.
    probs = new Map([[choice, 1]]);
    choiceOnly = true;
  }
  const n = distribution(probs, options, a);
  if (n.error !== null) return n;
  if (choiceOnly) n.confidenceSource = 'choice_only';
  n.riskRaw = weightedRisk(n.probabilities ?? {}, s.signal.riskWeights);
  return n;
}

function normalizeScore(a: RawAnswerInput, s: Spec): Normalized {
  const levels = specOptions(s);
  const probs = probabilityMap(a.probabilities);
  if (probs === null || !levels.some((l) => probs.has(l))) return failed('probabilities_missing');
  const n = distribution(probs, levels, a);
  if (n.error !== null) return n;
  let risk = 0;
  for (const l of [...new Set(s.signal.riskLevels ?? [])].sort((x, y) => x - y)) risk += own(n.probabilities, String(l)) ?? 0;
  n.riskRaw = clamp01(risk);
  return n;
}

/**
 * The shared choice/score rules: foreign keys, missing options, clipping, the sum check,
 * renormalization and the top option.
 */
function distribution(probs: Map<string, number>, options: string[], a: RawAnswerInput): Normalized {
  const isOption = new Set(options);
  for (const p of probs.values()) if (!Number.isFinite(p)) return failed('invalid_distribution');
  for (const [k, p] of probs) if (!isOption.has(k) && p > 0.01 + EPS) return failed('unexpected_option');
  const clipped = new Map<string, number>();
  let sum = 0;
  for (const o of options) {
    // options are in sorted key order
    const p = Math.max(0, probs.get(o) ?? 0);
    clipped.set(o, p);
    sum += p;
  }
  const tol = Math.max(0.1, 0.005 * options.length);
  if (sum === 0 || Math.abs(sum - 1) > tol + EPS) return failed('invalid_distribution');
  const out = record(options.map((o) => [o, (clipped.get(o) ?? 0) / sum] as const));
  const confidence = optNumber(a.confidence);
  return {
    error: null,
    probabilities: out,
    riskRaw: 0,
    top: topOption(out, options, typeof a.choice === 'string' ? a.choice : ''),
    vendorConfidence: confidence,
    confidenceSource: confidence !== null ? 'vendor' : 'fallback_top_prob',
    anomalies: Math.abs(sum - 1) > 0.02 + EPS ? ['sum_out_of_range'] : [],
  };
}

/**
 * The argmax; on a tie (within EPS) the vendor's choice wins if it is tied, otherwise the smallest
 * key (options are sorted, so the first tied one).
 */
function topOption(probs: Record<string, number>, options: string[], vendorChoice: string): string | null {
  let best = -Infinity;
  for (const o of options) best = Math.max(best, probs[o]);
  let first: string | null = null;
  for (const o of options) {
    if (probs[o] >= best - EPS) {
      if (o === vendorChoice) return o;
      first ??= o;
    }
  }
  return first;
}

/** clamp(sum over sorted weight keys of w_o * P(o)). */
function weightedRisk(probs: Record<string, number>, weights: ReadonlyMap<string, number> | null): number {
  if (weights === null) return 0;
  let risk = 0;
  for (const k of sortedUTF16(weights.keys())) risk += (weights.get(k) ?? 0) * (has(probs, k) ? probs[k] : 0);
  return clamp01(risk);
}
