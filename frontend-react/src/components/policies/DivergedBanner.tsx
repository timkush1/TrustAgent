/**
 * The diverged-file banner (SPEC §3.3 Policies, §7.2): policies.yaml and the dashboard disagree.
 *
 * SPEC §7.2 (and the gateway) define `file.status = "diverged"` as: the file changed after a newer
 * dashboard edit, so the file is **not applied** and the dashboard version stays in effect until
 * an admin adopts the file. The banner says exactly that, offers [View diff] (what the dashboard
 * edits changed since the file was last applied) and [Adopt file]. The file's new text is not
 * served by the API, so the banner names the file instead of showing it.
 *
 * If a proxy instead lets the file replace the edit (the policy in effect came from the file),
 * the banner reads as SPEC §3.3's copy — "policies.yaml changed at 14:02 and replaced your
 * dashboard edit from 13:50 — [View diff] [Restore my edit]" — with [Adopt file] to keep the file.
 * "Restore my edit" is only ever offered for an edit that is no longer in effect.
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

type Source = PolicyVersionMeta['source'];

/** Versions written from the dashboard (the gateway's "last dashboard version"). */
const DASHBOARD_SOURCES: ReadonlySet<Source> = new Set<Source>(['dashboard', 'rollback']);
/** Versions that came from policies.yaml (the gateway's "last file version"). */
const FILE_SOURCES: ReadonlySet<Source> = new Set<Source>(['file', 'adopt_file']);

/** The newest version older than the one in effect whose source is in `sources`. */
function newestBefore(items: readonly PolicyVersionMeta[], before: number, sources: ReadonlySet<Source>): PolicyVersionMeta | null {
  return items.filter((v) => v.id < before && sources.has(v.source)).sort((a, b) => b.id - a.id)[0] ?? null;
}

function VersionDiff({ from, currentYaml, label }: { from: PolicyVersionMeta; currentYaml: string; label: string }) {
  const ds = useDataSource();
  const q = useQuery(policyVersionQuery(ds, from.id));
  if (q.isPending) return <Skeleton className="h-32 w-full" />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  return <DiffView label={label} before={q.data.yaml} after={currentYaml} emptyText="The two versions are the same." />;
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

  // Which side is in effect decides what the banner may offer.
  const fileInEffect = FILE_SOURCES.has(state.source);
  // The file replaced an edit: the edit it replaced, which "Restore my edit" brings back.
  const replacedEdit = fileInEffect ? newestBefore(items, state.id, DASHBOARD_SOURCES) : null;
  // The edit is in effect: the file version it was made on top of, to show what the edits changed.
  const baseFile = fileInEffect ? null : newestBefore(items, state.id, FILE_SOURCES);

  let text: string;
  let diff: { from: PolicyVersionMeta; label: string; title: string; description: string } | null = null;
  if (fileInEffect) {
    const fileTime = time(state.created_at);
    const editTime = time(replacedEdit?.created_at);
    text =
      fileTime && editTime
        ? fillTemplate(copy.policies.diverged_file, { file_time: fileTime, edit_time: editTime })
        : 'policies.yaml changed and no longer matches the policy edited in the dashboard.';
    if (replacedEdit) {
      diff = {
        from: replacedEdit,
        label: `Your edit (version ${replacedEdit.id}) → policies.yaml in effect (version ${state.id})`,
        title: 'Your edit and policies.yaml',
        description: 'What changed between your last dashboard edit and the file now in effect.',
      };
    }
  } else {
    const editTime = time(state.created_at);
    text = `policies.yaml changed on disk after your dashboard edit${editTime ? ` from ${editTime}` : ''} and is not applied: version ${state.id} stays in effect until you adopt the file.`;
    if (baseFile) {
      diff = {
        from: baseFile,
        label: `policies.yaml as last applied (version ${baseFile.id}) → your edits in effect (version ${state.id})`,
        title: 'Your dashboard edits',
        description: `What the dashboard changed since policies.yaml was last applied. The file's new text is not shown here: open ${state.file.path} to read it.`,
      };
    }
  }

  const restore = useMutation({ mutationFn: (version: number) => ds.rollbackPolicy({ version, message: `Restored my dashboard edit (version ${version})` }) });
  const adopt = useMutation({ mutationFn: () => ds.adoptPolicyFile({ message: 'Adopted policies.yaml' }) });

  return (
    <div role="status" data-state="diverged" className="space-y-2 rounded-lg border border-dashed border-border-strong bg-surface px-4 py-3">
      <p className="flex items-start gap-2 text-sm font-medium text-fg">
        <FileWarning aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-warning" />
        {text}
      </p>
      <div className="flex flex-wrap items-center gap-2 pl-6">
        {diff ? (
          <Button size="sm" onClick={() => setDiffOpen(true)}>
            {copy.policies.view_diff}
          </Button>
        ) : null}
        {replacedEdit ? (
          <DisabledAction reason={writeReason}>
            {(p) => (
              <ConfirmDialog
                tone="neutral"
                title="Restore your dashboard edit?"
                description={`Version ${replacedEdit.id} becomes the policy in effect again, as a new version. policies.yaml on disk is not changed.`}
                confirmLabel={copy.policies.restore_edit}
                onConfirm={async () => {
                  const next = await restore.mutateAsync(replacedEdit.id);
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
        ) : null}
        <DisabledAction reason={writeReason}>
          {(p) => (
            <ConfirmDialog
              tone="neutral"
              title="Adopt policies.yaml?"
              description={
                fileInEffect
                  ? 'The file on disk stays the policy in effect and this warning goes away. Your dashboard edits stay in the version history.'
                  : `The file on disk replaces version ${state.id} as the policy in effect and this warning goes away. Your dashboard edits stay in the version history, so you can roll back to them.`
              }
              confirmLabel={copy.policies.adopt_file}
              onConfirm={async () => {
                const next = await adopt.mutateAsync();
                notify.success('policies.yaml is now the policy in effect');
                onChanged(next);
              }}
              trigger={
                <Button size="sm" variant={fileInEffect ? 'ghost' : 'secondary'} {...p}>
                  {copy.policies.adopt_file}
                </Button>
              }
            />
          )}
        </DisabledAction>
      </div>
      {diff ? (
        <Dialog open={diffOpen} onOpenChange={setDiffOpen}>
          <DialogContent size="lg" title={diff.title} description={diff.description}>
            <VersionDiff from={diff.from} currentYaml={state.yaml} label={diff.label} />
          </DialogContent>
        </Dialog>
      ) : null}
    </div>
  );
}
