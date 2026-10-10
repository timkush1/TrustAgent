import { createFileRoute } from '@tanstack/react-router';
import { StudioVersionPage } from '@/components/studio/StudioPage';

/**
 * Evaluator Studio on a saved evaluator version (SPEC §7.3, "Duplicate and edit"): a check made in
 * the dashboard gets its next version; a built-in or file check is copied as a new check. On the
 * static site (D107) the built-in checks are the library, so a version opens as a copy to export.
 */
function StudioVersionRoute() {
  const { id, version } = Route.useParams();
  return <StudioVersionPage id={id} version={version} />;
}

export const Route = createFileRoute('/app/evaluators/studio/$id/$version')({
  staticData: { title: 'Studio' },
  component: StudioVersionRoute,
});
