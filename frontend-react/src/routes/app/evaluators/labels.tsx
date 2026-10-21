import { createFileRoute } from '@tanstack/react-router';
import { z } from 'zod/mini';
import { LabelQueuePage } from '@/components/evaluators/LabelQueuePage';

/** `?evaluator=groundedness@1` narrows the queue to one check (the Label queue links pass it). */
const LabelsSearch = z.object({
  evaluator: z.catch(z.optional(z.string().check(z.regex(/^[a-z0-9_]+@[0-9]+$/))), undefined),
});

function LabelsRoute() {
  return <LabelQueuePage search={Route.useSearch()} />;
}

export const Route = createFileRoute('/app/evaluators/labels')({
  validateSearch: LabelsSearch,
  staticData: { title: 'Label queue' },
  component: LabelsRoute,
});
