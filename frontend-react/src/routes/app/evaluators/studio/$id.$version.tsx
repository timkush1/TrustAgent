import { createFileRoute } from '@tanstack/react-router';
import { PhasePlaceholder } from '@/components/layout/PhasePlaceholder';
import { useIsStatic } from '@/components/replay/useReplay';
import { StudioVersionPage } from '@/components/studio/StudioPage';
import { StudioStaticNote } from '@/components/studio/StudioStaticNote';

/**
 * Evaluator Studio on a saved evaluator version (SPEC §7.3, "Duplicate and edit"): a check made in
 * the dashboard gets its next version; a built-in or file check is copied as a new check. The static
 * build stays "Coming soon" until P5 (P4-PLAN D72).
 */
function StudioVersionRoute() {
  const { id, version } = Route.useParams();
  const isStatic = useIsStatic();
  if (isStatic) {
    return (
      <PhasePlaceholder
        question={`Edit ${id}, version ${version}`}
        phase="P5"
        phaseName="Studio in the online demo"
        badge="Coming soon"
        planned={[
          'Start from this version: change the question, the options or the thresholds',
          'Test the change on the same samples and on recorded traffic',
          'Save it as the next version, with a changelog and a diff',
        ]}
        meanwhile={<StudioStaticNote />}
      />
    );
  }
  return <StudioVersionPage id={id} version={version} />;
}

export const Route = createFileRoute('/app/evaluators/studio/$id/$version')({
  staticData: { title: 'Studio' },
  component: StudioVersionRoute,
});
