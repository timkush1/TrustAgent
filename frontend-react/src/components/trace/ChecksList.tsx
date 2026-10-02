/**
 * "Checks" (SPEC §3.3 "Trace detail" 2, right): one row per evaluator the fast check asked, named
 * from the evaluator library (GET /api/evaluators), with its level word and a risk bar with flag and
 * block ticks, or "Not checked — no documents". Shadow evaluators are labelled "Watching". A
 * decision that arrived after the deadline says so: it was recorded, never enforced. Each name
 * opens its evaluator; an assessed row of a check the route lists also offers "Adjust threshold" on
 * this request's route.
 */
import { Link, useNavigate } from '@tanstack/react-router';
import { Clock, SlidersHorizontal } from 'lucide-react';
import type { CSSProperties } from 'react';
import { RiskBar, RiskBarLegend } from '@/components/outcome/RiskBar';
import { IconButton } from '@/components/ui/icon-button';
import { Skeleton } from '@/components/ui/skeleton';
import { copy, levelWord, statusReason, statusReasonKey, toneVar } from '@/domain/presentation';
import { formatRisk } from '@/domain/format';
import { evalRefId } from '@/domain/words/evaluators';
import { cn } from '@/lib/cn';
import type { GateDecisionView, TraceSummary } from '@/types/api';
import { checkRows, type CheckRow } from './traceView';

function CheckItem({ row, route }: { row: CheckRow; route: string }) {
  const navigate = useNavigate();
  const level = row.kind === 'assessed' && row.level ? levelWord(row.level) : null;
  return (
    <li className="space-y-1.5 py-2.5">
      <div className="flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-sm font-medium text-fg">
          <Link to="/app/evaluators/$id" params={{ id: evalRefId(row.ref) }} className="underline-offset-2 hover:underline">
            {row.name}
          </Link>
          {row.shadow ? (
            <>
              {' '}
              <span className="ml-1 text-xs font-normal text-fg-muted">{copy.trace.watching}</span>
            </>
          ) : null}
        </span>
        {level ? (
          <span
            style={{ '--tone': `var(${toneVar(level.tone)})` } as CSSProperties}
            className={cn('shrink-0 text-xs', level.level === 'ok' ? 'text-fg-muted' : 'font-medium text-(--tone)')}
          >
            {level.label}
            <span className="ml-2 text-fg-muted tabular">{formatRisk(row.risk)}</span>
          </span>
        ) : null}
        {/* An internal check is not on the route, so it has no threshold there to adjust. */}
        {row.kind === 'assessed' && !row.internal ? (
          <IconButton
            size="sm"
            icon={SlidersHorizontal}
            aria-label={`${copy.trace.adjust_threshold}: ${row.name}`}
            onClick={() => void navigate({ to: '/app/policies', search: { route, evaluator: row.ref } })}
          />
        ) : null}
      </div>
      {row.kind === 'assessed' && row.level && row.risk !== null ? (
        <RiskBar
          risk={row.risk}
          level={row.level}
          warnAt={row.warnAt}
          criticalAt={row.criticalAt}
          label={`${row.name} risk`}
        />
      ) : (
        <p className="text-xs text-fg-muted">{row.note}</p>
      )}
    </li>
  );
}

/** "Arrived late, not enforced", with what that means. */
function LateNote() {
  return (
    <p data-testid="late-decision" className="flex items-start gap-2 border-b border-border py-2.5 text-sm text-fg-muted">
      <Clock aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
      <span>
        <span className="font-medium text-fg">{copy.trace.late}.</span> {copy.trace.late_note}
      </span>
    </p>
  );
}

export function ChecksList({
  summary,
  decision,
  names,
}: {
  summary: TraceSummary;
  decision: GateDecisionView | null;
  /** Evaluator names by id from the evaluator library; humanized ids without it. */
  names?: ReadonlyMap<string, string>;
}) {
  const rows = checkRows(decision, names);
  let content;
  if (summary.gate_status === 'pending') {
    content = (
      <div role="status" className="space-y-2 py-2">
        <p className="text-sm text-fg-muted">{copy.tiers.fast_check} is checking this answer…</p>
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-full" />
      </div>
    );
  } else if (!decision) {
    content = <p className="py-2 text-sm text-fg-muted">The check details are not available for this request.</p>;
  } else if (rows.length === 0) {
    const reason = statusReason(statusReasonKey({ ...summary, degraded: decision.degraded }));
    content = (
      <p className="py-2 text-sm text-fg-muted">{reason ? `No checks ran: ${reason}.` : 'No checks ran for this request.'}</p>
    );
  } else {
    content = (
      <>
        <ul aria-label="Checks" className="divide-y divide-border">
          {rows.map((row) => (
            <CheckItem key={row.ref} row={row} route={summary.route} />
          ))}
        </ul>
        {rows.some((r) => r.kind === 'assessed') ? <RiskBarLegend className="pt-1" /> : null}
      </>
    );
  }
  return (
    <section aria-labelledby="checks-title" className="space-y-2">
      <div className="flex items-baseline justify-between gap-2">
        <h2 id="checks-title" className="text-base font-semibold text-fg">
          {copy.trace.checks}
        </h2>
        <span className="text-xs text-fg-muted">{copy.tiers.fast_check}</span>
      </div>
      <div className="rounded-lg border border-border bg-surface px-4 py-1.5">
        {decision?.late ? <LateNote /> : null}
        {content}
      </div>
    </section>
  );
}
