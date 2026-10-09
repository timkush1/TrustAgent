/**
 * The label queue (SPEC §3.3 Evaluators; D59-D61): one answer at a time, the most uncertain
 * first (the server orders it by distance to the flag line, D60), so it holds answers the check
 * flagged and answers it let through. For each the viewer answers "Should <check> flag this
 * answer?": Yes, it has the problem (C, `should_fire`), No, it is fine (F, `should_not_fire`) or
 * Skip (S), with an optional note. Each label counts toward calibrating that check, so its
 * progress moves at once (optimistic, rolled back if the save fails), and the last label can be
 * undone.
 *
 * Keys work anywhere on the page except while typing in a field. After every step the focus moves
 * to the next answer's heading, so keyboard and screen-reader users keep their place.
 *
 * States: admin-only (a reason with the fix), loading, error with Retry, not served (an older
 * proxy), an empty queue, a finished queue, an expired body (409 `body_expired`: said, then moved
 * on) and a redacted answer (no text to judge: Skip only).
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { ArrowUpRight, CheckCheck, EyeOff, ListChecks, RotateCw, Undo2 } from 'lucide-react';
import { useEffect, useEffectEvent, useMemo, useRef, useState } from 'react';
import { useDataSource } from '@/app/services';
import { DegradedState } from '@/components/common/DegradedState';
import { DisabledAction, DisabledReason, type Reason } from '@/components/common/DisabledReason';
import { describeError } from '@/components/common/describeError';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState } from '@/components/common/StateViews';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { useNow } from '@/components/common/useNow';
import { useWriteReason } from '@/components/common/useWriteAccess';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Field } from '@/components/ui/field';
import { Textarea } from '@/components/ui/input';
import { Kbd } from '@/components/ui/kbd';
import { Select } from '@/components/ui/select';
import { Skeleton } from '@/components/ui/skeleton';
import { isApiError } from '@/data/errors';
import { evaluatorQuery, evaluatorsQuery, isNotServed, qkc, statusQuery } from '@/data/queries';
import { bumpProgress, invalidateAfterLabel, LABEL_QUEUE_DEFAULTS, labelQueueQuery } from '@/data/queries/evaluators';
import { useAppMode } from '@/data/useSystem';
import { formatCount, formatDateTime, formatRelativeTime, formatRisk } from '@/domain/format';
import { copy, fillTemplate, levelWord } from '@/domain/presentation';
import { evalLabelsStillNeeded, evalQueueAnswer, evalRefId, evalSavedText, type EvalFireLabel } from '@/domain/words/evaluators';
import type { EvaluatorDetail, LabelQueueItem } from '@/types/api';
import { CalibrationMeter } from './CalibrationMeter';
import { EvaluatorsTabs } from './EvaluatorsTabs';

export interface LabelQueueSearch {
  /** "<id>@<version>": only this check's answers. */
  evaluator?: string;
}

type Action = EvalFireLabel | 'skip';

/** The label keys: Y and N match the buttons' words, and C, F and S (SPEC §3.3) work too. */
const LABEL_KEYS: Readonly<Record<string, Action>> = { y: 'should_fire', c: 'should_fire', n: 'should_not_fire', f: 'should_not_fire', s: 'skip' };

interface Done {
  index: number;
  item: LabelQueueItem;
  label: EvalFireLabel;
}

type Status =
  | { kind: 'saved'; text: string; undo: Done }
  | { kind: 'undone'; text: string }
  | { kind: 'skipped'; text: string }
  | { kind: 'expired'; text: string }
  | { kind: 'error'; text: string; code: string | null };

const isRedacted = (item: LabelQueueItem) => item.user_request === null && item.response_text === null;

/**
 * Typing in a field (the note), a focused picker (its letters jump to an option) or working in a
 * dialog never triggers a shortcut.
 */
function ignoreKey(event: KeyboardEvent): boolean {
  if (event.altKey || event.ctrlKey || event.metaKey || event.repeat) return true;
  const target = event.target as HTMLElement | null;
  if (!target) return false;
  if (target.closest('input, textarea, select, [contenteditable="true"], [role="combobox"], [role="dialog"], [role="alertdialog"], [role="listbox"], [role="menu"]')) return true;
  return false;
}

