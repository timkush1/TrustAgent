/**
 * Settings → Budgets (SPEC §3.3 "two inputs with today's spend bars"; SV-3, D33, D46,
 * D53, D77): the daily caps of the fast check and the deep audit, each with today's real spend
 * (simulated checks never count) and, on the fast-check bar, the line where observe and flag
 * routes stop. Below them, the Prices panel: the table every cost estimate uses, and your
 * overrides.
 *
 * Writes are PATCH /api/settings with If-Match. A cap set in .env is read-only here and says
 * where to change it. A fast-check cap below today's spend, while a route blocks on failure,
 * comes back as 409 `confirmation_required` naming those routes: the dialog asks before saving
 * with `?confirm=true`. A stale version offers a reload; read-only, non-admin and no-database
 * stacks say why saving is off.
 */
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ArrowRight } from 'lucide-react';
import { useState } from 'react';
import { useDataSource } from '@/app/services';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { DegradedState } from '@/components/common/DegradedState';
import { DisabledAction, DisabledReason, type Reason } from '@/components/common/DisabledReason';
import { describeError } from '@/components/common/describeError';
import { notify } from '@/components/common/notify';
import { ErrorState } from '@/components/common/StateViews';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { useWriteReason } from '@/components/common/useWriteAccess';
import {
  BUDGET_ENV,
  BUDGET_KINDS,
  budgetEnvManaged,
  budgetsPatch,
  capDrafts,
  capOf,
  capsOf,
  capText,
  enforcingReserve,
  parseCap,
  rebaseCaps,
  spendToday,
  type CapDrafts,
  type Caps,
} from '@/components/savings/budgetsModel';
import { PricesPanel } from '@/components/savings/PricesPanel';
import { SpendBar } from '@/components/savings/SpendBar';
import { useSettingsDraft } from '@/components/savings/useSettingsDraft';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Field } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { Skeleton } from '@/components/ui/skeleton';
import { isApiError } from '@/data/errors';
import { isNotServed, qk } from '@/data/queries';
import { qkcSecurity, settingsQuery } from '@/data/queries/security';
import { useSystemStatus } from '@/data/useSystem';
import { formatUsd } from '@/domain/format';
import { copy, costBudgetView, fillTemplate, type CostBudgetKind } from '@/domain/presentation';
import type { FieldError, RuntimeSettingsPatch, SettingsView, SystemStatus } from '@/types/api';

/** What crossing today's spend does, per cap. */
const BELOW_SPEND: Record<CostBudgetKind, string> = {
  decision: 'observe and flag routes stop being checked for the rest of the day, and routes that block follow their fail mode.',
  llm: 'deep audits pause for the rest of the day.',
};

interface Pending {
  patch: RuntimeSettingsPatch;
  routes: string[];
}

