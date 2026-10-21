import { createFileRoute, redirect } from '@tanstack/react-router';

// Full build: `/` is the dashboard (the landing page lives at /welcome).
export const Route = createFileRoute('/')({
  beforeLoad: () => {
    throw redirect({ to: '/app', replace: true });
  },
});
