/**
 * "Adopt calibration" (SPEC §2.11 "Activation vs adoption"; P4-PLAN D62). Activating a calibration
 * changes nothing while a route reads raw scores; adopting it does: a new policy version moves the
 * route's thresholds for this check into calibrated space, at the recommended values. In order:
 *
 *   1. the fit (or an existing record), never active yet (done by the caller);
 *   2. Preview impact on past answers, with this record standing in for the active calibration
 *      (`calibration_ids`), so the preview shows what the adopted policy would do;
 *   3. the viewer confirms (Adopt stays disabled until the preview has loaded);
 *   4. #46b activates the record;
 *   5. PUT /api/policies saves the new thresholds (If-Match: the policy the preview used).
 *
 * A failed save after a successful activation says so plainly: activation alone changes nothing.
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { CircleAlert, RotateCw } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useDataSource } from '@/app/services';
import { DiffView } from '@/components/common/DiffView';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { describeError } from '@/components/common/describeError';
import { KeyValueList } from '@/components/common/KeyValue';
import { notify } from '@/components/common/notify';
import { useWriteReason } from '@/components/common/useWriteAccess';
import { DraftError, setEvaluatorThresholds } from '@/components/policies/policyDraft';
import { PreviewImpact } from '@/components/policies/PreviewImpact';
import { policyWriteReason, routeTitle } from '@/components/policies/routeWords';
import { Button } from '@/components/ui/button';
import { DialogFooter } from '@/components/ui/dialog';
import { Field } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { Select } from '@/components/ui/select';
import { Skeleton } from '@/components/ui/skeleton';
import { isApiError } from '@/data/errors';
import { policiesQuery, qkc } from '@/data/queries';
import { qkcEvaluators } from '@/data/queries/evaluators';
import { formatRisk } from '@/domain/format';
import { copy } from '@/domain/presentation';
import { evalCriticalNullText, evalQualityView } from '@/domain/words/evaluators';
import type { CalibrationRecommended, CalibrationRecordMeta, PolicyState, TimeWindow } from '@/types/api';

const PREVIEW_WINDOWS = [
  { value: '24h', label: '24h' },
  { value: '7d', label: '7d' },
  { value: '30d', label: '30d' },
] as const;

export interface AdoptPanelProps {
  /** "groundedness@1". */
  evaluatorRef: string;
  /** The check's display name. */
  name: string;
  /** Routes that run this check (active or watching). */
  routes: readonly string[];
  /** The route the fit used, when it used one. */
  initialRoute?: string;
  record: Pick<CalibrationRecordMeta, 'id' | 'active' | 'quality'>;
  recommended: CalibrationRecommended | null;
  simulated: boolean;
  onDone: (state: PolicyState) => void;
  onBack: () => void;
  /** "Back" or "Cancel". */
  backLabel?: string;
}

function thresholdText(value: number | null | undefined, space: 'raw' | 'calibrated' | null): string {
  if (value === null || value === undefined) return 'Never';
  return `${formatRisk(value)}${space ? ` (${space === 'calibrated' ? 'calibrated' : 'raw score'})` : ''}`;
}

