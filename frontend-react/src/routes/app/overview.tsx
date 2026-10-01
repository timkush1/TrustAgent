import { createFileRoute } from '@tanstack/react-router';
import { z } from 'zod/mini';
import { OverviewPage } from '@/components/overview/OverviewPage';

const OverviewSearch = z.object({
  tab: z.catch(z.optional(z.enum(['summary', 'savings'])), undefined),
  window: z.catch(z.optional(z.enum(['15m', '1h', '24h', '7d', '30d'])), undefined),
});

function OverviewRoute() {
  return <OverviewPage search={Route.useSearch()} />;
}

export const Route = createFileRoute('/app/overview')({
  validateSearch: OverviewSearch,
  staticData: { title: 'Overview' },
  component: OverviewRoute,
});
