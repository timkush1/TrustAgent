import { createFileRoute } from '@tanstack/react-router';
import { PhasePlaceholder } from '@/components/layout/PhasePlaceholder';
import { useIsStatic } from '@/components/replay/useReplay';
import { StudioPage } from '@/components/studio/StudioPage';
import { StudioStaticNote } from '@/components/studio/StudioStaticNote';

/**
 * Evaluator Studio (SPEC §7.3): a new check. The static build has no gateway to draft, test or save
 * with, so it stays "Coming soon" until P5 (D72).
 */
function StudioRoute() {
  const isStatic = useIsStatic();
  if (isStatic) {
    return (
      <PhasePlaceholder
        question="Write your own checks"
        phase="P5"
        phaseName="Studio in the online demo"
        badge="Coming soon"
        intro="Studio runs on your own TrustAgent stack. The online demo cannot draft or test checks yet. On your own stack you can:"
        planned={[
          'Describe a check in plain English; an LLM drafts it once as a question the fast check can answer',
          'Test the draft on recorded traffic before it touches anyone',
          'Calibrate it with a few labels, then watch it in shadow mode',
          'Attach it to a route: Jev then runs it on every request for a fraction of a cent',
        ]}
        meanwhile={<StudioStaticNote />}
      />
    );
  }
  return <StudioPage />;
}

export const Route = createFileRoute('/app/evaluators/studio/')({
  staticData: { title: 'Studio' },
  component: StudioRoute,
});
