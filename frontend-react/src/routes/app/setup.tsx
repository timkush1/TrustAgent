import { createFileRoute } from '@tanstack/react-router';
import { z } from 'zod/mini';
import { SetupWizard } from '@/components/setup/SetupWizard';

/** `?step=secure|connect|try` opens one step of the wizard; anything else opens the first step not done. */
const SetupSearch = z.object({
  step: z.catch(z.optional(z.enum(['secure', 'connect', 'try'])), undefined),
});

function SetupRoute() {
  return <SetupWizard step={Route.useSearch().step} />;
}

export const Route = createFileRoute('/app/setup')({
  validateSearch: SetupSearch,
  staticData: { title: 'Setup' },
  component: SetupRoute,
});
