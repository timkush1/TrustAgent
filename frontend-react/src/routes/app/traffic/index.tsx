import { createFileRoute } from '@tanstack/react-router';
import { PhasePlaceholder } from '@/components/layout/PhasePlaceholder';
import { NAV_ITEMS } from '@/components/layout/nav';
import { TrafficSearch } from '@/components/traffic/search';

function TrafficPage() {
  return (
    <PhasePlaceholder
      question={NAV_ITEMS[1].question}
      phase="S0"
      phaseName="walking skeleton"
      planned={[
        'Every request as it happens: outcome, route, model, the main reason and what it was checked against',
        'Live updates with pause and resume, and the deep-audit state of each answer',
        'A detail page per request: the answer, the reasons and the documents it was checked against',
      ]}
    />
  );
}

export const Route = createFileRoute('/app/traffic/')({
  validateSearch: TrafficSearch,
  staticData: { title: 'Traffic' },
  component: TrafficPage,
});
