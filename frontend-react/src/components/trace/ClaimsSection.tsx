/**
 * "Claims" (SPEC §3.3 "Trace detail" 2, right): a skeleton reading "Deep audit running… usually
 * 5–20 s" while the audit is on its way, then one row per claim with its verdict icon and label.
 * Without a deep audit it says why ("Not assessed", "Not sampled for deep audit", …).
 */
import { LoaderCircle } from 'lucide-react';
import { AuditState } from '@/components/outcome/AuditState';
import { VerdictBadge } from '@/components/outcome/VerdictBadge';
import { Skeleton } from '@/components/ui/skeleton';
import { auditStateView, copy } from '@/domain/presentation';
import type { AuditDetail, TraceSummary } from '@/types/api';
import { auditHasResult, claimRowId, claimsSummary, disagreementLine, orderedClaims } from './traceView';

const DEEP_AUDIT = copy.tiers.deep_audit;

function ClaimsPending({ label }: { label: string }) {
  return (
    <div role="status" className="space-y-2 py-2">
      <p className="flex items-center gap-2 text-sm text-fg-muted">
        <LoaderCircle aria-hidden="true" className="size-4 shrink-0 animate-spin" />
        {label}
      </p>
      <Skeleton className="h-10 w-full" />
      <Skeleton className="h-10 w-4/5" />
    </div>
  );
}

export function ClaimsSection({
  summary,
  audit,
  claimIdPrefix = '',
}: {
  summary: TraceSummary;
  audit: AuditDetail | null;
  /** Prefix of the row ids the answer's claim marks link to (claimRowId). */
  claimIdPrefix?: string;
}) {
  const state = auditStateView(summary.audit);
  const claims = orderedClaims(audit);
  const counts = claimsSummary(audit);
  const disagreement = disagreementLine({ summary, audit });

  let content;
  if (audit && auditHasResult(audit.status)) {
    content =
      claims.length > 0 ? (
        <ol aria-label="Claims" className="divide-y divide-border">
          {claims.map((c) => (
            <li
              key={c.index}
              id={claimRowId(claimIdPrefix, c.index)}
              tabIndex={-1}
              className="-mx-2 space-y-1 rounded-md px-2 py-2.5 focus:bg-surface-hover"
            >
              <div className="flex flex-wrap items-baseline gap-x-2">
                <VerdictBadge verdict={c.status} legend />
                {c.span === null && c.status !== 'NOT_CHECKED' ? (
                  <span className="text-xs text-fg-muted">{copy.trace.claim_not_located}</span>
                ) : null}
              </div>
              <p className="text-sm text-fg">{c.claim ?? 'Hidden: this key cannot see claim text.'}</p>
              {c.reasoning ? <p className="text-xs text-fg-muted">{c.reasoning}</p> : null}
            </li>
          ))}
        </ol>
      ) : (
        <p className="py-2 text-sm text-fg-muted">{state.label}</p>
      );
  } else if (state.pending) {
    content = <ClaimsPending label={copy.trace.claims_pending} />;
  } else if (auditHasResult(summary.audit.status)) {
    // Finished, and the result is on its way from the API.
    content = <ClaimsPending label={copy.states.loading} />;
  } else {
    content = <p className="py-2 text-sm text-fg-muted">{state.label}</p>;
  }

  return (
    <section aria-labelledby="claims-title" className="space-y-2">
      <div className="flex items-baseline justify-between gap-2">
        <h2 id="claims-title" className="text-base font-semibold text-fg">
          {copy.trace.claims}
        </h2>
        <span className="text-xs text-fg-muted">{DEEP_AUDIT}</span>
      </div>
      <div className="space-y-1 rounded-lg border border-border bg-surface px-4 py-1.5">
        {audit && auditHasResult(audit.status) ? (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border py-2">
            <AuditState audit={summary.audit} />
            {counts ? <span className="text-xs text-fg-muted tabular">{counts}</span> : null}
          </div>
        ) : null}
        {disagreement ? <p className="py-1 text-sm text-fg">{disagreement}</p> : null}
        {content}
      </div>
    </section>
  );
}
