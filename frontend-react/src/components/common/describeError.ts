/**
 * The words to show for a failure: the server's own message (SPEC §2.1 error body) and, for an
 * HTTP error, its status and code for support. Anything that is not an Error is stringified.
 */
import { isApiError } from '@/data/errors';

export interface ErrorDescription {
  message: string;
  /**
   * "HTTP 409 · stale_version", or null when there was no HTTP response: a failure the page itself
   * raised (status 0, such as blocked site data) has no status worth quoting.
   */
  code: string | null;
}

export function describeError(error: unknown): ErrorDescription {
  const message = isApiError(error) || error instanceof Error ? error.message : String(error);
  const code = isApiError(error) && error.kind === 'http' && error.status > 0 ? `HTTP ${error.status} · ${error.code}` : null;
  return { message: message || 'Something went wrong', code };
}
