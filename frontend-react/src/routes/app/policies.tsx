import { createFileRoute } from '@tanstack/react-router';
import { z } from 'zod/mini';
import { PoliciesPage } from '@/components/policies/PoliciesPage';

/**
 * `?route=support-bot&evaluator=groundedness@1` opens that route's threshold editor for that check
 * ("Adjust threshold" on a trace).
 */
const PoliciesSearch = z.object({
  route: z.catch(z.optional(z.string().check(z.maxLength(128))), undefined),
  evaluator: z.catch(z.optional(z.string().check(z.regex(/^[a-z0-9_]+@[0-9]+$/))), undefined),
});

function PoliciesRoute() {
  return <PoliciesPage search={Route.useSearch()} />;
}

export const Route = createFileRoute('/app/policies')({
  validateSearch: PoliciesSearch,
  staticData: { title: 'Policies' },
  component: PoliciesRoute,
});
