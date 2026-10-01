/**
 * The top of a trace (SPEC §3.3 "Trace detail" 1): the outcome pill and headline, what the answer
 * was checked against, the reasons in plain words, the next step, and a link to what the user
 * received when it was not the model's answer. "Was this right?", "Open evaluator" and "Adjust
 * threshold" arrive in P4 with labels (SPEC §2.4 #39-41) and threshold editing.
 */
import { ArrowDown } from 'lucide-react';
import type { MouseEvent } from 'react';
import { OutcomePill } from '@/components/outcome/OutcomePill';
import { reasonLines } from '@/components/traffic/trafficRows';
import { checkedAgainst, copy, traceOutcome } from '@/domain/presentation';
import { formatDateTime } from '@/domain/format';
import type { GateDecisionView, TraceSummary } from '@/types/api';
import type { Delivered } from './traceView';

/** Scrolls to the "What the user received instead" block and moves focus there. */
function jumpTo(event: MouseEvent<HTMLAnchorElement>, id: string) {
  const target = document.getElementById(id);
  if (!target) return;
  event.preventDefault();
  target.scrollIntoView?.({ block: 'nearest' }); // absent in jsdom
  target.focus({ preventScroll: true });
}

export function OutcomeCard({
  summary,
  decision,
  delivered = null,
  receivedTarget,
}: {
  summary: TraceSummary;
  decision: GateDecisionView | null;
  /** What the user received; a fallback answer or the block message gets a link to it. */
  delivered?: Delivered | null;
  /** The id of the answer section's "What the user received instead" block. */
  receivedTarget?: string;
}) {
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
      {receivedTarget && (delivered === 'fallback' || delivered === 'blocked') && !summary.redacted ? (
        <a
          href={`#${receivedTarget}`}
          onClick={(e) => jumpTo(e, receivedTarget)}
          className="inline-flex items-center gap-1 text-sm font-medium text-fg underline underline-offset-2"
        >
          {copy.trace.see_received}
          <ArrowDown aria-hidden="true" className="size-3.5" />
        </a>
      ) : null}
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
