import { createFileRoute, Link } from '@tanstack/react-router';
import { PhasePlaceholder } from '@/components/layout/PhasePlaceholder';

function TraceDetailPage() {
  const { traceId } = Route.useParams();
  return (
    <PhasePlaceholder
      question="What happened to this request?"
      phase="S0"
      phaseName="walking skeleton"
      planned={[
        'The outcome, its headline and what the answer was checked against',
        'The answer text with the fast check’s findings and, once the deep audit is done, its claims',
        'Each check with its risk level, and the timings, ids and costs of this request',
      ]}
      meanwhile={
        <p>
          Request <code className="font-mono text-xs text-fg">{traceId}</code>.{' '}
          <Link to="/app/traffic" className="font-medium text-fg underline underline-offset-2">
            Back to Traffic
          </Link>
        </p>
      }
    />
  );
}

export const Route = createFileRoute('/app/traffic/$traceId')({
  staticData: { title: 'Request' },
  component: TraceDetailPage,
});
