/**
 * Studio step 4, quick calibration (SPEC §7.3): from the test's risks and the viewer's labels, a
 * suggested threshold with its honesty text, computed in the browser by the TS mirror of
 * `POST /api/studio/calibrate` (free). It needs 8 labels with at least 3 of each; it shows the
 * message verbatim, never says "calibrated" and never sets a critical threshold. Blocking needs a
 * real calibration from the label queue (step 7).
 */
import { Gauge } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { formatCount, formatRisk } from '@/domain/format';
import { MIN_FIT_LABELS, MIN_FIT_PER_CLASS, quickCalibrationOf, type Expected, type TestRow } from '@/lib/studio/testBench';
import type { EvaluatorSpec, StudioSample } from '@/types/api';
import { StudioStep } from './StudioStep';

export interface QuickCalibrationStepProps {
  spec: EvaluatorSpec;
  results: readonly TestRow[] | null;
  samples: readonly StudioSample[];
  onSpecChange: (spec: EvaluatorSpec) => void;
}

export function QuickCalibrationStep({ spec, results, samples, onSpecChange }: QuickCalibrationStepProps) {
  const description = 'A first threshold from your labels, free and in the browser. It is a suggestion, not a calibration: keep the check watching and label more traffic.';
  if (!results) {
    return (
      <StudioStep n={4} id="step-quick" title="Suggest a threshold" description={description}>
        <p className="text-sm text-fg-muted">
          Run the test in step 3 first. Quick calibration needs {MIN_FIT_LABELS} labelled answers, with at least {MIN_FIT_PER_CLASS} that should fire and {MIN_FIT_PER_CLASS} that shouldn't.
        </p>
      </StudioStep>
    );
  }
  const labels = new Map<string, Expected | undefined>(samples.map((s) => [s.id, s.expected]));
  const { result: q, needed } = quickCalibrationOf(results, labels, spec.outcomes.warn_at);
  const suggested = q.enough && q.threshold !== null ? q.threshold : null;

  return (
    <StudioStep n={4} id="step-quick" title="Suggest a threshold" description={description}>
      <div className="flex flex-wrap items-center gap-2">
        <Gauge aria-hidden="true" className="size-4 text-fg-muted" />
        {q.enough ? (
          <p className="text-sm font-medium text-fg">
            From {formatCount(q.n)} labels ({formatCount(q.n_pos)} should fire, {formatCount(q.n_neg)} shouldn't)
          </p>
        ) : (
          <p className="text-sm font-medium text-fg">
            Needs {MIN_FIT_LABELS} labels (have {formatCount(q.n)}: {formatCount(q.n_pos)} should fire, {formatCount(q.n_neg)} shouldn't)
          </p>
        )}
        {q.unstable ? (
          <Badge tone="neutral" emphasis="quiet">
            Unstable
          </Badge>
        ) : null}
      </div>
      <p className="text-sm text-fg" data-testid="quick-calibration-message">
        {q.message}
      </p>
      {!q.enough ? (
        <p className="text-sm text-fg-muted">
          {[
            needed.total > 0 ? `${formatCount(needed.total)} more labelled ${needed.total === 1 ? 'answer' : 'answers'}` : null,
            needed.fire > 0 ? `${formatCount(needed.fire)} more that should fire` : null,
            needed.noFire > 0 ? `${formatCount(needed.noFire)} more that shouldn't` : null,
          ]
            .filter(Boolean)
            .join(', ')}
          : add samples in step 3, label them, and run the test again.
        </p>
      ) : null}
      {suggested !== null && suggested !== spec.outcomes.warn_at ? (
        <Button onClick={() => onSpecChange({ ...spec, outcomes: { ...spec.outcomes, warn_at: suggested } })}>
          Use the suggested threshold ({formatRisk(suggested)})
        </Button>
      ) : null}
      {suggested !== null && suggested === spec.outcomes.warn_at ? <p className="text-sm text-fg-muted">The check already flags at the suggested threshold.</p> : null}
    </StudioStep>
  );
}
