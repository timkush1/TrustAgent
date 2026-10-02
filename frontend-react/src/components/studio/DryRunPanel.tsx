/**
 * "Test on past traffic (paid)" (SPEC §7.3 step 6, §2.4 #38; P4-PLAN D82): runs the check on up to
 * 200 stored answers of a route over SSE. The first call never confirms the cost; when the server's
 * estimate is above the confirmation line it answers 409 with the estimate, the sample and today's
 * fast-check budget left, and the viewer confirms before anything is spent. While it runs: progress
 * and Cancel; a cancelled test, or one the budget stops partway, says what the checks that already
 * ran cost. The result: the fires over the window (extrapolated, an estimate, when only a sample was
 * tested), the fire rate with its 95% interval, overlap with other checks, agreement with deep
 * audits, examples and the actual cost. A result says so when the check or the route changed since.
 */
import { Link } from '@tanstack/react-router';
import { CircleAlert, CircleStop, History } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { useDataSource } from '@/app/services';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { describeError } from '@/components/common/describeError';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { STUDIO_DRY_RUN_MAX_SAMPLES } from '@/data/control/studioPlane';
import { errorCodeHint, isApiError } from '@/data/errors';
import { formatCount, formatRisk, formatUsd } from '@/domain/format';
import { copy, levelWord } from '@/domain/presentation';
import type { DryRunResult, EvaluatorSpec, StudioDryRunEvent, StudioDryRunRequest } from '@/types/api';
import { studioDryRunCancelledText, studioDryRunConfirmText, studioDryRunExtrapolated, studioDryRunResultText, studioWindowWords } from './studioWords';
import { CostFigure } from './CostFigure';

type Estimate = Extract<StudioDryRunEvent, { event: 'estimate' }>['data'];
type Progress = Extract<StudioDryRunEvent, { event: 'progress' }>['data'];
type DryWindow = StudioDryRunRequest['window'];

interface Confirmation {
  estimate_usd: number;
  samples: number;
  population: number;
  decision_budget_left_usd: number | null;
}

type Phase =
  | { kind: 'idle' }
  | { kind: 'confirm'; details: Confirmation }
  | { kind: 'running' }
  | { kind: 'done'; result: DryRunResult }
  | { kind: 'cancelled'; spent: number }
  /** `spent` is what the checks cost before the run failed; null when it failed before it started. */
  | { kind: 'error'; error: unknown; spent: number | null };

const num = (v: unknown): number | null => (typeof v === 'number' && Number.isFinite(v) ? v : null);

/** The D82 details of a 409 `confirmation_required`, or null when the error is something else. */
function confirmationOf(err: unknown): Confirmation | null {
  if (!isApiError(err) || err.status !== 409 || err.code !== 'confirmation_required') return null;
  const d = err.details;
  const estimate = num(d.estimate_usd);
  const samples = num(d.samples);
  const population = num(d.population);
  if (estimate === null || samples === null || population === null) return null;
  return { estimate_usd: estimate, samples, population, decision_budget_left_usd: num(d.decision_budget_left_usd) };
}

const pct = (p: number) => `${(Math.floor(p * 1000 + 0.5) / 10).toFixed(1)}%`;

export interface DryRunPanelProps {
  /** What to test: a saved ref, or the unsaved spec (D58). */
  target: { ref: string } | { spec: EvaluatorSpec };
  /** The check as it is now (saved or not), to tell when a shown result is for an older one. */
  spec: EvaluatorSpec;
  route: string;
  /** A mock fast check: the costs are simulated. */
  simulated: boolean;
  /** Why the test cannot run (write access, the spec, the model), or null. */
  reason: Reason | null;
  names: ReadonlyMap<string, string>;
}

