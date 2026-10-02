/**
 * Studio step 3, the test bench (SPEC §7.3): samples with the viewer's labels, the estimated cost
 * before "Run", then one row per sample ("Fires / Doesn't fire · Matches your label / Doesn't
 * match", with probabilities, latency, cost and snapshot on click), a summary ("5 of 6 match ·
 * $0.001") and the free threshold what-if, which replays the risks this run already returned.
 */
import { Link } from '@tanstack/react-router';
import { useMutation } from '@tanstack/react-query';
import { CircleAlert, RefreshCw } from 'lucide-react';
import { useState } from 'react';
import { useDataSource } from '@/app/services';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { describeError } from '@/components/common/describeError';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Slider } from '@/components/ui/slider';
import { STUDIO_TEST_MAX_SAMPLES } from '@/data/control/studioPlane';
import { errorCodeHint } from '@/data/errors';
import { formatCount, formatDurationMs, formatRisk } from '@/domain/format';
import { copy, decisionBackendLabel } from '@/domain/presentation';
import { testEstimate } from '@/lib/studio/estimate';
import { benchAt, runKeys, type Expected } from '@/lib/studio/testBench';
import type { DecisionModelStatus, EvaluatorSpec, StudioSample, StudioTestResponse } from '@/types/api';
import { studioAgreementWords, studioExpectedWords, studioFireWords } from './studioWords';
import { CostFigure } from './CostFigure';
import { AddSamples, SampleList } from './SampleList';
import { StudioStep } from './StudioStep';

/** A test run and what it ran on, so the bench can say when the check or samples changed since. */
export interface TestRun {
  response: StudioTestResponse;
  /** The spec and samples as sent (JSON), to compare with the current ones. */
  specKey: string;
  samplesKey: string;
}

export interface TestBenchProps {
  spec: EvaluatorSpec;
  samples: StudioSample[];
  onSamplesChange: (samples: StudioSample[]) => void;
  run: TestRun | null;
  onRun: (run: TestRun) => void;
  onSpecChange: (spec: EvaluatorSpec) => void;
  decisionModel: DecisionModelStatus | null;
  routes: readonly string[];
  defaultRoute: string | null;
  writeReason: Reason | null;
  /** Why the spec cannot run yet (errors in step 2), or null. */
  specReason: Reason | null;
  /** This is the step to do next: its action is the page's one primary button. */
  primary: boolean;
}

const pct = (p: number) => `${Math.floor(p * 100 + 0.5)}%`;

interface RunCost {
  label: 'simulated' | 'estimate';
  basis: string;
}

function runCostOf(response: StudioTestResponse): RunCost {
  return response.summary.backend === 'mock'
    ? { label: 'simulated', basis: 'A mock fast check answered; nothing was billed.' }
    : { label: 'estimate', basis: "Tokens × the price table's price for the fast-check model." };
}

