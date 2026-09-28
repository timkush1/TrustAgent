import { createFileRoute } from '@tanstack/react-router';
import { z } from 'zod/mini';
import { PhasePlaceholder, SeeTraffic } from '@/components/layout/PhasePlaceholder';
import { NAV_ITEMS } from '@/components/layout/nav';

const OverviewSearch = z.object({
  tab: z.catch(z.optional(z.enum(['summary', 'savings'])), undefined),
});

function OverviewPage() {
  return (
    <PhasePlaceholder
      question={NAV_ITEMS[0].question}
      phase="P3"
      phaseName="integration"
      planned={[
        'Requests by outcome over the selected time window',
        'What was caught, in plain words, with links to examples',
        'Evidence coverage: how many answers could be checked against documents, and why not',
        'Fast-check latency (p50 / p95) and your cost per 1,000 answers (Savings arrives in P4)',
      ]}
      meanwhile={<SeeTraffic />}
    />
  );
}

export const Route = createFileRoute('/app/overview')({
  validateSearch: OverviewSearch,
  staticData: { title: 'Overview' },
  component: OverviewPage,
});
