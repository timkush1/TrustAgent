/**
 * Step 1 of the setup wizard, "Secure this dashboard" (SPEC §3.3, §7.5; ON-7b):
 *
 * - With the proxy's one-time link (`#token=`) it claims the token: the proxy creates the first
 *   admin key, shows it once, and signs this browser in. Copying the key and ticking "I saved it"
 *   unlock Next, because the proxy keeps only its hash.
 * - Without the link it says how to find one (the proxy logs, or `make setup-url`) and takes the
 *   token pasted (the whole SETUP line works too).
 * - An admin (or any key holder) can always sign in with a key instead.
 * - Signed in as an admin already, the step is done: it says as whom and moves on.
 */
import { Link } from '@tanstack/react-router';
import { ArrowRight, KeyRound, LogOut, ShieldCheck } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { CodeBlock } from '@/components/common/CodeBlock';
import { DegradedState } from '@/components/common/DegradedState';
import { DisabledAction } from '@/components/common/DisabledReason';
import { describeError } from '@/components/common/describeError';
import { SecretOnce } from '@/components/common/SecretOnce';
import { Button } from '@/components/ui/button';
import { Field } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { isApiError } from '@/data/errors';
import { commandText, copy, secScopeView } from '@/domain/presentation';
import type { SessionInfo, SetupStatus } from '@/types/api';
import { tokenFromPaste } from './setupToken';
import type { useDashboardAuth } from './useDashboardAuth';

/** What to do after a refused claim or sign-in, in words. */
function failureHint(error: unknown, claiming: boolean): string | null {
  if (!isApiError(error)) return null;
  const setupUrl = commandText('setup_url') ?? 'make setup-url';
  switch (error.code) {
    case 'setup_token_used':
      return `This setup link was already used. Sign in with the admin key it created, or run ${setupUrl} for a new link while no admin exists.`;
    case 'persistence_disabled':
      return 'Dashboard sign-in needs the database (TRUTHTABLE_DATABASE_URL). Without one, set TRUTHTABLE_ADMIN_KEYS and send the key with each request.';
    case 'csrf_failed':
      return 'The proxy did not accept this page’s origin: add the dashboard URL to TRUTHTABLE_ALLOWED_ORIGINS or set TRUTHTABLE_PUBLIC_URL to it.';
    case 'storage_blocked':
      return null;
    default:
      if (error.status === 401) {
        return claiming ? `The setup link is not valid or has expired. Run ${setupUrl} for a new one.` : 'The key was not accepted.';
      }
      if (error.status === 403) return 'This key cannot open a dashboard session (it needs at least the read scope).';
      return null;
  }
}

function Failure({ error, claiming }: { error: unknown; claiming: boolean }) {
  const { message, code } = describeError(error);
  const hint = failureHint(error, claiming);
  return (
    <div role="alert" className="space-y-1 rounded-md border border-border px-3 py-2">
      <p className="text-sm font-medium text-fg">
        {claiming ? 'The setup link was refused' : 'Sign-in failed'}: {message}
      </p>
      {code ? <p className="font-mono text-2xs text-fg-subtle">{code}</p> : null}
      {hint ? <p className="text-sm text-fg-muted">{hint}</p> : null}
    </div>
  );
}

function SignedIn({ session, forThisTab, onSignOut, signingOut }: { session: SessionInfo; forThisTab: boolean; onSignOut: () => void; signingOut: boolean }) {
  const admin = session.scopes.includes('admin');
  return (
    <div className="space-y-2">
      <p className="flex flex-wrap items-center gap-2 text-sm text-fg">
        <ShieldCheck aria-hidden="true" className="size-4 text-tone-success" />
        {forThisTab ? 'Signed in for this tab as' : 'Signed in as'} <span className="font-medium">{session.actor}</span>
        <span className="text-fg-muted">({session.scopes.map((s) => secScopeView(s).label).join(', ')})</span>
      </p>
      {forThisTab ? (
        <p className="text-sm text-fg-muted">The proxy is on another origin, so this tab keeps the key and sends it with each request. Closing the tab or signing out forgets it.</p>
      ) : null}
      <p className="text-sm text-fg-muted">
        {admin
          ? 'Admin actions are available: connect keys, edit policies, run the Playground and manage keys.'
          : 'This session can read the dashboard; setting it up and changing it need an admin key.'}
      </p>
      <div className="flex flex-wrap items-center gap-3">
        {admin ? null : (
          <Link to="/app/overview" className="inline-flex items-center gap-1 text-sm font-medium text-fg underline underline-offset-2">
            Open Overview
            <ArrowRight aria-hidden="true" className="size-3.5" />
          </Link>
        )}
        <Button size="sm" variant="ghost" onClick={onSignOut} disabled={signingOut}>
          <LogOut aria-hidden="true" />
          Sign out
        </Button>
      </div>
    </div>
  );
}

export interface StepSecureProps {
  auth: ReturnType<typeof useDashboardAuth>;
  setup: SetupStatus | undefined;
  /** The token from the address (`#token=`), read once. */
  token: string | null;
  readonly: boolean;
  /** Ticked "I saved it" for the admin key this claim created. */
  saved: boolean;
  onSavedChange: (saved: boolean) => void;
}

