/**
 * "Was this right?" (SPEC §3.3 "Trace detail" 1; D59): on an answer where at least one
 * check of the model's answer (attempt 0, watching checks included) reached Elevated or High risk,
 * the viewer says whether the flag was right. Correct writes `should_fire` and False alarm writes
 * `should_not_fire`, one label per such check, each counting toward its calibration. Labels already
 * given show who gave them, with Undo.
 *
 * Labels have no live event (D61): the trace and each check's progress are refreshed locally.
 */
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Check, Undo2 } from 'lucide-react';
import { useId, useState } from 'react';
import { useDataSource } from '@/app/services';
import { DisabledAction, DisabledReason, type Reason } from '@/components/common/DisabledReason';
import { describeError } from '@/components/common/describeError';
import { useNow } from '@/components/common/useNow';
import { useWriteReason } from '@/components/common/useWriteAccess';
import { Button } from '@/components/ui/button';
import { isApiError } from '@/data/errors';
import { qk } from '@/data/queries';
import { invalidateAfterLabel } from '@/data/queries/evaluators';
import { formatDateTime, formatRelativeTime } from '@/domain/format';
import { copy } from '@/domain/presentation';
import { evalLabelerName, evalLabelWord, evalRefId, type EvalFireLabel } from '@/domain/words/evaluators';
import type { GateDecisionView, LabelView, TraceDetail } from '@/types/api';
import { evaluatorName, labelTargets } from './traceView';

/** Why the answer cannot be labelled (no stored text to judge), or null. */
function bodyReason(body: TraceDetail['body'] | null): Reason | null {
  if (!body || body.available) return null;
  switch (body.unavailable_reason) {
    case 'expired':
      return { text: copy.labels.body_expired };
    case 'not_stored':
      return { text: "This request's text was not stored, so there is nothing to judge." };
    case 'scope':
      return { text: "Your key can't read this request's text, so there is nothing to judge." };
    default:
      return { text: "This request's text is not available." };
  }
}

const isFireLabel = (label: string): label is EvalFireLabel => label === 'should_fire' || label === 'should_not_fire';

