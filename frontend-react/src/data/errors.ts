/**
 * One error type for every REST failure (SPEC §2.1 "Errors", §2.4 error codes).
 *
 * The server's body is `{"error":{code,message,field_errors?,details?,retry_after_ms?},"trace_id"?}`.
 * Legacy handlers still answer `{"error":"message"}`; both parse. Network failures, timeouts and
 * aborts get synthetic codes so the UI can tell them apart without inspecting exceptions.
 */
import type { ApiErrorBody, FieldError } from '@/types/api';

export type ApiErrorKind = 'http' | 'network' | 'timeout' | 'aborted' | 'invalid_response';

export interface ApiErrorInit {
  kind: ApiErrorKind;
  status: number;
  code: string;
  message: string;
  fieldErrors?: FieldError[];
  details?: Record<string, unknown>;
  retryAfterMs?: number | null;
  traceId?: string | null;
  cause?: unknown;
}

export class ApiError extends Error {
  readonly kind: ApiErrorKind;
  /** HTTP status; 0 when no response arrived. */
  readonly status: number;
  /** Server code (`not_found`, `readonly`, …) or `network` / `timeout` / `aborted` / `invalid_response`. */
  readonly code: string;
  readonly fieldErrors: FieldError[];
  readonly details: Record<string, unknown>;
  readonly retryAfterMs: number | null;
  readonly traceId: string | null;

  constructor(init: ApiErrorInit) {
    super(init.message, init.cause === undefined ? undefined : { cause: init.cause });
    this.name = 'ApiError';
    this.kind = init.kind;
    this.status = init.status;
    this.code = init.code;
    this.fieldErrors = init.fieldErrors ?? [];
    this.details = init.details ?? {};
    this.retryAfterMs = init.retryAfterMs ?? null;
    this.traceId = init.traceId ?? null;
  }

  /** Worth retrying automatically: network trouble, timeouts, 429 and 5xx other than 501. */
  get transient(): boolean {
    if (this.kind === 'network' || this.kind === 'timeout') return true;
    return this.status === 429 || (this.status >= 500 && this.status !== 501);
  }
}

export function isApiError(err: unknown): err is ApiError {
  return err instanceof ApiError;
}

const statusCodes: Record<number, string> = {
  400: 'bad_request',
  401: 'unauthorized',
  403: 'forbidden',
  404: 'not_found',
  409: 'conflict',
  412: 'stale_version',
  413: 'payload_too_large',
  422: 'validation_failed',
  423: 'locked',
  428: 'precondition_required',
  429: 'rate_limited',
  500: 'internal',
  502: 'upstream_error',
  503: 'unavailable',
  504: 'gateway_timeout',
};

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/** Parses `Retry-After` (seconds or an HTTP date) into milliseconds. */
export function parseRetryAfter(header: string | null, now = Date.now()): number | null {
  if (!header) return null;
  const secs = Number(header);
  if (Number.isFinite(secs) && secs >= 0) return Math.round(secs * 1000);
  const at = Date.parse(header);
  return Number.isNaN(at) ? null : Math.max(0, at - now);
}

/** Builds an ApiError from an HTTP status, its parsed body (if any) and the Retry-After header. */
export function apiErrorFromResponse(
  status: number,
  body: unknown,
  retryAfter: string | null = null,
): ApiError {
  const fallbackCode = statusCodes[status] ?? (status >= 500 ? 'internal' : 'http_error');
  const fallbackMessage = `The server answered HTTP ${status}.`;
  const headerRetry = parseRetryAfter(retryAfter);

  if (isRecord(body)) {
    const err = body.error;
    const traceId = typeof body.trace_id === 'string' ? body.trace_id : null;
    if (isRecord(err)) {
      const e = err as Partial<ApiErrorBody['error']>;
      return new ApiError({
        kind: 'http',
        status,
        code: typeof e.code === 'string' && e.code ? e.code : fallbackCode,
        message: typeof e.message === 'string' && e.message ? e.message : fallbackMessage,
        fieldErrors: Array.isArray(e.field_errors) ? e.field_errors : [],
        details: isRecord(e.details) ? e.details : {},
        retryAfterMs: typeof e.retry_after_ms === 'number' ? e.retry_after_ms : headerRetry,
        traceId,
      });
    }
    if (typeof err === 'string' && err) {
      // Legacy `{"error": "message"}`.
      return new ApiError({ kind: 'http', status, code: fallbackCode, message: err, retryAfterMs: headerRetry, traceId });
    }
  }
  return new ApiError({ kind: 'http', status, code: fallbackCode, message: fallbackMessage, retryAfterMs: headerRetry });
}

/** Wraps anything thrown by fetch (or a caller) as an ApiError. */
export function toApiError(err: unknown, timedOut = false): ApiError {
  if (err instanceof ApiError) return err;
  if (timedOut || (err instanceof DOMException && err.name === 'TimeoutError')) {
    return new ApiError({ kind: 'timeout', status: 0, code: 'timeout', message: 'The server took too long to answer.', cause: err });
  }
  if (err instanceof DOMException && err.name === 'AbortError') {
    return new ApiError({ kind: 'aborted', status: 0, code: 'aborted', message: 'The request was cancelled.', cause: err });
  }
  return new ApiError({
    kind: 'network',
    status: 0,
    code: 'network',
    message: "Couldn't reach the TrustAgent proxy. Check that it is running.",
    cause: err,
  });
}

/** One line for an error state: the server's own message, plus its code when it adds something. */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error && err.message) return err.message;
  return 'Something went wrong.';
}