function ResultRow({
  index,
  row,
  sample,
  fires,
  agrees,
  snapshot,
  cost,
}: {
  index: number;
  row: StudioTestResponse['results'][number];
  sample: StudioSample | undefined;
  fires: boolean | null;
  agrees: boolean | null;
  snapshot: string | null;
  cost: RunCost;
}) {
  const fire = studioFireWords(fires, row.status);
  const agreement = studioAgreementWords(agrees);
  const probabilities = Object.entries(row.probabilities ?? {}).sort(([, a], [, b]) => b - a);
  return (
    <li className="space-y-1.5 rounded-md border border-border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="tabular text-xs font-medium text-fg">Sample {index + 1}</span>
        <Badge tone={fire.tone} emphasis="tint" icon={fire.icon}>
          {fire.label}
        </Badge>
        {agreement ? (
          <Badge tone={agreement.tone} emphasis="quiet" icon={agreement.icon}>
            {agreement.label}
          </Badge>
        ) : (
          <span className="text-xs text-fg-muted">{studioExpectedWords(null)}</span>
        )}
        <span className="tabular ml-auto text-xs text-fg-muted">Risk {formatRisk(row.risk_raw)}</span>
      </div>
      {sample ? <p className="line-clamp-2 text-sm text-fg-muted">{sample.assistant_response}</p> : null}
      <details className="text-sm">
        <summary className="cursor-pointer text-xs font-medium text-fg-muted hover:text-fg">Details</summary>
        <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs">
          {row.reason ? (
            <>
              <dt className="text-fg-muted">Why</dt>
              <dd className="text-fg">{row.reason}</dd>
            </>
          ) : null}
          {probabilities.length > 0 ? (
            <>
              <dt className="text-fg-muted">Probabilities</dt>
              <dd className="space-y-0.5">
                {probabilities.map(([option, p]) => (
                  <div key={option} className="flex items-center gap-2">
                    <span className="w-36 truncate font-mono text-2xs text-fg">{option}</span>
                    <span aria-hidden="true" className="h-1.5 w-24 rounded-full bg-surface-sunken">
                      <span className="block h-1.5 rounded-full bg-fg-muted" style={{ width: pct(p) }} />
                    </span>
                    <span className="tabular text-fg">{pct(p)}</span>
                  </div>
                ))}
              </dd>
            </>
          ) : null}
          <dt className="text-fg-muted">Latency</dt>
          <dd className="tabular text-fg">{formatDurationMs(row.latency_ms)}</dd>
          <dt className="text-fg-muted">Cost</dt>
          <dd>
            <CostFigure usd={row.cost_usd} label={cost.label} basis={cost.basis} subject={`Cost of sample ${index + 1}`} approximate={false} />
          </dd>
          <dt className="text-fg-muted">Model snapshot</dt>
          <dd className="font-mono text-2xs text-fg">{snapshot ?? '—'}</dd>
        </dl>
      </details>
    </li>
  );
}

