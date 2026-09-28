/**
 * Why an action is unavailable, next to it, with a link to the fix when there is one
 * (SPEC §3.3 page contract: no dead buttons).
 */
import { Link } from '@tanstack/react-router';
import { Lock } from 'lucide-react';
import { copy } from '@/domain/presentation';

export type DisabledReasonId = keyof typeof copy.disabled_reasons;

export function DisabledReason({ reason, id }: { reason: DisabledReasonId; id?: string }) {
  const fix =
    reason === 'needs_connection' ? (
      <Link to="/app/settings/$section" params={{ section: 'connections' }} className="underline underline-offset-2 hover:text-fg">
        Open Connections
      </Link>
    ) : null;
  return (
    <p id={id} className="inline-flex items-center gap-1 text-xs text-fg-muted">
      <Lock aria-hidden="true" className="size-3" />
      {copy.disabled_reasons[reason]}
      {fix ? <> · {fix}</> : null}
    </p>
  );
}
