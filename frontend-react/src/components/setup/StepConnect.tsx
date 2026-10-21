/**
 * Step 2 of the setup wizard, "Connect OpenRouter" (SPEC §3.3, §6, §7.5; ON-7c, D38, D39,
 * D75, D76, D87, D88):
 *
 * - **Connect OpenRouter** (primary, D88): PKCE through the proxy; the browser leaves for OpenRouter
 *   and comes back through /connect/callback, which hands its outcome to this step.
 * - **Paste a key instead**: one PUT that stores, checks and binds the key to the fast check and
 *   the judge.
 * - **Try it without a key**: simulated checks ($0, labelled Simulated, never blocking).
 * - The prerequisite (about $5 of credit, its answer count labelled Estimate), the privacy line,
 *   the no-credits state with "I added credits — check again", the no-limit callout, and
 *   "Simulated checks are now off" after a bind.
 *
 * On the demo stack the step is done already (`StepConnectDemo`): its checks are simulated and keys
 * cannot be connected there, so it says so instead of offering three ways in that are all disabled.
 */
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { CircleCheck, ClipboardPaste, ExternalLink, FlaskConical, LogIn } from 'lucide-react';
import { useEffect, useId, useState, type FormEvent } from 'react';
import { useDataSource } from '@/app/services';
import { DisabledAction, DisabledReason, type Reason } from '@/components/common/DisabledReason';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { CredentialResult } from '@/components/settings/CredentialResult';
import { EnvManagedNote, ProblemNote, StoreNotice } from '@/components/settings/SettingsKit';
import { usePutCredential, useVerifyCredential } from '@/components/settings/useCredentialWrites';
import { useCredentialWriteReason } from '@/components/settings/useSettingsAccess';
import { Button } from '@/components/ui/button';
import { Field } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { isApiError } from '@/data/errors';
import { qk } from '@/data/queries';
import { qkcSecurity } from '@/data/queries/security';
import { copy } from '@/domain/presentation';
import type { BindingTarget, CredentialWriteResult, RuntimeSettingsPatch } from '@/types/api';
import { simulatedPatch, useStartConnect, type ConnectOutcome } from './connectFlow';
import type { SetupProgress } from './useSetupProgress';

/**
 * Which way in is the primary button (D88). Connect OpenRouter unless the owner's localhost PKCE
 * check fails, in which case the lead flips this to 'paste' (recorded in JEV-ACCESS.md).
 */
export const CONNECT_PRIMARY: 'connect' | 'paste' = 'connect';

/** The checks a wizard key runs: the fast check and the judge (D75 default). */
const BIND: BindingTarget[] = ['decision_model', 'judge'];

export interface StepConnectProps {
  progress: SetupProgress;
  /** Why connecting is unavailable here (read-only, demo, admin), or null. */
  writeReason: Reason | null;
}

/**
 * Step 2 on the demo stack: nothing to connect. The demo's mock fast check and mock judge already
 * run, so step 3 is open; how to use your own keys is in the demo banner, said once.
 */
export function StepConnectDemo() {
  return (
    <div className="space-y-2">
      <p role="status" className="flex items-start gap-1.5 text-sm text-fg">
        <CircleCheck aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-fg-muted" />
        {copy.setup.step_connect.demo_ready}
      </p>
      <p className="text-sm text-fg-muted">{copy.setup.step_connect.demo_note}</p>
    </div>
  );
}

