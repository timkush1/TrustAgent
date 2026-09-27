import { createFileRoute, redirect } from '@tanstack/react-router';

// Old trace links redirect to the Traffic detail route (SPEC §3.3).
export const Route = createFileRoute('/app/traces/$id')({
  beforeLoad: ({ params }) => {
    throw redirect({ to: '/app/traffic/$traceId', params: { traceId: params.id }, replace: true });
  },
});
