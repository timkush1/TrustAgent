/**
 * Words, icons and tones for the P4 evaluators server values (label values, calibration origins and quality, mismatch reasons).
 * Owned by WEB-EV. presentation.ts re-exports this module with `export *`, so every export here
 * starts with `eval` (P4-PLAN D86) and can never collide with another plane's words.
 *
 * Contract text comes from contracts/ui (`copy.labels`, `copy.trace`, `copy.evaluators`). The few
 * sentences below that are not in copy.v1.json yet are listed in the WEB-EV checkpoint report for
 * the lead to add (D86 rule 5); until then they live here, never in a component.
 *
 * This module imports only `../contracts` (not presentation.ts), because presentation.ts
 * re-exports it and a cycle would read `copy` before it is initialised.
 */
import { BadgeCheck, CircleDashed, FlaskConical, Hourglass, Users, type LucideIcon } from 'lucide-react';
import type {
  CalibrationImportResult,
  CalibrationRecordMeta,
  DecisionRequestView,
  LabelValue,
} from '@/types/api';
import { copy, outcomesContract } from '../contracts';

// ------------------------------------------------------------------------------------- labels

/** The two answers to "Was this right?" for an evaluator target (D59): Correct fires, False alarm does not. */
export type EvalFireLabel = Extract<LabelValue, 'should_fire' | 'should_not_fire'>;

const LABEL_WORDS: Record<string, string> = {
  should_fire: copy.labels.correct,
  should_not_fire: copy.labels.false_alarm,
  hallucinated: 'Hallucinated',
  grounded: 'Grounded',
  SUPPORTED: 'Supported',
  CONTRADICTED: 'Contradicted',
  UNSUPPORTED: 'Unsupported',
  PARTIALLY_SUPPORTED: 'Partly supported',
  UNKNOWN: 'Unknown',
  NOT_CHECKED: 'Not checked',
};

/** "Correct", "False alarm", a claim verdict in words, … (never the raw value). */
export function evalLabelWord(label: string): string {
  return LABEL_WORDS[label] ?? 'Other label';
}

/** The label queue's keys (D59): C = should_fire, F = should_not_fire, S = skip. */
export const EVAL_QUEUE_KEYS = { c: 'should_fire', f: 'should_not_fire', s: 'skip' } as const;

/** "Saved. Thanks — 157 more and this check can be calibrated." (or a plain thanks once there are enough). */
export function evalSavedText(labelsStillNeeded: number | null): string {
  if (labelsStillNeeded === null || labelsStillNeeded <= 0) return 'Saved. Thanks — this check has enough labels to calibrate.';
  return copy.labels.saved.replace('{n}', labelsStillNeeded.toLocaleString('en-US'));
}

/** The most labels any one requirement still needs (200 in all, 30 correct, 30 false alarms). */
export function evalLabelsStillNeeded(p: { human_labels: { n: number; pos: number; neg: number }; needed: { n: number; pos: number; neg: number } }): number {
  return Math.max(0, p.needed.n - p.human_labels.n, p.needed.pos - p.human_labels.pos, p.needed.neg - p.human_labels.neg);
}

// -------------------------------------------------------------------------------- calibration

/** Where a calibration record came from (copy.evaluators.calibration_origin, D86). */
const ORIGINS: Record<CalibrationRecordMeta['origin'], string> = copy.evaluators.calibration_origin;

export function evalCalibrationOriginLabel(origin: CalibrationRecordMeta['origin']): string {
  return ORIGINS[origin] ?? 'Other source';
}

export interface EvalQualityView {
  label: string;
  /** What the quality means, in one sentence. */
  description: string;
  icon: LucideIcon;
  /** Satisfies `require_calibration` (SPEC §2.11): only a measured calibration lets a route block. */
  canBlock: boolean;
}

const QUALITY: Record<CalibrationRecordMeta['quality'], EvalQualityView> = {
  measured: {
    label: 'Measured',
    description: 'Fitted on at least 200 human or public-dataset labels (30 of each kind): good enough to block on.',
    icon: BadgeCheck,
    canBlock: true,
  },
  provisional: {
    label: 'Provisional',
    description: 'Fitted on fewer labels than a measured calibration needs: it can flag, not block.',
    icon: Hourglass,
    canBlock: false,
  },
  proxy: {
    label: 'Proxy',
    description: 'Uses deep-audit (LLM judge) labels, a weaker signal than your own: it can flag, not block.',
    icon: Users,
    canBlock: false,
  },
  mock: {
    label: 'Simulated',
    description: 'Mock thresholds for the demo and test stacks; never used with a real fast check.',
    icon: FlaskConical,
    canBlock: false,
  },
};

