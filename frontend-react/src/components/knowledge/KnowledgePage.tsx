/**
 * Knowledge (SPEC §3.3, only when the add-on is enabled): the documents the gateway searches when a
 * request carries none of its own. Stats, the claims with paging and a total, the conflicts found
 * between claims, and uploads with their full result.
 *
 * When the add-on is off (409 knowledge_disabled, or /api/status says so) the page explains what
 * the add-on does and the one command that turns it on.
 */
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { BookOpen } from 'lucide-react';
import { useState } from 'react';
import { useDataSource } from '@/app/services';
import { CodeBlock } from '@/components/common/CodeBlock';
import { PageHeader } from '@/components/common/PageHeader';
import { Pagination } from '@/components/common/Pagination';
import { EmptyState, ErrorState, LoadingState } from '@/components/common/StateViews';
import { Term } from '@/components/common/Term';
import { useWriteReason } from '@/components/common/useWriteAccess';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { Skeleton } from '@/components/ui/skeleton';
import { isKnowledgeDisabled, kbClaimsQuery, kbConflictsQuery, kbStatsQuery } from '@/data/queries';
import { useAppMode, useSystemStatus } from '@/data/useSystem';
import { kbClaimStatusLabel } from '@/domain/controlPresentation';
import { formatCount, formatRisk } from '@/domain/format';
import { commandText } from '@/domain/presentation';
import { cn } from '@/lib/cn';
import type { KBClaim } from '@/types/api';
import { UploadCard } from './UploadCard';

const PAGE_SIZE = 20;
type StatusFilter = 'all' | 'accepted' | 'quarantined';

export function KnowledgeOff() {
  return (
    <EmptyState icon={BookOpen} title="The knowledge base add-on is off">
      <div className="space-y-3 text-left">
        <p>
          By default TrustAgent checks answers only against the documents each request already carries. The{' '}
          <Term id="knowledge_base">knowledge base</Term> adds a search over documents you upload, used when a request has none.
        </p>
        <CodeBlock label="Turn it on" code={commandText('knowledge_up') ?? 'COMPOSE_PROFILES=knowledge make up'} />
        <p>
          More in{' '}
          <Link to="/app/settings/$section" params={{ section: 'addons' }} className="font-medium text-fg underline underline-offset-2">
            Settings → Add-ons
          </Link>
          .
        </p>
      </div>
    </EmptyState>
  );
}

function ClaimRow({ c, as: Tag = 'li' }: { c: KBClaim; as?: 'li' | 'div' }) {
  return (
    <Tag className="space-y-0.5 py-2.5">
      <p className="text-sm text-fg">{c.claim}</p>
      <p className="flex flex-wrap gap-x-3 text-xs text-fg-muted">
        <span className={cn(c.status === 'quarantined' && 'font-medium text-fg')}>{kbClaimStatusLabel(c.status)}</span>
        <span className="font-mono">{c.source_doc_id}</span>
        <span className="tabular">support {formatRisk(c.entailment_score)}</span>
        {c.quarantine_reason ? <span>{c.quarantine_reason}</span> : null}
        {c.conflicts_with.length > 0 ? <span>conflicts with {c.conflicts_with.join(', ')}</span> : null}
      </p>
    </Tag>
  );
}

function Claims() {
  const ds = useDataSource();
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState<StatusFilter>('all');
  const q = useQuery(kbClaimsQuery(ds, { limit: PAGE_SIZE, offset: (page - 1) * PAGE_SIZE, status: status === 'all' ? undefined : status }));
  return (
    <Card>
      <CardHeader
        level={2}
        title="Claims"
        description="Facts extracted from your documents. Quarantined claims conflict with another or were not supported by their source."
        actions={
          <SegmentedControl<StatusFilter>
            aria-label="Show claims"
            size="sm"
            options={[
              { value: 'all', label: 'All' },
              { value: 'accepted', label: 'Accepted' },
              { value: 'quarantined', label: 'Quarantined' },
            ]}
            value={status}
            onValueChange={(v) => {
              setStatus(v);
              setPage(1);
            }}
          />
        }
      />
      <CardContent className="space-y-2">
        {q.isPending ? (
          <LoadingState rows={4} />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : q.data.claims.length === 0 ? (
          <p className="text-sm text-fg-muted">{status === 'all' ? 'No claims yet: upload documents to add some.' : 'No claims with this status.'}</p>
        ) : (
          <>
            <ul className="divide-y divide-border">
              {q.data.claims.map((c) => (
                <ClaimRow key={c.claim_id} c={c} />
              ))}
            </ul>
            <Pagination page={page} pageSize={PAGE_SIZE} total={q.data.total} onPageChange={setPage} noun="claims" />
          </>
        )}
      </CardContent>
    </Card>
  );
}

function Conflicts() {
  const ds = useDataSource();
  const q = useQuery(kbConflictsQuery(ds));
  return (
    <Card>
      <CardHeader level={2} title="Conflicts" description="Pairs of claims that cannot both be true. Fix the source documents and upload them again." />
      <CardContent>
        {q.isPending ? (
          <Skeleton className="h-16 w-full" />
        ) : q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : q.data.conflicts.length === 0 ? (
          <p className="text-sm text-fg-muted">No conflicts between claims.</p>
        ) : (
          <ul className="space-y-3">
            {q.data.conflicts.map((pair) => (
              <li key={`${pair.claim_a.claim_id}-${pair.claim_b.claim_id}`} className="grid gap-2 rounded-md border border-border p-3 md:grid-cols-2">
                <ClaimRow c={pair.claim_a} as="div" />
                <ClaimRow c={pair.claim_b} as="div" />
              </li>
            ))}
            {q.data.total > q.data.conflicts.length ? (
              <li className="text-xs text-fg-muted">
                Showing {formatCount(q.data.conflicts.length)} of {formatCount(q.data.total)} conflicts.
              </li>
            ) : null}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

export function KnowledgePage() {
  const ds = useDataSource();
  const { status } = useSystemStatus();
  const { demo } = useAppMode();
  const writeReason = useWriteReason('admin');
  const off = status ? !status.knowledge.enabled : false;
  const stats = useQuery({ ...kbStatsQuery(ds), enabled: !off });

  let body;
  if (off || (stats.isError && isKnowledgeDisabled(stats.error))) {
    body = <KnowledgeOff />;
  } else if (stats.isPending) {
    body = <LoadingState rows={5} />;
  } else if (stats.isError) {
    body = <ErrorState error={stats.error} onRetry={() => void stats.refetch()} />;
  } else {
    const s = stats.data;
    body = (
      <div className="space-y-4">
        <dl aria-label="Knowledge base" className="flex flex-wrap gap-x-8 gap-y-2 rounded-lg border border-border bg-surface px-4 py-3">
          {[
            ['Claims', s.total_claims],
            ['Accepted', s.accepted],
            ['Quarantined', s.quarantined],
            ['Conflicting pairs', s.conflict_pairs],
          ].map(([label, value]) => (
            <div key={label as string}>
              <dt className="text-xs text-fg-muted">{label}</dt>
              <dd className="text-xl font-semibold text-fg tabular">{formatCount(value as number)}</dd>
            </div>
          ))}
        </dl>
        <UploadCard writeReason={writeReason} maxBytes={status?.limits.max_upload_bytes ?? null} simulated={demo} />
        <div className="grid items-start gap-4 xl:grid-cols-2">
          <Claims />
          <Conflicts />
        </div>
      </div>
    );
  }

  return (
    <div>
      <PageHeader description="Documents the gateway searches when a request brings none of its own." />
      {body}
    </div>
  );
}
