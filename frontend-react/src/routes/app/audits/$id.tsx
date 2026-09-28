import { createFileRoute, redirect } from '@tanstack/react-router';
import { auditQuery } from '@/data/queries';

/**
 * Audit links redirect to the trace the audit belongs to (SPEC §3.3). Legacy rows have
 * trace_id = audit_id; newer audits are looked up once.
 */
export const Route = createFileRoute('/app/audits/$id')({
  staticData: { title: 'Request' },
  loader: async ({ params, context }) => {
    const audit = await context.queryClient.ensureQueryData(auditQuery(context.dataSource, params.id));
    throw redirect({ to: '/app/traffic/$traceId', params: { traceId: audit.trace_id }, replace: true });
  },
});
