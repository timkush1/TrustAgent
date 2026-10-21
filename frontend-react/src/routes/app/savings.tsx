import { createFileRoute, redirect } from '@tanstack/react-router';

// Deep link to the Overview "Savings" tab (SPEC §3.3).
export const Route = createFileRoute('/app/savings')({
  beforeLoad: () => {
    throw redirect({ to: '/app/overview', search: { tab: 'savings' }, replace: true });
  },
});
