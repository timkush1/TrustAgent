/**
 * Setup and sign-in (SPEC §2.4 #3-#6, §7.5): the page the proxy's one-time line
 * "SETUP: open <public URL>/app/setup#token=…" points at.
 *
 * - With a setup token in the address (and no admin yet) it offers to claim it: the proxy creates
 *   the first admin key, shows it once, and signs this browser in with a session cookie.
 * - Otherwise an admin (or any key holder) signs in with a key; the session cookie then carries
 *   the key's scopes, so admin actions such as saving a policy become available.
 * - Signed in, it says as whom and offers to sign out.
 *
 * The token leaves the address bar as soon as the page reads it. The rest of the setup wizard
 * (connections, budgets, first request) arrives in P4.
 *
 * Page contract: loading, the server's message on errors (a used or expired token says what to
 * do next), read-only and database-less instances explained, and no dead buttons.
 */
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ArrowRight, KeyRound, LogOut, ShieldCheck } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useDataSource } from '@/app/services';
import { CodeBlock } from '@/components/common/CodeBlock';
import { CopyButton } from '@/components/common/CopyButton';
import { DegradedState } from '@/components/common/DegradedState';
import { DisabledAction } from '@/components/common/DisabledReason';
import { describeError } from '@/components/common/describeError';
import { PageHeader } from '@/components/common/PageHeader';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Field } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { Skeleton } from '@/components/ui/skeleton';
import { isApiError } from '@/data/errors';
import { setupStatusQuery } from '@/data/queries';
import { commandText, secScopeView } from '@/domain/presentation';
import type { SessionInfo } from '@/types/api';
import { takeSetupToken } from './setupToken';
import { useDashboardAuth } from './useDashboardAuth';

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
      <p className="text-sm font-medium text-fg">{claiming ? 'The setup link was refused' : 'Sign-in failed'}: {message}</p>
      {code ? <p className="font-mono text-2xs text-fg-subtle">{code}</p> : null}
      {hint ? <p className="text-sm text-fg-muted">{hint}</p> : null}
    </div>
  );
}

/** The admin key the setup claim created: shown this once. */
function CreatedKey({ value }: { value: string }) {
  return (
    <div className="space-y-2 rounded-md border border-border-strong px-3 py-3">
      <p className="text-sm font-medium text-fg">Your admin key (shown once)</p>
      <div className="flex items-center gap-2">
        <code className="min-w-0 flex-1 truncate rounded-sm bg-surface-sunken px-2 py-1 font-mono text-xs text-fg">{value}</code>
        <CopyButton value={value} label="Copy the admin key" showLabel />
      </div>
      <p className="text-xs text-fg-muted">
        Store it in your password manager: it signs you in again after the session ends, and the proxy keeps only its hash.
      </p>
    </div>
  );
}

function SignedIn({
  session,
  onSignOut,
  signingOut,
  forThisTab,
}: {
  session: SessionInfo;
  onSignOut: () => void;
  signingOut: boolean;
  forThisTab: boolean;
}) {
  const admin = session.scopes.includes('admin');
  return (
    <div className="space-y-3">
      <p className="flex flex-wrap items-center gap-2 text-sm text-fg">
        <ShieldCheck aria-hidden="true" className="size-4 text-fg-muted" />
        {forThisTab ? 'Signed in for this tab as' : 'Signed in as'} <span className="font-medium">{session.actor}</span>
        <span className="text-fg-muted">({session.scopes.map((s) => secScopeView(s).label).join(', ')})</span>
      </p>
      {forThisTab ? (
        <p className="text-sm text-fg-muted">
          The proxy is on another origin, so this tab keeps the key and sends it with each request. Closing the tab or signing out forgets it.
        </p>
      ) : null}
      <p className="text-sm text-fg-muted">
        {admin
          ? 'Admin actions are available: edit policies, run the Playground and manage keys.'
          : 'This session can read the dashboard; policy edits and Playground runs need an admin key.'}
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <Link to="/app/overview" className="inline-flex items-center gap-1 text-sm font-medium text-fg underline underline-offset-2">
          Open Overview
          <ArrowRight aria-hidden="true" className="size-3.5" />
        </Link>
        {admin ? (
          <Link to="/app/policies" className="inline-flex items-center gap-1 text-sm font-medium text-fg underline underline-offset-2">
            Open Policies
            <ArrowRight aria-hidden="true" className="size-3.5" />
          </Link>
        ) : null}
        <Button size="sm" variant="ghost" onClick={onSignOut} disabled={signingOut}>
          <LogOut aria-hidden="true" />
          Sign out
        </Button>
      </div>
    </div>
  );
}