export function evalQualityView(quality: CalibrationRecordMeta['quality']): EvalQualityView {
  return QUALITY[quality] ?? { label: 'Unknown quality', description: 'The server reported a quality this dashboard does not know.', icon: CircleDashed, canBlock: false };
}

/** The fit dialog's honesty note (SPEC §2.11 quality rules). */
export const EVAL_FIT_HONESTY = [
  'A calibration maps the fast check’s raw score to how often your own labels agreed: it measures agreement with labels, not ground truth.',
  'Only your own labels (or a public dataset) can make it Measured: at least 200 labels, with 30 Correct and 30 False alarms. Deep-audit labels make it a Proxy, which can flag but never block.',
  'Fitting is free and changes nothing on its own: answers keep their current thresholds until you adopt it.',
] as const;

/** Why the server has no blocking threshold to recommend (its own sentence, else the SPEC §2.11 rule). */
export function evalCriticalNullText(reason: string | null): string {
  return reason ?? 'There is not enough evidence yet that blocks would be right at least 95% of the time.';
}

/** Why an import skipped an entry (copy.evaluators.calibration_import_rejected, D86). */
const IMPORT_REJECTIONS: Record<CalibrationImportResult['rejected'][number]['reason'], string> = copy.evaluators.calibration_import_rejected;

export function evalImportRejectionText(reason: CalibrationImportResult['rejected'][number]['reason']): string {
  return IMPORT_REJECTIONS[reason] ?? 'Skipped: the gateway refused this entry.';
}

/**
 * EvaluatorResult.calibration_warning in words (D56): the server sends one sentence today
 * ("calibrated with different state fields"); the dashboard says it with the contract's words and
 * never shows the server text. Null when there is no warning.
 */
export function evalCalibrationWarningText(warning: string | null | undefined): string | null {
  if (!warning) return null;
  const words = outcomesContract.status_reasons.calibration_warning ?? 'its calibration was fitted under different conditions (it still applies)';
  return `Calibration note: ${words}.`;
}

/**
 * The fit's 422 "not enough labels" answer (#45) in words, from its details
 * ({n, pos, neg, needed: {n, per_class}}); null for any other failure.
 */
export function evalNotEnoughLabelsText(details: unknown): string | null {
  if (!details || typeof details !== 'object') return null;
  const d = details as { n?: unknown; pos?: unknown; neg?: unknown; needed?: { n?: unknown; per_class?: unknown } | null };
  const nums = [d.n, d.pos, d.neg, d.needed?.n, d.needed?.per_class];
  if (!nums.every((v) => typeof v === 'number' && Number.isFinite(v))) return null;
  const [n, pos, neg, needN, perClass] = nums as number[];
  const fmt = (v: number) => v.toLocaleString('en-US');
  return (
    `Not enough labels to fit yet: ${fmt(n)} so far (${fmt(pos)} ${copy.labels.correct.toLowerCase()}, ${fmt(neg)} ${neg === 1 ? 'false alarm' : 'false alarms'}). ` +
    `A fit needs at least ${fmt(needN)} labels with ${fmt(perClass)} of each kind. Label more answers in the label queue, then try again.`
  );
}

/** "groundedness@1" from a target key "groundedness@1#<key>#<params>" (SPEC §2.11). */
export function evalTargetRef(targetKey: string): string {
  return targetKey.split('#')[0];
}

/** "groundedness" from "groundedness@1" or a target key. */
export function evalRefId(ref: string): string {
  const base = evalTargetRef(ref);
  const at = base.lastIndexOf('@');
  return at === -1 ? base : base.slice(0, at);
}

/** 1 from "groundedness@1", or null. */
export function evalRefVersion(ref: string): number | null {
  const base = evalTargetRef(ref);
  const at = base.lastIndexOf('@');
  const v = at === -1 ? Number.NaN : Number(base.slice(at + 1));
  return Number.isInteger(v) ? v : null;
}

// -------------------------------------------------------------------------- glass box (#12)

const MISMATCH: Record<NonNullable<DecisionRequestView['mismatch_reason']>, string> = {
  body_truncated: 'The stored text was cut to the storage limit, so the recompile sees less than the fast check did.',
  policy_missing: 'The policy version this request ran under is no longer stored, so the recompile uses another one.',
  evaluator_missing: 'An evaluator version this request used is no longer in the library.',
};

/** Why a recompile differs from what was sent, in words. */
export function evalMismatchText(reason: DecisionRequestView['mismatch_reason']): string {
  return reason ? MISMATCH[reason] ?? 'The recompile differs for a reason this dashboard does not know.' : 'The recompile differs from the request that was sent.';
}

/** "Matches a recompile of this request" / "Differs from a recompile of this request". */
export function evalRecompileText(matches: boolean): string {
  return matches ? copy.trace.recompile_match : copy.trace.recompile_mismatch;
}
