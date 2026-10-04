/**
 * Settings → Models (SPEC §3.3, §6; ON-6, D31, D33, D37, D77): which model runs each check.
 *
 * - Fast check (Jev): its live state and last error (the "See the last error" link on Policies lands
 *   here), the backend as three choices (OpenRouter recommended, TypeSafe direct, another gateway),
 *   the systemone route, model pin, base URL and path under Advanced, a one-probe Test (#60, which
 *   honours Retry-After), and Disconnect, the only way to turn it off (with the D77 confirmation).
 * - Deep audit judge: provider, model and which saved key it uses.
 * Every save is a PATCH with If-Match; a 412 (someone saved first) offers to reload.
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { FlaskConical, PlugZap, RotateCw, Save } from 'lucide-react';
import { useEffect, useState, type ReactNode } from 'react';
import { useDataSource } from '@/app/services';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { ConfirmAgain } from '@/components/common/confirmAgain';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { KeyValueList } from '@/components/common/KeyValue';
import { PageHeader } from '@/components/common/PageHeader';
import { QueryState } from '@/components/common/QueryState';
import { LoadingState } from '@/components/common/StateViews';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardFooter, CardHeader, Section } from '@/components/ui/card';
import { Field } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { RadioGroup, type RadioOption } from '@/components/ui/radio-group';
import { Select } from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { isApiError } from '@/data/errors';
import { qk, settingsQuery, statusQuery } from '@/data/queries';
import { credentialsQuery, qkcSecurity } from '@/data/queries/security';
import { formatDateTime, formatDurationMs, formatRelativeTime, formatUsd } from '@/domain/format';
import {
  copy,
  fillTemplate,
  SEC_BACKEND_CARDS,
  SEC_JUDGE_PROVIDERS,
  secBackendCardOf,
  secBackendSettingLabel,
  secComponentStateView,
  secCredentialLabel,
  secDecisionStateView,
  secEnvKeyOfPath,
  secJudgeCredential,
  secJudgeSettingLabel,
  secProbeReadyText,
  type SecBackendCard,
} from '@/domain/presentation';
import type { CredentialView, DecisionModelTestResult, RuntimeSettings, RuntimeSettingsPatch, SettingsView, SystemStatus } from '@/types/api';
import { confirmationRoutes, probeCostBasis, probeCostLabel } from './credentialModel';
import { EnvManagedNote, ProblemNote, SettingsWarnings } from './SettingsKit';
import { useSettingsWriteReason } from './useSettingsAccess';

const linkClass = 'font-medium text-fg underline underline-offset-2';

/** Saves a settings patch against the version the edit started from (If-Match, D31). */
function useSaveSettings() {
  const ds = useDataSource();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ patch, etag, confirm }: { patch: RuntimeSettingsPatch; etag: string; confirm?: boolean }) =>
      ds.patchSettings(patch, etag, confirm ? { confirm: true } : undefined),
    onSuccess: (next) => {
      qc.setQueryData(qkcSecurity.settings, next);
      void qc.invalidateQueries({ queryKey: qk.status });
      void qc.invalidateQueries({ queryKey: qkcSecurity.credentials });
    },
  });
}

function isStale(error: unknown): boolean {
  return isApiError(error) && (error.code === 'stale_version' || error.status === 412);
}

/** A failed save; a stale version (412) offers to reload the settings and start again. */
function SaveProblem({ error, onReload }: { error: unknown; onReload: () => void }) {
  return (
    <ProblemNote
      error={error}
      title="Not saved"
      action={
        isStale(error) ? (
          <Button size="sm" onClick={onReload}>
            <RotateCw aria-hidden="true" />
            Reload settings
          </Button>
        ) : undefined
      }
    />
  );
}

/** Whether the environment sets `path` (D33): the path itself or a parent of it is env-managed. */
function isLocked(settings: SettingsView, path: string): boolean {
  return settings.env_managed.some((m) => path === m || path.startsWith(`${m}.`));
}

/** Env-managed paths among `paths` (D33), with their env vars for the note. */
function envManaged(settings: SettingsView, paths: readonly string[]): { locked: (path: string) => boolean; keys: string[] } {
  const locked = (path: string) => isLocked(settings, path);
  const keys = paths.filter(locked).map((p) => secEnvKeyOfPath(p)).filter((k): k is string => k !== null);
  return { locked, keys: [...new Set(keys)] };
}

