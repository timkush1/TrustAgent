/** Router-level not-found and error views. */
import { Link, type ErrorComponentProps } from '@tanstack/react-router';
import { Compass } from 'lucide-react';
import { EmptyState, ErrorState } from './StateViews';

export function NotFound() {
  return (
    <EmptyState icon={Compass} title="There is no page at this address" className="m-6">
      <p>
        The link may be from an older version of the dashboard.{' '}
        <Link to="/app/overview" className="font-medium text-fg underline underline-offset-2">
          Go to Overview
        </Link>
      </p>
    </EmptyState>
  );
}

export function RouteError({ error, reset }: ErrorComponentProps) {
  return <ErrorState error={error} onRetry={reset} className="m-6" />;
}