export function TestBench({ spec, samples, onSamplesChange, run, onRun, onSpecChange, decisionModel, routes, defaultRoute, writeReason, specReason, primary }: TestBenchProps) {
  const ds = useDataSource();
  const [whatIf, setWhatIf] = useState<number | null>(null);
  const warnAt = spec.outcomes.warn_at;

  const test = useMutation({
    mutationFn: (sent: { spec: EvaluatorSpec; samples: StudioSample[] }) => ds.testEvaluator({ spec: sent.spec, samples: sent.samples }),
    onSuccess: (response, sent) => {
      setWhatIf(null);
      onRun({ response, ...runKeys(sent.spec, sent.samples) });
    },
  });

  const backend = decisionModel?.backend ?? null;
  const estimate = testEstimate(spec, samples, { backend: backend ?? 'openrouter-decisions', name: decisionModel?.snapshot ?? decisionModel?.model_requested ?? null });

  let runReason: Reason | null = writeReason;
  if (!runReason && (backend === 'off' || decisionModel?.state === 'off')) {
    runReason = {
      text: 'No fast-check model is connected',
      fix: (
        <Link to="/app/settings/$section" params={{ section: 'connections' }} className="font-medium text-fg underline underline-offset-2">
          Connect one
        </Link>
      ),
    };
  }
  if (!runReason) runReason = specReason;
  if (!runReason && samples.length === 0) runReason = { text: 'Add samples first' };
  if (!runReason && samples.length > STUDIO_TEST_MAX_SAMPLES) runReason = { text: `Test at most ${STUDIO_TEST_MAX_SAMPLES} samples at a time` };

  const labels = new Map<string, Expected | undefined>(samples.map((s) => [s.id, s.expected]));
  const keys = runKeys(spec, samples);
  const stale = run !== null && (run.specKey !== keys.specKey || run.samplesKey !== keys.samplesKey);
  const threshold = whatIf ?? warnAt;
  const summary = run ? benchAt(run.response.results, labels, threshold) : null;
  const bySample = new Map(samples.map((s) => [s.id, s]));
  const failure = test.error ? describeError(test.error) : null;
  const runCost = run ? runCostOf(run.response) : null;

  return (
    <StudioStep
      n={3}
      id="step-test"
      title={copy.studio.test_on_samples}
      description="Label what the check should catch, then run it on the fast check. Each sample is one call."
    >
      <SampleList samples={samples} onChange={onSamplesChange} disabled={test.isPending} />
      <AddSamples samples={samples} onChange={onSamplesChange} routes={routes} defaultRoute={defaultRoute} writeReason={writeReason} />

      <div className="flex flex-wrap items-center gap-3 border-t border-border pt-4">
        <DisabledAction reason={runReason}>
          {(p) => (
            <Button variant={primary ? 'primary' : 'secondary'} pending={test.isPending} onClick={() => test.mutate({ spec, samples })} {...p}>
              {run ? <RefreshCw aria-hidden="true" /> : null}
              {run ? 'Run again' : 'Run test'}
            </Button>
          )}
        </DisabledAction>
        {samples.length > 0 ? (
          <span className="flex flex-wrap items-center gap-1.5 text-sm text-fg-muted">
            {formatCount(samples.length)} {samples.length === 1 ? 'sample' : 'samples'} ·
            <CostFigure usd={estimate.usd} label={estimate.label} basis={estimate.basis} subject="Cost of this test" />
            {backend ? <span>· {decisionBackendLabel(backend)}</span> : null}
          </span>
        ) : null}
      </div>

      {failure ? (
        <div role="alert" className="space-y-1 rounded-md border border-border p-3 text-sm">
          <p className="flex items-start gap-1.5 text-fg">
            <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-danger" />
            The test did not run: {failure.message}
          </p>
          {errorCodeHint(test.error) ? <p className="text-fg-muted">{errorCodeHint(test.error)}</p> : null}
          {failure.code ? <p className="font-mono text-2xs text-fg-subtle">{failure.code}</p> : null}
        </div>
      ) : null}

      {run && summary ? (
        <section aria-label="Test results" className="space-y-3">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <p className="tabular text-base font-semibold text-fg">
              {summary.labelled > 0 ? `${formatCount(summary.matched)} of ${formatCount(summary.labelled)} match` : 'No labelled samples'}
              <span className="font-normal text-fg-muted"> · fires on {formatCount(summary.fired)} of {formatCount(summary.assessed)}</span>
            </p>
            {runCost ? <CostFigure usd={run.response.summary.cost_usd} label={runCost.label} basis={runCost.basis} subject="Cost of this run" approximate={false} /> : null}
          </div>
          {stale ? <p className="text-sm text-fg-muted">The check or the samples changed since this run. Run it again to see the new answers.</p> : null}
          <ol className="space-y-2">
            {run.response.results.map((row, i) => (
              <ResultRow
                key={row.sample_id}
                index={i}
                row={row}
                sample={bySample.get(row.sample_id)}
                fires={summary.rows[i].fires}
                agrees={summary.rows[i].agrees}
                snapshot={run.response.summary.snapshot}
                cost={runCostOf(run.response)}
              />
            ))}
          </ol>
          <div className="space-y-2 rounded-md border border-border bg-surface-sunken p-3">
            <Slider
              label="What if it flagged at"
              value={threshold}
              min={0}
              max={1}
              step={0.05}
              marks={[{ value: warnAt, label: 'Now' }]}
              description="Free: replays the risks this run returned. No new calls."
              onValueChange={setWhatIf}
            />
            <p className="tabular text-sm text-fg">
              At {formatRisk(threshold)}: fires on {formatCount(summary.fired)} of {formatCount(summary.assessed)}
              {summary.labelled > 0 ? ` · ${formatCount(summary.matched)} of ${formatCount(summary.labelled)} match your labels` : ''}
            </p>
            {threshold !== warnAt ? (
              <Button size="sm" onClick={() => onSpecChange({ ...spec, outcomes: { ...spec.outcomes, warn_at: threshold } })}>
                Use {formatRisk(threshold)} as the threshold
              </Button>
            ) : null}
          </div>
        </section>
      ) : null}
    </StudioStep>
  );
}
