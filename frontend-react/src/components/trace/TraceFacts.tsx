/**
 * "Details" (SPEC §3.3 "Trace detail" 3), collapsed: ids, timings, and what each tier ran on and
 * cost (every cost carries its label, DESIGN.md §8: vendor, estimate, simulated); the timing
 * waterfall; the response headers your app received, rebuilt from the stored decision (with the
 * route's `headers` setting, read when Details opens); the trace's flags in words; and the fast
 * check of every attempt (attempt 1 is the fallback check) with its raw decision/v1. "Show exact
 * Jev request" arrives in P4 (GET /api/traces/:id/decision-request).
 */
import { ChevronRight } from 'lucide-react';
import { useState, type ReactNode } from 'react';
import { CodeBlock } from '@/components/common/CodeBlock';
import { KeyValueList, type KeyValueItem } from '@/components/common/KeyValue';
import { decisionHeaders, deferredHeaders } from '@/domain/decisionHeaders';
import { formatDateTime, formatDurationMs, formatUsd } from '@/domain/format';
import {
  copy,
  costSourceLabel,
  decisionBackendLabel,
  flagWord,
  judgeProviderLabel,
  weakestCostSource,
} from '@/domain/presentation';
import type { AuditDetail, GateDecisionView, TraceSummary } from '@/types/api';
import { TimingWaterfall } from './TimingWaterfall';
import { attemptLabel, attemptSummary } from './traceView';
import { useRouteHeaders } from './useRouteHeaders';
import { waterfallRows } from './waterfall';

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

function Subsection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="space-y-2">
      <h3 className="text-sm font-semibold text-fg">{title}</h3>
      {children}
    </div>
  );
}

function ResponseHeaders({ summary, decision, open }: { summary: TraceSummary; decision: GateDecisionView | null; open: boolean }) {
  const setting = useRouteHeaders(summary.route, summary.policy_version, open);
  const headers = decisionHeaders({ summary, decision, headers: setting });
  const notes = [copy.trace.headers_description];
  if (setting === 'action_only') notes.push(copy.trace.headers_action_only);
  if (setting === null && summary.policy_version !== null) notes.push(copy.trace.headers_setting_unknown);
  if (decision?.late && setting !== 'action_only') notes.push(copy.trace.headers_late);
  if (summary.stream && !deferredHeaders(summary, decision)) notes.push(copy.trace.headers_stream);
  return (
    <Subsection title={copy.trace.headers_title}>
      {notes.map((n) => (
        <p key={n} className="text-xs text-fg-muted">
          {n}
        </p>
      ))}
      <CodeBlock
        code={headers.map((h) => `${h.name}: ${h.value}`).join('\n')}
        label={`${headers.length} header${headers.length === 1 ? '' : 's'}`}
        copyLabel="Copy the response headers"
        maxHeight="20rem"
      />
    </Subsection>
  );
}

function Attempts({ attempts }: { attempts: readonly GateDecisionView[] }) {
  return (
    <Subsection title={copy.trace.attempts_title}>
      <ol className="space-y-3">
        {attempts.map((d) => {
          const label = attemptLabel(d.attempt);
          return (
            <li key={d.attempt} data-attempt={d.attempt} className="space-y-1.5">
              <p className="flex flex-wrap items-baseline gap-x-2 text-sm">
                <span className="font-medium text-fg">{label}</span>
                <span className="text-fg-muted tabular">{attemptSummary(d)}</span>
                {d.late ? <span className="text-xs font-medium text-fg-muted">{copy.trace.late}</span> : null}
              </p>
              <details className="group/raw text-sm">
                <summary className="inline-flex cursor-pointer list-none items-center gap-1 rounded-sm text-xs font-medium text-fg-muted hover:text-fg [&::-webkit-details-marker]:hidden">
                  <ChevronRight aria-hidden="true" className="size-3.5 transition-transform group-open/raw:rotate-90" />
                  {copy.trace.raw_decision}
                </summary>
                <CodeBlock className="mt-2" code={JSON.stringify(d, null, 2)} label={`decision/v1 · ${label}`} maxHeight="24rem" />
              </details>
            </li>
          );
        })}
      </ol>
    </Subsection>
  );
}

export function TraceFacts({
  summary,
  decision,
  attempts = decision ? [decision] : [],
  audit,
}: {
  summary: TraceSummary;
  decision: GateDecisionView | null;
  /** Every decision, one per attempt (attempt 1 is the fallback check). */
  attempts?: readonly GateDecisionView[];
  audit: AuditDetail | null;
}) {
  // What only Details needs (the route's headers setting) is read once it opens.
  const [open, setOpen] = useState(false);
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
    ...(t.ttft_ms !== null ? [{ label: 'First token', value: ms(t.ttft_ms), numeric: true }] : []),
    { label: copy.tiers.fast_check, value: ms(t.gate_ms ?? decision?.gate_ms), numeric: true },
    { label: 'Proxy overhead', value: ms(t.overhead_ms), numeric: true },
    { label: 'Total', value: ms(t.total_ms), numeric: true },
    { label: copy.tiers.deep_audit, value: ms(audit?.processing_ms), numeric: true },
  ];
  const rows = waterfallRows({ timings: t, gateMs: decision?.gate_ms, deferred: deferredHeaders(summary, decision) });
  const flags = summary.flags ?? [];
  return (
    <details className="group rounded-lg border border-border bg-surface" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary className="flex cursor-pointer list-none items-center gap-2 rounded-lg px-4 py-3 text-base font-semibold text-fg hover:bg-surface-hover [&::-webkit-details-marker]:hidden">
        <ChevronRight aria-hidden="true" className="size-4 shrink-0 transition-transform group-open:rotate-90" />
        <h2 className="text-base font-semibold">{copy.trace.details}</h2>
      </summary>
      <div className="space-y-6 border-t border-border px-4 py-3">
        <div className="grid gap-x-8 gap-y-4 @3xl:grid-cols-2">
          <KeyValueList items={ids} />
          <KeyValueList items={timings} />
          <KeyValueList items={[...fastCheckFacts(decision), ...deepAuditFacts(audit)]} />
          {flags.length > 0 ? (
            <Subsection title={copy.trace.flags_title}>
              <ul className="space-y-1 text-sm text-fg">
                {flags.map((f) => (
                  <li key={f} data-flag={f}>
                    {flagWord(f)}
                  </li>
                ))}
              </ul>
            </Subsection>
          ) : null}
        </div>
        {rows.length >= 2 ? <TimingWaterfall rows={rows} /> : null}
        {decision || summary.mode === 'observe' ? <ResponseHeaders summary={summary} decision={decision} open={open} /> : null}
        {attempts.length > 0 ? <Attempts attempts={attempts} /> : null}
      </div>
    </details>
  );
}
