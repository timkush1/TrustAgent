/**
 * Step 3 of the setup wizard, "See it work" (SPEC §3.3; ON-7d, D87):
 *
 * - Disabled, with the reason and the way back, until a key runs the fast check or simulated checks
 *   are on.
 * - Runs the refund example of the Playground (the answer says 60 days, the policy says 30) in audit
 *   mode with `mode_override: flag`, so no upstream key is needed, and shows what happened step by
 *   step with a link to the trace.
 * - Mints an app key (ingest scope) only when none is active, and shows it once.
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ArrowRight, KeyRound, Play, RotateCw } from 'lucide-react';
import { useState } from 'react';
import { useDataSource } from '@/app/services';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { SecretOnce } from '@/components/common/SecretOnce';
import { buildRequest, EXAMPLES } from '@/components/playground/playgroundModel';
import { ResultTimeline } from '@/components/playground/ResultTimeline';
import { ProblemNote } from '@/components/settings/SettingsKit';
import { Button } from '@/components/ui/button';
import { apiKeysQuery, qkcSecurity } from '@/data/queries/security';
import { copy } from '@/domain/presentation';
import type { CreatedApiKey } from '@/types/api';
import type { SetupProgress } from './useSetupProgress';

/** The refund example (60 days in the answer, 30 in the policy), flagged rather than blocked. */
const REFUND_EXAMPLE = EXAMPLES.find((e) => e.id === 'refund-contradiction-flag') ?? EXAMPLES[0];

const linkClass = 'inline-flex items-center gap-1 text-sm font-medium text-fg underline underline-offset-2';

function AppKey({ writeReason }: { writeReason: Reason | null }) {
  const ds = useDataSource();
  const qc = useQueryClient();
  const keys = useQuery(apiKeysQuery(ds));
  const [created, setCreated] = useState<CreatedApiKey | null>(null);
  const [saved, setSaved] = useState(false);
  const mint = useMutation({
    mutationFn: () => ds.createApiKey({ label: 'my-app', scopes: ['ingest'] }),
    onSuccess: (key) => {
      setCreated(key);
      void qc.invalidateQueries({ queryKey: qkcSecurity.apiKeys });
    },
  });
  // An app key sends requests and nothing else: the admin key also has the ingest scope, but it
  // must never sit in an app's config, so it does not count.
  const active = keys.data?.items.find((k) => k.revoked_at === null && k.scopes.includes('ingest') && !k.scopes.includes('admin'));

  return (
    <section aria-labelledby="app-key-title" className="space-y-2 border-t border-border pt-4">
      <h3 id="app-key-title" className="text-sm font-semibold text-fg">
        Your app's key
      </h3>
      {created ? (
        <SecretOnce title={`${created.label} (shown once)`} value={created.secret} note={copy.setup.step_try.ingest_key} copyLabel="Copy the app key" saved={saved} onSavedChange={setSaved} />
      ) : active ? (
        <p className="text-sm text-fg">
          Your apps send <code className="font-mono text-xs">X-TrustAgent-Key</code> with the key “{active.label}” (
          <code className="font-mono text-xs">{active.prefix}…</code>). Manage keys in{' '}
          <Link to="/app/settings/$section" params={{ section: 'access' }} className="font-medium underline underline-offset-2">
            Settings → Access
          </Link>
          .
        </p>
      ) : keys.isPending ? (
        <p className="text-sm text-fg-muted">{copy.states.loading}</p>
      ) : keys.isError ? (
        // Without the list it is unknown whether an app key exists: no second key on a guess (D87).
        <ProblemNote
          error={keys.error}
          title="The API keys could not be read"
          action={
            <Button size="sm" onClick={() => void keys.refetch()} pending={keys.isFetching}>
              <RotateCw aria-hidden="true" />
              Try again
            </Button>
          }
        />
      ) : (
        <>
          <p className="text-sm text-fg-muted">Your apps send their requests with an app key (it can only send requests, never read them back).</p>
          <DisabledAction reason={writeReason}>
            {(p) => (
              <Button onClick={() => mint.mutate()} pending={mint.isPending} {...p}>
                <KeyRound aria-hidden="true" />
                Create an app key
              </Button>
            )}
          </DisabledAction>
          {mint.isError ? <ProblemNote error={mint.error} title="The key was not created" /> : null}
        </>
      )}
      {/* Its own line: inline beside "Create an app key" the two ran together with no gap. */}
      <p>
        <Link to="/app/overview" className={linkClass}>
          Connect your app
          <ArrowRight aria-hidden="true" className="size-3.5" />
        </Link>
      </p>
    </section>
  );
}

export interface StepSeeItWorkProps {
  progress: SetupProgress;
  writeReason: Reason | null;
}

export function StepSeeItWork({ progress, writeReason }: StepSeeItWorkProps) {
  const ds = useDataSource();
  const scripted = progress.status?.demo === true || progress.status?.test_mode === true;
  const form = { ...REFUND_EXAMPLE.form, modeOverride: 'flag' as const };
  const run = useMutation({ mutationFn: () => ds.runPlayground(buildRequest(form, { scenario: scripted })) });

  if (!progress.canTry) {
    return (
      <div className="space-y-2">
        <p className="text-sm text-fg">{copy.setup.step_try.disabled_reason}</p>
        <Link to="/app/setup" search={{ step: 'connect' }} className={linkClass}>
          Back to {copy.setup.step_connect.title}
          <ArrowRight aria-hidden="true" className="size-3.5" />
        </Link>
      </div>
    );
  }

  const question = form.messages.find((m) => m.role === 'user')?.content ?? '';
  return (
    <div className="space-y-5">
      <p className="text-sm text-fg">{copy.setup.step_see_it_work.body}</p>
      <dl className="grid gap-2 rounded-md border border-border px-3 py-2.5 text-sm sm:grid-cols-[8rem_minmax(0,1fr)]">
        <dt className="text-fg-muted">Question</dt>
        <dd className="text-fg">{question}</dd>
        <dt className="text-fg-muted">Answer to check</dt>
        <dd className="text-fg">{form.response}</dd>
      </dl>
      {progress.simulated ? <p className="text-sm text-fg-muted">{copy.setup.step_connect.simulated_note}</p> : null}
      <DisabledAction reason={writeReason}>
        {(p) => (
          <Button variant="primary" onClick={() => run.mutate()} pending={run.isPending} {...p}>
            <Play aria-hidden="true" />
            {run.data ? 'Check it again' : 'Check this answer'}
          </Button>
        )}
      </DisabledAction>
      {run.isError ? <ProblemNote error={run.error} title="The check did not run" /> : null}
      {run.data || run.isPending ? (
        <div className="space-y-3">
          <ResultTimeline state={{ mode: 'audit', tokens: '', decision: null, result: run.data ?? null, running: run.isPending }} />
          {run.data ? (
            <Link to="/app/traffic/$traceId" params={{ traceId: run.data.trace_id }} className={linkClass}>
              Open the trace
              <ArrowRight aria-hidden="true" className="size-3.5" />
            </Link>
          ) : null}
        </div>
      ) : null}
      <AppKey writeReason={writeReason} />
    </div>
  );
}
