import { createFileRoute, Link } from '@tanstack/react-router';
import { PhasePlaceholder } from '@/components/layout/PhasePlaceholder';

/**
 * Connect OpenRouter returns here with `?code=` (SPEC §7.5; P4-PLAN D38). Full build only (D72).
 * WEB-ON replaces the placeholder with the callback page (P4-PLAN §10 ON-7e).
 */
function ConnectCallbackRoute() {
  return (
    <main className="mx-auto max-w-2xl px-4 py-10">
      <PhasePlaceholder
        question="Connecting OpenRouter"
        phase="P4"
        phaseName="Connect OpenRouter"
        planned={[
          'Finish the sign-in that OpenRouter started',
          'Check the new key and use it for the fast check and the deep audit',
          'Return to the page you started from',
        ]}
        meanwhile={
          <p>
            Meanwhile, paste a key in{' '}
            <Link
              to="/app/settings/$section"
              params={{ section: 'connections' }}
              className="font-medium text-fg underline underline-offset-2"
            >
              Settings → Connections
            </Link>
            .
          </p>
        }
      />
    </main>
  );
}

export const Route = createFileRoute('/connect/callback')({
  component: ConnectCallbackRoute,
});
