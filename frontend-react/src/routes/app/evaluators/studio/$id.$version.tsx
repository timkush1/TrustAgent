import { createFileRoute, Link } from '@tanstack/react-router';
import { PhasePlaceholder } from '@/components/layout/PhasePlaceholder';
import { useIsStatic } from '@/components/replay/useReplay';

/**
 * Evaluator Studio on an existing evaluator version ("Duplicate and edit", SPEC §7.3). WEB-ST
 * replaces the placeholder with the Studio steps (P4-PLAN §12 STW-3).
 */
function StudioVersionRoute() {
  const { id, version } = Route.useParams();
  const isStatic = useIsStatic();
  return (
    <PhasePlaceholder
      question={`Edit ${id}, version ${version}`}
      phase="P4"
      phaseName="Evaluator Studio"
      badge={isStatic ? 'Coming soon' : undefined}
      planned={[
        'Start from this version: change the question, the options or the thresholds',
        'Test the change on the same samples and on recorded traffic',
        'Save it as the next version, with a changelog and a diff',
      ]}
      meanwhile={
        isStatic ? undefined : (
          <p>
            Meanwhile, see this check on{' '}
            <Link to="/app/evaluators/$id" params={{ id }} className="font-medium text-fg underline underline-offset-2">
              its evaluator page
            </Link>
            .
          </p>
        )
      }
    />
  );
}

export const Route = createFileRoute('/app/evaluators/studio/$id/$version')({
  staticData: { title: 'Studio' },
  component: StudioVersionRoute,
});
