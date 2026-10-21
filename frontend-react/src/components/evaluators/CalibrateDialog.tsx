/**
 * "Calibrate now" and "Adopt calibration" (SPEC §3.3 Evaluators, §2.11; D62): fit a
 * calibration from labels (free, #45 with `activate: false`), read what came out of it, then keep it
 * active or adopt it on a route.
 *
 * The form says what the fit can and cannot claim before it runs (the honesty note): only the
 * viewer's own labels make a Measured calibration, deep-audit labels make a Proxy one, and a fit
 * changes nothing until it is adopted. The result shows the quality, the label counts, the
 * reliability chart ("agreement with labels, not ground truth"), the before/after metrics and the
 * recommended thresholds, each labelled with where it comes from.
 */
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { CircleAlert, ShieldCheck } from 'lucide-react';
import { useState, type ReactElement } from 'react';
import { useDataSource } from '@/app/services';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { describeError } from '@/components/common/describeError';
import { KeyValueList } from '@/components/common/KeyValue';
import { notify } from '@/components/common/notify';
import { routeTitle } from '@/components/policies/routeWords';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogFooter, DialogTrigger } from '@/components/ui/dialog';
import { Field } from '@/components/ui/field';
import { RadioGroup } from '@/components/ui/radio-group';
import { Select } from '@/components/ui/select';
import { isApiError } from '@/data/errors';
import { qkc } from '@/data/queries';
import { qkcEvaluators } from '@/data/queries/evaluators';
import { useAppMode } from '@/data/useSystem';
import { formatCount, formatRisk } from '@/domain/format';
import { copy } from '@/domain/presentation';
import { EVAL_CRITICAL_NULL_TEXT, EVAL_FIT_HONESTY, evalNotEnoughLabelsText, evalQualityView } from '@/domain/words/evaluators';
import { MIN_FIT_LABELS, MIN_FIT_PER_CLASS } from '@/lib/decision/calibrate';
import type { CalibrationFitResponse, EvaluatorDetail } from '@/types/api';
import { AdoptPanel } from './AdoptPanel';
import { canFitOnOwnLabels } from './evaluatorWords';
import { ReliabilityChart } from './ReliabilityChart';

type Sources = 'labels' | 'labels_and_audit';
const ALL_ROUTES = '__all__';

/** "0.14 → 0.06". */
function beforeAfter(before: number | null, after: number | null): string | null {
  if (before === null && after === null) return null;
  return `${before === null ? '—' : formatRisk(before)} → ${after === null ? '—' : formatRisk(after)}`;
}

export function FitResult({ fit, simulated }: { fit: CalibrationFitResponse; simulated: boolean }) {
  const quality = evalQualityView(fit.record.quality);
  const QualityIcon = quality.icon;
  const entry = fit.record.entry;
  const rec = fit.recommended;
  const fromJudge = (entry.label_source?.uniform_audit ?? 0) > 0;
  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <p className="flex flex-wrap items-center gap-2 text-sm text-fg">
          <Badge icon={QualityIcon} emphasis="quiet">
            {quality.label}
          </Badge>
          <span>Calibration #{fit.record.id}</span>
        </p>
        <p className="text-sm text-fg-muted">{quality.description}</p>
      </div>
      <div className="grid items-start gap-5 md:grid-cols-[minmax(0,18rem)_minmax(0,1fr)]">
        <ReliabilityChart data={fit.reliability} />
        <div className="space-y-3">
          <KeyValueList
            items={[
              { label: 'Labels used', value: formatCount(entry.n), numeric: true, hint: `${formatCount(entry.n_pos)} with the problem · ${formatCount(entry.n_neg)} fine` },
              { label: 'Calibration error', value: beforeAfter(fit.metrics.ece_before, fit.metrics.ece_after), numeric: true, hint: 'ECE before → after, lower is better' },
              { label: 'Brier score', value: beforeAfter(fit.metrics.brier_before, fit.metrics.brier_after), numeric: true, hint: 'before → after, lower is better' },
              { label: 'Ranking (AUROC)', value: fit.metrics.auroc === null ? null : formatRisk(fit.metrics.auroc), numeric: true, hint: '1.0 ranks every problem above every fine answer' },
            ]}
          />
          <EvidenceBadge
            label={simulated || fit.record.quality === 'mock' ? 'simulated' : fromJudge ? 'estimate' : 'measured'}
            basis={
              fromJudge
                ? 'Computed on your labels plus deep-audit (LLM judge) labels: a proxy for your own judgement, not ground truth.'
                : 'Computed on your own labels for this fast-check version: agreement with labels, not ground truth.'
            }
            subject="Calibration metrics"
          />
        </div>
      </div>
      <div className="space-y-1">
        <h4 className="text-xs font-medium text-fg-muted">Recommended thresholds (calibrated)</h4>
        <KeyValueList
          items={[
            {
              label: 'Flag from',
              value: rec.warn_at ? formatRisk(rec.warn_at.value) : 'None recommended',
              numeric: true,
              hint: rec.warn_at ? `precision ${formatRisk(rec.warn_at.precision)} · recall ${formatRisk(rec.warn_at.recall)}` : undefined,
            },
            {
              label: 'Block from',
              value: rec.critical_at ? formatRisk(rec.critical_at.value) : 'None recommended',
              numeric: true,
              hint: rec.critical_at
                ? `precision at least ${formatRisk(rec.critical_at.precision_lb95)} (95% lower bound) on ${formatCount(rec.critical_at.predicted_pos)} answers`
                : EVAL_CRITICAL_NULL_TEXT,
            },
          ]}
        />
      </div>
    </div>
  );
}

