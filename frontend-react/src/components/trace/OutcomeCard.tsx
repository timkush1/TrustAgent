/**
 * The top of a trace (SPEC §3.3 "Trace detail" 1): the outcome pill and headline, what the answer
 * was checked against, the reasons in plain words, and the next step. "Was this right?", "Open
 * evaluator" and "Adjust threshold" join with labels (P3) and the evaluator pages (P4).
 */
import { OutcomePill } from '@/components/outcome/OutcomePill';
import { reasonLines } from '@/components/traffic/trafficRows';
import { checkedAgainst, traceOutcome } from '@/domain/presentation';
import { formatDateTime } from '@/domain/format';
import type { GateDecisionView, TraceSummary } from '@/types/api';

export function OutcomeCard({ summary, decision }: { summary: TraceSummary; decision: GateDecisionView | null }) {
  const outcome = traceOutcome({ ...summary, degraded: decision?.degraded ?? null });
  const pending = summary.gate_status === 'pending';
  const evidence = checkedAgainst(summary.checked_against ?? decision?.evidence_summary);
  const reasons = reasonLines(summary.reasons, decision);
  return (
    <section aria-labelledby="outcome-title" className="space-y-3 rounded-lg border border-border bg-surface p-4">
      <h2 id="outcome-title" className="sr-only">
        Outcome
      </h2>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <OutcomePill outcome={outcome} />
        <p className="text-lg font-semibold text-fg">{outcome.headlineText}</p>
      </div>
      {pending ? null : <p className="text-sm text-fg">{evidence.text}</p>}
      {reasons.length > 0 ? (
        <ul aria-label="Reasons" className="space-y-1 text-sm text-fg">
          {reasons.map((r, i) => (
            <li key={summary.reasons[i]} className="flex gap-2">
              <span aria-hidden="true" className="mt-2 size-1 shrink-0 rounded-full bg-fg-subtle" />
              {r}
            </li>
          ))}
        </ul>
      ) : null}
      {outcome.next_step ? <p className="text-sm text-fg-muted">{outcome.next_step}</p> : null}
      <p className="text-xs text-fg-muted">
        <span>{summary.route}</span>
        {summary.model ? <span> · {summary.model}</span> : null}
        <span> · </span>
        <time dateTime={summary.created_at} className="tabular">
          {formatDateTime(summary.created_at)}
        </time>
      </p>
    </section>
  );
}
