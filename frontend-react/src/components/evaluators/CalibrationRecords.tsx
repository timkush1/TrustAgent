/**
 * The calibration records of one evaluator (SPEC §2.4 #42-#46b; D55, D62): every calibration
 * the proxy holds for it (from files, imported, or fitted from labels), which one is active for
 * each fast-check version, and the actions on them: deactivate an imported or fitted one (the
 * newest file takes over again), adopt one, or import a calibration file (`make calibrate` writes
 * them). The server re-derives each import's quality, so a file cannot claim to be Measured.
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ChevronRight, CircleAlert, FileUp, Gauge } from 'lucide-react';
import { useId, useRef, useState } from 'react';
import { useDataSource } from '@/app/services';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { DegradedState } from '@/components/common/DegradedState';
import { DisabledAction } from '@/components/common/DisabledReason';
import { describeError } from '@/components/common/describeError';
import { notify } from '@/components/common/notify';
import { EmptyState, ErrorState } from '@/components/common/StateViews';
import { useNow } from '@/components/common/useNow';
import { useWriteReason } from '@/components/common/useWriteAccess';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent } from '@/components/ui/dialog';
import { Section } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { isApiError } from '@/data/errors';
import { isNotServed, qkc } from '@/data/queries';
import { calibrationQuery, calibrationsQuery, qkcEvaluators } from '@/data/queries/evaluators';
import { useAppMode } from '@/data/useSystem';
import { formatDateTime, formatRelativeTime } from '@/domain/format';
import { commandText, copy } from '@/domain/presentation';
import { evalCalibrationOriginLabel, evalImportRejectionText, evalQualityView, evalRefId, evalTargetRef } from '@/domain/words/evaluators';
import type { CalibrationFile, CalibrationImportResult, CalibrationRecordMeta, EvaluatorDetail } from '@/types/api';
import { AdoptPanel } from './AdoptPanel';

function AdoptRecordDialog({ detail, meta, onClose }: { detail: EvaluatorDetail; meta: CalibrationRecordMeta; onClose: () => void }) {
  const ds = useDataSource();
  const { demo } = useAppMode();
  const record = useQuery(calibrationQuery(ds, meta.id));
  const routes = [...new Set(detail.attached_routes.map((r) => r.route))];
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent
        size="lg"
        className="w-[min(52rem,calc(100vw-2rem))]"
        title={`${copy.evaluators.adopt_calibration}: ${detail.name}`}
        description={`Calibration #${meta.id}, ${evalCalibrationOriginLabel(meta.origin).toLowerCase()}, for ${meta.snapshot}.`}
      >
        {record.isPending ? (
          <Skeleton className="h-40 w-full" />
        ) : record.isError ? (
          <ErrorState error={record.error} onRetry={() => void record.refetch()} />
        ) : (
          <AdoptPanel
            evaluatorRef={evalTargetRef(meta.target_key)}
            name={detail.name}
            routes={routes}
            record={meta}
            recommended={record.data.entry.recommended ?? null}
            simulated={demo}
            onDone={onClose}
            onBack={onClose}
            backLabel="Cancel"
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

function ImportResult({ result }: { result: CalibrationImportResult }) {
  return (
    <div role="status" className="space-y-2 rounded-md border border-border px-3 py-2 text-sm">
      <p className="text-fg">
        Imported {result.imported.length} {result.imported.length === 1 ? 'calibration' : 'calibrations'}
        {result.rejected.length > 0 ? `; ${result.rejected.length} skipped` : ''}.
      </p>
      {result.imported.length > 0 ? (
        <ul className="space-y-0.5 text-xs text-fg-muted">
          {result.imported.map((r) => (
            <li key={r.id}>
              #{r.id} · {evalTargetRef(r.target_key)} · {evalQualityView(r.quality).label}
              {r.active ? ' · active' : ' · not active (another calibration is active for it)'}
            </li>
          ))}
        </ul>
      ) : null}
      {result.rejected.length > 0 ? (
        <ul className="space-y-1">
          {result.rejected.map((r, i) => (
            <li key={`${r.target_key ?? 'entry'}-${i}`} className="flex items-start gap-1.5 text-xs text-fg">
              <CircleAlert aria-hidden="true" className="mt-0.5 size-3.5 shrink-0 text-tone-warning" />
              <div className="min-w-0 space-y-0.5">
                <p>
                  {r.target_key ? <span className="font-mono">{evalTargetRef(r.target_key)}</span> : 'One entry'}: {evalImportRejectionText(r.reason)}
                </p>
                {r.message ? (
                  <details className="group/detail">
                    <summary className="inline-flex cursor-pointer list-none items-center gap-1 rounded-sm font-medium text-fg-muted hover:text-fg [&::-webkit-details-marker]:hidden">
                      <ChevronRight aria-hidden="true" className="size-3 transition-transform group-open/detail:rotate-90" />
                      Technical detail
                    </summary>
                    <p className="mt-0.5 break-words text-fg-muted">{r.message}</p>
                  </details>
                ) : null}
              </div>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function ImportAction({ disabledReason }: { disabledReason: ReturnType<typeof useWriteReason> }) {
  const ds = useDataSource();
  const queryClient = useQueryClient();
  const inputRef = useRef<HTMLInputElement>(null);
  const inputId = useId();
  const [parseError, setParseError] = useState<string | null>(null);
  const importFile = useMutation({
    mutationFn: (file: CalibrationFile) => ds.importCalibration({ file, activate: true }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: qkcEvaluators.calibrationLists });
      void queryClient.invalidateQueries({ queryKey: qkc.evaluators.all });
    },
  });

  const onFile = async (file: File | undefined) => {
    setParseError(null);
    importFile.reset();
    if (!file) return;
    let parsed: unknown;
    try {
      parsed = JSON.parse(await file.text());
    } catch {
      setParseError(`${file.name} is not JSON. A calibration file is the JSON that ${commandText('calibrate') ?? 'make calibrate'} writes.`);
      return;
    }
    if (typeof parsed !== 'object' || parsed === null || (parsed as { schema?: unknown }).schema !== 'trustagent.calibration/v1') {
      setParseError(`${file.name} is not a calibration file (trustagent.calibration/v1).`);
      return;
    }
    importFile.mutate(parsed as CalibrationFile);
  };

  const failure = importFile.error ? describeError(importFile.error) : null;
  return (
    <div className="space-y-2">
      <input
        ref={inputRef}
        id={inputId}
        type="file"
        accept="application/json,.json"
        className="sr-only"
        tabIndex={-1}
        aria-hidden="true"
        onChange={(e) => {
          void onFile(e.currentTarget.files?.[0]);
          e.currentTarget.value = '';
        }}
      />
      <DisabledAction reason={disabledReason}>
        {(p) => (
          <Button size="sm" pending={importFile.isPending} onClick={() => inputRef.current?.click()} {...p}>
            <FileUp aria-hidden="true" />
            Import a calibration file
          </Button>
        )}
      </DisabledAction>
      <p className="text-xs text-fg-muted">
        An imported calibration becomes the active one for its check and fast-check version. Routes that read raw scores are unchanged until you adopt it.
      </p>
      {parseError ? (
        <p role="alert" className="text-xs text-fg">
          {parseError}
        </p>
      ) : null}
      {failure ? (
        <p role="alert" className="text-xs text-fg">
          The import failed: {failure.message}
          {failure.code ? <span className="ml-1 font-mono text-2xs text-fg-subtle">{failure.code}</span> : null}
        </p>
      ) : null}
      {importFile.data ? <ImportResult result={importFile.data} /> : null}
    </div>
  );
}

function RecordRow({ meta, detail, writeReason, onAdopt }: { meta: CalibrationRecordMeta; detail: EvaluatorDetail; writeReason: ReturnType<typeof useWriteReason>; onAdopt: () => void }) {
  const ds = useDataSource();
  const queryClient = useQueryClient();
  const now = useNow();
  const quality = evalQualityView(meta.quality);
  const deactivate = useMutation({
    mutationFn: () => ds.deactivateCalibration(meta.id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: qkcEvaluators.calibrationLists });
      void queryClient.invalidateQueries({ queryKey: qkc.evaluators.detail(detail.id) });
      notify.success(`Calibration #${meta.id} is no longer active`);
    },
  });
  // A file row is never changed from here (D55): files are managed on disk.
  const deactivateReason = writeReason ?? (meta.origin === 'file' ? { text: 'This one comes from a calibration file: remove or replace the file instead.' } : null);
  // A calibration applies to one fast-check version: adopting one fitted for another would leave the
  // route reading raw scores against calibrated thresholds, capped to Flag (SPEC §2.11).
  const current = detail.calibration_progress.snapshot;
  const adoptReason =
    writeReason ??
    (current !== null && meta.snapshot !== current
      ? { text: `This one is for fast-check version ${meta.snapshot}; the check now runs on ${current}. Fit a new calibration for it.` }
      : null);
  return (
    <li className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2 px-4 py-3" data-calibration={meta.id}>
      <div className="min-w-0 space-y-1">
        <p className="flex flex-wrap items-center gap-2 text-sm">
          <span className="font-medium text-fg">Calibration #{meta.id}</span>
          <Badge size="sm" icon={quality.icon} emphasis="quiet">
            {quality.label}
          </Badge>
          {meta.active ? (
            <Badge size="sm" emphasis="quiet" icon={Gauge}>
              Active
            </Badge>
          ) : (
            <span className="text-xs text-fg-muted">Not active</span>
          )}
        </p>
        <p className="text-xs text-fg-muted">
          {evalCalibrationOriginLabel(meta.origin)} · <span className="font-mono">{evalTargetRef(meta.target_key)}</span> · fast check{' '}
          <span className="font-mono">{meta.snapshot}</span> ·{' '}
          <time dateTime={meta.created_at} title={formatDateTime(meta.created_at)}>
            {formatRelativeTime(meta.created_at, now)}
          </time>
        </p>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <DisabledAction reason={adoptReason} layout="stacked" className="items-end">
          {(p) => (
            <Button size="sm" onClick={onAdopt} {...p}>
              Adopt…
            </Button>
          )}
        </DisabledAction>
        {meta.active ? (
          <DisabledAction reason={deactivateReason} layout="stacked" className="items-end">
            {(p) => (
              <ConfirmDialog
                title={`Deactivate calibration #${meta.id}?`}
                description="The newest calibration file for this check and fast-check version takes over again, if there is one; otherwise it has no calibration. Routes that adopted calibrated thresholds read raw scores until another calibration is active, and blocking pauses there."
                confirmLabel="Deactivate"
                onConfirm={() => deactivate.mutateAsync()}
                trigger={
                  <Button size="sm" variant="ghost" {...p}>
                    Deactivate
                  </Button>
                }
              />
            )}
          </DisabledAction>
        ) : null}
      </div>
    </li>
  );
}

export function CalibrationRecords({ detail }: { detail: EvaluatorDetail }) {
  const ds = useDataSource();
  const writeReason = useWriteReason('admin');
  const q = useQuery(calibrationsQuery(ds, {}));
  const [adopting, setAdopting] = useState<CalibrationRecordMeta | null>(null);
  const mine = (q.data?.items ?? []).filter((r) => evalRefId(r.target_key) === detail.id).sort((a, b) => Number(b.active) - Number(a.active) || b.id - a.id);

  let body;
  if (q.isPending) body = <Skeleton className="h-24 w-full" />;
  else if (q.isError) {
    body =
      isApiError(q.error) && q.error.code === 'persistence_disabled' ? (
        <DegradedState
          title="Calibration records need a database"
          reason="This proxy runs without Postgres, so it reads calibrations only from TRUTHTABLE_CALIBRATION_DIR."
          stillWorks="Calibration files in that directory still apply; fitting, importing and adopting need a database."
        />
      ) : isApiError(q.error) && q.error.code === 'not_implemented' ? (
        <DegradedState
          title="Calibration records need a running gateway"
          reason={q.error.message}
          stillWorks="Each check's calibration state and reliability still show from the recording."
        />
      ) : isNotServed(q.error) ? (
        <DegradedState
          title="Calibration records are not available on this proxy"
          reason="It does not serve GET /api/calibration yet (an older version)."
          stillWorks="Calibration files still apply; the calibration state shows on each route."
        />
      ) : (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      );
  } else if (mine.length === 0) {
    body = (
      <EmptyState icon={Gauge} title="No calibrations for this check yet" className="py-8">
        <p>Fit one from your labels with Calibrate now, or import a calibration file.</p>
      </EmptyState>
    );
  } else {
    body = (
      <ul aria-label="Calibrations" className="divide-y divide-border rounded-lg border border-border bg-surface">
        {mine.map((meta) => (
          <RecordRow key={meta.id} meta={meta} detail={detail} writeReason={writeReason} onAdopt={() => setAdopting(meta)} />
        ))}
      </ul>
    );
  }

  return (
    <Section
      title="Calibrations"
      description="Every calibration this proxy holds for the check. The active one maps raw scores to calibrated ones; adopting one moves a route's thresholds into calibrated space."
    >
      {body}
      <ImportAction disabledReason={writeReason} />
      {adopting ? <AdoptRecordDialog detail={detail} meta={adopting} onClose={() => setAdopting(null)} /> : null}
    </Section>
  );
}
