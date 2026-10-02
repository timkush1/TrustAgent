import { createFileRoute, useNavigate } from '@tanstack/react-router';
import { z } from 'zod/mini';
import { EvaluatorDetailPage } from '@/components/evaluators/EvaluatorDetailPage';

/** `?tab=calibration` opens the Calibration tab (the label queue and adopt flows link to it). */
const EvaluatorSearch = z.object({
  tab: z.catch(z.optional(z.enum(['overview', 'calibration', 'versions'])), undefined),
});

function EvaluatorDetailRoute() {
  const { id } = Route.useParams();
  const { tab } = Route.useSearch();
  const navigate = useNavigate({ from: '/app/evaluators/$id' });
  // Keyed, so moving between evaluators never shows the previous one's state.
  return (
    <EvaluatorDetailPage
      key={id}
      id={id}
      tab={tab ?? 'overview'}
      onTabChange={(next) => void navigate({ search: { tab: next === 'overview' ? undefined : next }, replace: true })}
    />
  );
}

export const Route = createFileRoute('/app/evaluators/$id')({
  validateSearch: EvaluatorSearch,
  staticData: { title: 'Evaluator' },
  component: EvaluatorDetailRoute,
});