export function StepSecure({ auth, setup, token, readonly, saved, onSavedChange }: StepSecureProps) {
  const { signedIn, signIn, signOut, createdKey, cookieLost, crossOrigin } = auth;
  const [key, setKey] = useState('');
  const [pasted, setPasted] = useState('');
  const claiming = signIn.variables !== undefined && 'setup_token' in signIn.variables;
  const setupRequired = setup?.setup_required === true;
  const pastedToken = tokenFromPaste(pasted);

  const onSubmitKey = (e: FormEvent) => {
    e.preventDefault();
    if (key.trim() !== '') signIn.mutate({ key: key.trim() }, { onSuccess: () => setKey('') });
  };
  const onSubmitToken = (e: FormEvent) => {
    e.preventDefault();
    if (pastedToken) signIn.mutate({ setup_token: pastedToken }, { onSuccess: () => setPasted('') });
  };

  return (
    <div className="space-y-5">
      {createdKey ? (
        <SecretOnce
          title="Your admin key (shown once)"
          value={createdKey}
          note={`${copy.setup.step_secure.save_admin_key} Store it in your password manager: it signs you in again after the session ends, and the proxy keeps only its hash.`}
          copyLabel="Copy the admin key"
          saved={saved}
          onSavedChange={onSavedChange}
        />
      ) : null}
      {cookieLost ? (
        <DegradedState
          title="This browser did not keep the session"
          reason="The proxy accepted the sign-in, but the browser did not keep its session cookie (cookies are blocked for this site, or a secure cookie on an http address)."
          stillWorks="Keys still work for API calls: send one in X-TrustAgent-Key."
          action={<p className="text-sm text-fg-muted">Allow cookies for this site, or open the dashboard at its public URL, then sign in again.</p>}
        />
      ) : null}
      {signIn.isError ? <Failure error={signIn.error} claiming={claiming} /> : null}
      {signedIn ? (
        <>
          <SignedIn session={signedIn} forThisTab={crossOrigin} onSignOut={() => signOut.mutate()} signingOut={signOut.isPending} />
          {signOut.isError ? <Failure error={signOut.error} claiming={false} /> : null}
        </>
      ) : (
        <>
          {token ? (
            <section aria-labelledby="claim-title" className="space-y-2">
              <h3 id="claim-title" className="text-sm font-semibold text-fg">
                Create the admin key
              </h3>
              <p className="text-sm text-fg-muted">
                {setupRequired
                  ? 'This one-time link creates the first admin key for this TrustAgent and signs this browser in.'
                  : 'This TrustAgent already has an admin. A link made with -force can still create another admin key.'}
              </p>
              <DisabledAction reason={readonly ? 'readonly' : null}>
                {(p) => (
                  <Button variant="primary" onClick={() => signIn.mutate({ setup_token: token })} {...p} pending={signIn.isPending && claiming}>
                    <ShieldCheck aria-hidden="true" />
                    Create the admin key
                  </Button>
                )}
              </DisabledAction>
            </section>
          ) : setupRequired ? (
            <section aria-labelledby="setup-title" className="space-y-3">
              <h3 id="setup-title" className="text-sm font-semibold text-fg">
                No admin yet
              </h3>
              <p className="text-sm text-fg-muted">{copy.setup.step_secure.no_token}</p>
              <CodeBlock label="Find the setup link in the proxy logs" code={commandText('setup_token_from_logs') ?? 'docker compose logs proxy | grep -m1 SETUP'} />
              <CodeBlock label="Print a new setup link" code={setup?.recovery_command || (commandText('setup_url') ?? 'make setup-url')} />
              <form onSubmit={onSubmitToken} className="space-y-2">
                <Field label={copy.setup.step_secure.paste_token} description="The token, or the whole “SETUP: open …#token=…” line.">
                  <Input value={pasted} onValueChange={setPasted} autoComplete="off" spellCheck={false} />
                </Field>
                <DisabledAction reason={readonly ? 'readonly' : pasted.trim() === '' ? { text: 'Paste the token first' } : !pastedToken ? { text: 'That does not look like a setup token' } : null}>
                  {(p) => (
                    <Button type="submit" variant="primary" {...p} pending={signIn.isPending && claiming}>
                      <ShieldCheck aria-hidden="true" />
                      Create the admin key
                    </Button>
                  )}
                </DisabledAction>
              </form>
            </section>
          ) : null}
          <form onSubmit={onSubmitKey} className="space-y-2" aria-labelledby="key-title">
            <h3 id="key-title" className="text-sm font-semibold text-fg">
              Sign in with a key
            </h3>
            <Field label="Key" description="An admin key (or a read key) from TRUTHTABLE_ADMIN_KEYS, TRUTHTABLE_READ_KEYS or one created in Settings → Access.">
              <Input type="password" autoComplete="current-password" value={key} onValueChange={setKey} />
            </Field>
            <DisabledAction reason={key.trim() === '' ? { text: 'Enter a key first' } : null}>
              {(p) => (
                <Button type="submit" {...p} pending={signIn.isPending && !claiming}>
                  <KeyRound aria-hidden="true" />
                  Sign in
                </Button>
              )}
            </DisabledAction>
          </form>
        </>
      )}
    </div>
  );
}
