/**
 * Calibration progress for one evaluator (SPEC §3.3 Evaluators): "Labels 43/200 (31 correct,
 * 12 false alarms)" with a meter per requirement (200 labels, 30 of each kind), the calibration
 * state, what is still missing, and the actions: Label queue (this check's answers) and Calibrate
 * now (the fit dialog). Each says why when it is not available.
 */
import { Link } from '@tanstack/react-router';
import { useId } from 'react';
import { DisabledAction, DisabledReason } from '@/components/common/DisabledReason';
import { Term } from '@/components/common/Term';
import { useWriteReason } from '@/components/common/useWriteAccess';
import { Button } from '@/components/ui/button';
import { buttonVariants } from '@/components/ui/button-variants';
import { formatCount } from '@/domain/format';
import { copy } from '@/domain/presentation';
import { cn } from '@/lib/cn';
import type { CalibrationProgress, EvaluatorDetail } from '@/types/api';
import { CalibrateDialog } from './CalibrateDialog';
import { calibrateReason, progressParts, progressText } from './evaluatorWords';

function Bar({ label, value, have, need }: { label: string; value: number; have: number; need: number }) {
  return (
    <div className="space-y-0.5">
      <div className="flex justify-between gap-2 text-2xs text-fg-muted">
        <span>{label}</span>
        <span className="tabular">
          {formatCount(have)}/{formatCount(need)}
        </span>
      </div>
      <div
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={need}
        aria-valuenow={Math.min(have, need)}
        aria-valuetext={`${formatCount(have)} of ${formatCount(need)}`}
        className="h-1.5 overflow-hidden rounded-full bg-surface-sunken ring-1 ring-border ring-inset"
      >
        <div className="h-full rounded-full bg-fg-muted" style={{ width: `${value * 100}%` }} />
      </div>
    </div>
  );
}

function Actions({ progress, detail }: { progress: CalibrationProgress; detail: EvaluatorDetail }) {
  const writeReason = useWriteReason('admin');
  const reasonId = useId();
  if (writeReason) {
    // Both need an admin on a writable instance: one reason, referenced by both buttons.
    return (
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" disabled aria-describedby={reasonId}>
          {copy.evaluators.label_queue}
        </Button>
        <Button size="sm" disabled aria-describedby={reasonId}>
          {copy.evaluators.calibrate_now}
        </Button>
        <DisabledReason id={reasonId} reason={writeReason} />
      </div>
    );
  }
  return (
    <div className="flex flex-wrap items-start gap-2">
      <Link to="/app/evaluators/labels" search={{ evaluator: progress.evaluator_ref }} className={buttonVariants({ size: 'sm' })}>
        {copy.evaluators.label_queue}{' '}
        <span className="sr-only">for {detail.name}</span>
      </Link>
      <DisabledAction reason={calibrateReason(progress)} layout="stacked">
        {(p) =>
          p.disabled ? (
            <Button size="sm" {...p}>
              {copy.evaluators.calibrate_now}
            </Button>
          ) : (
            <CalibrateDialog
              detail={detail}
              trigger={
                <Button size="sm">
                  {copy.evaluators.calibrate_now}{' '}
                  <span className="sr-only">for {detail.name}</span>
                </Button>
              }
            />
          )
        }
      </DisabledAction>
    </div>
  );
}

export function CalibrationMeter({
  progress,
  detail,
  showActions = true,
  className,
}: {
  progress: CalibrationProgress;
  /** The evaluator, for the actions (Label queue, Calibrate now). Without it there are none. */
  detail?: EvaluatorDetail;
  showActions?: boolean;
  className?: string;
}) {
  const parts = progressParts(progress);
  return (
    <div className={cn('space-y-2', className)}>
      <p className="flex flex-wrap items-baseline justify-between gap-x-2 text-sm">
        <span className="text-fg tabular">{progressText(progress)}</span>
        <span className="text-xs text-fg-muted">
          <Term id="calibration">{parts.stateLabel}</Term>
        </span>
      </p>
      <div className="grid grid-cols-3 gap-3">
        <Bar label="Labels" value={parts.total} have={progress.human_labels.n} need={progress.needed.n} />
        <Bar label="Correct" value={parts.correct} have={progress.human_labels.pos} need={progress.needed.pos} />
        <Bar label="False alarms" value={parts.falseAlarms} have={progress.human_labels.neg} need={progress.needed.neg} />
      </div>
      {parts.missing ? <p className="text-xs text-fg-muted">{parts.missing}</p> : <p className="text-xs text-fg-muted">Enough labels to calibrate for blocking.</p>}
      {showActions && detail ? <Actions progress={progress} detail={detail} /> : null}
    </div>
  );
}
