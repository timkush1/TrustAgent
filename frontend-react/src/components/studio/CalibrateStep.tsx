/**
 * Studio step 7, calibrate for blocking (SPEC §7.3): blocking needs a calibration from human labels
 * (measured at 200 labels with 30 of each, provisional below that), collected in the label queue
 * and fitted on the evaluator's page ("Calibrate now", then "Adopt calibration" after Preview impact).
 */
import { Link } from '@tanstack/react-router';
import { Scale, Tags } from 'lucide-react';
import { buttonVariants } from '@/components/ui/button-variants';
import { MEASURED_MIN_LABELS, MEASURED_MIN_PER_CLASS } from '@/lib/decision/calibrate';
import { StudioStep } from './StudioStep';

export function CalibrateStep({ evaluatorId }: { evaluatorId: string | null }) {
  return (
    <StudioStep
      n={7}
      id="step-calibrate"
      title="Calibrate for blocking"
      description={`Before a check may block, its risk needs a calibration from people's labels: measured with ${MEASURED_MIN_LABELS} labels (at least ${MEASURED_MIN_PER_CLASS} of each), provisional with fewer. Labels from an AI judge only count as a proxy.`}
    >
      {evaluatorId ? (
        <div className="flex flex-wrap gap-2">
          <Link to="/app/evaluators/labels" className={buttonVariants({ variant: 'secondary' })}>
            <Tags aria-hidden="true" />
            Label answers in the label queue
          </Link>
          <Link to="/app/evaluators/$id" params={{ id: evaluatorId }} search={{ tab: 'calibration' }} className={buttonVariants({ variant: 'secondary' })}>
            <Scale aria-hidden="true" />
            Calibrate on the check's page
          </Link>
        </div>
      ) : (
        <p className="text-sm text-fg-muted">Save a version first (step 5): labels and calibrations belong to a saved check.</p>
      )}
    </StudioStep>
  );
}
