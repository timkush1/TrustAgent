/**
 * Words for the Evaluators pages (SPEC §3.3 Evaluators, §2.8): calibration progress in the
 * contract's phrasing, question types and evidence needs, and when Calibrate now is possible.
 */
import type { CustomReason } from '@/components/common/DisabledReason';
import { calibrationStateView } from '@/domain/controlPresentation';
import { formatCount } from '@/domain/format';
import { copy, fillTemplate } from '@/domain/presentation';
import type { CalibrationProgress, EvaluatorSpec } from '@/types/api';

/** "Labels 43/200 (31 correct, 12 false alarms)". */
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
  /** What is still missing, in words ("157 more labels, 18 more false alarms"). */
  missing: string | null;
  /** The calibration state in words. */
  stateLabel: string;
}

const clamp = (x: number) => Math.min(1, Math.max(0, x));

export function progressParts(p: CalibrationProgress): ProgressParts {
  const more = (have: number, need: number) => Math.max(0, need - have);
  const parts = [
    more(p.human_labels.n, p.needed.n) > 0 ? `${formatCount(more(p.human_labels.n, p.needed.n))} more labels` : null,
    more(p.human_labels.pos, p.needed.pos) > 0 ? `${formatCount(more(p.human_labels.pos, p.needed.pos))} more correct` : null,
    more(p.human_labels.neg, p.needed.neg) > 0 ? `${formatCount(more(p.human_labels.neg, p.needed.neg))} more false alarms` : null,
  ].filter((x): x is string => x !== null);
  return {
    total: clamp(p.human_labels.n / p.needed.n),
    correct: clamp(p.human_labels.pos / p.needed.pos),
    falseAlarms: clamp(p.human_labels.neg / p.needed.neg),
    complete: parts.length === 0,
    missing: parts.length > 0 ? `Needs ${parts.join(', ')} to calibrate for blocking.` : null,
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

/** Why "Calibrate now" is not available yet, or null (the server says when a first fit is possible). */
export function calibrateReason(p: CalibrationProgress): CustomReason | null {
  if (p.snapshot === null) return { text: 'No fast-check version has checked traffic for this check yet, so there is nothing to calibrate.' };
  if (!p.can_fit_provisional && !p.can_fit_measured) return { text: 'Not enough labels for a first fit yet: label a few answers in the queue.' };
  return null;
}
