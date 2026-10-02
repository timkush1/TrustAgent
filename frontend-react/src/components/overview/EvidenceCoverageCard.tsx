/**
 * Evidence coverage (SPEC §3.3, §7.1): "Groundedness checked on 85% of requests". Groundedness can
 * only be judged against documents the request carried, so the card explains the rest in a
 * "Why not?" panel: which sources supplied documents, why the other requests had none, suggested
 * fixes from the proxy (admin, when it serves them), and the `trustagent.context` snippet that
 * attaches documents to any request. A suggested tag pattern goes into a route's
 * `evidence.tag_patterns` in one step: it becomes a policy draft and opens the policy review (diff,
 * validation, Preview impact) right here, and nothing changes until it is saved.
 */
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ChevronDown, FileSearch } from 'lucide-react';
import { lazy, Suspense, useId, useState } from 'react';
import { useDataSource } from '@/app/services';
import { CodeBlock } from '@/components/common/CodeBlock';
import { CopyButton } from '@/components/common/CopyButton';
import { DegradedState } from '@/components/common/DegradedState';
import { Term } from '@/components/common/Term';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { isApiError } from '@/data/errors';
import { evidenceCoverageQuery, evidenceSuggestionsQuery } from '@/data/queries';

// The policy draft and review (YAML parsing, diffs) load only when a suggestion can use them.
const AddTagPatternAction = lazy(() => import('@/components/policies/AddTagPatternAction'));
import { formatCount } from '@/domain/format';
import { copy } from '@/domain/presentation';
import { formatPercent } from '@/domain/rates';
import { contextExtensionSnippet } from '@/domain/snippets';
import type { EvidenceSuggestion, OverviewStats, TimeWindow } from '@/types/api';
import { coverageSummary, type CoverageSummary } from './overviewModel';

function SourceBreakdown({ summary }: { summary: CoverageSummary }) {
  const max = Math.max(1, ...summary.bySource.map((s) => s.count));
  return (
    <div className="space-y-2">
      <h4 className="text-xs font-medium text-fg-muted">Where documents came from</h4>
      <ul className="space-y-1.5">
        {summary.bySource.map((s) => (
          <li key={s.source} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-0.5 text-sm">
            <span className="min-w-0 truncate text-fg">{s.label}</span>
            <span className="text-xs text-fg-muted tabular">
              {formatCount(s.count)} · {formatPercent(s.share)}
            </span>
            <span aria-hidden="true" className="col-span-2 h-1 overflow-hidden rounded-full bg-surface-sunken">
              <span className="block h-full rounded-full bg-fg-subtle" style={{ width: `${(s.count / max) * 100}%` }} />
            </span>
          </li>
        ))}
      </ul>
      <p className="text-xs text-fg-muted">A request can carry documents from more than one source.</p>
    </div>
  );
}

function Gaps({ summary }: { summary: CoverageSummary }) {
  if (summary.gaps.length === 0) return null;
  return (
    <div className="space-y-2">
      <h4 className="text-xs font-medium text-fg-muted">Why the others were not checked</h4>
      <ul className="space-y-2">
        {summary.gaps.map((g) => (
          <li key={g.reason} className="text-sm">
            <p className="flex items-baseline justify-between gap-3">
              <span className="font-medium text-fg">{g.label}</span>
              <span className="text-xs text-fg-muted tabular">{formatCount(g.count)} requests</span>
            </p>
            {g.hint ? <p className="text-xs text-fg-muted">{g.hint}</p> : null}
          </li>
        ))}
      </ul>
    </div>
  );
}

function SuggestionItem({ s }: { s: EvidenceSuggestion }) {
  return (
    <li className="space-y-1.5 rounded-md border border-border px-3 py-2">
      <p className="text-sm font-medium text-fg">{s.label}</p>
      <p className="text-xs text-fg-muted">
        {formatCount(s.would_gain)} of your last {formatCount(s.sample)} requests would gain documents.
      </p>
      {s.pattern ? (
        <div className="flex items-center gap-1">
          <code className="min-w-0 truncate rounded-sm bg-surface-sunken px-1.5 py-0.5 font-mono text-2xs text-fg">
            {s.pattern.pattern}
            {s.pattern.flags ? ` (flags ${s.pattern.flags})` : ''}
          </code>
          <CopyButton value={s.pattern.pattern} label="Copy pattern" />
        </div>
      ) : null}
      {s.pattern && s.kind === 'tag_pattern' ? (
        <Suspense fallback={<Skeleton className="h-8 w-48" />}>
          <AddTagPatternAction pattern={s.pattern} />
        </Suspense>
      ) : null}
      {s.example_trace_id ? (
        <p className="text-xs">
          <Link to="/app/traffic/$traceId" params={{ traceId: s.example_trace_id }} className="font-medium text-fg underline underline-offset-2">
            See an example request
          </Link>
        </p>
      ) : null}
    </li>
  );
}

