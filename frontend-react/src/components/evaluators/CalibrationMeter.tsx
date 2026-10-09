/**
 * Calibration progress for one evaluator (SPEC §3.3 Evaluators): "Labels 43/200 (31 with the
 * problem, 12 fine)" with a meter per requirement (200 labels, 30 of each kind), the calibration
 * state, what is still missing, and the actions: Label queue (this check's answers) and Calibrate
 * now (the fit dialog). Each says why when it is not available.
 */
import { Link } from '@tanstack/react-router';
import { ArrowRight } from 'lucide-react';
import { useId } from 'react';
import { DisabledAction, DisabledReason } from '@/components/common/DisabledReason';
import { Term } from '@/components/common/Term';
import { useWriteReason } from '@/components/common/useWriteAccess';
import { Button } from '@/components/ui/button';
import { buttonVariants } from '@/components/ui/button-variants';
import { formatCount } from '@/domain/format';
import { copy } from '@/domain/presentation';
import { cn } from '@/lib/cn';
import { MIN_FIT_LABELS } from '@/lib/decision/calibrate';
import type { CalibrationProgress, EvaluatorDetail } from '@/types/api';
import { CalibrateDialog } from './CalibrateDialog';
import { calibrateReason, labelsForFirstFit, progressParts, progressText } from './evaluatorWords';

/** What a first fit gives and what blocking needs, in one sentence (the two thresholds). */
const FIT_THRESHOLDS = `From ${MIN_FIT_LABELS} labels you get a provisional calibration that sharpens flagging; blocking needs 200 of your own labels.`;

function Bar({ label, value, have, need }: { label: string; value: number; have: number; need: number }) {
  return (
    <div className="space-y-0.5">
      <div className="flex justify-between gap-2 text-2xs text-fg-muted">
        <span className="truncate">{label}</span>
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

/**
 * A library card's one action: the label queue for this check. Calibrate now lives on the check's
 * Calibration tab, where its records and Adopt are; the card links there once a fit is possible.
 */
function CardActions({ progress, detail, note }: { progress: CalibrationProgress; detail: EvaluatorDetail; note: string | null }) {
  const writeReason = useWriteReason('admin');
  const reasonId = useId();
  if (writeReason) {
    return (
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" disabled aria-describedby={reasonId}>
          {copy.evaluators.label_queue}
        </Button>
        <DisabledReason id={reasonId} reason={writeReason} />
        {note ? <span className="text-xs text-fg-muted">{note}</span> : null}
      </div>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
      <Link to="/app/evaluators/labels" search={{ evaluator: progress.evaluator_ref }} className={buttonVariants({ size: 'sm' })}>
        {copy.evaluators.label_queue}{' '}
        <span className="sr-only">for {detail.name}</span>
      </Link>
      {note ? <span className="text-xs text-fg-muted">{note}</span> : null}
      {calibrateReason(progress) === null ? (
        <Link
          to="/app/evaluators/$id"
          params={{ id: detail.id }}
          search={{ tab: 'calibration' }}
          className="inline-flex items-center gap-0.5 text-sm font-medium text-fg underline underline-offset-2"
        >
          Calibrate{' '}
          <span className="sr-only">{detail.name}</span>
          <ArrowRight aria-hidden="true" className="size-3.5" />
        </Link>
      ) : null}
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
  compact = false,
  className,
}: {
  progress: CalibrationProgress;
  /** A library card: one Labels bar instead of three, and one next step. */
  compact?: boolean;
  /** The evaluator, for the actions (Label queue, Calibrate now). Without it there are none. */
  detail?: EvaluatorDetail;
  showActions?: boolean;
  className?: string;
}) {
  const parts = progressParts(progress);
  const actions = showActions && detail !== undefined;
  // A library card keeps one meter and one short next step: before a first fit is possible, how many
  // labels it needs (the full reason and the blocking shortfall are on the check's own page); after,
  // what blocking needs.
  const firstFit = compact ? calibrateReason(progress) : null;
  const more = labelsForFirstFit(progress);
  const cardNote = firstFit ? (progress.snapshot !== null && more > 0 ? `${formatCount(more)} ${more === 1 ? 'label' : 'labels'} to a first calibration` : firstFit.text) : null;
  const nextStep = compact ? (firstFit ? (actions ? null : cardNote) : (parts.missing ?? 'Enough labels to calibrate for blocking.')) : (parts.missing ?? 'Enough labels to calibrate for blocking.');
  return (
    <div className={cn('@container space-y-2', className)}>
      <p className="flex flex-wrap items-baseline justify-between gap-x-2 text-sm">
        {compact ? <span className="text-xs text-fg-muted">Calibration</span> : <span className="text-fg tabular">{progressText(progress)}</span>}
        <span className="text-xs text-fg-muted">
          <Term id="calibration">{parts.stateLabel}</Term>
        </span>
      </p>
      {compact ? (
        <Bar label="Labels" value={parts.total} have={progress.human_labels.n} need={progress.needed.n} />
      ) : (
        // Side by side where there is room; stacked in a narrow card (the label queue's sidebar).
        <div className="grid gap-x-3 gap-y-2 @sm:grid-cols-3">
          <Bar label="Labels" value={parts.total} have={progress.human_labels.n} need={progress.needed.n} />
          <Bar label="Has the problem" value={parts.correct} have={progress.human_labels.pos} need={progress.needed.pos} />
          <Bar label="Fine" value={parts.falseAlarms} have={progress.human_labels.neg} need={progress.needed.neg} />
        </div>
      )}
      {nextStep ? <p className="text-xs text-fg-muted">{nextStep}</p> : null}
      {/* What a first fit gives and what blocking needs, said once, on the check's own page. */}
      {!compact && progress.state !== 'measured' ? <p className="text-xs text-fg-muted">{FIT_THRESHOLDS}</p> : null}
      {actions ? (
        compact ? (
          <CardActions progress={progress} detail={detail} note={cardNote} />
        ) : (
          <Actions progress={progress} detail={detail} />
        )
      ) : null}
    </div>
  );
}
