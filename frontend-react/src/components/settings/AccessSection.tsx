/**
 * Settings → Access (SPEC §3.3; P4-PLAN ON-3, D44, D53, D81): who can use this TrustAgent.
 *
 * - API keys: list, create (scopes in plain words; "See prompts and answers" is its own scope), the
 *   secret shown once with "I saved it", and revoke behind a confirmation that warns about the key
 *   this browser is signed in with and the last admin key (the proxy refuses that one, D81).
 * - "Sign out everywhere" ends every dashboard session, this one included.
 * - Admin activity: every admin change (#70), newest first, with words for each action.
 */
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { KeyRound, LogOut, Plus, ShieldOff } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useDataSource } from '@/app/services';
import { CodeBlock } from '@/components/common/CodeBlock';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { DataTable, type DataTableColumn } from '@/components/common/DataTable';
import { DisabledAction } from '@/components/common/DisabledReason';
import { KeyValueList } from '@/components/common/KeyValue';
import { notify } from '@/components/common/notify';
import { PageHeader } from '@/components/common/PageHeader';
import { QueryState } from '@/components/common/QueryState';
import { SecretOnce } from '@/components/common/SecretOnce';
import { EmptyState, LoadingState } from '@/components/common/StateViews';
import { Badge, Chip } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Checkbox } from '@/components/ui/checkbox';
import { Dialog, DialogContent, DialogTrigger } from '@/components/ui/dialog';
import { Field } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { readDashboardKey } from '@/data/http';
import { sessionQuery, statusQuery } from '@/data/queries';
import { adminAuditQuery, apiKeysQuery, qkcSecurity } from '@/data/queries/security';
import { formatDateTime, formatRelativeTime } from '@/domain/format';
import { copy, SEC_SCOPES, secAuditActionLabel, secScopeView } from '@/domain/presentation';
import type { AdminAuditEntry, ApiKeyView, CreatedApiKey, Scope } from '@/types/api';
import { ProblemNote } from './SettingsKit';
import { useSettingsWriteReason } from './useSettingsAccess';

function ScopeChips({ scopes }: { scopes: readonly Scope[] }) {
  return (
    <span className="flex flex-wrap gap-1">
      {SEC_SCOPES.filter((s) => scopes.includes(s)).map((s) => (
        <Chip key={s}>{secScopeView(s).label}</Chip>
      ))}
    </span>
  );
}

// ------------------------------------------------------------------------------- create