// -------------------------------------------------------------------------------- fast check

interface FastDraft {
  etag: string;
  card: SecBackendCard['id'] | null;
  systemone: boolean;
  model: string;
  baseUrl: string;
  path: string;
}

/** The card a saved setting is on: the backend's own card, or for `auto` the bound key's card. */
function currentCard(runtime: RuntimeSettings): SecBackendCard | null {
  const own = secBackendCardOf(runtime.decision_model.backend);
  if (own) return own;
  if (runtime.decision_model.backend === 'auto') return SEC_BACKEND_CARDS.find((c) => c.credential === runtime.bindings.decision_model) ?? null;
  return null;
}

function fastDraftOf(settings: SettingsView): FastDraft {
  const r = settings.runtime.decision_model;
  return {
    etag: settings.etag,
    card: currentCard(settings.runtime)?.id ?? null,
    systemone: r.backend === 'openrouter-systemone',
    model: r.model ?? '',
    baseUrl: r.base_url ?? '',
    path: r.path ?? '',
  };
}

function fastPatch(settings: SettingsView, draft: FastDraft): RuntimeSettingsPatch {
  const card = SEC_BACKEND_CARDS.find((c) => c.id === draft.card);
  const before = fastDraftOf(settings);
  const decision: RuntimeSettingsPatch['decision_model'] = {};
  const patch: RuntimeSettingsPatch = {};
  // The backend changes only when the choice did: a model pin alone leaves `auto` as it is.
  if (card && (draft.card !== before.card || draft.systemone !== before.systemone)) {
    decision.backend = card.id === 'openrouter' && draft.systemone ? 'openrouter-systemone' : card.backend;
    // The binding follows the card unless the environment decides it (D33: an env decision key or a
    // pinned backend); sending it then would be refused with 409 env_managed.
    if (!isLocked(settings, 'bindings.decision_model') && settings.runtime.bindings.decision_model !== card.credential) {
      patch.bindings = { decision_model: card.credential };
    }
  }
  const text = (v: string) => (v.trim() === '' ? null : v.trim());
  if (draft.model !== before.model) decision.model = text(draft.model);
  if (draft.baseUrl !== before.baseUrl) decision.base_url = text(draft.baseUrl);
  if (draft.path !== before.path) decision.path = text(draft.path);
  if (Object.keys(decision).length > 0) patch.decision_model = decision;
  return patch;
}

function sameDraft(a: FastDraft, b: FastDraft): boolean {
  return a.card === b.card && a.systemone === b.systemone && a.model === b.model && a.baseUrl === b.baseUrl && a.path === b.path;
}

function LastError({ status }: { status: SystemStatus['decision_model'] }) {
  const e = status.last_error;
  if (!e) return null;
  return (
    <section id="last-error" aria-labelledby="last-error-title" className="space-y-1 rounded-md border border-border-strong px-3 py-2">
      <h3 id="last-error-title" className="text-sm font-semibold text-fg">
        Last error <span className="font-normal text-fg-muted">· <time dateTime={e.at} title={formatDateTime(e.at)}>{formatRelativeTime(e.at)}</time></span>
      </h3>
      <p className="text-sm text-fg">{e.message}</p>
      {e.hint ? <p className="text-sm text-fg-muted">{e.hint}</p> : null}
    </section>
  );
}

function TestResult({ result }: { result: DecisionModelTestResult }) {
  if (result.ok) {
    // A cost is shown only with its label (vendor, estimate, simulated).
    const costLabel = probeCostLabel(result.cost_source);
    const cost = result.cost_usd !== null && costLabel ? formatUsd(result.cost_usd) : null;
    return (
      <div role="status" className="space-y-1 rounded-md border border-border px-3 py-2 text-sm">
        <p className="flex flex-wrap items-center gap-1.5 text-fg">
          <span className="tabular">{secProbeReadyText(result.latency_ms, cost)}</span>
          {cost && costLabel ? <EvidenceBadge label={costLabel} basis={probeCostBasis(costLabel)} subject="Test cost" /> : null}
        </p>
        <p className="text-xs text-fg-muted">
          {secBackendSettingLabel(result.backend)}
          {result.snapshot ? <> · <span className="font-mono">{result.snapshot}</span></> : null}
          {result.endpoint_host ? <> · {result.endpoint_host}</> : null}
        </p>
      </div>
    );
  }
  return (
    <div role="status" className="space-y-1 rounded-md border border-border px-3 py-2 text-sm">
      <p className="text-fg">{result.error?.message ?? 'The check failed.'}</p>
      {result.error?.hint ? <p className="text-fg-muted">{result.error.hint}</p> : null}
    </div>
  );
}

