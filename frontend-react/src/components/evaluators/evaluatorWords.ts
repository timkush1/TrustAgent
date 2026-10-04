/**
 * Words for the Evaluators pages (SPEC §3.3 Evaluators, §2.8): calibration progress in the
 * contract's phrasing, question types and evidence needs, and when Calibrate now is possible.
 */
import type { CustomReason } from '@/components/common/DisabledReason';
import { calibrationStateView } from '@/domain/controlPresentation';
import { formatCount } from '@/domain/format';
import { copy, fillTemplate } from '@/domain/presentation';
import { MIN_FIT_LABELS } from '@/lib/decision/calibrate';
import type { CalibrationProgress, EvaluatorSpec } from '@/types/api';

/** "Labels 43/200 (31 with the problem, 12 fine)". */
export function progressText(p: CalibrationProgress): string {
  return fillTemplate(copy.evaluators.calibration_progress, {
    n: formatCount(p.human_labels.n),
    needed: formatCount(p.needed.n),
    correct: formatCount(p.human_labels.pos),
    false_alarms: formatCount(p.human_labels.neg),
  });
}

export interface ProgressParts {
  /** Share of each requirement met, in [0, 1]. */
  total: number;
  correct: number;
  falseAlarms: number;
  /** All three requirements met: a measured calibration can be fitted. */
  complete: boolean;
  /** What blocking still needs, in words ("Blocking needs 157 more of your own labels, including at least 18 that are fine."). */
  missing: string | null;
  /** The calibration state in words. */
  stateLabel: string;
}

const clamp = (x: number) => Math.min(1, Math.max(0, x));

export function progressParts(p: CalibrationProgress): ProgressParts {
  const more = (have: number, need: number) => Math.max(0, need - have);
  const pos = more(p.human_labels.pos, p.needed.pos);
  const neg = more(p.human_labels.neg, p.needed.neg);
  // Every new label is one of the two kinds, so the labels still needed are at least both kinds'
  // shortfalls together: "200 more …, including at least 30 that have the problem and 30 that are fine".
  const labels = Math.max(more(p.human_labels.n, p.needed.n), pos + neg);
  const kinds = [pos > 0 ? `${formatCount(pos)} that ${pos === 1 ? 'has' : 'have'} the problem` : null, neg > 0 ? `${formatCount(neg)} that ${neg === 1 ? 'is' : 'are'} fine` : null].filter(
    (x): x is string => x !== null,
  );
  // Only your own labels count toward blocking; a first fit also takes the deep-audit labels.
  const missing =
    labels === 0
      ? null
      : `Blocking needs ${formatCount(labels)} more of your own ${labels === 1 ? 'label' : 'labels'}${kinds.length > 0 ? `, including at least ${kinds.join(' and ')}` : ''}.`;
  return {
    total: clamp(p.human_labels.n / p.needed.n),
    correct: clamp(p.human_labels.pos / p.needed.pos),
    falseAlarms: clamp(p.human_labels.neg / p.needed.neg),
    complete: missing === null,
    missing,
    stateLabel: calibrationStateView(p.state).label,
  };
}

const QUESTION_TYPES: Record<EvaluatorSpec['question']['type'], string> = {
  noul: 'Yes / no',
  choice: 'Pick one',
  score: 'Scale',
};

export function questionTypeLabel(type: EvaluatorSpec['question']['type']): string {
  return QUESTION_TYPES[type] ?? type;
}

export function evidenceNeedLabel(requires: string | undefined): string {
  switch (requires) {
    case 'required':
      return 'Needs your documents (not checked without them)';
    case 'optional':
      return 'Uses your documents when there are some';
    default:
      return 'Does not use documents';
  }
}

/** Your own labels alone are enough for a first fit (the server's count, #45's minimum). */
export function canFitOnOwnLabels(p: CalibrationProgress): boolean {
  return p.can_fit_provisional || p.can_fit_measured;
}

/**
 * Why "Calibrate now" is not available yet, or null. The server says when your own labels allow a
 * first fit; a fit may also add the deep-audit labels (a Proxy calibration), so it is offered once
 * both together reach the fit's minimum. Their split between kinds is the server's to check: too
 * few of one kind answers 422, which the dialog explains with the counts.
 */
export function calibrateReason(p: CalibrationProgress): CustomReason | null {
  if (p.snapshot === null) return { text: 'No fast-check version has checked traffic for this check yet, so there is nothing to calibrate.' };
  const more = labelsForFirstFit(p);
  if (more > 0) {
    return {
      text: `Calibrate now needs ${MIN_FIT_LABELS} labels, and your deep audits count too: label ${more} more ${more === 1 ? 'answer' : 'answers'} in the queue.`,
    };
  }
  return null;
}

/** How many more labels a first fit needs (0 when one is possible). */
export function labelsForFirstFit(p: CalibrationProgress): number {
  if (canFitOnOwnLabels(p)) return 0;
  return Math.max(0, MIN_FIT_LABELS - (p.human_labels.n + p.proxy_labels));
}
