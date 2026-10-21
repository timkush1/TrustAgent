import { createFileRoute } from '@tanstack/react-router';
import { EvaluatorsPage } from '@/components/evaluators/EvaluatorsPage';

export const Route = createFileRoute('/app/evaluators/')({
  staticData: { title: 'Evaluators' },
  component: EvaluatorsPage,
});
