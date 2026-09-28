import { createFileRoute } from '@tanstack/react-router';
import { Term } from '@/components/common/Term';
import { PhasePlaceholder } from '@/components/layout/PhasePlaceholder';
import { NAV_ITEMS } from '@/components/layout/nav';

function EvaluatorsPage() {
  return (
    <PhasePlaceholder
      question={NAV_ITEMS[2].question}
      phase="P4"
      phaseName="security, credentials, Studio, ROI"
      planned={[
        'The evaluator library: each check, its category and the routes it runs on',
        'Calibration progress from your labels, and a label queue (Correct / False alarm)',
        'Versions, a reliability chart and committed benchmark runs',
        'Studio: describe a new check in plain English, test it on samples, then watch it on a route',
      ]}
      meanwhile={
        <p>
          The demo route runs two <Term id="evaluator">evaluators</Term>:{' '}
          <Term id="groundedness">groundedness</Term> and <Term id="contradiction">contradiction</Term>, defined in{' '}
          <code className="font-mono text-xs text-fg">config/policies.yaml</code>.
        </p>
      }
    />
  );
}

export const Route = createFileRoute('/app/evaluators/')({
  staticData: { title: 'Evaluators' },
  component: EvaluatorsPage,
});
