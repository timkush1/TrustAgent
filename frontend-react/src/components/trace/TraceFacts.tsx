/**
 * "Details" (SPEC §3.3 "Trace detail" 3), collapsed: ids and timings. The timings waterfall,
 * costs, snapshot, headers, raw decision/v1 and "Show exact Jev request" join in later phases.
 */
import { ChevronRight } from 'lucide-react';
import type { ReactNode } from 'react';
import { copy } from '@/domain/presentation';
import { formatDateTime, formatDurationMs } from '@/domain/format';
import type { AuditDetail, GateDecisionView, TraceSummary } from '@/types/api';

function Fact({ label, children, mono = false }: { label: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className="grid grid-cols-[10rem_minmax(0,1fr)] gap-3 py-1.5">
      <dt className="text-fg-muted">{label}</dt>
      <dd className={mono ? 'font-mono text-xs break-all text-fg' : 'text-fg tabular'}>{children}</dd>
    </div>
  );
}

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
  return (
    <details className="group rounded-lg border border-border bg-surface">
      <summary className="flex cursor-pointer list-none items-center gap-2 rounded-lg px-4 py-3 text-base font-semibold text-fg hover:bg-surface-hover [&::-webkit-details-marker]:hidden">
        <ChevronRight aria-hidden="true" className="size-4 shrink-0 transition-transform group-open:rotate-90" />
        <h2 className="text-base font-semibold">{copy.trace.details}</h2>
      </summary>
      <div className="grid gap-x-8 border-t border-border px-4 py-2 text-sm @3xl:grid-cols-2">
        <dl>
          <Fact label="Request id" mono>
            {summary.trace_id}
          </Fact>
          {summary.client_request_id ? (
            <Fact label="Client request id" mono>
              {summary.client_request_id}
            </Fact>
          ) : null}
          <Fact label="Deep audit id" mono>
            {summary.audit.audit_id ?? '—'}
          </Fact>
          <Fact label="Received">{formatDateTime(summary.created_at)}</Fact>
          <Fact label="Route">{policy}</Fact>
          <Fact label="Model">{summary.model ?? '—'}</Fact>
        </dl>
        <dl>
          <Fact label="Model answer">{formatDurationMs(t.upstream_ms)}</Fact>
          <Fact label={copy.tiers.fast_check}>{formatDurationMs(t.gate_ms ?? decision?.gate_ms)}</Fact>
          <Fact label="Proxy overhead">{formatDurationMs(t.overhead_ms)}</Fact>
          <Fact label="Total">{formatDurationMs(t.total_ms)}</Fact>
          <Fact label={copy.tiers.deep_audit}>{formatDurationMs(audit?.processing_ms)}</Fact>
        </dl>
      </div>
    </details>
  );
}