function CreateKeyDialog({ writeReason }: { writeReason: ReturnType<typeof useSettingsWriteReason> }) {
  const ds = useDataSource();
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [label, setLabel] = useState('');
  const [scopes, setScopes] = useState<Scope[]>(['ingest']);
  const [created, setCreated] = useState<CreatedApiKey | null>(null);
  const [saved, setSaved] = useState(false);
  const create = useMutation({
    mutationFn: () => ds.createApiKey({ label: label.trim(), scopes }),
    onSuccess: (key) => {
      setCreated(key);
      void qc.invalidateQueries({ queryKey: qkcSecurity.apiKeys });
    },
  });

  const reset = () => {
    setLabel('');
    setScopes(['ingest']);
    setCreated(null);
    setSaved(false);
    create.reset();
  };
  const onOpenChange = (next: boolean) => {
    // A shown secret closes only through "Done", after "I saved it".
    if (!next && created && !saved) return;
    setOpen(next);
    if (!next) reset();
  };
  const toggle = (scope: Scope, on: boolean) => setScopes((cur) => (on ? [...cur, scope] : cur.filter((s) => s !== scope)));
  const problem = label.trim() === '' ? 'Name the key first' : scopes.length === 0 ? 'Choose at least one scope' : null;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!problem) create.mutate();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DisabledAction reason={writeReason}>
        {(p) => (
          <DialogTrigger
            render={
              <Button variant="primary" {...p}>
                <Plus aria-hidden="true" />
                Create API key
              </Button>
            }
          />
        )}
      </DisabledAction>
      <DialogContent
        title={created ? 'Your new API key' : 'Create an API key'}
        description={created ? undefined : 'Apps send it as X-TrustAgent-Key. Give each app its own key, so you can revoke one without the others.'}
        hideClose={Boolean(created) && !saved}
        footer={
          created ? (
            <DisabledAction reason={saved ? null : { text: 'Tick “I saved it” first' }}>
              {(p) => (
                <Button variant="primary" onClick={() => onOpenChange(false)} {...p}>
                  Done
                </Button>
              )}
            </DisabledAction>
          ) : (
            <>
              <Button variant="secondary" onClick={() => onOpenChange(false)}>
                Cancel
              </Button>
              <DisabledAction reason={problem ? { text: problem } : null}>
                {(p) => (
                  <Button variant="primary" type="submit" form="create-key-form" pending={create.isPending} {...p}>
                    Create key
                  </Button>
                )}
              </DisabledAction>
            </>
          )
        }
      >
        {created ? (
          <SecretOnce title={`${created.label} (shown once)`} value={created.secret} saved={saved} onSavedChange={setSaved} copyLabel="Copy the API key" />
        ) : (
          <form id="create-key-form" onSubmit={submit} className="space-y-4">
            <Field label="Name" description="Where the key is used, for example “checkout-service”.">
              <Input value={label} onValueChange={setLabel} maxLength={64} autoComplete="off" />
            </Field>
            <fieldset className="space-y-2">
              <legend className="text-sm font-medium text-fg">What it can do</legend>
              {SEC_SCOPES.map((scope) => (
                <Checkbox
                  key={scope}
                  label={secScopeView(scope).label}
                  description={secScopeView(scope).description}
                  checked={scopes.includes(scope)}
                  onCheckedChange={(on) => toggle(scope, on === true)}
                />
              ))}
            </fieldset>
            {create.isError ? <ProblemNote error={create.error} title="The key was not created" /> : null}
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

// ------------------------------------------------------------------------------- revoke

function RevokeKey({ apiKey, keys, current, writeReason }: { apiKey: ApiKeyView; keys: readonly ApiKeyView[]; current: boolean; writeReason: ReturnType<typeof useSettingsWriteReason> }) {
  const ds = useDataSource();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const lastAdmin =
    apiKey.scopes.includes('admin') && !keys.some((k) => k.id !== apiKey.id && k.revoked_at === null && k.scopes.includes('admin'));
  return (
    <DisabledAction reason={writeReason}>
      {(p) => (
        <ConfirmDialog
          trigger={
            <Button size="sm" aria-label={`Revoke ${apiKey.label}`} {...p}>
              <ShieldOff aria-hidden="true" />
              Revoke
            </Button>
          }
          title={`Revoke “${apiKey.label}”?`}
          description="Apps and people using this key are refused from now on. A revoked key cannot be turned back on."
          confirmLabel="Revoke key"
          onConfirm={async () => {
            await ds.revokeApiKey(apiKey.id);
            await qc.invalidateQueries({ queryKey: qkcSecurity.apiKeys });
            notify.success(`Revoked “${apiKey.label}”`);
            if (current) {
              await qc.invalidateQueries();
              void navigate({ to: '/app/setup' });
            }
          }}
        >
          {current || lastAdmin ? (
            <ul className="space-y-1 text-sm text-fg">
              {current ? <li>{copy.settings.current_session_key}</li> : null}
              {lastAdmin ? <li>{copy.settings.last_admin_key}</li> : null}
            </ul>
          ) : null}
        </ConfirmDialog>
      )}
    </DisabledAction>
  );
}

function KeyRow({ apiKey, keys, current, writeReason }: { apiKey: ApiKeyView; keys: readonly ApiKeyView[]; current: boolean; writeReason: ReturnType<typeof useSettingsWriteReason> }) {
  const revoked = apiKey.revoked_at !== null;
  return (
    <li className="flex flex-wrap items-start justify-between gap-3 py-3" data-revoked={revoked || undefined}>
      <div className="min-w-0 space-y-1">
        <p className="flex flex-wrap items-center gap-2 text-sm">
          <span className={revoked ? 'font-medium text-fg-muted line-through' : 'font-medium text-fg'}>{apiKey.label}</span>
          <code className="rounded-sm bg-surface-sunken px-1 font-mono text-2xs text-fg-muted">{apiKey.prefix}…</code>
          {current ? <Badge emphasis="quiet">This browser</Badge> : null}
          {revoked ? <Badge emphasis="quiet">Revoked {formatRelativeTime(apiKey.revoked_at)}</Badge> : null}
        </p>
        <ScopeChips scopes={apiKey.scopes} />
        <p className="text-xs text-fg-muted tabular">
          Created <time dateTime={apiKey.created_at} title={formatDateTime(apiKey.created_at)}>{formatRelativeTime(apiKey.created_at)}</time>
          {' · '}
          {apiKey.last_used_at ? (
            <>
              last used <time dateTime={apiKey.last_used_at} title={formatDateTime(apiKey.last_used_at)}>{formatRelativeTime(apiKey.last_used_at)}</time>
            </>
          ) : (
            'never used'
          )}
        </p>
      </div>
      {revoked ? null : <RevokeKey apiKey={apiKey} keys={keys} current={current} writeReason={writeReason} />}
    </li>
  );
}

function ApiKeys({ writeReason }: { writeReason: ReturnType<typeof useSettingsWriteReason> }) {
  const ds = useDataSource();
  const keys = useQuery(apiKeysQuery(ds));
  const session = useQuery(sessionQuery(ds));
  const tabKey = readDashboardKey();
  const isCurrent = (k: ApiKeyView) => (tabKey ? tabKey.startsWith(k.prefix) : session.data?.actor === k.label);
  return (
    <Card>
      <CardHeader level={2} title="API keys" description="Keys for your apps (send requests) and for people (read the dashboard, change settings). Only a hash is kept." />
      <CardContent>
        <QueryState
          query={keys}
          isEmpty={(d) => d.items.length === 0}
          empty={
            <EmptyState icon={KeyRound} title="No API keys yet">
              Create one for each app that sends requests through the gateway.
            </EmptyState>
          }
        >
          {(data) => {
            const sorted = [...data.items].sort((a, b) => Number(a.revoked_at !== null) - Number(b.revoked_at !== null));
            return (
              <ul aria-label="API keys" className="divide-y divide-border">
                {sorted.map((k) => (
                  <KeyRow key={k.id} apiKey={k} keys={data.items} current={k.revoked_at === null && isCurrent(k)} writeReason={writeReason} />
                ))}
              </ul>
            );
          }}
        </QueryState>
      </CardContent>
    </Card>
  );
}

// ------------------------------------------------------------------------- sessions

function SignOutEverywhere({ writeReason }: { writeReason: ReturnType<typeof useSettingsWriteReason> }) {
  const ds = useDataSource();
  const qc = useQueryClient();
  const navigate = useNavigate();
  return (
    <DisabledAction reason={writeReason}>
      {(p) => (
        <ConfirmDialog
          trigger={
            <Button {...p}>
              <LogOut aria-hidden="true" />
              {copy.settings.sign_out_everywhere}
            </Button>
          }
          title={`${copy.settings.sign_out_everywhere}?`}
          description={copy.settings.sign_out_everywhere_confirm}
          confirmLabel={copy.settings.sign_out_everywhere}
          onConfirm={async () => {
            await ds.signOutEverywhere();
            await qc.invalidateQueries();
            void navigate({ to: '/app/setup' });
          }}
        />
      )}
    </DisabledAction>
  );
}

// ---------------------------------------------------------------------- admin activity

function diffSummary(entry: AdminAuditEntry): string {
  const diff = entry.diff;
  if (diff && typeof diff === 'object' && !Array.isArray(diff)) {
    const keys = Object.keys(diff);
    if (keys.length > 0) return keys.slice(0, 3).join(', ') + (keys.length > 3 ? ` +${keys.length - 3}` : '');
  }
  return '—';
}

const AUDIT_COLUMNS: readonly DataTableColumn<AdminAuditEntry>[] = [
  {
    id: 'at',
    header: 'When',
    width: '8rem',
    cell: (e) => (
      <time dateTime={e.at} title={formatDateTime(e.at)} className="tabular">
        {formatRelativeTime(e.at)}
      </time>
    ),
  },
  { id: 'actor', header: 'Who', width: 'minmax(6rem,1fr)', cell: (e) => <span className="truncate">{e.actor}</span> },
  { id: 'action', header: 'What', width: 'minmax(10rem,2fr)', cell: (e) => <span className="truncate">{secAuditActionLabel(e.action)}</span> },
  { id: 'target', header: 'On', width: 'minmax(6rem,1fr)', cell: (e) => <span className="truncate font-mono text-xs">{e.target ?? '—'}</span> },
  { id: 'diff', header: 'Changed', width: 'minmax(8rem,2fr)', cell: (e) => <span className="truncate font-mono text-xs text-fg-muted">{diffSummary(e)}</span> },
];

function AdminActivity() {
  const ds = useDataSource();
  const audit = useInfiniteQuery(adminAuditQuery(ds));
  const [open, setOpen] = useState<AdminAuditEntry | null>(null);
  const rows = audit.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <Card>
      <CardHeader level={2} title="Admin activity" description="Every change to keys, settings, evaluators and calibrations, newest first. Secrets are never recorded." />
      <CardContent>
        <DataTable
          aria-label="Admin activity"
          columns={AUDIT_COLUMNS}
          rows={rows}
          getRowId={(e) => String(e.id)}
          onRowActivate={(e) => setOpen(e)}
          isLoading={audit.isPending}
          error={audit.error ?? undefined}
          onRetry={() => void audit.refetch()}
          totalRows={audit.hasNextPage ? -1 : rows.length}
          onEndReached={() => {
            if (audit.hasNextPage && !audit.isFetchingNextPage) void audit.fetchNextPage();
          }}
          footer={audit.isFetchingNextPage ? <span className="text-xs text-fg-muted">Loading more…</span> : undefined}
          empty={<EmptyState title="No admin changes yet">Creating keys, saving settings and connecting providers show up here.</EmptyState>}
          maxHeight="min(50vh, 24rem)"
        />
        <Dialog open={open !== null} onOpenChange={(next) => !next && setOpen(null)}>
          {open ? (
            <DialogContent title={secAuditActionLabel(open.action)} description={`${open.actor} · ${formatDateTime(open.at)}`} size="lg">
              <KeyValueList
                items={[
                  { label: 'Who', value: open.actor },
                  { label: 'On', value: open.target, mono: true },
                  { label: 'Entry', value: String(open.id), mono: true },
                ]}
              />
              <CodeBlock label="What changed" code={JSON.stringify(open.diff ?? null, null, 2)} maxHeight="20rem" />
            </DialogContent>
          ) : null}
        </Dialog>
      </CardContent>
    </Card>
  );
}

// ----------------------------------------------------------------------------- section

export function AccessSection() {
  const writeReason = useSettingsWriteReason();
  const status = useQuery(statusQuery(useDataSource()));
  // Unknown scopes (a proxy without /api/status) try the admin view; the server answers for itself.
  const admin = status.data ? status.data.auth.caller_scopes.includes('admin') : !status.isPending;
  if (status.isPending) return <LoadingState rows={4} />;
  return (
    <div className="space-y-5">
      <PageHeader
        description="Who can use this TrustAgent: API keys for your apps and for people, dashboard sessions, and every admin change."
        actions={
          admin ? (
            <>
              <SignOutEverywhere writeReason={writeReason} />
              <CreateKeyDialog writeReason={writeReason} />
            </>
          ) : null
        }
      />
      {admin ? (
        <>
          <ApiKeys writeReason={writeReason} />
          <AdminActivity />
        </>
      ) : (
        <EmptyState icon={KeyRound} title="Only admins see API keys" action={<DisabledAction reason="needs_admin">{(p) => <Button {...p}>Create API key</Button>}</DisabledAction>}>
          This browser is signed in without the admin scope.
        </EmptyState>
      )}
    </div>
  );
}