/** Test (#60), disabled with a countdown while the proxy asks to wait (429 Retry-After). */
function TestButton({ reason, simulated }: { reason: Reason | null; simulated: boolean }) {
  const ds = useDataSource();
  const qc = useQueryClient();
  const [waitUntil, setWaitUntil] = useState<number | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const test = useMutation({
    mutationFn: () => ds.testDecisionModel(),
    onSettled: () => void qc.invalidateQueries({ queryKey: qk.status }),
    onError: (err) => {
      if (isApiError(err) && err.status === 429 && err.retryAfterMs) setWaitUntil(Date.now() + err.retryAfterMs);
    },
  });
  const { reset } = test;
  useEffect(() => {
    if (waitUntil === null) return;
    const timer = setInterval(() => {
      const t = Date.now();
      setNow(t);
      if (t >= waitUntil) {
        setWaitUntil(null);
        // The wait is over: the 429 it came from is no longer news.
        reset();
      }
    }, 500);
    return () => clearInterval(timer);
  }, [waitUntil, reset]);
  const waitS = waitUntil === null ? 0 : Math.max(1, Math.ceil((waitUntil - now) / 1000));
  const waitReason: Reason | null = waitUntil !== null ? { text: `Checked a moment ago: try again in ${waitS} s` } : null;
  return (
    <div className="space-y-2">
      <DisabledAction reason={reason ?? waitReason}>
        {(p) => (
          <Button size="sm" onClick={() => test.mutate()} pending={test.isPending} {...p}>
            <FlaskConical aria-hidden="true" />
            Test the fast check
          </Button>
        )}
      </DisabledAction>
      <p className="text-xs text-fg-muted">
        {simulated ? 'One simulated check on the mock model: nothing is charged.' : 'One real check with a tiny prompt: about $0.00001 (estimate).'}
      </p>
      {test.data ? <TestResult result={test.data} /> : null}
      {test.isError && !waitReason ? <ProblemNote error={test.error} title="The test did not run" /> : null}
    </div>
  );
}

function Disconnect({ settings, reason, onReload }: { settings: SettingsView; reason: Reason | null; onReload: () => void }) {
  const save = useSaveSettings();
  const [routes, setRoutes] = useState<string[] | null>(null);
  return (
    <DisabledAction reason={reason}>
      {(p) => (
        <ConfirmDialog
          trigger={
            <Button size="sm" {...p}>
              <PlugZap aria-hidden="true" />
              Disconnect
            </Button>
          }
          onOpenChange={(open) => {
            if (!open) setRoutes(null);
          }}
          title={`Turn the ${copy.tiers.fast_check} off?`}
          description="Answers pass through unchecked until you choose a backend again. Deep audits keep running."
          confirmLabel={routes ? 'Turn off anyway' : 'Turn off'}
          onConfirm={async () => {
            try {
              await save.mutateAsync({ patch: { decision_model: { backend: 'off' } }, etag: settings.etag, confirm: routes !== null });
            } catch (err) {
              const needed = confirmationRoutes(err);
              if (needed && !routes) {
                setRoutes(needed);
                throw new ConfirmAgain();
              }
              if (isStale(err)) onReload();
              throw err;
            }
          }}
        >
          {routes ? <p className="rounded-md border border-border-strong px-3 py-2 text-sm text-fg">{fillTemplate(copy.settings.fail_closed_confirm, { routes: routes.join(', ') })}</p> : null}
        </ConfirmDialog>
      )}
    </DisabledAction>
  );
}

