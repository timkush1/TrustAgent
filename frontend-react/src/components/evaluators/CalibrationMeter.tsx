/**
 * Calibration progress for one evaluator (SPEC §3.3 Evaluators): "Labels 43/200 (31 correct,
 * 12 false alarms)" with a meter per requirement (200 labels, 30 of each kind), the calibration
 * state, what is still missing, and the Label queue / Calibrate now actions, which arrive in P4
 * and say so.
 */
import { DisabledAction } from '@/components/common/DisabledReason';
import { Term } from '@/components/common/Term';
import { Button } from '@/components/ui/button';
import { formatCount } from '@/domain/format';
import { copy } from '@/domain/presentation';
import { cn } from '@/lib/cn';
import type { CalibrationProgress } from '@/types/api';
import { P4_REASONS, progressParts, progressText } from './evaluatorWords';

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

export function CalibrationMeter({
  progress,
  showActions = true,
  className,
}: {
  progress: CalibrationProgress;
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
      {showActions ? (
        <div className="flex flex-wrap items-start gap-2">
          <DisabledAction reason={P4_REASONS.labelQueue} layout="stacked">
            {(p) => (
              <Button size="sm" {...p}>
                {copy.evaluators.label_queue}
              </Button>
            )}
          </DisabledAction>
          <DisabledAction reason={P4_REASONS.calibrate} layout="stacked">
            {(p) => (
              <Button size="sm" {...p}>
                {copy.evaluators.calibrate_now}
              </Button>
            )}
          </DisabledAction>
        </div>
      ) : null}
    </div>
  );
}
