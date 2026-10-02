import { createFileRoute } from '@tanstack/react-router';
import { BenchmarksPage } from '@/components/evaluators/BenchmarksPage';

/** Benchmarks (SPEC §3.3 Evaluators; P4-PLAN D72): committed, measured runs only. */
function BenchmarksRoute() {
  return <BenchmarksPage />;
}

export const Route = createFileRoute('/app/evaluators/benchmarks')({
  staticData: { title: 'Benchmarks' },
  component: BenchmarksRoute,
});