function BudgetsCard({ settings, status, writeReason }: { settings: SettingsView; status: SystemStatus | null; writeReason: Reason | null }) {
  const ds = useDataSource();
  const queryClient = useQueryClient();
  // The caps as typed, over the version they were based on (its ETag is the save's If-Match).
  const form = useSettingsDraft<Caps, CapDrafts>(settings, { read: capsOf, toDraft: capDrafts, rebase: rebaseCaps });
  const drafts = form.draft;
  const [touched, setTouched] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const [confirm, setConfirm] = useState<Pending | null>(null);

  const parsed = Object.fromEntries(BUDGET_KINDS.map((k) => [k, parseCap(drafts[k])])) as Record<CostBudgetKind, ReturnType<typeof parseCap>>;
  const values: Partial<Record<CostBudgetKind, number>> = {};
  for (const k of BUDGET_KINDS) {
    const p = parsed[k];
    if ('value' in p && !budgetEnvManaged(settings, k)) values[k] = p.value;
  }
  const invalid = BUDGET_KINDS.some((k) => 'error' in parsed[k] && !budgetEnvManaged(settings, k));
  const patch = invalid ? null : budgetsPatch(settings, values);
  const edited = (kind: CostBudgetKind) => drafts[kind] !== capText(form.base[kind]);
  const dirty = BUDGET_KINDS.some(edited);
  const serverErrors = isApiError(error) ? error.fieldErrors : [];
  const reserve = enforcingReserve(settings);
  const allEnv = BUDGET_KINDS.every((k) => budgetEnvManaged(settings, k));

  const applied = (view: SettingsView) => {
    queryClient.setQueryData(qkcSecurity.settings, view);
    // /api/status carries the caps the banner and the spend bars read.
    void queryClient.invalidateQueries({ queryKey: qk.status });
    form.reset(view);
    setTouched(false);
    setConfirm(null);
    notify.success('Budgets saved');
  };

  const save = async () => {
    setTouched(true);
    if (!patch) return;
    setSaving(true);
    setError(null);
    try {
      applied(await ds.patchSettings(patch, form.etag));
    } catch (err) {
      if (isApiError(err) && err.code === 'confirmation_required') {
        const routes = Array.isArray(err.details.routes) ? err.details.routes.filter((r): r is string => typeof r === 'string') : [];
        setConfirm({ patch, routes });
      } else {
        setError(err);
      }
    } finally {
      setSaving(false);
    }
  };

  const reason: Reason | null =
    writeReason ??
    (allEnv ? 'env_managed' : invalid ? { text: 'Fix the amounts above first' } : patch ? null : { text: 'Nothing to save yet' });
  // A 422 shows on the cap it names; one that names neither cap is shown here, never dropped.
  const onCap = (f: FieldError) => BUDGET_KINDS.some((k) => f.path === costBudgetView(k).path);
  const failure = error && !(isApiError(error) && error.status === 422 && error.fieldErrors.some(onCap)) ? describeError(error) : null;
  const stale = isApiError(error) && error.code === 'stale_version';

  return (
    <Card>
      <CardHeader
        level={2}
        title="Daily budgets"
        description="Caps on what checking may spend each day (UTC). Spend counts real calls only: simulated checks never count."
      />
      <CardContent className="space-y-5">
        {BUDGET_KINDS.map((kind) => {
          const view = costBudgetView(kind);
          const env = budgetEnvManaged(settings, kind);
          const p = parsed[kind];
          const spend = spendToday(status, kind);
          const fieldError =
            serverErrors.find((f) => f.path === view.path)?.message ?? ((touched || edited(kind)) && !env && 'error' in p ? p.error : undefined);
          const below = !env && 'value' in p && p.value > 0 && spend !== null && p.value < spend.spent;
          return (
            <div key={kind} data-budget={kind} className="space-y-2">
              <Field label={`${view.title}, USD per day`} description={view.description} error={fieldError}>
                <Input
                  value={env ? capText(capOf(settings, kind)) : drafts[kind]}
                  onValueChange={(v) => form.setDraft((d) => ({ ...d, [kind]: v }))}
                  readOnly={env || writeReason !== null}
                  inputMode="decimal"
                  className="max-w-40 tabular"
                />
              </Field>
              {env ? <DisabledReason reason={{ text: fillTemplate(copy.settings.env_managed, { keys: BUDGET_ENV[kind] }) }} /> : null}
              {below ? (
                <p className="text-xs text-fg">
                  Today’s spend ({formatUsd(spend.spent)}) is already above this cap: once saved, {BELOW_SPEND[kind]}
                </p>
              ) : null}
              <SpendBar label={view.title} cap={capOf(settings, kind)} spend={spend} observeLine={kind === 'decision' ? 1 - reserve : undefined} />
            </div>
          );
        })}
        {failure ? (
          <div role="alert" className="space-y-1 rounded-md border border-border bg-surface px-3 py-2 text-sm">
            <p className="text-fg">
              Couldn’t save the budgets: {stale ? copy.settings.stale_version : failure.message}
            </p>
            {serverErrors
              .filter((f) => !onCap(f))
              .map((f, i) => (
                <p key={i} className="text-fg">
                  {f.message}
                </p>
              ))}
            {failure.code ? <p className="font-mono text-2xs text-fg-subtle">{failure.code}</p> : null}
            {stale ? (
              <Button
                size="sm"
                onClick={() => {
                  // Drop the edits and follow the latest version (the refetch brings anything newer).
                  form.reset(settings);
                  setTouched(false);
                  setError(null);
                  void queryClient.invalidateQueries({ queryKey: qkcSecurity.settings });
                }}
              >
                Reload settings
              </Button>
            ) : null}
          </div>
        ) : null}
        <div className="flex flex-wrap items-center gap-2">
          <DisabledAction reason={reason}>
            {(props) => (
              <Button variant="primary" pending={saving} onClick={() => void save()} {...props}>
                Save budgets
              </Button>
            )}
          </DisabledAction>
          {dirty && writeReason === null ? (
            <Button
              variant="ghost"
              disabled={saving}
              onClick={() => {
                form.reset(settings);
                setTouched(false);
                setError(null);
              }}
            >
              Discard changes
            </Button>
          ) : null}
        </div>
        <ConfirmDialog
          open={confirm !== null}
          onOpenChange={(open) => {
            if (!open) setConfirm(null);
          }}
          title="Save a fast-check cap below today’s spend?"
          description={fillTemplate(copy.settings.fail_closed_confirm, { routes: (confirm?.routes ?? []).join(', ') })}
          confirmLabel="Save anyway"
          onConfirm={async () => {
            if (!confirm) return;
            applied(await ds.patchSettings(confirm.patch, form.etag, { confirm: true }));
          }}
        >
          <p className="text-sm text-fg-muted">
            Until the cap resets at midnight UTC, or you raise it again, every answer on these routes is blocked.
          </p>
        </ConfirmDialog>
      </CardContent>
    </Card>
  );
}

export function BudgetsSection() {
  const ds = useDataSource();
  const q = useQuery(settingsQuery(ds));
  const { status } = useSystemStatus();
  const showLoading = useDelayedLoading(q.isPending);
  const write = useWriteReason('admin');
  const writeReason: Reason | null = write ?? (status?.persistence.configured === false ? 'needs_database' : null);

  if (q.isPending) {
    return showLoading ? (
      <div aria-busy="true" className="space-y-4">
        <span role="status" className="sr-only">
          {copy.states.loading}
        </span>
        <Skeleton className="h-64 w-full" />
        <Skeleton className="h-48 w-full" />
      </div>
    ) : null;
  }
  if (q.isError) {
    return isNotServed(q.error) ? (
      <DegradedState
        title="Budgets can’t be changed from this dashboard"
        reason="This proxy does not serve GET /api/settings: it is older than this dashboard."
        stillWorks={`The caps still apply: set ${BUDGET_ENV.decision} and ${BUDGET_ENV.llm} in .env and restart.`}
        action={
          <Link to="/app/overview" search={{ tab: 'savings' }} className="inline-flex items-center gap-0.5 text-sm font-medium text-fg underline underline-offset-2">
            See what checking costs
            <ArrowRight aria-hidden="true" className="size-3.5" />
          </Link>
        }
      />
    ) : (
      <ErrorState error={q.error} onRetry={() => void q.refetch()} />
    );
  }
  return (
    <div className="space-y-6">
      <BudgetsCard settings={q.data} status={status} writeReason={writeReason} />
      <Card>
        <CardHeader level={2} title="Prices" description="What every cost estimate on this dashboard is priced with, per million tokens." />
        <CardContent>
          <PricesPanel settings={q.data} writeReason={writeReason} />
        </CardContent>
      </Card>
    </div>
  );
}
