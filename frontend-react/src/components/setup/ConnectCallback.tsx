/**
 * /connect/callback: where OpenRouter sends the browser back after Connect OpenRouter (SPEC §7.5;
 * P4-PLAN ON-7e, D38). In order:
 *
 * 1. read `code` (the route passes it);
 * 2. take it out of the address bar (`history.replaceState`), so it never lands in history or a
 *    screenshot;
 * 3. read and forget the pending nonce this tab kept when Connect started;
 * 4. POST /api/connect/openrouter/complete (#59): the proxy exchanges the code for a key, stores it
 *    encrypted, checks it and binds it;
 * 5. hand the outcome ("Ready · …", or no credits) to the page it returns to;
 * 6. go to the server's `return_to`, through the router, and only when it is a dashboard path.
 *
 * Failures stay here with what to do: no sign-in waiting in this tab (open the dashboard at its
 * public URL), an expired or used sign-in, or OpenRouter refusing.
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { ArrowRight, CircleAlert } from 'lucide-react';
import { useEffect, useRef } from 'react';
import { useDataSource } from '@/app/services';
import { describeError } from '@/components/common/describeError';
import { Spinner } from '@/components/ui/spinner';
import { CredentialResult } from '@/components/settings/CredentialResult';
import { simulatedOn } from '@/components/settings/credentialModel';
import { ApiError, isApiError } from '@/data/errors';
import { setupStatusQuery } from '@/data/queries';
import { qkcSecurity } from '@/data/queries/security';
import { copy } from '@/domain/presentation';
import { callbackMissingText, safeReturnPath, takePendingConnect, type ConnectOutcome } from './connectFlow';

const MISSING = 'connect_missing';

function missingNonce(): ApiError {
  return new ApiError({ kind: 'http', status: 0, code: MISSING, message: 'No Connect OpenRouter is waiting in this tab.' });
}

function Failure({ error, publicUrl }: { error: unknown; publicUrl: string | null }) {
  const code = isApiError(error) ? error.code : null;
  let headline: string;
  if (code === MISSING) headline = callbackMissingText(publicUrl);
  else if (code === 'invalid_nonce') headline = copy.setup.step_connect.callback_expired;
  else if (code === 'openrouter_refused') headline = 'OpenRouter did not finish the sign-in, so nothing was connected.';
  else headline = 'The OpenRouter sign-in could not be finished.';
  const { message, code: detail } = describeError(error);
  return (
    <div role="alert" className="space-y-2 rounded-lg border border-border bg-surface px-4 py-4">
      <p className="flex items-start gap-2 text-sm font-medium text-fg">
        <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-tone-danger" />
        {headline}
      </p>
      {code === MISSING || code === 'invalid_nonce' || code === 'openrouter_refused' ? null : <p className="pl-6 text-sm text-fg-muted">{message}</p>}
      {detail ? <p className="pl-6 font-mono text-2xs text-fg-subtle">{detail}</p> : null}
      <div className="flex flex-wrap gap-4 pl-6">
        <Link to="/app/setup" search={{ step: 'connect' }} className="inline-flex items-center gap-1 text-sm font-medium text-fg underline underline-offset-2">
          Connect again
          <ArrowRight aria-hidden="true" className="size-3.5" />
        </Link>
        <Link to="/app/settings/$section" params={{ section: 'connections' }} className="inline-flex items-center gap-1 text-sm font-medium text-fg underline underline-offset-2">
          Paste a key instead
          <ArrowRight aria-hidden="true" className="size-3.5" />
        </Link>
      </div>
    </div>
  );
}

export interface ConnectCallbackProps {
  code?: string;
  /** OpenRouter's own refusal (`?error=`), when the viewer declined. */
  error?: string;
}

export function ConnectCallback({ code, error }: ConnectCallbackProps) {
  const ds = useDataSource();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const setup = useQuery(setupStatusQuery(ds));
  const complete = useMutation({
    // The query string arrives as variables, read when the page mounted: taking `code` out of the
    // address bar re-renders this page without it (the router follows history.replaceState).
    mutationFn: async (callback: ConnectCallbackProps): Promise<ConnectOutcome & { returnTo: string | null }> => {
      const nonce = takePendingConnect();
      if (callback.error) throw new ApiError({ kind: 'http', status: 0, code: 'openrouter_refused', message: callback.error });
      if (!nonce || !callback.code) throw missingNonce();
      // Whether simulated checks were on, so the next page can say they are now off (D76).
      const before = await ds.getSettings().catch(() => null);
      const result = await ds.completeConnect({ code: callback.code, nonce });
      return { result, wasSimulated: simulatedOn(before ?? undefined), returnTo: safeReturnPath(result.return_to) };
    },
    onSuccess: ({ result, wasSimulated, returnTo }) => {
      qc.setQueryData<ConnectOutcome>(qkcSecurity.connectResult, { result, wasSimulated });
      void qc.invalidateQueries({ queryKey: qkcSecurity.all });
      if (returnTo) void navigate({ href: returnTo, replace: true });
    },
  });

  // Once per page (StrictMode mounts twice): the nonce is good for one call only.
  const started = useRef(false);
  const { mutate } = complete;
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    const callback = { code, error };
    window.history.replaceState(window.history.state, '', window.location.pathname);
    mutate(callback);
  }, [mutate, code, error]);

  const publicUrl = setup.data?.public_url ?? null;
  return (
    <main className="mx-auto max-w-xl space-y-4 px-4 py-10">
      <h1 className="text-xl font-semibold text-fg">{copy.setup.step_connect.title}</h1>
      {complete.isError ? (
        <Failure error={complete.error} publicUrl={publicUrl} />
      ) : complete.data ? (
        <div className="space-y-3">
          <CredentialResult result={complete.data.result} wasSimulated={complete.data.wasSimulated} />
          <Link to="/app/setup" search={{ step: 'connect' }} className="inline-flex items-center gap-1 text-sm font-medium text-fg underline underline-offset-2">
            Continue setup
            <ArrowRight aria-hidden="true" className="size-3.5" />
          </Link>
        </div>
      ) : (
        <p role="status" className="flex items-center gap-2 text-sm text-fg-muted">
          <Spinner size="sm" />
          {copy.setup.step_connect.callback_working}
        </p>
      )}
    </main>
  );
}
