/**
 * Connect OpenRouter, the browser's half (SPEC §7.5 PKCE, self-hosted; P4-PLAN D38):
 *
 * 1. POST /api/connect/openrouter/start (#58): the proxy keeps the PKCE verifier and answers the
 *    OpenRouter `auth_url` and a nonce.
 * 2. The nonce goes to `sessionStorage["ta.connect.pending"]` (this tab only), and the browser goes
 *    to `auth_url` with `window.location.assign`: a link, not a form, because the dashboard's CSP
 *    allows forms to post only to itself (`form-action 'self'`).
 * 3. OpenRouter sends the browser back to /connect/callback?code=…, which reads and clears the
 *    nonce and calls complete (#59). The key itself never reaches browser JavaScript.
 */
import { useMutation } from '@tanstack/react-query';
import { useDataSource } from '@/app/services';
import { ApiError } from '@/data/errors';

export const CONNECT_PENDING_KEY = 'ta.connect.pending';

/** Where the browser goes next; tests replace `assign`. */
export const browserNavigation = {
  assign(url: string): void {
    window.location.assign(url);
  },
};

function storage(): Storage | null {
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

/** Keeps the nonce of a started Connect for the callback page; false when the browser refuses. */
export function savePendingConnect(nonce: string): boolean {
  try {
    const s = storage();
    if (!s) return false;
    s.setItem(CONNECT_PENDING_KEY, JSON.stringify({ nonce }));
    return true;
  } catch {
    return false;
  }
}

/** Reads and forgets the pending nonce (the callback uses it once). */
export function takePendingConnect(): string | null {
  try {
    const s = storage();
    const raw = s?.getItem(CONNECT_PENDING_KEY) ?? null;
    s?.removeItem(CONNECT_PENDING_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as unknown;
    const nonce = typeof parsed === 'object' && parsed !== null ? (parsed as { nonce?: unknown }).nonce : undefined;
    return typeof nonce === 'string' && nonce !== '' ? nonce : null;
  } catch {
    return null;
  }
}

/**
 * Starts Connect OpenRouter and leaves for OpenRouter. `returnTo` is the dashboard path to come back
 * to (under /app/); the proxy checks it and echoes it from complete.
 */
export function useStartConnect(returnTo: string) {
  const ds = useDataSource();
  return useMutation({
    mutationFn: async () => {
      const start = await ds.startConnect({ return_to: returnTo });
      if (!savePendingConnect(start.nonce)) {
        throw new ApiError({
          kind: 'http',
          status: 0,
          code: 'storage_blocked',
          message: 'This browser does not let the dashboard remember the sign-in for this tab (site data is blocked). Allow it for this site, or paste a key instead.',
        });
      }
      browserNavigation.assign(start.auth_url);
      return start;
    },
  });
}
