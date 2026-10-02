import { Eye, FlaskConical, FlaskRound } from 'lucide-react';
import { useStreamStore } from '@/data/stream/streamStore';
import { useAppMode, useSystemStatus } from '@/data/useSystem';
import { copy } from '@/domain/presentation';

const badgeClass =
  'inline-flex h-6 items-center gap-1 rounded-full border border-dashed border-border-strong px-2 text-xs font-medium text-fg-muted';

/**
 * Whether the fast check runs on the mock model outside the demo stack (D39, D89): "Try it
 * without a key" or an env-pinned mock. From /api/status when it is served, otherwise from the
 * stream's `hello` summary.
 */
function useSimulatedChecks(demo: boolean): boolean {
  const { status } = useSystemStatus();
  const helloState = useStreamStore((s) => s.hello?.status.decision_model);
  if (demo) return false;
  return status ? status.decision_model.backend === 'mock' : helloState === 'mock';
}

/**
 * "Demo", "Read-only" and "Simulated checks" markers in the top bar. The banner explains demo and
 * read-only; the simulated badge stays put (D89) and says why its numbers are labelled Simulated.
 */
export function ModeBadge() {
  const { demo, readonly } = useAppMode();
  const simulated = useSimulatedChecks(demo);
  if (!demo && !readonly && !simulated) return null;
  return (
    <span className="flex items-center gap-1.5">
      {demo ? (
        <span className={badgeClass}>
          <FlaskConical aria-hidden="true" className="size-3.5" />
          Demo
          <span className="sr-only">: {copy.banners.demo.body}</span>
        </span>
      ) : null}
      {simulated ? (
        <span data-badge="simulated-checks" title={copy.badges.simulated_checks_hint} className={badgeClass}>
          <FlaskRound aria-hidden="true" className="size-3.5" />
          {copy.badges.simulated_checks}
          <span className="sr-only">: {copy.badges.simulated_checks_hint}</span>
        </span>
      ) : null}
      {readonly ? (
        <span className={badgeClass}>
          <Eye aria-hidden="true" className="size-3.5" />
          Read-only
        </span>
      ) : null}
    </span>
  );
}