export function WasThisRight({
  traceId,
  decision,
  labels,
  body,
  names,
}: {
  traceId: string;
  decision: GateDecisionView | null;
  /** The labels stored for this trace (TraceDetail.labels). */
  labels: readonly LabelView[];
  body: TraceDetail['body'] | null;
  names?: ReadonlyMap<string, string>;
}) {
  const ds = useDataSource();
  const queryClient = useQueryClient();
  const now = useNow();
  const writeReason = useWriteReason('admin');
  const [error, setError] = useState<{ message: string; code: string | null } | null>(null);
  const reasonId = useId();
  const targets = labelTargets(decision);
  // Labels are per labeller: the caller's own can be undone; another admin's is shown, and the
  // caller can still add their own.
  const given = labels.filter((l) => targets.includes(l.target) && isFireLabel(l.label) && l.mine);
  const others = labels.filter((l) => targets.includes(l.target) && isFireLabel(l.label) && !l.mine);

  const setLabels = (change: (current: LabelView[]) => LabelView[]) =>
    queryClient.setQueryData<TraceDetail>(qk.traces.detail(traceId), (d) => (d ? { ...d, labels: change(d.labels ?? []) } : d));

  const put = useMutation({
    mutationFn: (label: EvalFireLabel) => Promise.all(targets.map((target) => ds.putLabel({ trace_id: traceId, target, label }))),
    onMutate: (label) => {
      setError(null);
      const at = new Date().toISOString();
      setLabels((current) => [
        ...current.filter((l) => !(targets.includes(l.target) && l.mine)),
        ...targets.map((target) => ({ target, label, labeled_by: 'you', created_at: at, note: null, mine: true })),
      ]);
    },
    onError: (err) => {
      // Take the optimistic labels back at once; the refetch below shows any that were saved.
      setLabels((current) => current.filter((l) => !(targets.includes(l.target) && l.mine && l.labeled_by === 'you')));
      const { message, code } = describeError(err);
      setError({ message: isApiError(err) && err.code === 'body_expired' ? copy.labels.body_expired : `The label was not saved: ${message}`, code: code ?? null });
    },
    onSettled: () => invalidateAfterLabel(queryClient, { traceId, evaluatorIds: targets.map(evalRefId) }),
  });

  const undo = useMutation({
    mutationFn: () => Promise.all(given.map((l) => ds.deleteLabel({ trace_id: traceId, target: l.target }))),
    onMutate: () => {
      setError(null);
      setLabels((current) => current.filter((l) => !(targets.includes(l.target) && l.mine)));
    },
    onError: (err) => {
      const { message, code } = describeError(err);
      setError({ message: `The label was not removed: ${message}`, code: code ?? null });
    },
    onSettled: () => invalidateAfterLabel(queryClient, { traceId, evaluatorIds: given.map((l) => evalRefId(l.target)) }),
  });

  if (targets.length === 0) return null;

  const checks = targets.map((ref) => evaluatorName(ref, names)).join(', ');
  const reason = writeReason ?? bodyReason(body);
  const kinds = [...new Set(given.map((l) => l.label))];
  const latest = given.reduce<LabelView | null>((a, l) => (!a || l.created_at > a.created_at ? l : a), null);
  const otherKinds = [...new Set(others.map((l) => l.label))];
  const otherLatest = others.reduce<LabelView | null>((a, l) => (!a || l.created_at > a.created_at ? l : a), null);

  return (
    <div className="space-y-2 border-t border-border pt-3" data-testid="was-this-right">
      {given.length > 0 && latest ? (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
          <span className="inline-flex items-center gap-1.5 text-fg">
            <Check aria-hidden="true" className="size-4 text-fg-muted" />
            {kinds.length === 1 ? `Labelled ${evalLabelWord(kinds[0]).toLowerCase()}` : 'Labelled differently per check'}
            <span className="text-fg-muted">
              {' '}
              by {evalLabelerName(latest.labeled_by)} ·{' '}
              <time dateTime={latest.created_at} title={formatDateTime(latest.created_at)}>
                {formatRelativeTime(latest.created_at, now)}
              </time>
            </span>
          </span>
          {/* Undo waits for the save it would undo, so the delete never overtakes the write. */}
          <DisabledAction reason={writeReason}>
            {(p) => (
              <Button size="sm" variant="ghost" pending={undo.isPending || put.isPending} onClick={() => undo.mutate()} {...p}>
                <Undo2 aria-hidden="true" />
                Undo
              </Button>
            )}
          </DisabledAction>
        </div>
      ) : (
        <div className="space-y-1.5">
          {otherLatest ? (
            <p className="text-sm text-fg-muted">
              {otherKinds.length === 1 ? `Labelled ${evalLabelWord(otherKinds[0]).toLowerCase()}` : 'Labelled differently per check'} by{' '}
              {evalLabelerName(otherLatest.labeled_by)} ·{' '}
              <time dateTime={otherLatest.created_at} title={formatDateTime(otherLatest.created_at)}>
                {formatRelativeTime(otherLatest.created_at, now)}
              </time>
              . Your own label counts too.
            </p>
          ) : null}
          <div role="group" aria-label={copy.trace.was_this_right} className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium text-fg">{copy.trace.was_this_right}</span>
            <Button
              size="sm"
              disabled={Boolean(reason)}
              aria-describedby={reason ? reasonId : undefined}
              pending={put.isPending && put.variables === 'should_fire'}
              onClick={() => put.mutate('should_fire')}
            >
              {copy.trace.correct}
            </Button>
            <Button
              size="sm"
              disabled={Boolean(reason)}
              aria-describedby={reason ? reasonId : undefined}
              pending={put.isPending && put.variables === 'should_not_fire'}
              onClick={() => put.mutate('should_not_fire')}
            >
              {copy.trace.false_alarm}
            </Button>
            {reason ? <DisabledReason id={reasonId} reason={reason} /> : null}
          </div>
          <p className="text-xs text-fg-muted">
            Correct: the answer really had the problem. False alarm: it was fine. Counts toward calibrating {checks}.
          </p>
        </div>
      )}
      {error ? (
        <p role="alert" className="text-sm text-fg">
          {error.message}
          {error.code ? <span className="ml-1 font-mono text-2xs text-fg-subtle">{error.code}</span> : null}
        </p>
      ) : null}
    </div>
  );
}