function useEvaluatorNames() {
  const ds = useDataSource();
  const q = useQuery({ ...evaluatorsQuery(ds), retry: false });
  return useMemo(() => q.data?.items ?? [], [q.data]);
}

function nameOf(ref: string, items: readonly { id: string; name: string }[]): string {
  const id = evalRefId(ref);
  return items.find((e) => e.id === id)?.name ?? id;
}

function ItemView({
  item,
  checkName,
  position,
  total,
  headingRef,
}: {
  item: LabelQueueItem;
  checkName: string;
  position: number;
  total: number;
  headingRef: React.RefObject<HTMLHeadingElement | null>;
}) {
  const now = useNow();
  const { demo } = useAppMode();
  const level = item.level ? levelWord(item.level) : null;
  const sources = item.evidence_labels.length > 0 ? fillTemplate(copy.trace.checked_against, { sources: item.evidence_labels.join(' · ') }) : 'No documents came with this request.';
  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <p className="text-xs text-fg-muted tabular">
          Answer {formatCount(position)} of {formatCount(total)}
        </p>
        <h2 ref={headingRef} tabIndex={-1} className="text-lg font-semibold text-fg outline-none">
          Should {checkName} flag this answer?
        </h2>
        <p className="text-xs text-fg-muted">
          <span className="font-mono">{item.route}</span> ·{' '}
          <time dateTime={item.created_at} title={formatDateTime(item.created_at)}>
            {formatRelativeTime(item.created_at, now)}
          </time>
          {item.risk_raw !== null ? (
            <>
              {' '}
              · The fast check said {level ? level.label.toLowerCase() : 'risk'} <span className="tabular">{formatRisk(item.risk_raw)}</span> (raw score
              {/* The zero-key demo's fast check is a mock: its scores are made up. */}
              {demo ? ` · ${copy.value_labels.simulated.toLowerCase()}` : ''})
            </>
          ) : null}
        </p>
      </div>
      {isRedacted(item) ? (
        <DegradedState
          title="This answer's text is hidden"
          reason="It was redacted before it was stored, so there is nothing to judge here."
          stillWorks="Skip it, or open the request to see what was kept."
        />
      ) : (
        <div className="space-y-3">
          <div className="space-y-1">
            <h3 className="text-xs font-medium text-fg-muted">Question</h3>
            <p className="text-sm whitespace-pre-wrap text-fg">{item.user_request ?? 'Not stored.'}</p>
          </div>
          <div className="space-y-1">
            <h3 className="text-xs font-medium text-fg-muted">Answer</h3>
            <p className="rounded-md border border-border bg-surface-sunken px-3 py-2 text-sm whitespace-pre-wrap text-fg">{item.response_text ?? 'Not stored.'}</p>
          </div>
          <p className="text-xs text-fg-muted">{sources}</p>
        </div>
      )}
      <Link
        to="/app/traffic/$traceId"
        params={{ traceId: item.trace_id }}
        className="inline-flex items-center gap-1 text-xs font-medium text-fg underline underline-offset-2"
      >
        Open the full request
        <ArrowUpRight aria-hidden="true" className="size-3" />
      </Link>
    </div>
  );
}

