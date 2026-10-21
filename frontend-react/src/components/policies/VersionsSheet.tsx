/**
 * Policy versions and rollback (SPEC §3.3 Policies, §2.4 #19-#21, #24): every saved version with
 * where it came from, who made it and why; its diff against the policy in effect; and a one-step
 * rollback behind a confirmation. Rolling back creates a new version, so it can itself be undone.
 */
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { History, RotateCcw } from 'lucide-react';
import { useState } from 'react';
import { useDataSource } from '@/app/services';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { DiffView } from '@/components/common/DiffView';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { notify } from '@/components/common/notify';
import { ErrorState, LoadingState } from '@/components/common/StateViews';
import { useNow } from '@/components/common/useNow';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Sheet, SheetContent, SheetTrigger } from '@/components/ui/sheet';
import { Skeleton } from '@/components/ui/skeleton';
import { policyVersionQuery, qkc } from '@/data/queries';
import { policySourceLabel } from '@/domain/controlPresentation';
import { formatDateTime, formatRelativeTime } from '@/domain/format';
import type { PolicyState, PolicyVersionMeta } from '@/types/api';

const PAGE = 50;

function VersionDiff({ id, currentYaml }: { id: number; currentYaml: string }) {
  const ds = useDataSource();
  const q = useQuery(policyVersionQuery(ds, id));
  if (q.isPending) return <Skeleton className="h-24 w-full" />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  return <DiffView label={`Version ${id} → the policy in effect`} before={q.data.yaml} after={currentYaml} emptyText="Same as the policy in effect." />;
}

function VersionItem({
  v,
  state,
  writeReason,
  onRolledBack,
}: {
  v: PolicyVersionMeta;
  state: PolicyState;
  writeReason: Reason | null;
  onRolledBack: (next: PolicyState) => void;
}) {
  const ds = useDataSource();
  const now = useNow();
  const [showDiff, setShowDiff] = useState(false);
  const current = v.id === state.id;
  const rollback = useMutation({ mutationFn: () => ds.rollbackPolicy({ version: v.id, message: `Rolled back to version ${v.id}` }) });

  return (
    <li className="space-y-2 py-3" data-version={v.id}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0 space-y-0.5">
          <p className="flex flex-wrap items-center gap-2 text-sm font-medium text-fg">
            Version {v.id}
            {current ? (
              <Badge size="sm" emphasis="quiet">
                In effect
              </Badge>
            ) : null}
            <span className="text-xs font-normal text-fg-muted">{policySourceLabel(v.source)}</span>
          </p>
          <p className="text-sm text-fg">{v.message || 'No message'}</p>
          <p className="text-xs text-fg-muted">
            {v.author} · <time dateTime={v.created_at} title={formatDateTime(v.created_at)}>{formatRelativeTime(v.created_at, now)}</time>
          </p>
        </div>
        {current ? null : (
          <div className="flex shrink-0 flex-wrap items-center gap-2">
            <Button size="sm" variant="ghost" aria-expanded={showDiff} onClick={() => setShowDiff((s) => !s)}>
              {showDiff ? 'Hide diff' : 'View diff'}{' '}
              <span className="sr-only">of version {v.id}</span>
            </Button>
            <DisabledAction reason={writeReason} layout="stacked" className="items-end">
              {(p) => (
                <ConfirmDialog
                  tone="neutral"
                  title={`Roll back to version ${v.id}?`}
                  description={`This saves version ${v.id} again as the newest version. The policy in effect now stays in the history, so you can return to it.`}
                  confirmLabel="Roll back"
                  onConfirm={async () => {
                    const next = await rollback.mutateAsync();
                    notify.success(`Rolled back: version ${next.id} is now in effect`);
                    onRolledBack(next);
                  }}
                  trigger={
                    <Button size="sm" {...p}>
                      <RotateCcw aria-hidden="true" />
                      Roll back{' '}
                      <span className="sr-only">to version {v.id}</span>
                    </Button>
                  }
                />
              )}
            </DisabledAction>
          </div>
        )}
      </div>
      {showDiff ? <VersionDiff id={v.id} currentYaml={state.yaml} /> : null}
    </li>
  );
}

export function VersionsSheet({ state, writeReason }: { state: PolicyState; writeReason: Reason | null }) {
  const ds = useDataSource();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const versions = useInfiniteQuery({
    queryKey: qkc.policies.versions,
    queryFn: ({ pageParam, signal }) => ds.listPolicyVersions({ limit: PAGE, cursor: pageParam }, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    enabled: open,
  });
  const items = versions.data?.pages.flatMap((p) => p.items) ?? [];

  const onRolledBack = (next: PolicyState) => {
    queryClient.setQueryData(qkc.policies.state, next);
    void queryClient.invalidateQueries({ queryKey: qkc.policies.versions });
  };

  return (
    <Sheet open={open} onOpenChange={setOpen}>
      <SheetTrigger
        render={
          <Button>
            <History aria-hidden="true" />
            Versions
          </Button>
        }
      />
      <SheetContent
        size="lg"
        title="Policy versions"
        description={state.persisted ? 'Every saved version, newest first. Roll back to any of them.' : 'This proxy keeps no history without a database.'}
      >
        {versions.isPending ? (
          <LoadingState rows={4} />
        ) : versions.isError ? (
          <ErrorState error={versions.error} onRetry={() => void versions.refetch()} />
        ) : items.length === 0 ? (
          <p className="text-sm text-fg-muted">No saved versions yet.</p>
        ) : (
          <>
            <ul className="divide-y divide-border">
              {items.map((v) => (
                <VersionItem key={v.id} v={v} state={state} writeReason={writeReason} onRolledBack={onRolledBack} />
              ))}
            </ul>
            {versions.hasNextPage ? (
              <Button size="sm" pending={versions.isFetchingNextPage} onClick={() => void versions.fetchNextPage()}>
                Show older versions
              </Button>
            ) : null}
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}
