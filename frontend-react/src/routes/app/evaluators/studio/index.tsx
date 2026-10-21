import { createFileRoute } from '@tanstack/react-router';
import { StudioPage } from '@/components/studio/StudioPage';

/**
 * Evaluator Studio (SPEC §7.3): a new check. On the static site (D107) it drafts and tests with the
 * tab's own OpenRouter key and exports the check as a file instead of saving it.
 */
export const Route = createFileRoute('/app/evaluators/studio/')({
  staticData: { title: 'Studio' },
  component: StudioPage,
});