export function StepConnect({ progress, writeReason }: StepConnectProps) {
  const ds = useDataSource();
  const qc = useQueryClient();
  const settings = progress.settings;
  // The outcome /connect/callback left (or a paste on this page); read once, then dropped from the cache.
  const [outcome, setOutcome] = useState<ConnectOutcome | null>(() => qc.getQueryData<ConnectOutcome>(qkcSecurity.connectResult) ?? null);
  useEffect(() => {
    qc.removeQueries({ queryKey: qkcSecurity.connectResult, exact: true });
  }, [qc]);
  const [pasting, setPasting] = useState(CONNECT_PRIMARY === 'paste');
  const [secret, setSecret] = useState('');
  const connect = useStartConnect('/app/setup?step=connect');
  const put = usePutCredential();
  const verify = useVerifyCredential();
  const simulate = useMutation({
    mutationFn: (patch: RuntimeSettingsPatch) => ds.patchSettings(patch, settings?.etag ?? '"s0"'),
    onSuccess: (next) => {
      qc.setQueryData(qkcSecurity.settings, next);
      void qc.invalidateQueries({ queryKey: qk.status });
    },
    // Someone saved first (412): read the settings again, so the next click sends the new version.
    onError: (err) => {
      if (isApiError(err) && err.status === 412) void qc.invalidateQueries({ queryKey: qkcSecurity.settings });
    },
  });

  const envKey = progress.status?.decision_model.credential.source === 'env';
  const openrouterEnv = envKey && progress.status?.decision_model.credential.name === 'openrouter';
  // Saving a key also needs the credentials store (D32): a database and a master key.
  const credentialReason = useCredentialWriteReason();
  const keyReason: Reason | null = writeReason ?? credentialReason ?? (openrouterEnv ? 'env_managed' : null);
  const simPatch = simulatedPatch(settings?.env_managed ?? []);
  const noDatabase = progress.status?.credentials.store === 'no_database';
  const simReason: Reason | null =
    writeReason ?? (noDatabase ? 'needs_database' : !settings ? { text: 'Reading the settings…' } : simPatch ? null : 'env_managed');
  const reasonId = useId();

  const record = (result: CredentialWriteResult, wasSimulated: boolean) => setOutcome({ result, wasSimulated });
  const submitPaste = (e: FormEvent) => {
    e.preventDefault();
    if (secret.trim() === '') return;
    const wasSimulated = progress.simulated;
    const request = { provider: 'openrouter' as const, secret: secret.trim(), verify: true, bind: BIND };
    setSecret('');
    put.mutate({ name: 'openrouter', request }, { onSuccess: (r) => record(r, wasSimulated) });
  };
  const checkAgain = () => {
    const wasSimulated = outcome?.wasSimulated ?? progress.simulated;
    verify.mutate({ name: 'openrouter', request: { bind: BIND } }, { onSuccess: (r) => record(r, wasSimulated) });
  };

  // Once a key is connected, Next is the step's only primary action: connecting or pasting again
  // only replaces the key, and simulated checks are not offered (they would replace real checks).
  const connected = progress.connected;
  const primary = (way: typeof CONNECT_PRIMARY) => (!connected && CONNECT_PRIMARY === way ? 'primary' : 'secondary');
  // Both ways in share one reason when neither is available.
  const off = keyReason ? { disabled: true, 'aria-describedby': reasonId } : { disabled: false };
  const connectButton = (
    <Button variant={primary('connect')} onClick={() => connect.mutate()} pending={connect.isPending} {...off}>
      <LogIn aria-hidden="true" />
      {copy.setup.step_connect.primary}
    </Button>
  );

  return (
    <div className="space-y-5">
      <p className="text-sm text-fg">
        {copy.setup.step_connect.prerequisite}{' '}
        <EvidenceBadge label="estimate" basis={copy.setup.step_connect.prerequisite_basis} subject="Answers $5 of credit covers" className="align-middle" />{' '}
        <a href="https://openrouter.ai/settings/credits" target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-0.5 font-medium underline underline-offset-2">
          {copy.setup.step_connect.prerequisite_link}
          <ExternalLink aria-hidden="true" className="size-3" />
        </a>
      </p>

      {/* Why keys cannot be saved here (no database, no master key) and how to fix it (D32). */}
      <StoreNotice stillWorks="Keys set in .env keep working." />

      {openrouterEnv ? (
        <div className="space-y-1">
          <p className="text-sm font-medium text-fg">The fast check already uses the OpenRouter key from .env.</p>
          <EnvManagedNote keys={['OPENROUTER_API_KEY']} />
        </div>
      ) : null}

      {connected && !openrouterEnv && !outcome ? (
        <p role="status" className="flex items-center gap-1.5 text-sm text-fg">
          <CircleCheck aria-hidden="true" className="size-4 shrink-0 text-fg-muted" />
          A key is connected, so answers are checked for real. Go on to step 3, or replace the key below.
        </p>
      ) : null}

      <div className="space-y-1.5">
        {connected && !openrouterEnv ? <p className="text-xs text-fg-muted">To use a different key:</p> : null}
        <div className="flex flex-wrap items-start gap-3">
          {CONNECT_PRIMARY === 'connect' ? connectButton : null}
          <Button variant={primary('paste')} onClick={() => setPasting((v) => !v)} aria-expanded={pasting} {...off}>
            <ClipboardPaste aria-hidden="true" />
            {copy.setup.step_connect.paste_key}
          </Button>
          {CONNECT_PRIMARY === 'paste' ? connectButton : null}
        </div>
        {keyReason ? <DisabledReason id={reasonId} reason={keyReason} /> : null}
      </div>
      {connect.isError ? <ProblemNote error={connect.error} title="Connect OpenRouter did not start" /> : null}

      {pasting && keyReason === null ? (
        <form onSubmit={submitPaste} className="space-y-2 rounded-md border border-border px-3 py-3" aria-label="Paste an OpenRouter key">
          <Field
            label="OpenRouter key"
            description={
              <>
                Create one at{' '}
                <a href="https://openrouter.ai/settings/keys" target="_blank" rel="noopener noreferrer" className="font-medium text-fg underline underline-offset-2">
                  openrouter.ai/settings/keys
                </a>{' '}
                (set a credit limit on it, for example $2). It is encrypted here and only ever shown masked.
              </>
            }
          >
            <Input type="password" value={secret} onValueChange={setSecret} autoComplete="off" spellCheck={false} placeholder="sk-or-v1-…" />
          </Field>
          <DisabledAction reason={secret.trim() === '' ? { text: 'Paste the key first' } : null}>
            {(p) => (
              <Button type="submit" variant="primary" size="sm" pending={put.isPending} {...p}>
                Save and check
              </Button>
            )}
          </DisabledAction>
          {put.isError ? <ProblemNote error={put.error} title="The key was not saved" /> : null}
        </form>
      ) : null}

      {outcome ? (
        <CredentialResult result={outcome.result} wasSimulated={outcome.wasSimulated} onCheckAgain={checkAgain} checkingAgain={verify.isPending} modelsLink />
      ) : null}
      {verify.isError ? <ProblemNote error={verify.error} title="The check did not run" /> : null}

      <p className="text-sm text-fg-muted">{copy.setup.step_connect.privacy}</p>

      {connected ? null : (
        <section aria-labelledby="simulated-title" className="space-y-2 border-t border-border pt-4">
          <h3 id="simulated-title" className="text-sm font-semibold text-fg">
            No key yet?
          </h3>
          {progress.simulated ? (
            <p role="status" className="text-sm text-fg">
              {copy.setup.step_connect.simulated_on}
            </p>
          ) : (
            <>
              <p className="text-sm text-fg-muted">{copy.setup.step_connect.simulated_note}</p>
              <DisabledAction reason={simReason}>
                {(p) => (
                  <Button onClick={() => simPatch && simulate.mutate(simPatch)} pending={simulate.isPending} {...p}>
                    <FlaskConical aria-hidden="true" />
                    {copy.setup.step_connect.simulated}
                  </Button>
                )}
              </DisabledAction>
              {simulate.isError ? <ProblemNote error={simulate.error} title="Simulated checks were not turned on" /> : null}
            </>
          )}
        </section>
      )}
    </div>
  );
}