export function SetupPage() {
  const ds = useDataSource();
  // Read once: the token leaves the address bar on the first render.
  const [token] = useState(takeSetupToken);
  const [key, setKey] = useState('');
  const setup = useQuery(setupStatusQuery(ds));
  const { crossOrigin, session, signedIn, signIn, signOut, createdKey, cookieLost } = useDashboardAuth();

  const setupRequired = setup.data?.setup_required === true;
  const readonly = setup.data?.readonly === true;
  const claiming = signIn.variables !== undefined && 'setup_token' in signIn.variables;

  const onSubmitKey = (e: FormEvent) => {
    e.preventDefault();
    if (key.trim() !== '') signIn.mutate({ key: key.trim() }, { onSuccess: () => setKey('') });
  };

  let body;
  if (setup.isPending || session.isPending) {
    body = (
      <div aria-busy="true" className="space-y-3">
        <Skeleton className="h-5 w-2/3" />
        <Skeleton className="h-8 w-full" />
      </div>
    );
  } else if (signedIn) {
    body = (
      <div className="space-y-4">
        <SignedIn session={signedIn} onSignOut={() => signOut.mutate()} signingOut={signOut.isPending} forThisTab={crossOrigin} />
        {signOut.isError ? <Failure error={signOut.error} claiming={false} /> : null}
      </div>
    );
  } else {
    body = (
      <div className="space-y-5">
        {token ? (
          <section aria-labelledby="claim-title" className="space-y-2">
            <h3 id="claim-title" className="text-sm font-semibold text-fg">
              Create the admin key
            </h3>
            <p className="text-sm text-fg-muted">
              {setupRequired
                ? 'This one-time link creates the first admin key for this TrustAgent instance and signs this browser in.'
                : 'This instance already has an admin. A link made with -force can still create another admin key.'}
            </p>
            <DisabledAction reason={readonly ? 'readonly' : null}>
              {(p) => (
                <Button variant="primary" onClick={() => signIn.mutate({ setup_token: token })} {...p} disabled={p.disabled || signIn.isPending}>
                  <ShieldCheck aria-hidden="true" />
                  Create the admin key
                </Button>
              )}
            </DisabledAction>
          </section>
        ) : setupRequired ? (
          <section aria-labelledby="setup-title" className="space-y-2">
            <h3 id="setup-title" className="text-sm font-semibold text-fg">
              No admin yet
            </h3>
            <p className="text-sm text-fg-muted">
              Open the one-time link the proxy printed at start (a line starting with “SETUP: open”), or print a new one:
            </p>
            <CodeBlock label="Print a new setup link" code={setup.data?.recovery_command || (commandText('setup_url') ?? 'make setup-url')} />
          </section>
        ) : null}
        <form onSubmit={onSubmitKey} className="space-y-2" aria-labelledby="key-title">
          <h3 id="key-title" className="text-sm font-semibold text-fg">
            Sign in with a key
          </h3>
          <Field label="Key" description="An admin key (or a read key) from TRUTHTABLE_ADMIN_KEYS, TRUTHTABLE_READ_KEYS or a key created here.">
            <Input type="password" autoComplete="current-password" value={key} onChange={(e) => setKey(e.target.value)} />
          </Field>
          <DisabledAction reason={key.trim() === '' ? { text: 'Enter a key first' } : null}>
            {(p) => (
              <Button type="submit" {...p} disabled={p.disabled || signIn.isPending}>
                <KeyRound aria-hidden="true" />
                Sign in
              </Button>
            )}
          </DisabledAction>
        </form>
      </div>
    );
  }

  return (
    <div className="max-w-2xl">
      <PageHeader description="Sign this browser in to TrustAgent, or create the first admin key with the one-time setup link." />
      <Card>
        <CardHeader level={2} title={signedIn ? 'Signed in' : 'Sign in'} />
        <CardContent className="space-y-4">
          {createdKey ? <CreatedKey value={createdKey} /> : null}
          {cookieLost ? (
            <DegradedState
              title="This browser did not keep the session"
              reason="The proxy accepted the sign-in, but the browser did not keep its session cookie (cookies are blocked for this site, or a secure cookie on an http address)."
              stillWorks="Keys still work for API calls: send one in X-TrustAgent-Key."
              action={<p className="text-sm text-fg-muted">Allow cookies for this site, or open the dashboard at its public URL, then sign in again.</p>}
            />
          ) : null}
          {signIn.isError ? <Failure error={signIn.error} claiming={claiming} /> : null}
          {setup.isError && !isApiError(setup.error) ? <Failure error={setup.error} claiming={false} /> : null}
          {body}
        </CardContent>
      </Card>
      <p className="mt-3 text-xs text-fg-muted">
        Connections, budgets and the rest of the first-run wizard arrive in P4. Keys and sessions never leave this proxy.
      </p>
    </div>
  );
}