export interface CalibrateDialogProps {
  detail: EvaluatorDetail;
  /** The button that opens the dialog. */
  trigger: ReactElement;
  /** `adopt` goes straight from the fit to the adopt step (D62 order). */
  intent?: 'calibrate' | 'adopt';
}

export function CalibrateDialog({ detail, trigger, intent = 'calibrate' }: CalibrateDialogProps) {
  const ds = useDataSource();
  const queryClient = useQueryClient();
  const { demo } = useAppMode();
  const [open, setOpen] = useState(false);
  const [step, setStep] = useState<'form' | 'result' | 'adopt'>('form');
  const [route, setRoute] = useState<string>(detail.attached_routes[0]?.route ?? ALL_ROUTES);
  // Without enough labels of your own, a fit needs the deep-audit labels too (calibrateReason).
  const defaultSources = (): Sources => (canFitOnOwnLabels(detail.calibration_progress) ? 'labels' : 'labels_and_audit');
  const [sources, setSources] = useState<Sources>(defaultSources);
  const ref = `${detail.id}@${detail.latest.version}`;
  const progress = detail.calibration_progress;
  const routes = [...new Set(detail.attached_routes.map((r) => r.route))];

  const fit = useMutation({
    mutationFn: () =>
      ds.fitCalibration({
        target: ref,
        ...(route !== ALL_ROUTES ? { route } : {}),
        ...(progress.snapshot ? { snapshot: progress.snapshot } : {}),
        sources: sources === 'labels' ? ['labels'] : ['labels', 'uniform_audit'],
        method: 'auto',
        activate: false,
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: qkcEvaluators.calibrationLists });
      setStep(intent === 'adopt' ? 'adopt' : 'result');
    },
  });

  const activate = useMutation({
    mutationFn: (id: number) => ds.activateCalibration(id),
    onSuccess: (meta) => {
      void queryClient.invalidateQueries({ queryKey: qkcEvaluators.calibrationLists });
      void queryClient.invalidateQueries({ queryKey: qkc.evaluators.detail(detail.id) });
      notify.success(`Calibration #${meta.id} is active`, { description: 'Routes keep their current thresholds until you adopt it on them.' });
      setOpen(false);
    },
  });

  const onOpenChange = (next: boolean) => {
    setOpen(next);
    if (next) {
      setStep('form');
      setSources(defaultSources());
      fit.reset();
      activate.reset();
    }
  };

  // Too few labels is the fit's usual refusal (422): say it with the counts, not the server's refs.
  const tooFew = isApiError(fit.error) && fit.error.status === 422 ? evalNotEnoughLabelsText(fit.error.details) : null;
  const failure = tooFew
    ? { message: tooFew, code: null }
    : fit.error
      ? describeError(fit.error)
      : activate.error
        ? describeError(activate.error)
        : null;
  const result = fit.data;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger render={trigger} />
      <DialogContent
        size="lg"
        className="w-[min(52rem,calc(100vw-2rem))]"
        title={intent === 'adopt' || step === 'adopt' ? `${copy.evaluators.adopt_calibration}: ${detail.name}` : `Calibrate ${detail.name}`}
        description={
          intent === 'adopt' || step === 'adopt'
            ? 'Preview what the calibrated thresholds would have done to past answers, then adopt them as a new policy version.'
            : 'Fit how often this check’s raw score agreed with your labels, so its thresholds mean what they say.'
        }
      >
        {step === 'form' ? (
          <div className="space-y-5">
            <ul className="space-y-1.5 rounded-md border border-border bg-surface-sunken px-3 py-2 text-sm text-fg">
              {EVAL_FIT_HONESTY.map((line) => (
                <li key={line} className="flex gap-2">
                  <ShieldCheck aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-fg-muted" />
                  <span>{line}</span>
                </li>
              ))}
            </ul>
            <p className="text-sm text-fg-muted tabular">
              You have {formatCount(progress.human_labels.n)} labels ({formatCount(progress.human_labels.pos)} with the problem, {formatCount(progress.human_labels.neg)}{' '}
              fine) and {formatCount(progress.proxy_labels)} deep-audit labels for this version.
            </p>
            <RadioGroup<Sources>
              label="Labels to fit on"
              value={sources}
              onValueChange={setSources}
              options={[
                {
                  value: 'labels',
                  label: 'Your labels only',
                  description: progress.can_fit_measured
                    ? 'Enough for a Measured calibration.'
                    : canFitOnOwnLabels(progress)
                      ? 'Provisional until you have 200 labels with 30 of each kind.'
                      : `Not enough of your own labels for a fit yet: it needs ${MIN_FIT_LABELS}, with ${MIN_FIT_PER_CLASS} of each kind.`,
                },
                { value: 'labels_and_audit', label: 'Your labels and the deep-audit sample', description: 'More labels, but the result is a Proxy: it can flag, never block.' },
              ]}
            />
            {routes.length > 0 ? (
              <Field label="Route" description="Checks with route settings (like allowed commitments) are calibrated per route.">
                <Select
                  value={route}
                  onValueChange={(v) => v && setRoute(v)}
                  options={[{ value: ALL_ROUTES, label: 'Every route it runs on' }, ...routes.map((r) => ({ value: r, label: routeTitle(r) }))]}
                  className="w-64"
                />
              </Field>
            ) : null}
            {failure ? (
              <p role="alert" className="flex items-start gap-2 text-sm text-fg">
                <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-danger" />
                <span>
                  {tooFew ? null : 'The fit failed: '}
                  {failure.message}
                  {failure.code ? <span className="ml-1 font-mono text-2xs text-fg-subtle">{failure.code}</span> : null}
                </span>
              </p>
            ) : null}
            <DialogFooter>
              <Button variant="secondary" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button variant="primary" pending={fit.isPending} onClick={() => fit.mutate()}>
                {intent === 'adopt' ? 'Fit and preview (free)' : 'Fit calibration (free)'}
              </Button>
            </DialogFooter>
          </div>
        ) : step === 'result' && result ? (
          <div className="space-y-5">
            <FitResult fit={result} simulated={demo} />
            {failure ? (
              <p role="alert" className="text-sm text-fg">
                {failure.message}
                {failure.code ? <span className="ml-1 font-mono text-2xs text-fg-subtle">{failure.code}</span> : null}
              </p>
            ) : null}
            <p className="text-xs text-fg-muted">Activating keeps it ready for routes that already use calibrated thresholds; every other route keeps its current thresholds until you adopt it there.</p>
            <DialogFooter>
              <Button variant="secondary" onClick={() => setOpen(false)}>
                Close
              </Button>
              {result.record.active ? null : (
                <Button pending={activate.isPending} onClick={() => activate.mutate(result.record.id)}>
                  Activate only
                </Button>
              )}
              <Button variant="primary" onClick={() => setStep('adopt')}>
                {copy.evaluators.adopt_calibration}…
              </Button>
            </DialogFooter>
          </div>
        ) : step === 'adopt' && result ? (
          <AdoptPanel
            evaluatorRef={ref}
            name={detail.name}
            routes={routes}
            initialRoute={route !== ALL_ROUTES ? route : undefined}
            record={result.record}
            recommended={result.recommended}
            simulated={demo}
            onDone={() => setOpen(false)}
            onBack={() => setStep(intent === 'adopt' ? 'form' : 'result')}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