function DryRunResultView({ result, simulated, names }: { result: DryRunResult; simulated: boolean; names: ReadonlyMap<string, string> }) {
  const nameOf = (ref: string) => {
    const id = ref.includes('@') ? ref.slice(0, ref.lastIndexOf('@')) : ref;
    return names.get(id) ?? ref;
  };
  const cost = (
    <CostFigure
      usd={result.cost_usd}
      label={simulated ? 'simulated' : 'estimate'}
      basis={simulated ? 'A mock fast check answered; nothing was billed.' : "Tokens × the price table's price for the fast-check model."}
      subject="Cost of this test"
      approximate={false}
    />
  );
  if (result.sampled === 0) {
    return (
      <div className="space-y-1.5" role="status">
        <p className="text-sm text-fg">
          {result.route} has no stored answers from the last {studioWindowWords(result.window)} to test on, so nothing ran. Answers are stored as the gateway sees traffic; try a longer window
          later.
        </p>
        <p className="flex flex-wrap items-center gap-1.5 text-sm text-fg-muted">This test cost {cost}</p>
      </div>
    );
  }
  const judged = result.assessed + result.not_assessed;
  const extrapolated = studioDryRunExtrapolated(result);
  return (
    <div className="space-y-3" role="status">
      <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-base font-semibold text-fg">
        <span>{studioDryRunResultText(result)}</span>
        {extrapolated ? (
          <EvidenceBadge
            label="estimate"
            basis={`Scaled up from the ${formatCount(result.sampled)} tested answers to all ${formatCount(result.population)}, each counted by its chance of being sampled.`}
            subject="Fires on every answer in the window"
          />
        ) : null}
      </p>
      <dl className="grid gap-x-6 gap-y-1.5 text-sm sm:grid-cols-[auto_1fr]">
        <dt className="text-fg-muted">Checked</dt>
        <dd className="tabular text-fg">
          {formatCount(result.assessed)} of {formatCount(result.sampled)} sampled answers ({formatCount(result.population)} in the window)
          {result.not_assessed > 0 ? ` · ${formatCount(result.not_assessed)} not checked` : ''}
          {result.errors > 0 ? ` · ${formatCount(result.errors)} errors` : ''}
        </dd>
        <dt className="text-fg-muted">In the sample</dt>
        <dd className="tabular text-fg">
          Fired on {formatCount(result.fired)} of {formatCount(judged)}
        </dd>
        <dt className="text-fg-muted">Fires together with</dt>
        <dd className="text-fg">
          {result.overlap.length === 0
            ? 'No other check fired on the same answers'
            : result.overlap.map((o) => `${nameOf(o.evaluator_ref)} (${formatCount(o.both_fired)})`).join(', ')}
        </dd>
        <dt className="text-fg-muted">Deep audits</dt>
        <dd className="text-fg">
          {result.agreement_with_audit
            ? `Agrees with the deep audit on ${formatCount(result.agreement_with_audit.agree)} of ${formatCount(result.agreement_with_audit.n)} audited answers`
            : 'None of these answers had a deep audit to compare with'}
        </dd>
        <dt className="text-fg-muted">This test cost</dt>
        <dd>{cost}</dd>
      </dl>
      {result.examples.length > 0 ? (
        <div className="space-y-1">
          <h4 className="text-xs font-medium text-fg-muted">Answers it would have fired on</h4>
          <ul className="space-y-1">
            {result.examples.map((ex) => {
              const level = levelWord(ex.level);
              return (
                <li key={ex.trace_id} className="flex flex-wrap items-center gap-2 text-sm">
                  <Badge tone={level.tone} emphasis="tint">
                    {level.label}
                  </Badge>
                  <span className="tabular text-fg-muted">risk {formatRisk(ex.risk)}</span>
                  <Link to="/app/traffic/$traceId" params={{ traceId: ex.trace_id }} className="font-mono text-xs text-fg underline underline-offset-2">
                    {ex.trace_id}
                  </Link>
                </li>
              );
            })}
          </ul>
        </div>
      ) : null}
      <p className="text-xs text-fg-muted">
        Fire rate {pct(result.fire_rate)} (95% interval {pct(result.fire_rate_ci95[0])}–{pct(result.fire_rate_ci95[1])}) over the {formatCount(judged)} sampled answers it could judge
        {result.errors > 0 ? '; errors are left out' : ''}.{result.snapshot ? ` Model snapshot ${result.snapshot}.` : ''}
      </p>
    </div>
  );
}

