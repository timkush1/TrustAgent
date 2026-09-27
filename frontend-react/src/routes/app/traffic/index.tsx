import { createFileRoute } from '@tanstack/react-router';
import { TrafficPage } from '@/components/traffic/TrafficPage';
import { TrafficSearch } from '@/components/traffic/search';

function TrafficRoute() {
  return <TrafficPage search={Route.useSearch()} />;
}

export const Route = createFileRoute('/app/traffic/')({
  validateSearch: TrafficSearch,
  staticData: { title: 'Traffic' },
  component: TrafficRoute,
});
