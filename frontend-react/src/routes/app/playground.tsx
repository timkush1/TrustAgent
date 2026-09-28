import { createFileRoute } from '@tanstack/react-router';
import { PhasePlaceholder, SeeTraffic } from '@/components/layout/PhasePlaceholder';
import { NAV_ITEMS } from '@/components/layout/nav';

function PlaygroundPage() {
  return (
    <PhasePlaceholder
      question={NAV_ITEMS[4].question}
      phase="P3"
      phaseName="integration"
      planned={[
        'Send a chat request, or paste an answer to audit, on any route and model',
        'Attach evidence documents and choose streaming',
        'Follow it through: upstream, fast check, what your app received, deep audit',
        'The equivalent curl command, with the key masked',
      ]}
      meanwhile={<SeeTraffic />}
    />
  );
}

export const Route = createFileRoute('/app/playground')({
  staticData: { title: 'Playground' },
  component: PlaygroundPage,
});
