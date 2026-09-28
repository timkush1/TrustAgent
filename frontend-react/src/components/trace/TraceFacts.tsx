/**
 * "Details" (SPEC §3.3 "Trace detail" 3), collapsed: ids, timings, and what each tier ran on and
 * cost. Every cost carries its label (DESIGN.md §8: vendor, estimate, simulated). The timings
 * waterfall, headers, raw decision/v1 and "Show exact Jev request" join in P3.
 */
import { ChevronRight } from 'lucide-react';
import { KeyValueList, type KeyValueItem } from '@/components/common/KeyValue';
import { copy, costSourceLabel, decisionBackendLabel, judgeProviderLabel, weakestCostSource } from '@/domain/presentation';
import { formatDateTime, formatDurationMs, formatUsd } from '@/domain/format';
import type { AuditDetail, GateDecisionView, TraceSummary } from '@/types/api';

/** The fast check's model and cost from the decision's usage record. */
function fastCheckFacts(decision: GateDecisionView | null): KeyValueItem[] {
  const usage = decision?.decision_model ?? null;
  if (!usage) return [{ label: `${copy.tiers.fast_check} model`, value: null }];
  const label = costSourceLabel(usage.cost_source);
  return [
    {
      label: `${copy.tiers.fast_check} model`,
      value: usage.snapshot ?? usage.model_requested,
      mono: true,
      hint: decisionBackendLabel(usage.backend),
    },
    {
      label: `${copy.tiers.fast_check} cost`,
      value: label === null ? null : formatUsd(usage.cost_usd),
      numeric: true,
      hint: label ?? 'not reported',
    },
  ];
}

/** The deep audit's judge and the cost of its model calls (AuditDetail.costs). */
function deepAuditFacts(audit: AuditDetail | null): KeyValueItem[] {
  const judge = audit?.judge ?? null;
  const items: KeyValueItem[] = [
    {
      label: `${copy.tiers.deep_audit} judge`,
      value: judge ? judge.model : null,
      mono: true,
      hint: judge ? `${judgeProviderLabel(judge.effective_provider)}${judge.fallback_active ? ' · fallback judge' : ''}` : undefined,
    },
  ];
  const costs = audit?.costs ?? [];
  if (costs.length > 0) {
    const source = weakestCostSource(costs.map((c) => c.cost_source));
    const label = costSourceLabel(source);
    const total = costs.reduce((sum, c) => sum + c.cost_usd, 0);
    const calls = `${costs.length} model call${costs.length === 1 ? '' : 's'}`;
    items.push({
      label: `${copy.tiers.deep_audit} cost`,
      value: label === null ? null : formatUsd(total),
      numeric: true,
      hint: label === null ? `${calls}, not reported` : `${label} · ${calls}`,
    });
  }
  return items;
}

/** A duration, or null (shown as a quiet em dash) when it was not measured. */
const ms = (v: number | null | undefined) => (v === null || v === undefined ? null : formatDurationMs(v));

export function TraceFacts({
  summary,
  decision,
  audit,
}: {
  summary: TraceSummary;
  decision: GateDecisionView | null;
  audit: AuditDetail | null;
}) {
  const t = summary.timings;
  const policy = summary.policy_version === null ? summary.route : `${summary.route} · policy version ${summary.policy_version}`;
  const ids: KeyValueItem[] = [
    { label: 'Request id', value: summary.trace_id, mono: true, copy: true },
    ...(summary.client_request_id ? [{ label: 'Client request id', value: summary.client_request_id, mono: true }] : []),
    { label: 'Deep audit id', value: summary.audit.audit_id, mono: true },
    { label: 'Received', value: formatDateTime(summary.created_at) },
    { label: 'Route', value: policy },
    { label: 'Model', value: summary.model },
  ];
  const timings: KeyValueItem[] = [
    { label: 'Model answer', value: ms(t.upstream_ms), numeric: true },
    { label: copy.tiers.fast_check, value: ms(t.gate_ms ?? decision?.gate_ms), numeric: true },
    { label: 'Proxy overhead', value: ms(t.overhead_ms), numeric: true },
    { label: 'Total', value: ms(t.total_ms), numeric: true },
    { label: copy.tiers.deep_audit, value: ms(audit?.processing_ms), numeric: true },
  ];
  return (
    <details className="group rounded-lg border border-border bg-surface">
      <summary className="flex cursor-pointer list-none items-center gap-2 rounded-lg px-4 py-3 text-base font-semibold text-fg hover:bg-surface-hover [&::-webkit-details-marker]:hidden">
        <ChevronRight aria-hidden="true" className="size-4 shrink-0 transition-transform group-open:rotate-90" />
        <h2 className="text-base font-semibold">{copy.trace.details}</h2>
      </summary>
      <div className="grid gap-x-8 gap-y-4 border-t border-border px-4 py-3 @3xl:grid-cols-2">
        <KeyValueList items={ids} />
        <KeyValueList items={timings} />
        <KeyValueList items={[...fastCheckFacts(decision), ...deepAuditFacts(audit)]} />
      </div>
    </details>
  );
}
