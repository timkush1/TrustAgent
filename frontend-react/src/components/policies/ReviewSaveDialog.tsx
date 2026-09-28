/**
 * Review and save a policy draft (SPEC §3.3 Policies, §2.4 #22, #23, #26): the diff, the server's
 * validation (errors, warnings, block readiness), "Preview impact (free — replays past answers)",
 * a message, and the save itself with `If-Match`, so an edit made elsewhere in the meantime is
 * never overwritten silently (412 stale_version).
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { CircleAlert, RotateCw, TriangleAlert } from 'lucide-react';
import { useState } from 'react';
import { useDataSource } from '@/app/services';
import { DiffView } from '@/components/common/DiffView';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { describeError } from '@/components/common/describeError';
import { notify } from '@/components/common/notify';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent } from '@/components/ui/dialog';
import { Field } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { Skeleton } from '@/components/ui/skeleton';
import { isApiError } from '@/data/errors';
import { qkc } from '@/data/queries';
import { readinessWords } from '@/domain/controlPresentation';
import { copy } from '@/domain/presentation';
import type { FieldError, PolicyState, TimeWindow } from '@/types/api';
import { PreviewImpact } from './PreviewImpact';
import { evaluatorDisplayName, routeTitle } from './routeWords';

const PREVIEW_WINDOWS = [
  { value: '24h', label: '24h' },
  { value: '7d', label: '7d' },
  { value: '30d', label: '30d' },
] as const;

export interface ReviewSaveDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  state: PolicyState;
  draftYaml: string;
  /**
   * The ETag of the policy version the draft was started from. The save sends it as If-Match, so
   * a version saved elsewhere in the meantime (already shown here through a refetch) is answered
   * with 412 instead of being overwritten by a draft that never saw it.
   */
  baseEtag: string;
  names: ReadonlyMap<string, string>;
  /** Why saving is not possible (read-only, not an admin, file-managed, locked). */
  writeReason: Reason | null;
  /** Why read-scope POSTs (validate, preview) are not possible (read-only). */
  postReason: Reason | null;
  simulated: boolean;
  onSaved: (state: PolicyState) => void;
  /** The server has a newer policy than the draft's base: reload and drop the draft. */
  onReloadLatest: () => void;
}