export function DryRunPanel({ target, spec, route, simulated, reason, names }: DryRunPanelProps) {
  const ds = useDataSource();
  const [since, setSince] = useState<DryWindow>('24h');
  const [maxSamples, setMaxSamples] = useState<'50' | '100' | '200'>('200');
  const [phase, setPhase] = useState<Phase>({ kind: 'idle' });
  const [estimate, setEstimate] = useState<Estimate | null>(null);
  const [progress, setProgress] = useState<Progress | null>(null);
  const controller = useRef<AbortController | null>(null);
  const spent = useRef(0);
  const started = useRef(false);
  /** What the shown result ran on (the check and the route), to say when either changed since. */
  const [ranOn, setRanOn] = useState<string | null>(null);
  // Saving the check only changes its version (and the target from the spec to its ref).
  const ranOnKey = JSON.stringify([{ ...spec, version: 0 }, route]);

  useEffect(() => () => controller.current?.abort(), []);

  const start = async (confirm: boolean) => {
    controller.current?.abort();
    const c = new AbortController();
    controller.current = c;
    spent.current = 0;
    started.current = false;
    setRanOn(ranOnKey);
    setEstimate(null);
    setProgress(null);
    setPhase({ kind: 'running' });
    // The first call never confirms the cost (D82): the server asks when its estimate needs it.
    const request: StudioDryRunRequest = { ...target, route, window: since, max_samples: Math.min(Number(maxSamples), STUDIO_DRY_RUN_MAX_SAMPLES), ...(confirm ? { confirm_cost: true } : {}) };
    try {
      const result = await ds.dryRunEvaluator(
        request,
        {
          onEstimate: (e) => {
            started.current = true;
            setEstimate(e);
          },
          onProgress: (p) => {
            spent.current = p.cost_usd;
            setProgress(p);
          },
        },
        c.signal,
      );
      if (controller.current === c) setPhase({ kind: 'done', result });
    } catch (err) {
      if (controller.current !== c) return;
      if (c.signal.aborted) {
        setPhase({ kind: 'cancelled', spent: spent.current });
        return;
      }
      const details = confirm ? null : confirmationOf(err);
      if (details) {
        setPhase({ kind: 'confirm', details });
        return;
      }
      // An `error` event after the stream started (the budget ran out partway) reports what was spent.
      const reported = isApiError(err) ? num(err.details.cost_usd) : null;
      setPhase({ kind: 'error', error: err, spent: started.current ? (reported ?? spent.current) : null });
    }
  };

  const running = phase.kind === 'running';
  const failure = phase.kind === 'error' ? describeError(phase.error) : null;
  const changedSince = phase.kind === 'done' && ranOn !== null && ranOn !== ranOnKey;
  const total = progress ? Math.max(estimate?.sampled ?? 0, progress.evaluated) : (estimate?.sampled ?? 0);

  return (
    <div className="space-y-3 rounded-md border border-border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <History aria-hidden="true" className="size-4 text-fg-muted" />
        <h3 className="text-sm font-semibold text-fg">{copy.studio.dry_run}</h3>
      </div>
      <p className="text-sm text-fg-muted">
        Runs the check on stored answers from <span className="font-medium text-fg">{route}</span> to see how often it would fire. Each answer is one fast-check call; nothing changes for users.
      </p>
      <div className="flex flex-wrap items-end gap-4">
        <div className="space-y-1.5">
          <p className="text-sm font-medium text-fg">Answers from the last</p>
          <SegmentedControl<DryWindow>
            aria-label="Answers from the last"
            size="sm"
            options={[
              { value: '24h', label: '24 h' },
              { value: '7d', label: '7 days' },
            ]}
            value={since}
            onValueChange={setSince}
            disabled={running}
          />
        </div>
        <div className="space-y-1.5">
          <p className="text-sm font-medium text-fg">At most</p>
          <SegmentedControl<'50' | '100' | '200'>
            aria-label="At most this many answers"
            size="sm"
            options={[
              { value: '50', label: '50' },
              { value: '100', label: '100' },
              { value: '200', label: '200' },
            ]}
            value={maxSamples}
            onValueChange={setMaxSamples}
            disabled={running}
          />
        </div>
        {running ? (
          <Button onClick={() => controller.current?.abort()}>
            <CircleStop aria-hidden="true" />
            Cancel the test
          </Button>
        ) : (
          <DisabledAction reason={reason}>
            {(p) => (
              <Button onClick={() => void start(false)} {...p}>
                {phase.kind === 'done' || phase.kind === 'cancelled' ? 'Test again' : copy.studio.dry_run}
              </Button>
            )}
          </DisabledAction>
        )}
      </div>

      {running ? (
        <div role="status" className="space-y-1.5">
          {estimate ? (
            <p className="flex flex-wrap items-center gap-1.5 text-sm text-fg">
              Testing {formatCount(estimate.sampled)} of {formatCount(estimate.population)} answers ·
              <CostFigure usd={estimate.est_cost_usd} label={simulated ? 'simulated' : 'estimate'} basis="The server's estimate before the test started." subject="Estimated cost of this test" />
            </p>
          ) : (
            <p className="text-sm text-fg-muted">Starting the test…</p>
          )}
          {progress ? (
            <>
              <div
                role="progressbar"
                aria-label="Answers checked"
                aria-valuemin={0}
                aria-valuemax={total || undefined}
                aria-valuenow={progress.evaluated}
                className="h-1.5 w-full max-w-md rounded-full bg-surface-sunken"
              >
                <div className="h-1.5 rounded-full bg-fg-muted" style={{ width: total ? `${Math.min(100, (progress.evaluated / total) * 100)}%` : '0%' }} />
              </div>
              <p className="tabular text-sm text-fg-muted">
                Checked {formatCount(progress.evaluated)}
                {total ? ` of ${formatCount(total)}` : ''} · fired on {formatCount(progress.fired)} · spent ≈ {formatUsd(progress.cost_usd)}
              </p>
            </>
          ) : null}
        </div>
      ) : null}

      {phase.kind === 'done' ? <DryRunResultView result={phase.result} simulated={simulated} names={names} /> : null}
      {changedSince ? <p className="text-sm text-fg-muted">The check or the route changed since this test. Test again to see how the new one would fire.</p> : null}
      {phase.kind === 'cancelled' ? (
        <p role="status" className="text-sm text-fg">
          {studioDryRunCancelledText(phase.spent)}
        </p>
      ) : null}
      {failure ? (
        <div role="alert" className="space-y-1 text-sm">
          <p className="flex items-start gap-1.5 text-fg">
            <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-danger" />
            {phase.kind === 'error' && phase.spent !== null ? `The test stopped partway: ${failure.message}` : `The test did not run: ${failure.message}`}
          </p>
          {phase.kind === 'error' && phase.spent !== null ? <p className="text-fg">{studioDryRunCancelledText(phase.spent)}</p> : null}
          {phase.kind === 'error' && errorCodeHint(phase.error) ? <p className="text-fg-muted">{errorCodeHint(phase.error)}</p> : null}
          {failure.code ? <p className="font-mono text-2xs text-fg-subtle">{failure.code}</p> : null}
        </div>
      ) : null}

      <ConfirmDialog
        open={phase.kind === 'confirm'}
        onOpenChange={(open) => {
          // Closing without confirming goes back to idle; confirming has already started the run.
          if (!open) setPhase((p) => (p.kind === 'confirm' ? { kind: 'idle' } : p));
        }}
        tone="neutral"
        title="Run this paid test?"
        description={phase.kind === 'confirm' ? studioDryRunConfirmText(phase.details) : undefined}
        confirmLabel="Run the test"
        onConfirm={() => {
          void start(true);
        }}
      />
    </div>
  );
}
