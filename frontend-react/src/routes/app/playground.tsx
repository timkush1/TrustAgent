import { createFileRoute } from '@tanstack/react-router';
import { PlaygroundPage } from '@/components/playground/PlaygroundPage';
import { StaticPlayground } from '@/components/replay/StaticPlayground';
import { useIsStatic } from '@/components/replay/useReplay';

/** The static build has no gateway to run against: it opens the recorded examples instead. */
function PlaygroundRoute() {
  return useIsStatic() ? <StaticPlayground /> : <PlaygroundPage />;
}

export const Route = createFileRoute('/app/playground')({
  staticData: { title: 'Playground' },
  component: PlaygroundRoute,
});
