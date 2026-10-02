import { createFileRoute } from '@tanstack/react-router';
import { PhasePlaceholder, SeeTraffic } from '@/components/layout/PhasePlaceholder';

/**
 * Benchmarks (SPEC §3.3 Evaluators; P4-PLAN D72): committed benchmark runs only. WEB-EV replaces
 * the placeholder (P4-PLAN §11 EVW-6).
 */
function BenchmarksRoute() {
  return (
    <PhasePlaceholder
      question="How well do the checks do on public datasets?"
      phase="P4"
      phaseName="benchmarks"
      planned={[
        'Committed benchmark runs, each figure labelled measured',
        'Agreement with human labels per check and dataset',
      ]}
      meanwhile={<SeeTraffic />}
    />
  );
}

export const Route = createFileRoute('/app/evaluators/benchmarks')({
  staticData: { title: 'Benchmarks' },
  component: BenchmarksRoute,
});
