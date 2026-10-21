/**
 * Words, icons and tones for the evaluators server values (label values, calibration origins and quality, mismatch reasons).
 * Owned by WEB-EV. presentation.ts re-exports this module with `export *`, so every export here
 * starts with `eval` (D86) and can never collide with another plane's words.
 *
 * Every sentence comes from contracts/ui (`copy.labels`, `copy.trace`, `copy.evaluators`; D86,
 * CO-6); src/test/lint/wordsFromCopy.test.ts keeps it that way.
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

/** `{name}` placeholders filled in order (presentation.ts fillTemplate would be an import cycle). */
function fill(template: string, vars: Record<string, string>): string {
  return template.replace(/\{([a-z_]+)\}/g, (m, name: string) => vars[name] ?? m);
}

// ------------------------------------------------------------------------------------- labels

/**
 * The two labels an evaluator target takes (D59): `should_fire` (the answer has the problem the
 * check looks for) and `should_not_fire` (it is fine). "Was this right?" asks them as Correct and
 * False alarm, the label queue as Yes and No.
 */
export type EvalFireLabel = Extract<LabelValue, 'should_fire' | 'should_not_fire'>;

/** Every label PUT /api/labels accepts (D59; claim labels are only the four verdicts the gateway takes). */
const LABEL_WORDS: Record<string, string> = {
  should_fire: copy.labels.correct,
  should_not_fire: copy.labels.false_alarm,
  hallucinated: 'Hallucinated',
  grounded: 'Grounded',
  SUPPORTED: 'Supported',
  CONTRADICTED: 'Contradicted',
  UNSUPPORTED: 'Unsupported',
  PARTIALLY_SUPPORTED: 'Partly supported',
};

/** "Correct", "False alarm", a claim verdict in words, … (never the raw value). For a flag's label. */
export function evalLabelWord(label: string): string {
  return LABEL_WORDS[label] ?? 'Other label';
}

/**
 * Who gave a label (LabelView.labeled_by, D59), in words. A dashboard session labels as its
 * subject: `env:<key label>#<fingerprint>` for a key from TRUTHTABLE_ADMIN_KEYS, `key:<id>` for a
 * key minted in the dashboard. A request signed with a key header labels as that key's label, and
 * an instance without keys as `public`. The fingerprint and the id are never shown.
 */
export function evalLabelerName(labeledBy: string): string {
  const env = /^env:(.+)#[0-9a-f]+$/.exec(labeledBy);
  if (env) return env[1];
  if (labeledBy.startsWith('key:')) return copy.evaluators.labeler.api_key;
  if (labeledBy === 'public') return copy.evaluators.labeler.public;
  return labeledBy;
}

/**
 * The label queue's answer to "Should <check> flag this answer?" in words: Yes (`should_fire`) or No
 * (`should_not_fire`). The queue also holds answers the check did not flag, so it never says
 * Correct or False alarm.
 */
export function evalQueueAnswer(label: EvalFireLabel): string {
  return label === 'should_fire' ? copy.labels.yes : copy.labels.no;
}

/** The label queue's keys (D59): C = should_fire, F = should_not_fire, S = skip. */
export const EVAL_QUEUE_KEYS = { c: 'should_fire', f: 'should_not_fire', s: 'skip' } as const;

/** "Saved. Thanks — 157 more and this check can be calibrated." (or a plain thanks once there are enough). */
export function evalSavedText(labelsStillNeeded: number | null): string {
  if (labelsStillNeeded === null || labelsStillNeeded <= 0) return copy.evaluators.saved_enough;
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

const QUALITY_WORDS = copy.evaluators.quality;
const QUALITY: Record<CalibrationRecordMeta['quality'], EvalQualityView> = {
  measured: { ...QUALITY_WORDS.measured, icon: BadgeCheck, canBlock: true },
  provisional: { ...QUALITY_WORDS.provisional, icon: Hourglass, canBlock: false },
  proxy: { ...QUALITY_WORDS.proxy, icon: Users, canBlock: false },
  mock: { ...QUALITY_WORDS.mock, icon: FlaskConical, canBlock: false },
};

export function evalQualityView(quality: CalibrationRecordMeta['quality']): EvalQualityView {
  return QUALITY[quality] ?? { ...QUALITY_WORDS.unknown, icon: CircleDashed, canBlock: false };
}

/** The fit dialog's honesty note (SPEC §2.11 quality rules). */
export const EVAL_FIT_HONESTY: readonly string[] = copy.evaluators.fit_honesty;

/**
 * Why a fit recommends no blocking threshold (SPEC §2.11: no threshold whose 95% lower bound on
 * precision reaches the target). The server's `critical_null_reason` is a fixed technical phrase
 * ("insufficient evidence to block"), so the dashboard says it in its own words and never shows it.
 */
export const EVAL_CRITICAL_NULL_TEXT: string = copy.evaluators.critical_null;

/** Why an import skipped an entry (copy.evaluators.calibration_import_rejected, D86). */
const IMPORT_REJECTIONS: Record<CalibrationImportResult['rejected'][number]['reason'], string> = copy.evaluators.calibration_import_rejected;

export function evalImportRejectionText(reason: CalibrationImportResult['rejected'][number]['reason']): string {
  return IMPORT_REJECTIONS[reason] ?? copy.evaluators.import_rejected_other;
}

/**
 * EvaluatorResult.calibration_warning in words (D56): the server sends one sentence today
 * ("calibrated with different state fields"); the dashboard says it with the contract's words and
 * never shows the server text. Null when there is no warning.
 */
export function evalCalibrationWarningText(warning: string | null | undefined): string | null {
  if (!warning) return null;
  const words = outcomesContract.status_reasons.calibration_warning ?? copy.evaluators.calibration_warning_fallback;
  return fill(copy.evaluators.calibration_note, { words });
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
  return fill(copy.evaluators.not_enough_labels, { n: fmt(n), pos: fmt(pos), neg: fmt(neg), needed_n: fmt(needN), per_class: fmt(perClass) });
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

const MISMATCH: Record<NonNullable<DecisionRequestView['mismatch_reason']>, string> = copy.evaluators.mismatch;

/** Why a recompile differs from what was sent, in words. */
export function evalMismatchText(reason: DecisionRequestView['mismatch_reason']): string {
  return reason ? (Object.hasOwn(MISMATCH, reason) ? MISMATCH[reason] : copy.evaluators.mismatch.unknown) : copy.evaluators.mismatch.differs;
}

/** "Rebuilt from the stored request: identical" / "…: different". */
export function evalRecompileText(matches: boolean): string {
  return matches ? copy.trace.recompile_match : copy.trace.recompile_mismatch;
}

/**
 * Evaluator names by id, for showing refs in words. When two checks share a name (a Studio copy
 * of "Competitor mentions", say), each of them gets its id in brackets so they stay apart:
 * "Competitor mentions (proof_muree135)". A later entry for the same id replaces an earlier one.
 */
export function evalNameMap(items: readonly { id: string; name: string }[]): Map<string, string> {
  const byId = new Map<string, string>();
  for (const e of items) byId.set(e.id, e.name);
  const count = new Map<string, number>();
  for (const name of byId.values()) count.set(name, (count.get(name) ?? 0) + 1);
  return new Map([...byId].map(([id, name]) => [id, (count.get(name) ?? 0) > 1 ? `${name} (${id})` : name]));
}