function backendOptions(credentials: readonly CredentialView[] | undefined): RadioOption<SecBackendCard['id']>[] {
  return SEC_BACKEND_CARDS.map((c) => {
    const key = credentials?.find((k) => k.name === c.credential && !k.undecryptable);
    const missing = credentials !== undefined && !key;
    return {
      value: c.id,
      label: (
        <span className="inline-flex flex-wrap items-center gap-2">
          {c.title}
          {c.recommended ? <Badge emphasis="quiet">Recommended</Badge> : null}
        </span>
      ),
      description: missing ? (
        <>
          {c.description} Connect its key first:{' '}
          <Link to="/app/settings/$section" params={{ section: 'connections' }} className={linkClass}>
            open Connections
          </Link>
          .
        </>
      ) : (
        <>
          {c.description}
          {key ? <span className="font-mono"> Key {key.masked || 'set in .env'}.</span> : null}
        </>
      ),
      disabled: missing,
    };
  });
}

function FastCheckCard({ settings, status, credentials, writeReason, onReload }: { settings: SettingsView; status: SystemStatus | null; credentials: readonly CredentialView[] | undefined; writeReason: Reason | null; onReload: () => void }) {
  const save = useSaveSettings();
  const [draft, setDraft] = useState<FastDraft | null>(null);
  const base = fastDraftOf(settings);
  const shown = draft ?? base;
  const dirty = draft !== null && !sameDraft(draft, base);
  const env = envManaged(settings, ['decision_model.backend', 'decision_model.model', 'decision_model.base_url', 'decision_model.path']);
  const edit = (change: Partial<FastDraft>) => setDraft({ ...(draft ?? base), ...change });
  const backend = settings.runtime.decision_model.backend;
  const dm = status?.decision_model ?? null;
  const stateView = dm ? secDecisionStateView(dm.state) : null;
  const reasonFor = (path: string): Reason | null => writeReason ?? (env.locked(path) ? 'env_managed' : null);
  const reload = () => {
    setDraft(null);
    save.reset();
    onReload();
  };
  const submit = () =>
    save.mutate({ patch: fastPatch(settings, shown), etag: shown.etag }, { onSuccess: () => setDraft(null) });

  return (
    <Card>
      <CardHeader
        level={2}
        title={copy.tiers.fast_check}
        description="Checks every answer before your app gets it, with one call to Jev, TypeSafe AI's checking model."
        actions={stateView ? <Badge tone={stateView.tone} emphasis={stateView.tone === 'neutral' ? 'quiet' : 'tint'}>{stateView.label}</Badge> : null}
      />
      <CardContent className="space-y-4">
        {dm ? (
          <KeyValueList
            items={[
              { label: 'Running on', value: secBackendSettingLabel(dm.backend) },
              { label: 'Model', value: dm.snapshot ?? dm.model_requested, mono: true },
              { label: 'Latency (p50 / p95)', value: dm.p50_ms === null ? null : `${formatDurationMs(dm.p50_ms)} / ${formatDurationMs(dm.p95_ms)}`, numeric: true },
            ]}
          />
        ) : null}
        {dm ? <LastError status={dm} /> : null}
        {backend === 'mock' ? <p className="text-sm text-fg">{copy.setup.step_connect.simulated_on}</p> : null}
        {backend === 'off' ? <p className="text-sm text-fg">The {copy.tiers.fast_check} is off: answers pass through unchecked. Choose a backend and save to turn it on.</p> : null}
        <RadioGroup<SecBackendCard['id']>
          label="Backend"
          options={backendOptions(credentials)}
          value={shown.card ?? undefined}
          onValueChange={(card) => edit({ card })}
          disabled={reasonFor('decision_model.backend') !== null}
        />
        <Section title="Advanced" collapsible>
          <div className="space-y-3">
            {shown.card === 'openrouter' ? (
              <Switch
                label="Use OpenRouter's alternate decisions route"
                description="A fallback for when OpenRouter changes the decisions route. Same model, same price."
                checked={shown.systemone}
                onCheckedChange={(systemone) => edit({ systemone })}
                disabled={reasonFor('decision_model.backend') !== null}
              />
            ) : null}
            <Field label="Model pin" optional description="Leave empty for the default. Pinning a newer snapshot needs a new calibration." disabled={reasonFor('decision_model.model') !== null}>
              <Input value={shown.model} onValueChange={(model) => edit({ model })} placeholder="typesafe/jev-1.13" autoComplete="off" spellCheck={false} disabled={reasonFor('decision_model.model') !== null} />
            </Field>
            <Field label="Base URL" optional description="Only for another gateway or a proxy in between. A new address needs the key pasted again." disabled={reasonFor('decision_model.base_url') !== null}>
              <Input value={shown.baseUrl} onValueChange={(baseUrl) => edit({ baseUrl })} placeholder="https://openrouter.ai/api" autoComplete="off" spellCheck={false} disabled={reasonFor('decision_model.base_url') !== null} />
            </Field>
            <Field label="Path" optional disabled={reasonFor('decision_model.path') !== null}>
              <Input value={shown.path} onValueChange={(path) => edit({ path })} placeholder="/alpha/decisions" autoComplete="off" spellCheck={false} disabled={reasonFor('decision_model.path') !== null} />
            </Field>
          </div>
        </Section>
        {env.keys.length > 0 ? <EnvManagedNote keys={env.keys} /> : null}
        {save.isError ? <SaveProblem error={save.error} onReload={reload} /> : null}
      </CardContent>
      <CardFooter className="flex flex-wrap items-start justify-between gap-3">
        <TestButton reason={writeReason} simulated={backend === 'mock'} />
        <div className="flex flex-wrap items-center gap-2">
          {backend !== 'off' ? <Disconnect settings={settings} reason={writeReason ?? (env.locked('decision_model.backend') ? 'env_managed' : null)} onReload={reload} /> : null}
          <DisabledAction reason={writeReason ?? (dirty ? null : { text: 'Change something first' })}>
            {(p) => (
              <Button variant="primary" size="sm" onClick={submit} pending={save.isPending} {...p}>
                <Save aria-hidden="true" />
                Save
              </Button>
            )}
          </DisabledAction>
        </div>
      </CardFooter>
    </Card>
  );
}