function Suggestions({ timeWindow }: { timeWindow: TimeWindow }) {
  const ds = useDataSource();
  const q = useQuery(evidenceSuggestionsQuery(ds, { window: timeWindow }));
  if (q.isPending) return <Skeleton className="h-12 w-full" />;
  if (q.isError) {
    // Admin-only, and new in P4: say so plainly instead of showing an error.
    const status = isApiError(q.error) ? q.error.status : 0;
    const text =
      status === 401 || status === 403
        ? 'Suggestions scan stored prompts, so they need an admin sign-in.'
        : status === 404 || status === 501
          ? 'This proxy does not suggest evidence patterns yet.'
          : `Suggestions are not available right now: ${q.error.message}`;
    return <p className="text-xs text-fg-muted">{text}</p>;
  }
  if (q.data.items.length === 0) return <p className="text-xs text-fg-muted">No repeated document shapes found in recent prompts.</p>;
  return (
    <ul className="space-y-2">
      {q.data.items.map((s, i) => (
        <SuggestionItem key={`${s.kind}-${i}`} s={s} />
      ))}
    </ul>
  );
}

export function EvidenceCoverageCard({ stats, timeWindow }: { stats: OverviewStats; timeWindow: TimeWindow }) {
  const ds = useDataSource();
  const [open, setOpen] = useState(false);
  const panelId = useId();
  // The coverage endpoint adds the reasons; the overview's own numbers carry the rest.
  // Only the Why not? panel needs the reasons and suggestions: fetch them when it opens.
  const coverage = useQuery({ ...evidenceCoverageQuery(ds, { window: timeWindow }), enabled: open });
  const summary = coverageSummary(stats.totals.requests, {
    groundedness_assessed: stats.evidence_coverage.groundedness_assessed,
    by_source: stats.evidence_coverage.by_source,
    top_reasons: coverage.data?.top_reasons,
  });
  const noTraffic = stats.totals.requests === 0;

  return (
    <Card id="evidence-coverage" className="scroll-mt-20">
      <CardHeader
        title={
          <>
            <Term id="evidence">Evidence</Term> coverage
          </>
        }
        description="Groundedness is only judged against documents the request carried."
      />
      <CardContent className="space-y-3">
        {noTraffic ? (
          <p className="text-sm text-fg-muted">No requests in this window yet.</p>
        ) : (
          <>
            <div className="space-y-1">
              <p className="text-2xl font-semibold text-fg tabular">{formatPercent(summary.rate)}</p>
              <p className="text-sm text-fg">{summary.headline}</p>
              <p className="text-xs text-fg-muted">
                {formatCount(stats.evidence_coverage.with_evidence)} of {formatCount(stats.totals.requests)} requests carried documents;
                the rest are marked <Term id="not_checked">not checked</Term> instead of guessed.
              </p>
            </div>
            <Button variant="secondary" size="sm" aria-expanded={open} aria-controls={panelId} onClick={() => setOpen((v) => !v)}>
              <FileSearch aria-hidden="true" />
              {copy.overview.why_not}
              <ChevronDown aria-hidden="true" className={open ? 'rotate-180 transition-transform' : 'transition-transform'} />
            </Button>
          </>
        )}
        <div id={panelId} hidden={!open || noTraffic} className="space-y-4 border-t border-border pt-3">
          {open && !noTraffic ? (
            <>
              <SourceBreakdown summary={summary} />
              {coverage.isError ? (
                <DegradedState
                  title="The reasons are not available"
                  reason={coverage.error.message}
                  stillWorks="The per-source numbers above come from the overview and are current."
                />
              ) : (
                <Gaps summary={summary} />
              )}
              <div className="space-y-2">
                <h4 className="text-xs font-medium text-fg-muted">Suggestions</h4>
                <Suggestions timeWindow={timeWindow} />
              </div>
              <CodeBlock label="Attach documents to any request" code={contextExtensionSnippet()} wrap />
            </>
          ) : null}
        </div>
      </CardContent>
    </Card>
  );
}
