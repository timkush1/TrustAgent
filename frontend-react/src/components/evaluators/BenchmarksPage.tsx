/**
 * Benchmarks (SPEC §3.3 Evaluators, §2.13; D72, D111): the committed benchmark runs in
 * docs/benchmarks/results/*.json, bundled at build time (the static build hides this page). Only
 * measured runs are shown (`measured: true`, `mock: false`); every figure sits under a Measured
 * badge, costs carry their own source label, and a confidence interval is written next to a metric
 * when the run has one. With no committed run the page says how to make one (`make bench-lite`).
 */
import { FlaskConical } from 'lucide-react';
import type { ReactNode } from 'react';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { KeyValueList } from '@/components/common/KeyValue';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState } from '@/components/common/StateViews';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { formatCount, formatDateTime, formatDurationMs, formatRisk, formatUsd } from '@/domain/format';
import { copy, costSourceLabel, fillTemplate, splitCommands } from '@/domain/presentation';
import type { BenchmarkRun } from '@/types/api';
import { committedRuns } from './benchmarkRuns';
import { EvaluatorsTabs } from './EvaluatorsTabs';

type SystemId = BenchmarkRun['systems'][number]['id'];
type Metric = 'precision' | 'recall' | 'f1' | 'auroc' | 'ece';

const SYSTEMS: Record<SystemId, { label: string; description: string }> = {
  gate: { label: copy.tiers.fast_check, description: 'All route checks as questions in one call' },
  llm_same_checks: { label: 'LLM, same checks', description: 'The same questions asked of a general LLM' },
  llm_full_audit: { label: copy.tiers.deep_audit, description: 'Claim by claim against the documents' },
  cascade_response: { label: 'Cascade', description: 'Fast check first, deep audit on what it flags' },
};

const METRICS: { id: Metric; label: string; hint: string }[] = [
  { id: 'precision', label: 'Precision', hint: 'flagged answers that were hallucinated' },
  { id: 'recall', label: 'Recall', hint: 'hallucinated answers it flagged' },
  { id: 'f1', label: 'F1', hint: 'balance of the two' },
  { id: 'auroc', label: 'AUROC', hint: 'ranking, 1.0 is perfect' },
  { id: 'ece', label: 'Calibration error', hint: 'ECE, lower is better' },
];

function systemLabel(id: SystemId): { label: string; description: string } {
  return SYSTEMS[id] ?? { label: 'Other system', description: '' };
}

function metricText(system: BenchmarkRun['systems'][number], metric: Metric): ReactNode {
  const value = system.metrics[metric];
  if (value === null || value === undefined) return <span className="text-fg-subtle">—</span>;
  const ci = system.metrics.ci95?.[metric];
  return (
    <>
      <span className="text-fg">{formatRisk(value)}</span>
      {ci && ci.length === 2 ? (
        <span className="block text-2xs text-fg-muted">
          95% CI {formatRisk(ci[0])}–{formatRisk(ci[1])}
        </span>
      ) : null}
    </>
  );
}

