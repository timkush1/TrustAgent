/**
 * A live check's result (D104): the label "Live · {snapshot} · uncalibrated · your key", what the
 * check cost (OpenRouter's figure, labelled vendor; else priced here, labelled estimate), the
 * Playground's timeline with the deep-audit lane saying it does not run in the browser, and "Show
 * exact Jev request" with the bodies that were sent. A live result is never compared with a
 * scenario's expected outcome. Owned by WEB-LIVE (LV-6).
 */
import { ChevronRight } from 'lucide-react';
import { CommandText } from '@/components/common/CommandText';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { ResultTimeline } from '@/components/playground/ResultTimeline';
import { JevRequestBody } from '@/components/trace/JevRequestPanel';
import { formatUsd } from '@/domain/format';
import { copy, fillTemplate } from '@/domain/presentation';
import { liveResultLabel } from '@/domain/words/live';
import { liveText } from './liveText';
import type { LiveCheckResult } from '@/lib/gate/liveCheck';

export function LiveResult({ check }: { check: LiveCheckResult }) {
  const usage = check.result.decision.decision_model;
  const snapshot = check.gate.snapshot ?? usage?.model_requested ?? '';
  return (
    <div className="space-y-4" data-testid="live-result">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <span className="inline-flex min-h-6 max-w-full items-center rounded-md border border-border bg-surface-sunken px-2 py-0.5 text-xs font-medium break-words text-fg-muted" data-testid="live-label">
          {liveResultLabel(snapshot)}
        </span>
        {check.cost ? (
          <span className="inline-flex items-center gap-1.5 text-xs text-fg-muted tabular" data-testid="live-check-cost">
            {liveText.this_check}: {formatUsd(check.cost.usd)}
            <EvidenceBadge
              label={check.cost.label}
              basis={check.cost.label === 'vendor' ? liveText.vendor_basis : liveText.computed_basis}
              subject={liveText.check_cost_subject}
            />
          </span>
        ) : null}
      </div>
      <ResultTimeline
        state={{
          mode: 'audit',
          tokens: '',
          decision: null,
          result: check.result,
          running: false,
          deepAudit: (
            <p className="text-sm text-fg-muted">
              <CommandText text={fillTemplate(copy.playground_live.deep_audit_not_in_browser)} />
            </p>
          ),
        }}
      />
      <details className="group">
        <summary className="inline-flex cursor-pointer list-none items-center gap-1 text-sm font-medium text-fg [&::-webkit-details-marker]:hidden">
          <ChevronRight aria-hidden="true" className="size-4 transition-transform group-open:rotate-90" />
          {liveText.show_request}
        </summary>
        <JevRequestBody decision={check.result.decision} view={check.request} />
      </details>
    </div>
  );
}
