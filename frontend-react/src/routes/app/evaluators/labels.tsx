import { createFileRoute } from '@tanstack/react-router';
import { PhasePlaceholder, SeeTraffic } from '@/components/layout/PhasePlaceholder';

/**
 * The label queue (SPEC §3.3 Evaluators; P4-PLAN D59-D60). WEB-EV replaces the placeholder with
 * the queue (P4-PLAN §11 EVW-2).
 */
function LabelsRoute() {
  return (
    <PhasePlaceholder
      question="Was the fast check right?"
      phase="P4"
      phaseName="labels and calibration"
      planned={[
        'One answer at a time, the most uncertain first',
        'Correct, False alarm or Skip, with the keys C, F and S',
        'Each label counts toward calibrating that check',
      ]}
      meanwhile={<SeeTraffic />}
    />
  );
}

export const Route = createFileRoute('/app/evaluators/labels')({
  staticData: { title: 'Label queue' },
  component: LabelsRoute,
});
