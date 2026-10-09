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

// ------------------------------------------------------------- settings, credentials, costs and Studio codes

/**
 * The error codes the settings, credentials, cost, label, calibration and Studio endpoints answer with (SPEC §2.4 "Error codes"), each with its
 * HTTP status and a plain hint on what to do. Pages show the server's message first and may add the
 * hint; anything with a name or a route in it (`details.names`, `details.routes`) gets its words
 * from contracts/ui through the domain words modules. `not_implemented` is the Step-0b stub answer.
 */
export const CONTROL_ERROR_CODES = {
  conflict: { status: 409, hint: 'Something else changed this first. Reload and try again.' },
  env_managed: { status: 409, hint: 'This is set by an environment variable. Change it in .env and restart.' },
  ingest_key_required: { status: 409, hint: 'Create an ingest API key first, so only your apps can use the upstream key.' },
  confirmation_required: { status: 409, hint: 'This change needs a confirmation. Review what it affects, then confirm.' },
  body_expired: { status: 409, hint: "This answer's stored text has expired." },
  file_managed: { status: 409, hint: 'This comes from a file on the server. Change the file instead.' },
  invalid_nonce: { status: 400, hint: 'This sign-in link expired or was already used. Start again from the dashboard.' },
  stale_version: { status: 412, hint: 'This changed in another tab or session. Reload to see the latest, then try again.' },
  precondition_required: { status: 428, hint: 'Reload the page and try again.' },
  budget_exhausted: { status: 503, hint: "Today's budget is used up. Raise the daily cap in Settings → Budgets, or wait until tomorrow." },
  credentials_unavailable: { status: 503, hint: 'Keys cannot be saved or used here yet. Set TRUTHTABLE_SECRET_KEY and restart.' },
  decision_model_unavailable: { status: 503, hint: 'No fast-check model is connected. Connect one in Settings → Connections.' },
  engine_unavailable: { status: 503, hint: 'The audit engine is not reachable. Check that it is running.' },
  persistence_disabled: { status: 503, hint: 'This needs a database. Start the stack with Postgres to save changes.' },
  no_judge: { status: 503, hint: 'No deep-audit judge is set. Choose one in Settings → Models.' },
  not_implemented: { status: 404, hint: 'This dashboard build cannot do this yet.' },
} as const satisfies Record<string, { status: number; hint: string }>;

export type ControlErrorCode = keyof typeof CONTROL_ERROR_CODES;

/** Field-error codes inside a 422 `validation_failed` (FieldError.code; D37, D41). */
export const CONTROL_FIELD_ERROR_CODES = {
  reenter_secret: 'The address changed, so paste the key again to send it there.',
  fallback_key_required: 'A fallback on another host needs its own upstream key.',
} as const satisfies Record<string, string>;

export type ControlFieldErrorCode = keyof typeof CONTROL_FIELD_ERROR_CODES;

export function isControlErrorCode(code: string): code is ControlErrorCode {
  return Object.hasOwn(CONTROL_ERROR_CODES, code);
}

/** The plain hint for an error's code, or null when the code is not one of them. */
export function errorCodeHint(err: unknown): string | null {
  return isApiError(err) && isControlErrorCode(err.code) ? CONTROL_ERROR_CODES[err.code].hint : null;
}
