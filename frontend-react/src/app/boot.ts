/**
 * Boot helpers for main.tsx: the static PKCE callback page (D105) boots without the recording and
 * without the stream, so nothing reaches the network before the page has taken the code out of
 * the address bar.
 */
import { unavailableControlPlane } from '@/data/controlPlane';
import { ApiError } from '@/data/errors';
import type { DataSource } from '@/data/source';

/** `${basePath}connect/callback/` (with or without the slash or index.html). */
export function isStaticCallbackPath(pathname: string, basePath: string): boolean {
  const base = basePath.endsWith('/') ? basePath : `${basePath}/`;
  if (!pathname.startsWith(base)) return false;
  return /^connect\/callback(\/(index\.html)?)?$/.test(pathname.slice(base.length));
}

/** A DataSource that reads nothing and streams nothing (the static callback page). */
export function idleDataSource(message = 'This page does not load the demo recording.'): DataSource {
  const reject = () => Promise.reject(new ApiError({ kind: 'http', status: 404, code: 'not_found', message }));
  return {
    ...unavailableControlPlane(message),
    kind: 'replay',
    getHealth: reject,
    getStatus: reject,
    listTraces: reject,
    getTrace: reject,
    listDecisions: reject,
    listAudits: reject,
    getAudit: reject,
    subscribe: () => ({ retryNow: () => {}, stop: () => {} }),
  };
}
