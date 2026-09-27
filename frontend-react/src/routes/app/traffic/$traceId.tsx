import { createFileRoute } from '@tanstack/react-router';
import { TraceDetail } from '@/components/trace/TraceDetail';

function TraceDetailRoute() {
  const { traceId } = Route.useParams();
  // Keyed, so moving between requests never shows the previous one's state.
  return <TraceDetail key={traceId} traceId={traceId} />;
}

export const Route = createFileRoute('/app/traffic/$traceId')({
  staticData: { title: 'Request' },
  component: TraceDetailRoute,
});
