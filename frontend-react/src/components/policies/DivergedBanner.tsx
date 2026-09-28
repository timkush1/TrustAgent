/**
 * The diverged-file banner (SPEC §3.3 Policies, §7.2): policies.yaml changed after a dashboard
 * edit, so the file and the dashboard disagree. "policies.yaml changed at 14:02 and replaced your
 * dashboard edit from 13:50 — [View diff] [Restore my edit]", plus [Adopt file], which accepts the
 * file and clears the warning.
 */
import { useMutation, useQuery } from '@tanstack/react-query';
import { FileWarning } from 'lucide-react';
import { useState } from 'react';
import { useDataSource } from '@/app/services';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { DiffView } from '@/components/common/DiffView';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { notify } from '@/components/common/notify';
import { ErrorState } from '@/components/common/StateViews';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent } from '@/components/ui/dialog';
import { Skeleton } from '@/components/ui/skeleton';
import { policyVersionQuery, qkc } from '@/data/queries';
import { copy, fillTemplate } from '@/domain/presentation';
import type { PolicyState, PolicyVersionMeta } from '@/types/api';

const clock = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit' });

function time(iso: string | undefined): string | null {
  if (!iso) return null;
  const t = Date.parse(iso);
  return Number.isNaN(t) ? null : clock.format(t);
}

function EditDiff({ edit, currentYaml }: { edit: PolicyVersionMeta; currentYaml: string }) {
  const ds = useDataSource();
  const q = useQuery(policyVersionQuery(ds, edit.id));
  if (q.isPending) return <Skeleton className="h-32 w-full" />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  return <DiffView label={`Your edit (version ${edit.id}) → policies.yaml in effect`} before={q.data.yaml} after={currentYaml} />;
}

export function DivergedBanner({
  state,
  writeReason,
  onChanged,
}: {
  state: PolicyState;
  writeReason: Reason | null;
  onChanged: (next: PolicyState) => void;
}) {
  const ds = useDataSource();
  const [diffOpen, setDiffOpen] = useState(false);
  const versions = useQuery({
    queryKey: [...qkc.policies.versions, 'recent'],
    queryFn: ({ signal }) => ds.listPolicyVersions({ limit: 50 }, signal),
  });
  const items = versions.data?.items ?? [];
  const lastEdit = items.find((v) => v.source === 'dashboard' && v.id !== state.id);
  const lastFile = items.find((v) => v.source === 'file' || v.source === 'adopt_file');
  const fileTime = time(lastFile?.created_at ?? (state.source === 'file' ? state.created_at : undefined));
  const editTime = time(lastEdit?.created_at);

  const text =
    fileTime && editTime
      ? fillTemplate(copy.policies.diverged_file, { file_time: fileTime, edit_time: editTime })
      : 'policies.yaml changed and no longer matches the policy edited in the dashboard.';

  const restore = useMutation({ mutationFn: (version: number) => ds.rollbackPolicy({ version, message: `Restored my dashboard edit (version ${version})` }) });
  const adopt = useMutation({ mutationFn: () => ds.adoptPolicyFile({ message: 'Adopted policies.yaml' }) });

  return (
    <div role="status" data-state="diverged" className="space-y-2 rounded-lg border border-dashed border-border-strong bg-surface px-4 py-3">
      <p className="flex items-start gap-2 text-sm font-medium text-fg">
        <FileWarning aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-warning" />
        {text}
      </p>
      <div className="flex flex-wrap items-center gap-2 pl-6">
        {lastEdit ? (
          <>
            <Button size="sm" onClick={() => setDiffOpen(true)}>
              {copy.policies.view_diff}
            </Button>
            <DisabledAction reason={writeReason}>
              {(p) => (
                <ConfirmDialog
                  tone="neutral"
                  title="Restore your dashboard edit?"
                  description={`Version ${lastEdit.id} becomes the policy in effect again, as a new version. policies.yaml on disk is not changed.`}
                  confirmLabel={copy.policies.restore_edit}
                  onConfirm={async () => {
                    const next = await restore.mutateAsync(lastEdit.id);
                    notify.success('Your dashboard edit is back in effect');
                    onChanged(next);
                  }}
                  trigger={
                    <Button size="sm" {...p}>
                      {copy.policies.restore_edit}
                    </Button>
                  }
                />
              )}
            </DisabledAction>
          </>
        ) : null}
        <DisabledAction reason={writeReason}>
          {(p) => (
            <ConfirmDialog
              tone="neutral"
              title="Adopt policies.yaml?"
              description="The file on disk becomes the policy in effect and this warning goes away. Your dashboard edits stay in the version history."
              confirmLabel={copy.policies.adopt_file}
              onConfirm={async () => {
                const next = await adopt.mutateAsync();
                notify.success('policies.yaml is now the policy in effect');
                onChanged(next);
              }}
              trigger={
                <Button size="sm" variant="ghost" {...p}>
                  {copy.policies.adopt_file}
                </Button>
              }
            />
          )}
        </DisabledAction>
      </div>
      {lastEdit ? (
        <Dialog open={diffOpen} onOpenChange={setDiffOpen}>
          <DialogContent size="lg" title="Your edit and policies.yaml" description="What changed between your last dashboard edit and the file now in effect.">
            <EditDiff edit={lastEdit} currentYaml={state.yaml} />
          </DialogContent>
        </Dialog>
      ) : null}
    </div>
  );
}
