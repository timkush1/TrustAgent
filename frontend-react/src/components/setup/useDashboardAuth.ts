/**
 * Who this browser is signed in as, and signing in and out (SPEC §2.4 #3-#6, §7.5; D71).
 *
 * - Same origin: POST /api/session sets the HttpOnly session cookie; sign-out deletes it.
 * - Cross origin (the dashboard on another origin than the proxy): cookies are never sent, so the
 *   key is checked with GET /api/session and kept for this tab only (http.ts `storeDashboardKey`,
 *   sessionStorage). Claiming the setup link keeps the admin key it created the same way. Signing
 *   out forgets the key; closing the tab does too.
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { useDataSource, useRuntimeConfig } from '@/app/services';
import type { SessionRequest } from '@/data/controlPlane';
import { ApiError, isApiError } from '@/data/errors';
import { clearDashboardKey, readDashboardKey, storeDashboardKey } from '@/data/http';
import { sessionQuery } from '@/data/queries';
import type { SessionInfo } from '@/types/api';

function storageRefused(): ApiError {
  return new ApiError({
    kind: 'http',
    status: 0,
    code: 'storage_blocked',
    message: 'This browser does not let the dashboard keep the key for this tab (site data is blocked). Allow it for this site, or serve the dashboard and the proxy on one origin.',
  });
}

function needsRead(): ApiError {
  return new ApiError({
    kind: 'http',
    status: 403,
    code: 'forbidden',
    message: 'This key cannot open the dashboard: it does not have the read scope.',
  });
}

export function useDashboardAuth() {
  const ds = useDataSource();
  const qc = useQueryClient();
  const { crossOrigin } = useRuntimeConfig();
  const session = useQuery(sessionQuery(ds));
  const [createdKey, setCreatedKey] = useState<string | null>(null);

  // A cross-origin key the proxy no longer accepts (revoked, or another stack) is forgotten.
  const rejected = session.isError && isApiError(session.error) && session.error.status === 401;
  useEffect(() => {
    if (crossOrigin && rejected && readDashboardKey() !== null) clearDashboardKey();
  }, [crossOrigin, rejected]);

  const signIn = useMutation({
    mutationFn: async (request: SessionRequest): Promise<SessionInfo> => {
      if (crossOrigin && request.key !== undefined) {
        const previous = readDashboardKey();
        if (!storeDashboardKey(request.key)) throw storageRefused();
        try {
          const info = await ds.getSession();
          if (!info.scopes.includes('read')) throw needsRead();
          return info;
        } catch (err) {
          if (previous) storeDashboardKey(previous);
          else clearDashboardKey();
          throw err;
        }
      }
      const info = await ds.createSession(request);
      if (crossOrigin && info.created_admin_key && !storeDashboardKey(info.created_admin_key)) {
        // The claim worked and the key is on screen; this tab just cannot keep it.
        setCreatedKey(info.created_admin_key);
        throw storageRefused();
      }
      return info;
    },
    onSuccess: async (info) => {
      if (info.created_admin_key) setCreatedKey(info.created_admin_key);
      // Scopes changed: every page re-reads what this browser may see and do, and the session is
      // read back to confirm the browser kept it.
      await qc.invalidateQueries();
    },
  });

  const signOut = useMutation({
    mutationFn: async () => {
      if (crossOrigin) clearDashboardKey();
      else await ds.deleteSession();
    },
    onSuccess: async () => {
      setCreatedKey(null);
      qc.removeQueries({ queryKey: sessionQuery(ds).queryKey });
      await qc.invalidateQueries();
    },
  });

  // A failed read (401 after the session ended) means signed out, whatever was read before.
  const signedIn = session.isError ? null : (session.data ?? null);
  // Same origin only: the proxy accepted the sign-in but the next read has no session, so the
  // browser did not keep the cookie (cookies blocked for this site, say).
  const cookieLost = !crossOrigin && signIn.isSuccess && !signedIn && session.isError && !session.isFetching;

  return { crossOrigin, session, signedIn, signIn, signOut, createdKey, cookieLost };
}
