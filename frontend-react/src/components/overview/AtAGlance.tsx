/**
 * One quiet strip of the numbers that answer "Is my AI answering safely?" (SPEC §3.3 Overview):
 * requests, what was stopped or flagged, how much could be checked against documents, the fast
 * check's p50/p95 and the hallucination rate. A single surface with hairline dividers, not a row
 * of stat cards (DESIGN.md).
 */
import type { ReactNode } from 'react';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { LabelledRateView } from '@/components/common/LabelledRate';
import { Term } from '@/components/common/Term';
import { formatCount, formatDurationMs } from '@/domain/format';
import { copy } from '@/domain/presentation';
import { formatPercent, share } from '@/domain/rates';
import type { OverviewStats, ValueLabel } from '@/types/api';

function Item({ label, children, note }: { label: ReactNode; children: ReactNode; note?: ReactNode }) {
  return (
    <div className="min-w-0 space-y-1 px-4 py-3">
      <dt className="text-xs text-fg-muted">{label}</dt>
      <dd className="space-y-0.5">
        {children}
        {note ? <div className="text-xs text-fg-muted">{note}</div> : null}
      </dd>
    </div>
  );
}

export function AtAGlance({ stats, label, replay = false }: { stats: OverviewStats; label: ValueLabel; replay?: boolean }) {
  const t = stats.totals;
  const stopped = t.blocked + t.fallback;
  const gate = stats.latency_ms.gate;
  return (
    <dl
      aria-label="At a glance"
      className="grid grid-cols-1 divide-border rounded-lg border border-border bg-surface sm:grid-cols-2 sm:divide-x lg:grid-cols-5 max-lg:[&>*]:border-b max-lg:[&>*]:border-border"
    >
      <Item label="Requests" note={<EvidenceBadge label={label} subject="Request counts" />}>
        <span className="text-xl font-semibold text-fg tabular">{formatCount(t.requests)}</span>
      </Item>
      <Item
        label={copy.savings.stopped}
        note={
          <>
            {`${formatCount(t.flagged)} flagged to your app · ${formatCount(t.gate_errors)} check errors`}{' '}
            <EvidenceBadge label={label} subject="Answers stopped or flagged" />
          </>
        }
      >
        <span className="text-xl font-semibold text-fg tabular">{formatCount(stopped)}</span>
      </Item>
      <Item
        label={
          <>
            <Term id="groundedness">Groundedness</Term> checked
          </>
        }
        note={
          <>
            <a href="#evidence-coverage" className="font-medium text-fg underline underline-offset-2">
              {copy.overview.why_not}
            </a>{' '}
            <EvidenceBadge label={label} subject="Groundedness checked" />
          </>
        }
      >
        <span className="text-xl font-semibold text-fg tabular">
          {/* The same share the evidence coverage card headlines ("Groundedness checked on 12% of requests"). */}
          {formatPercent(share(stats.evidence_coverage.groundedness_assessed, t.requests))}
        </span>
      </Item>
      <Item
        label={`${copy.tiers.fast_check} p50 / p95`}
        note={
          <>
            Time the fast check added before the answer was released{' '}
            <EvidenceBadge label={label} subject={`${copy.tiers.fast_check} latency`} />
          </>
        }
      >
        <span className="text-xl font-semibold text-fg tabular">
          {formatDurationMs(gate.p50)} <span className="text-fg-subtle">/</span> {formatDurationMs(gate.p95)}
        </span>
      </Item>
      <Item
        label={
          <>
            <Term id="hallucination_rate">Hallucination rate</Term>
          </>
        }
      >
        {/* A replay's audits were picked by risk: it has no uniform sample, and says why. */}
        <LabelledRateView
          rate={stats.hallucination_rate}
          subject="Hallucination rate"
          needsTemplate={replay && stats.hallucination_rate.n === 0 ? copy.recording.no_uniform_sample : undefined}
        />
      </Item>
    </dl>
  );
}
