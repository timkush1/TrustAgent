/**
 * Performance (SPEC §3.3 Overview, collapsed by default): where the time goes (request overhead,
 * fast check, your model, deep audit), each pipeline stage's p95, and which fast-check backend
 * answered. Latency and counts carry no verdict, so everything here is in the neutral ink.
 */
import { BarChart } from '@/components/charts/BarChart';
import { ChartTable } from '@/components/charts/ChartParts';
import { Section } from '@/components/ui/card';
import { backendLabel, latencyKindLabel, STAGE_ORDER, stageLatencyLabel } from '@/domain/controlPresentation';
import { formatCount, formatDurationMs } from '@/domain/format';
import { copy } from '@/domain/presentation';
import { formatPercent, share } from '@/domain/rates';
import type { DecisionBackend, OverviewStats } from '@/types/api';

const LATENCY_KINDS = ['overhead', 'gate', 'upstream', 'audit'] as const;

export function PerformanceSection({ stats }: { stats: OverviewStats }) {
  const stages = STAGE_ORDER.map((s) => ({ key: s, label: stageLatencyLabel(s), ...stats.stage_latency_ms[s] })).filter(
    (s) => s.p95 !== null && s.p95 !== undefined,
  );
  const mix = Object.entries(stats.backend_mix) as [DecisionBackend, number][];
  const mixTotal = mix.reduce((sum, [, n]) => sum + n, 0);

  return (
    <Section collapsible title={copy.overview.performance} description="Latency percentiles and which fast-check backend answered.">
      <div className="grid gap-4 lg:grid-cols-2">
        <div className="space-y-2 rounded-lg border border-border bg-surface p-4">
          <h3 className="text-sm font-semibold text-fg">Time per request</h3>
          <ChartTable
            caption="Latency per request, p50 and p95"
            columns={[
              { key: 'label', label: 'Part' },
              { key: 'p50', label: 'p50', numeric: true },
              { key: 'p95', label: 'p95', numeric: true },
            ]}
            rows={LATENCY_KINDS.map((k) => ({
              label: latencyKindLabel(k),
              p50: formatDurationMs(stats.latency_ms[k]?.p50),
              p95: formatDurationMs(stats.latency_ms[k]?.p95),
            }))}
          />
        </div>
        <div className="space-y-2 rounded-lg border border-border bg-surface p-4">
          {stages.length > 0 ? (
            <BarChart
              title="Slowest stages (p95)"
              description="Each stage of the fast check and the deep audit"
              valueLabel="p95"
              format={formatDurationMs}
              data={stages.map((s) => ({ key: s.key, label: s.label, value: s.p95 ?? 0 }))}
              labelWidth={170}
            />
          ) : (
            <p className="text-sm text-fg-muted">No stage timings in this window.</p>
          )}
        </div>
        <div className="space-y-2 rounded-lg border border-border bg-surface p-4 lg:col-span-2">
          <h3 className="text-sm font-semibold text-fg">{copy.tiers.fast_check} backend</h3>
          {mix.length === 0 ? (
            <p className="text-sm text-fg-muted">No fast checks in this window.</p>
          ) : (
            <ul className="flex flex-wrap gap-x-6 gap-y-1 text-sm">
              {mix.map(([backend, n]) => (
                <li key={backend} className="flex items-baseline gap-2">
                  <span className="text-fg">{backendLabel(backend)}</span>
                  <span className="text-xs text-fg-muted tabular">
                    {formatCount(n)} checks · {formatPercent(share(n, mixTotal))}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </Section>
  );
}
