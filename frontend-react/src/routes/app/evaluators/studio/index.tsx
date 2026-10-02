import { createFileRoute } from '@tanstack/react-router';
import { PhasePlaceholder, SeeTraffic } from '@/components/layout/PhasePlaceholder';
import { useIsStatic } from '@/components/replay/useReplay';

/**
 * Evaluator Studio (SPEC §7.3): a new check. The route exists so the static build's "Studio" item
 * and links to /app/evaluators/studio land on an honest page instead of an evaluator named
 * "studio". WEB-ST replaces the placeholder with the Studio steps (P4-PLAN §12 STW-3); the static
 * build keeps "Coming soon" until P5 (D72).
 */
function StudioRoute() {
  const isStatic = useIsStatic();
  return (
    <PhasePlaceholder
      question="Write your own checks"
      phase="P4"
      phaseName="Evaluator Studio"
      badge={isStatic ? 'Coming soon' : undefined}
      planned={[
        'Describe a check in plain English; an LLM drafts it once as a question the fast check can answer',
        'Test the draft on recorded traffic before it touches anyone',
        'Calibrate it with a few labels, then watch it in shadow mode',
        'Attach it to a route: Jev then runs it on every request for a fraction of a cent',
      ]}
      meanwhile={<SeeTraffic />}
    />
  );
}

export const Route = createFileRoute('/app/evaluators/studio/')({
  staticData: { title: 'Studio' },
  component: StudioRoute,
});