function RunCard({ run }: { run: BenchmarkRun }) {
  const dataset = run.dataset.subset ? `${run.dataset.name}: ${run.dataset.subset}` : run.dataset.name;
  const titleId = `bench-${run.run_id}`;
  return (
    <Card>
      <CardHeader
        id={titleId}
        title={`${dataset} · ${run.tier === 'lite' ? 'Lite run' : 'Full run'}`}
        description={`${formatCount(run.dataset.n)} answers from the ${run.dataset.split} split, run on ${formatDateTime(run.created_at)}.`}
        actions={
          <EvidenceBadge
            label="measured"
            basis={`Measured by make bench-${run.tier} at commit ${run.git_sha.slice(0, 12)} on ${formatCount(run.dataset.n)} labelled answers (${dataset}, revision ${run.dataset.revision}): agreement with the dataset's labels.`}
            subject={`${dataset} benchmark`}
          />
        }
      />
      <CardContent className="space-y-4">
        <div className="overflow-x-auto">
          <table aria-labelledby={titleId} className="w-full min-w-[44rem] text-sm tabular">
            <thead>
              <tr className="border-b border-border text-left text-xs text-fg-muted">
                <th scope="col" className="py-2 pr-3 font-medium">
                  System
                </th>
                {METRICS.map((m) => (
                  <th key={m.id} scope="col" className="px-2 py-2 font-medium" title={m.hint}>
                    {m.label}
                  </th>
                ))}
                <th scope="col" className="px-2 py-2 font-medium">
                  Latency p50 / p95
                </th>
                <th scope="col" className="py-2 pl-2 font-medium">
                  Cost per 1,000 answers
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {run.systems.map((s) => {
                const sys = systemLabel(s.id);
                const model = s.decision_model?.snapshot ?? s.judge?.model ?? null;
                const costLabel = costSourceLabel(s.cost.cost_source);
                return (
                  <tr key={s.id} data-system={s.id} className="align-top">
                    <th scope="row" className="py-2 pr-3 text-left font-medium text-fg">
                      {sys.label}
                      <span className="block text-2xs font-normal text-fg-muted">{sys.description}</span>
                      {model ? <span className="block font-mono text-2xs font-normal text-fg-muted">{model}</span> : null}
                    </th>
                    {METRICS.map((m) => (
                      <td key={m.id} className="px-2 py-2">
                        {metricText(s, m.id)}
                      </td>
                    ))}
                    <td className="px-2 py-2 text-fg">
                      {formatDurationMs(s.latency_ms.p50)} / {formatDurationMs(s.latency_ms.p95)}
                    </td>
                    <td className="py-2 pl-2 text-fg">
                      {costLabel === null ? (
                        <span className="text-fg-subtle">not reported</span>
                      ) : (
                        <>
                          {formatUsd(s.cost.usd_per_1k)}
                          <span className="block text-2xs text-fg-muted">{costLabel}</span>
                        </>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        <KeyValueList
          items={[
            { label: 'Dataset revision', value: run.dataset.revision, mono: true },
            { label: 'License', value: run.dataset.license_note },
            { label: 'Commit', value: run.git_sha, mono: true, copy: true },
            { label: 'Failed calls', value: formatCount(run.errors.failed), numeric: true },
            { label: 'Left out to stay under budget', value: formatCount(run.errors.excluded_over_budget), numeric: true },
            { label: 'Where it ran', value: `${run.environment.region}, ${formatCount(run.environment.concurrency)} at a time` },
          ]}
        />
      </CardContent>
    </Card>
  );
}

function EmptyRuns() {
  const text = fillTemplate(copy.evaluators.benchmarks_empty);
  const [title, ...rest] = text.split(/(?<=\.)\s+/);
  return (
    <EmptyState icon={FlaskConical} title={title}>
      <p>
        {splitCommands(rest.join(' ')).map((part, i) =>
          part.command ? (
            <code key={i} className="rounded-sm bg-surface-sunken px-1 font-mono text-xs text-fg">
              {part.text}
            </code>
          ) : (
            <span key={i}>{part.text}</span>
          ),
        )}
      </p>
      <p className="mt-2">It needs an OpenRouter key and costs at most $5; the result is committed to docs/benchmarks/results and shown here.</p>
    </EmptyState>
  );
}

export function BenchmarksPage({ runs = committedRuns() }: { runs?: readonly BenchmarkRun[] }) {
  return (
    <div>
      <EvaluatorsTabs current="benchmarks" />
      <PageHeader description="How the checks do on public, human-labelled datasets. Committed runs only: every figure was measured, none simulated." />
      {runs.length === 0 ? (
        <EmptyRuns />
      ) : (
        <div className="space-y-6">
          {runs.map((run) => (
            <RunCard key={run.run_id} run={run} />
          ))}
        </div>
      )}
    </div>
  );
}