function ProgressCard({ evaluatorId }: { evaluatorId: string }) {
  const ds = useDataSource();
  const q = useQuery({ ...evaluatorQuery(ds, evaluatorId), retry: false });
  return (
    <Card>
      <CardHeader title={q.data ? `${q.data.name}: calibration` : 'Calibration'} description="Each label counts toward calibrating this check for blocking." />
      <CardContent>
        {q.isPending ? (
          <Skeleton className="h-20 w-full" />
        ) : q.isError ? (
          <p className="text-xs text-fg-muted">Progress is not available: {q.error.message}</p>
        ) : (
          <div className="space-y-3">
            <CalibrationMeter progress={q.data.calibration_progress} showActions={false} />
            <Link to="/app/evaluators/$id" params={{ id: evaluatorId }} search={{ tab: 'calibration' }} className="inline-flex text-xs font-medium text-fg underline underline-offset-2">
              Calibrate {q.data.name}
            </Link>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function QueueSession({ items, names, onReload }: { items: readonly LabelQueueItem[]; names: readonly { id: string; name: string }[]; onReload: () => void }) {
  const ds = useDataSource();
  const queryClient = useQueryClient();
  const [index, setIndex] = useState(0);
  const [note, setNote] = useState('');
  const [status, setStatus] = useState<Status | null>(null);
  const [tally, setTally] = useState({ labelled: 0, skipped: 0 });
  const headingRef = useRef<HTMLHeadingElement | null>(null);
  const focusNext = useRef(false);

  const current = index < items.length ? items[index] : null;

  useEffect(() => {
    if (!focusNext.current) return;
    focusNext.current = false;
    headingRef.current?.focus();
  }, [index, current]);

  const go = (next: number) => {
    focusNext.current = true;
    setIndex(next);
    setNote('');
  };

  /**
   * Moves this check's progress by one label at once. The progress counts one evaluator version, so
   * a label for another version (an older one still in the window) is left to the refetch.
   */
  const moveProgress = (item: LabelQueueItem, label: EvalFireLabel, by: 1 | -1) => {
    const key = qkc.evaluators.detail(evalRefId(item.evaluator_ref));
    queryClient.setQueryData<EvaluatorDetail>(key, (d) =>
      d && d.calibration_progress.evaluator_ref === item.evaluator_ref ? { ...d, calibration_progress: bumpProgress(d.calibration_progress, label, by) } : d,
    );
  };

  const stillNeeded = (item: LabelQueueItem) => {
    const d = queryClient.getQueryData<EvaluatorDetail>(qkc.evaluators.detail(evalRefId(item.evaluator_ref)));
    return d ? evalLabelsStillNeeded(d.calibration_progress) : null;
  };

  const put = useMutation({
    mutationFn: (v: { item: LabelQueueItem; label: EvalFireLabel; note: string }) =>
      ds.putLabel({ trace_id: v.item.trace_id, target: v.item.evaluator_ref, label: v.label, ...(v.note ? { note: v.note } : {}) }),
  });
  const del = useMutation({
    mutationFn: (done: Done) => ds.deleteLabel({ trace_id: done.item.trace_id, target: done.item.evaluator_ref }),
  });

  const label = (item: LabelQueueItem, at: number, value: EvalFireLabel) => {
    const text = note.trim();
    moveProgress(item, value, 1);
    go(at + 1);
    // Per-call promise handlers, not mutate()'s callbacks: those fire only for the latest call, so a
    // quick C, C would drop the first label's rollback (and its body_expired) without them.
    put.mutateAsync({ item, label: value, note: text }).then(
      () => {
        setTally((t) => ({ ...t, labelled: t.labelled + 1 }));
        setStatus({ kind: 'saved', text: `${evalQueueAnswer(value)}. ${evalSavedText(stillNeeded(item))}`, undo: { index: at, item, label: value } });
        invalidateAfterLabel(queryClient, { traceId: item.trace_id, evaluatorIds: [evalRefId(item.evaluator_ref)] });
      },
      (err: unknown) => {
        moveProgress(item, value, -1);
        // A refetch that landed while this label was in flight never counted it: read the truth again.
        void queryClient.invalidateQueries({ queryKey: qkc.evaluators.detail(evalRefId(item.evaluator_ref)) });
        if (isApiError(err) && err.code === 'body_expired') {
          setStatus({ kind: 'expired', text: `${copy.labels.body_expired} It was left out.` });
          return;
        }
        const { message, code } = describeError(err);
        setStatus({ kind: 'error', text: `The label was not saved: ${message}`, code: code ?? null });
        go(at);
        setNote(text);
      },
    );
  };

  const act = (action: Action) => {
    if (!current) return;
    if (action === 'skip') {
      setTally((t) => ({ ...t, skipped: t.skipped + 1 }));
      setStatus({ kind: 'skipped', text: 'Skipped. It stays in the queue for next time.' });
      go(index + 1);
      return;
    }
    if (isRedacted(current)) return;
    label(current, index, action);
  };

  const undo = (done: Done) => {
    del.mutate(done, {
      onSuccess: () => {
        moveProgress(done.item, done.label, -1);
        setTally((t) => ({ ...t, labelled: Math.max(0, t.labelled - 1) }));
        setStatus({ kind: 'undone', text: `Label removed: ${nameOf(done.item.evaluator_ref, names)} is back for you to judge again.` });
        invalidateAfterLabel(queryClient, { traceId: done.item.trace_id, evaluatorIds: [evalRefId(done.item.evaluator_ref)] });
        go(done.index);
      },
      onError: (err) => {
        const { message, code } = describeError(err);
        setStatus({ kind: 'error', text: `The label was not removed: ${message}`, code: code ?? null });
      },
    });
  };

  // The listener stays put; the event reads this render's item, index and note.
  const onKey = useEffectEvent((event: KeyboardEvent) => {
    if (ignoreKey(event)) return;
    const action: Action | null = LABEL_KEYS[event.key.toLowerCase()] ?? null;
    if (!action) return;
    event.preventDefault();
    act(action);
  });
  useEffect(() => {
    const listener = (event: KeyboardEvent) => onKey(event);
    document.addEventListener('keydown', listener);
    return () => document.removeEventListener('keydown', listener);
  }, []);

  const redactedReason: Reason | null = current && isRedacted(current) ? { text: 'The text is hidden, so there is nothing to judge.' } : null;

  const statusLine = status ? (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
      <span className={status.kind === 'error' ? 'text-fg' : 'text-fg-muted'}>
        {status.text}
        {status.kind === 'error' && status.code ? <span className="ml-1 font-mono text-2xs text-fg-subtle">{status.code}</span> : null}
      </span>
      {status.kind === 'saved' ? (
        <Button size="sm" variant="ghost" pending={del.isPending} onClick={() => undo(status.undo)}>
          <Undo2 aria-hidden="true" />
          Undo
        </Button>
      ) : null}
    </div>
  ) : null;

  return (
    <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_20rem]">
      <div className="space-y-4">
        {current ? (
          <Card>
            <CardContent className="space-y-5 pt-4">
              <ItemView item={current} checkName={nameOf(current.evaluator_ref, names)} position={index + 1} total={items.length} headingRef={headingRef} />
              <Field label="Note" optional description="Kept with the label, for whoever calibrates next.">
                <Textarea value={note} onChange={(e) => setNote(e.currentTarget.value)} rows={2} maxLength={500} disabled={Boolean(redactedReason)} />
              </Field>
              <div className="space-y-2">
                <div role="group" aria-label="Label this answer" className="flex flex-wrap items-center gap-2">
                  <DisabledAction reason={redactedReason}>
                    {(p) => (
                      <Button variant="primary" onClick={() => act('should_fire')} aria-keyshortcuts="Y C" {...p}>
                        {copy.labels.yes}
                        <Kbd>Y</Kbd>
                      </Button>
                    )}
                  </DisabledAction>
                  <DisabledAction reason={redactedReason}>
                    {(p) => (
                      <Button onClick={() => act('should_not_fire')} aria-keyshortcuts="N F" {...p}>
                        {copy.labels.no}
                        <Kbd>N</Kbd>
                      </Button>
                    )}
                  </DisabledAction>
                  <Button variant="ghost" onClick={() => act('skip')} aria-keyshortcuts="S">
                    {copy.labels.skip}
                    <Kbd>S</Kbd>
                  </Button>
                </div>
                <p className="text-xs text-fg-muted">
                  {copy.labels.yes_no_hint} {copy.labels.keys_hint}.
                </p>
              </div>
            </CardContent>
          </Card>
        ) : (
          <EmptyState icon={CheckCheck} title="You're through the queue" action={
            <Button size="sm" onClick={onReload}>
              <RotateCw aria-hidden="true" />
              Check for more
            </Button>
          }>
            <p>New answers near the flag line show up as traffic arrives.</p>
          </EmptyState>
        )}
        {/* Under the card, beside the session's tally, so the answer card's top lines up with the
            calibration card's. Announced politely; an error is announced at once. */}
        <div role={status?.kind === 'error' ? 'alert' : 'status'} aria-live={status?.kind === 'error' ? 'assertive' : 'polite'} className="min-h-6">
          {statusLine}
        </div>
        <p className="text-xs text-fg-muted tabular">
          This session: {formatCount(tally.labelled)} labelled · {formatCount(tally.skipped)} skipped
        </p>
      </div>
      {current ? <ProgressCard evaluatorId={evalRefId(current.evaluator_ref)} /> : null}
    </div>
  );
}

export function LabelQueuePage({ search }: { search: LabelQueueSearch }) {
  const ds = useDataSource();
  const navigate = useNavigate({ from: '/app/evaluators/labels' });
  const writeReason = useWriteReason('admin');
  const evaluators = useEvaluatorNames();
  const params = useMemo(() => ({ ...LABEL_QUEUE_DEFAULTS, ...(search.evaluator ? { evaluator: search.evaluator } : {}) }), [search.evaluator]);
  // Admin-only: wait until /api/status has answered (who the caller is), so a reader never sends it.
  const statusQ = useQuery(statusQuery(ds));
  const q = useQuery({ ...labelQueueQuery(ds, params), enabled: !writeReason && !statusQ.isPending });
  const showLoading = useDelayedLoading(q.isPending && !writeReason);

  const attached = evaluators.filter((e) => !e.archived && e.attached_routes.length > 0);
  const filter = (
    <Select<string>
      aria-label="Check"
      value={search.evaluator ?? 'all'}
      onValueChange={(v) => void navigate({ search: { evaluator: !v || v === 'all' ? undefined : v } })}
      options={[
        { value: 'all', label: 'All checks' },
        ...attached.map((e) => ({ value: `${e.id}@${e.latest_version}`, label: e.name })),
        ...(search.evaluator && !attached.some((e) => `${e.id}@${e.latest_version}` === search.evaluator)
          ? [{ value: search.evaluator, label: nameOf(search.evaluator, evaluators) }]
          : []),
      ]}
      className="w-56"
    />
  );

  let body;
  if (writeReason) {
    const readonly = writeReason === 'readonly';
    body = (
      <EmptyState icon={ListChecks} title={readonly ? 'Labelling is off on a read-only instance' : 'Labelling needs an admin key'}>
        <p>Labels change how checks are calibrated, so only admins can add them{readonly ? ', on an instance that accepts changes' : ''}.</p>
        <p className="mt-2">
          <DisabledReason reason={writeReason} />
        </p>
      </EmptyState>
    );
  } else if (q.isPending) {
    body = showLoading ? (
      <div aria-busy="true" className="space-y-3">
        <span role="status" className="sr-only">
          {copy.states.loading}
        </span>
        <Skeleton className="h-64 w-full" />
      </div>
    ) : null;
  } else if (q.isError) {
    const status = isApiError(q.error) ? q.error.status : 0;
    body =
      status === 401 || status === 403 ? (
        <EmptyState icon={ListChecks} title="Labelling needs an admin key">
          <p>{q.error.message}</p>
          <p className="mt-2">
            <DisabledReason reason="needs_admin" />
          </p>
        </EmptyState>
      ) : isApiError(q.error) && q.error.code === 'persistence_disabled' ? (
        <DegradedState
          title="Labelling needs a database"
          reason="This proxy runs without Postgres, so it keeps no answers to label and no labels."
          stillWorks="Checks still run, and calibration files in TRUTHTABLE_CALIBRATION_DIR still apply."
        />
      ) : isApiError(q.error) && q.error.code === 'not_implemented' ? (
        <DegradedState title="Labelling needs a running gateway" reason={q.error.message} />
      ) : isNotServed(q.error) ? (
        <DegradedState
          title="The label queue is not available on this proxy"
          reason="It does not serve GET /api/labels/queue yet (an older version)."
          stillWorks="Calibration progress and reliability still show the labels you already have."
        />
      ) : (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      );
  } else if (q.data.items.length === 0) {
    body = (
      <EmptyState icon={EyeOff} title={copy.labels.empty} action={
        <Button size="sm" onClick={() => void q.refetch()}>
          <RotateCw aria-hidden="true" />
          Check again
        </Button>
      }>
        <p>
          Only answers whose text is still stored and that you have not labelled yet appear here.{' '}
          <Link to="/app/traffic" className="font-medium text-fg underline underline-offset-2">
            See recent traffic
          </Link>
        </p>
      </EmptyState>
    );
  } else {
    body = <QueueSession key={q.dataUpdatedAt} items={q.data.items} names={evaluators} onReload={() => void q.refetch()} />;
  }

  return (
    <div>
      <EvaluatorsTabs current="labels" />
      <PageHeader description={copy.labels.intro} actions={writeReason ? null : filter} />
      {body}
    </div>
  );
}
