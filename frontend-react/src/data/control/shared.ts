/**
 * Helpers shared by the control planes (securityPlane, costPlane, evaluatorPlane, studioPlane).
 * Lead-owned like controlPlane.ts: a plane file imports these, it never edits them.
 */
import { ApiError } from '../errors';

/** The message of a plane method whose body has not landed yet (the static build's unavailable plane). */
export const NOT_IMPLEMENTED_MESSAGE = 'This dashboard build does not call this endpoint yet.';

/** 404 `not_implemented`: a plane method that is declared but not wired, or a build that never serves it. */
export function notImplemented(message: string = NOT_IMPLEMENTED_MESSAGE): ApiError {
  return new ApiError({ kind: 'http', status: 404, code: 'not_implemented', message });
}

/** A method body that rejects with `notImplemented(message)`; the arguments are ignored. */
export function rejectNotImplemented(message?: string): () => Promise<never> {
  return () => Promise.reject(notImplemented(message));
}

/** One path segment (an id or a name), URL-encoded. */
export function enc(segment: string): string {
  return encodeURIComponent(segment);
}
