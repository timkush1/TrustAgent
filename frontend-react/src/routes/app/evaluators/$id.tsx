import { createFileRoute } from '@tanstack/react-router';
import { EvaluatorDetailPage } from '@/components/evaluators/EvaluatorDetailPage';

function EvaluatorDetailRoute() {
  const { id } = Route.useParams();
  // Keyed, so moving between evaluators never shows the previous one's state.
  return <EvaluatorDetailPage key={id} id={id} />;
}

export const Route = createFileRoute('/app/evaluators/$id')({
  staticData: { title: 'Evaluator' },
  component: EvaluatorDetailRoute,
});