export function AdoptPanel({ evaluatorRef, name, routes, initialRoute, record, recommended, simulated, onDone, onBack, backLabel = 'Back' }: AdoptPanelProps) {
  const ds = useDataSource();
  const queryClient = useQueryClient();
  const instanceReason = useWriteReason('admin');
  const policies = useQuery(policiesQuery(ds));
  const [route, setRoute] = useState(initialRoute && routes.includes(initialRoute) ? initialRoute : routes[0] ?? '');
  const [previewWindow, setPreviewWindow] = useState<TimeWindow>('7d');
  const [message, setMessage] = useState(`Adopt calibration #${record.id} for ${evaluatorRef} on ${route}`);
  const [step, setStep] = useState<'idle' | 'activating' | 'saving'>('idle');
  // Activation is not repeated when a save is retried.
  const [activated, setActivated] = useState(record.active);

  // Captured once per policy version the panel saw: the save sends it as If-Match.
  const state = policies.data;
  const current = state?.effective_routes.find((r) => r.name === route)?.evaluators.find((e) => e.ref === evaluatorRef) ?? null;
  const draft = useMemo((): { yaml: string; error: null } | { yaml: null; error: string } | null => {
    if (!state || !route || !recommended?.warn_at) return null;
    try {
      return {
        yaml: setEvaluatorThresholds(state.yaml, route, evaluatorRef, {
          space: 'calibrated',
          warn_at: recommended.warn_at.value,
          critical_at: recommended.critical_at?.value ?? null,
        }),
        error: null,
      };
    } catch (err) {
      return { yaml: null, error: err instanceof DraftError ? err.message : String(err) };
    }
  }, [state, route, evaluatorRef, recommended]);

  const preview = useQuery({
    queryKey: [...qkcEvaluators.all, 'adopt-preview', record.id, previewWindow, draft?.yaml ?? ''] as const,
    queryFn: ({ signal }) => ds.dryRunPolicy({ yaml: draft?.yaml ?? '', window: previewWindow, calibration_ids: [record.id] }, signal),
    enabled: Boolean(draft?.yaml),
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
  });

  const adopt = useMutation({
    mutationFn: async () => {
      if (!state || !draft?.yaml) throw new Error('Nothing to adopt.');
      if (!activated) {
        setStep('activating');
        await ds.activateCalibration(record.id);
        setActivated(true);
      }
      setStep('saving');
      return ds.savePolicy({ yaml: draft.yaml, message: message.trim() }, state.etag);
    },
    onSettled: () => {
      setStep('idle');
      void queryClient.invalidateQueries({ queryKey: qkcEvaluators.calibrationLists });
    },
    onSuccess: (next) => {
      queryClient.setQueryData(qkc.policies.state, next);
      void queryClient.invalidateQueries({ queryKey: qkc.policies.versions });
      void queryClient.invalidateQueries({ queryKey: qkc.evaluators.all });
      notify.success(`${routeTitle(route)} now uses the calibrated thresholds for ${name}`, { description: `Saved as policy version ${next.id}.` });
      onDone(next);
    },
  });

  const writeReason = policyWriteReason(state, instanceReason);
  const quality = evalQualityView(record.quality);
  let reason: Reason | null = writeReason;
  if (!reason && routes.length === 0) reason = { text: 'Attach this check to a route in Policies first.' };
  if (!reason && !recommended?.warn_at) reason = { text: 'This calibration recommends no flag threshold, so there is nothing to adopt.' };
  if (!reason && draft?.error) reason = { text: draft.error };
  if (!reason && (preview.isPending || preview.isFetching)) reason = { text: 'Wait for Preview impact to finish: Adopt runs it first.' };
  if (!reason && preview.isError) reason = { text: 'Preview impact failed. Try it again before adopting.' };
  if (!reason && message.trim() === '') reason = { text: 'Say what changed and why first.' };

  const failure = adopt.error ? describeError(adopt.error) : null;
  const stale = isApiError(adopt.error) && (adopt.error.status === 412 || adopt.error.code === 'stale_version');

  if (policies.isPending) return <Skeleton className="h-48 w-full" />;
  if (policies.isError || !state) {
    return (
      <div className="space-y-3">
        <p className="text-sm text-fg">The policy could not be read, so nothing can be adopted: {policies.error?.message}</p>
        <Button size="sm" onClick={() => void policies.refetch()}>
          <RotateCw aria-hidden="true" />
          Try again
        </Button>
      </div>
    );
  }

  return (
    <div className="space-y-5">
      <ol className="space-y-1 text-sm text-fg-muted">
        <li>1. Calibration #{record.id} is fitted ({quality.label}).</li>
        <li className={preview.data ? undefined : 'font-medium text-fg'}>2. Preview impact on past answers, as if it were adopted.</li>
        <li>3. Adopt: activate it and save a policy version with the calibrated thresholds.</li>
      </ol>

      {routes.length > 1 ? (
        <Field label="Route">
          <Select
            value={route}
            onValueChange={(v) => {
              if (!v) return;
              setRoute(v);
              setMessage(`Adopt calibration #${record.id} for ${evaluatorRef} on ${v}`);
            }}
            options={routes.map((r) => ({ value: r, label: routeTitle(r) }))}
            className="w-56"
          />
        </Field>
      ) : null}

      {!quality.canBlock ? (
        <p className="flex items-start gap-2 rounded-md border border-dashed border-border-strong px-3 py-2 text-sm text-fg">
          <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-warning" />
          <span>
            This calibration is {quality.label.toLowerCase()}: {quality.description} Blocking routes keep acting as Flag until a measured one is adopted.
          </span>
        </p>
      ) : null}

      <KeyValueList
        items={[
          { label: 'Flags from', value: `${thresholdText(current?.warn_at, current?.threshold_space ?? null)} → ${thresholdText(recommended?.warn_at?.value, 'calibrated')}`, numeric: true },
          {
            label: 'Blocks from',
            value: `${thresholdText(current?.critical_at, current?.threshold_space ?? null)} → ${thresholdText(recommended?.critical_at?.value ?? null, 'calibrated')}`,
            numeric: true,
            hint: recommended && !recommended.critical_at ? evalCriticalNullText(recommended.critical_null_reason) : undefined,
          },
        ]}
      />

      {draft?.yaml ? <DiffView label={`Changes to ${routeTitle(route)}`} before={state.yaml} after={draft.yaml} /> : null}

      <section aria-labelledby="adopt-preview-title" className="space-y-2 rounded-lg border border-border p-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h4 id="adopt-preview-title" className="text-sm font-semibold text-fg">
            {copy.policies.preview_impact}
          </h4>
          <SegmentedControl<TimeWindow> aria-label="Replay answers from" size="sm" options={PREVIEW_WINDOWS} value={previewWindow} onValueChange={setPreviewWindow} />
        </div>
        {!draft?.yaml ? (
          <p className="text-sm text-fg-muted">There is no change to preview.</p>
        ) : preview.isPending || preview.isFetching ? (
          <div role="status" className="space-y-2">
            <span className="sr-only">Replaying past answers…</span>
            <Skeleton className="h-5 w-2/3" />
            <Skeleton className="h-16 w-full" />
          </div>
        ) : preview.isError ? (
          <div className="space-y-2">
            <p className="text-sm text-fg">The preview failed: {preview.error.message}</p>
            <Button size="sm" onClick={() => void preview.refetch()}>
              <RotateCw aria-hidden="true" />
              Try the preview again
            </Button>
          </div>
        ) : (
          <PreviewImpact result={preview.data} simulated={simulated} />
        )}
      </section>

      <Field label="What changed and why" description="Kept with the policy version.">
        <Input value={message} onValueChange={setMessage} maxLength={500} />
      </Field>

      {stale ? (
        <p role="alert" className="text-sm text-fg">
          The policy changed while you were reviewing, so nothing was saved{activated ? ' (the calibration is active, which changes nothing on its own)' : ''}. Close this and adopt again to preview the latest policy.
        </p>
      ) : failure ? (
        <div role="alert" className="flex items-start gap-2 rounded-md border border-border bg-surface px-3 py-2">
          <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-danger" />
          <div className="min-w-0 space-y-0.5">
            <p className="text-sm text-fg">
              {activated ? 'The calibration is active, but the policy was not changed: ' : 'Nothing was changed: '}
              {failure.message}
            </p>
            {failure.code ? <p className="font-mono text-2xs text-fg-subtle">{failure.code}</p> : null}
          </div>
        </div>
      ) : null}

      <DialogFooter>
        <Button variant="secondary" onClick={onBack}>
          {backLabel}
        </Button>
        <DisabledAction reason={reason}>
          {(p) => (
            <Button variant="primary" pending={adopt.isPending} onClick={() => adopt.mutate()} {...p}>
              {step === 'activating' ? 'Activating…' : step === 'saving' ? 'Saving the policy…' : copy.evaluators.adopt_calibration}
            </Button>
          )}
        </DisabledAction>
      </DialogFooter>
    </div>
  );
}