function Problems({ title, items, tone }: { title: string; items: readonly FieldError[]; tone: 'error' | 'warning' }) {
  if (items.length === 0) return null;
  const Icon = tone === 'error' ? CircleAlert : TriangleAlert;
  return (
    <div role={tone === 'error' ? 'alert' : undefined} className="space-y-1">
      <h4 className="text-xs font-medium text-fg-muted">{title}</h4>
      <ul className="space-y-1">
        {items.map((e, i) => (
          <li key={`${e.path}-${i}`} className="flex items-start gap-1.5 text-sm text-fg">
            <Icon aria-hidden="true" className={tone === 'error' ? 'mt-0.5 size-3.5 shrink-0 text-tone-danger' : 'mt-0.5 size-3.5 shrink-0 text-tone-warning'} />
            <span>
              {e.line ? <span className="font-mono text-xs text-fg-muted">line {e.line}{e.col ? `:${e.col}` : ''} · </span> : null}
              {e.path ? <span className="font-mono text-xs text-fg-muted">{e.path} · </span> : null}
              {e.message}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

export function ReviewSaveDialog({
  open,
  onOpenChange,
  state,
  draftYaml,
  baseEtag,
  names,
  writeReason,
  postReason,
  simulated,
  onSaved,
  onReloadLatest,
}: ReviewSaveDialogProps) {
  const ds = useDataSource();
  const queryClient = useQueryClient();
  const [message, setMessage] = useState('');
  const [previewWindow, setPreviewWindow] = useState<TimeWindow>('7d');

  const validation = useQuery({
    queryKey: qkc.policyReview.validate(draftYaml),
    queryFn: ({ signal }) => ds.validatePolicy({ yaml: draftYaml }, signal),
    enabled: open && !postReason,
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
  });

  const preview = useQuery({
    queryKey: qkc.policyReview.dryRun(previewWindow, draftYaml),
    queryFn: ({ signal }) => ds.dryRunPolicy({ yaml: draftYaml, window: previewWindow }, signal),
    enabled: open && !postReason,
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
  });

  const save = useMutation({
    mutationFn: () => ds.savePolicy({ yaml: draftYaml, message: message.trim() }, baseEtag),
    onSuccess: (next) => {
      queryClient.setQueryData(qkc.policies.state, next);
      void queryClient.invalidateQueries({ queryKey: qkc.policies.versions });
      void queryClient.invalidateQueries({ queryKey: qkc.evaluators.all });
      notify.success(`Saved policy version ${next.id}`);
      setMessage('');
      onSaved(next);
      onOpenChange(false);
    },
  });

  const errors = validation.data?.errors ?? [];
  const serverFieldErrors = isApiError(save.error) ? save.error.fieldErrors : [];
  const stale = isApiError(save.error) && (save.error.status === 412 || save.error.code === 'stale_version');
  const notReady = Object.entries(validation.data?.block_readiness ?? {}).flatMap(([route, items]) =>
    items.filter((b) => !b.ready).map((b) => ({ route, ...b })),
  );

  let saveReason: Reason | null = writeReason;
  if (!saveReason && errors.length > 0) saveReason = { text: 'Fix the errors above first.' };
  if (!saveReason && preview.isFetching) saveReason = { text: 'Wait for the preview to finish.' };
  if (!saveReason && message.trim() === '') saveReason = { text: 'Say what changed and why first.' };

  const failure = save.error && !stale ? describeError(save.error) : null;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        size="lg"
        title="Review and save a new policy version"
        description="Check the change, see what it would have done to past answers, then save it as a new version. Every version can be rolled back."
        footer={
          <>
            <Button variant="secondary" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <DisabledAction reason={saveReason}>
              {(p) => (
                <Button variant="primary" pending={save.isPending} onClick={() => save.mutate()} {...p}>
                  Save version
                </Button>
              )}
            </DisabledAction>
          </>
        }
      >
        <div className="space-y-5">
          <section aria-label="Changes" className="space-y-2">
            {validation.data?.diff ? (
              <DiffView label="Changes to the policy" unified={validation.data.diff} />
            ) : (
              <DiffView label="Changes to the policy" before={state.yaml} after={draftYaml} />
            )}
          </section>

          <section aria-label="Checks on the draft" className="space-y-2">
            {postReason ? null : validation.isPending ? (
              <Skeleton className="h-6 w-1/2" />
            ) : validation.isError ? (
              <p className="text-sm text-fg-muted">The server could not check the draft: {validation.error.message}</p>
            ) : (
              <>
                {errors.length === 0 ? <p className="text-sm text-fg">The server accepts this policy.</p> : null}
                <Problems title="Errors" items={errors} tone="error" />
                <Problems title="Warnings" items={validation.data.warnings} tone="warning" />
                {notReady.length > 0 ? (
                  <div className="space-y-1">
                    <h4 className="text-xs font-medium text-fg-muted">Not ready to block yet (these routes will act as Flag)</h4>
                    <ul className="space-y-0.5 text-sm text-fg">
                      {notReady.map((b) => (
                        <li key={`${b.route}-${b.evaluator}`}>
                          <span className="font-medium">{routeTitle(b.route)}</span> · {evaluatorDisplayName(b.evaluator, names)}:{' '}
                          {b.missing.map((m) => readinessWords(m).label.toLowerCase()).join(', ')}
                        </li>
                      ))}
                    </ul>
                  </div>
                ) : null}
              </>
            )}
          </section>

          <section aria-labelledby="preview-impact-title" className="space-y-2 rounded-lg border border-border p-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h4 id="preview-impact-title" className="text-sm font-semibold text-fg">
                {copy.policies.preview_impact}
              </h4>
              <SegmentedControl<TimeWindow>
                aria-label="Replay answers from"
                size="sm"
                options={PREVIEW_WINDOWS}
                value={previewWindow}
                onValueChange={setPreviewWindow}
                disabled={Boolean(postReason)}
              />
            </div>
            {postReason ? (
              <p className="text-sm text-fg-muted">The preview is not available on a read-only instance.</p>
            ) : preview.isPending || preview.isFetching ? (
              <div role="status" className="space-y-2">
                <span className="sr-only">Replaying past answers…</span>
                <Skeleton className="h-5 w-2/3" />
                <Skeleton className="h-16 w-full" />
              </div>
            ) : preview.isError ? (
              <div className="space-y-2">
                <p className="text-sm text-fg">
                  The preview failed: {preview.error.message}
                  {isApiError(preview.error) && preview.error.code === 'rate_limited' ? ' (at most 6 previews a minute)' : ''}
                </p>
                <Button size="sm" onClick={() => void preview.refetch()}>
                  <RotateCw aria-hidden="true" />
                  Try the preview again
                </Button>
                <p className="text-xs text-fg-muted">You can still save without a preview.</p>
              </div>
            ) : (
              <PreviewImpact result={preview.data} simulated={simulated} />
            )}
          </section>

          <Field label="What changed and why" description="Kept with the version, so the history explains itself." error={serverFieldErrors.length ? serverFieldErrors[0].message : undefined}>
            <Input value={message} onValueChange={setMessage} maxLength={500} placeholder="Block contradictions on payments-faq" />
          </Field>

          {stale ? (
            <div role="alert" className="space-y-2 rounded-md border border-border bg-surface px-3 py-2">
              <p className="flex items-start gap-2 text-sm text-fg">
                <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-danger" />
                Someone changed the policy while you were editing (it is now a newer version), so your draft was not saved.
              </p>
              <Button
                size="sm"
                onClick={() => {
                  onOpenChange(false);
                  onReloadLatest();
                }}
              >
                Load the latest policy (discards this draft)
              </Button>
            </div>
          ) : null}
          {failure ? (
            <div role="alert" className="flex items-start gap-2 rounded-md border border-border bg-surface px-3 py-2">
              <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-danger" />
              <div className="min-w-0 space-y-0.5">
                <p className="text-sm text-fg">{failure.message}</p>
                {failure.code ? <p className="font-mono text-2xs text-fg-subtle">{failure.code}</p> : null}
              </div>
            </div>
          ) : null}
        </div>
      </DialogContent>
    </Dialog>
  );
}