// ------------------------------------------------------------------------------------- judge

interface JudgeDraft {
  etag: string;
  provider: RuntimeSettings['judge']['provider'];
  model: string;
}

function JudgeCard({ settings, status, credentials, writeReason, onReload }: { settings: SettingsView; status: SystemStatus | null; credentials: readonly CredentialView[] | undefined; writeReason: Reason | null; onReload: () => void }) {
  const save = useSaveSettings();
  const base: JudgeDraft = { etag: settings.etag, provider: settings.runtime.judge.provider, model: settings.runtime.judge.model ?? '' };
  const [draft, setDraft] = useState<JudgeDraft | null>(null);
  const shown = draft ?? base;
  const dirty = draft !== null && (draft.provider !== base.provider || draft.model !== base.model);
  const env = envManaged(settings, ['judge.provider', 'judge.model']);
  const engineEnv = env.locked('judge.provider') || env.locked('bindings.judge');
  const reason = writeReason ?? (engineEnv ? 'env_managed' : null);
  // LLM_MODEL alone pins the model; the provider stays editable.
  const modelReason = reason ?? (env.locked('judge.model') ? 'env_managed' : null);
  const keyName = secJudgeCredential(shown.provider);
  const key = keyName ? credentials?.find((c) => c.name === keyName && !c.undecryptable) : undefined;
  // A key in the engine's own .env (D80) is used there as is: nothing to bind.
  const engineKey = key?.source === 'env' && (key.name === 'anthropic' || key.name === 'openai');
  const bound = settings.runtime.bindings.judge;
  const judgeState = status ? secComponentStateView(status.judge.state) : null;

  // Only what changed goes in the PATCH: an unchanged env-managed field would be refused (409
  // env_managed, D33), and a binding sent again would still count as a bind (D76 turns simulated
  // checks off).
  const submit = () => {
    const judge: NonNullable<RuntimeSettingsPatch['judge']> = {};
    if (shown.provider !== base.provider) judge.provider = shown.provider;
    if (shown.model !== base.model && !env.locked('judge.model')) judge.model = shown.model.trim() === '' ? null : shown.model.trim();
    const patch: RuntimeSettingsPatch = Object.keys(judge).length > 0 ? { judge } : {};
    // The binding moves only once the saved keys are known: an unread list must never unbind a key.
    const wanted = shown.provider === 'auto' ? bound : key && !engineKey ? key.name : null;
    if (credentials !== undefined && wanted !== bound) patch.bindings = { judge: wanted };
    save.mutate({ patch, etag: shown.etag }, { onSuccess: () => setDraft(null) });
  };
  const reload = () => {
    setDraft(null);
    save.reset();
    onReload();
  };

  let keyLine: ReactNode;
  if (shown.provider === 'auto') keyLine = bound ? `Uses the ${secCredentialLabel(bound)} key.` : 'Uses the first connected key that can run a judge.';
  else if (!keyName) keyLine = shown.provider === 'ollama' ? 'Runs on the local LLM add-on: no key needed.' : 'Needs no key.';
  else if (key && engineKey) keyLine = `Uses the ${secCredentialLabel(key.name)} key from the engine's .env.`;
  else if (key) keyLine = `Uses the ${secCredentialLabel(key.name)} key${key.masked ? ` (${key.masked})` : ''}.`;
  else
    keyLine = (
      <>
        Needs a {secCredentialLabel(keyName)} key:{' '}
        <Link to="/app/settings/$section" params={{ section: 'connections' }} className={linkClass}>
          paste one in Connections
        </Link>
        .
      </>
    );

  return (
    <Card>
      <CardHeader
        level={2}
        title={`${copy.tiers.deep_audit} judge`}
        description="Splits flagged answers into claims and checks each against the documents, in the background. It explains what the fast check flagged."
        actions={judgeState ? <Badge tone={judgeState.tone} emphasis={judgeState.tone === 'neutral' ? 'quiet' : 'tint'}>{judgeState.label}</Badge> : null}
      />
      <CardContent className="space-y-4">
        {status ? (
          <KeyValueList
            items={[
              { label: 'Running on', value: status.judge.provider ? secJudgeSettingLabel(status.judge.provider as RuntimeSettings['judge']['provider']) : null },
              { label: 'Model', value: status.judge.model, mono: true },
            ]}
          />
        ) : null}
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Provider" disabled={reason !== null}>
            <Select<RuntimeSettings['judge']['provider']>
              options={SEC_JUDGE_PROVIDERS.map((p) => ({ value: p, label: secJudgeSettingLabel(p) }))}
              value={shown.provider}
              onValueChange={(provider) => provider && setDraft({ ...shown, provider })}
              disabled={reason !== null}
            />
          </Field>
          <Field label="Model" optional description="Leave empty for the provider's default judge." disabled={modelReason !== null}>
            <Input value={shown.model} onValueChange={(model) => setDraft({ ...shown, model })} placeholder="anthropic/claude-haiku-4.5" autoComplete="off" spellCheck={false} disabled={modelReason !== null} />
          </Field>
        </div>
        <p className="text-sm text-fg-muted">{keyLine}</p>
        {engineEnv ? <EnvManagedNote keys={env.keys.length > 0 ? env.keys : ['LLM_PROVIDER']} engine={!env.locked('judge.provider')} /> : null}
        {!engineEnv && env.keys.length > 0 ? <EnvManagedNote keys={env.keys} /> : null}
        {save.isError ? <SaveProblem error={save.error} onReload={reload} /> : null}
      </CardContent>
      <CardFooter className="flex flex-col items-end gap-1">
        <DisabledAction reason={reason ?? (dirty ? null : { text: 'Change something first' })}>
          {(p) => (
            <Button variant="primary" size="sm" onClick={submit} pending={save.isPending} {...p}>
              <Save aria-hidden="true" />
              Save
            </Button>
          )}
        </DisabledAction>
      </CardFooter>
    </Card>
  );
}

// ----------------------------------------------------------------------------------- section

export function ModelsSection() {
  const ds = useDataSource();
  const settings = useQuery(settingsQuery(ds));
  const status = useQuery(statusQuery(ds));
  const writeReason = useSettingsWriteReason();
  const admin = status.data ? status.data.auth.caller_scopes.includes('admin') : true;
  const credentials = useQuery({ ...credentialsQuery(ds), enabled: admin && !status.isPending });
  const onReload = () => void settings.refetch();
  return (
    <div className="space-y-5">
      <PageHeader description={`Which model runs each check. The ${copy.tiers.fast_check} checks every answer in a fraction of a second; the ${copy.tiers.deep_audit} explains and confirms in the background.`} />
      <QueryState query={settings} loading={<LoadingState rows={6} />}>
        {(view) => (
          <div className="space-y-5">
            <SettingsWarnings warnings={view.warnings} />
            <FastCheckCard settings={view} status={status.data ?? null} credentials={credentials.data?.items} writeReason={writeReason} onReload={onReload} />
            <JudgeCard settings={view} status={status.data ?? null} credentials={credentials.data?.items} writeReason={writeReason} onReload={onReload} />
          </div>
        )}
      </QueryState>
    </div>
  );
}
