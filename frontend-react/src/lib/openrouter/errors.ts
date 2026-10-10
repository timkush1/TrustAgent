/**
 * OpenRouter failures in the browser (D100, D136): backend-go internal/jev/classify.go ported to
 * ApiError (data/errors.ts) with the jev error kinds as `code`, and the map from each failure to
 * what the live panel says and offers (copy.playground_live.*, never the vendor's text or the
 * gateway's banners, which name env vars and settings a visitor does not have).
 *
 * - `classifyStatus` is classify.go `classifyStatus`: 401/403 auth (a moderation 403 is the input's
 *   fault, bad_request), 402 credits (transient when OpenRouter's in-flight budget sent
 *   Retry-After), 404 data_policy or not_found, 413 and a size 400 too_large, 408/429/5xx
 *   retryable, other 4xx bad_request.
 * - `checkContentType` is classify.go `checkContentType`: a 2xx must be JSON (`application/json`,
 *   `application/*+json`, or no Content-Type with a JSON object body).
 * - A vendor message is scrubbed (secret patterns and the key itself) and kept on the error for
 *   debugging only; nothing renders it.
 * - `diagnoseOpaqueFailure`: when error bodies are not readable from the browser
 *   (ERRORS_READABLE=false), a request the browser blocked is explained by one free key check.
 *
 * Owned by WEB-LIVE (LV-2).
 */
import { ApiError, parseRetryAfter, type ApiErrorKind } from '@/data/errors';
import { ERRORS_READABLE } from '@/domain/liveScenarios';
import { OPENROUTER_ERROR_KINDS, type CallUsage, type LedgerRefusal, type OpenRouterErrorKind } from './types';

/** OpenRouter's metadata.limit_source of a transient 402 (classify.go LimitSourceInFlight). */
export const LIMIT_SOURCE_IN_FLIGHT = 'openrouter_in_flight_budget';

/** What an OpenRouter ApiError carries in `details`. */
export interface OpenRouterErrorDetails {
  /** The failure may pass on a retry (Retry is offered; nothing retries by itself). */
  retryable: boolean;
  /** OpenRouter's moderation refused the input (a 403 that is not about the key). */
  moderation: boolean;
  /** A 402 from the key's in-flight budget rather than the account balance. */
  in_flight: boolean;
  /** The usage an error response reported (it then counts as billable, D101). */
  usage?: CallUsage;
  /** The kind was found by the free key check after an unreadable failure. */
  diagnosed?: boolean;
}

const API_KIND: Partial<Record<OpenRouterErrorKind, ApiErrorKind>> = {
  timeout: 'timeout',
  network: 'network',
  aborted: 'aborted',
  bad_response: 'invalid_response',
  bad_content_type: 'invalid_response',
};

/** An ApiError for an OpenRouter failure, coded with its kind. */
export function openRouterError(
  kind: OpenRouterErrorKind,
  init: { status: number; message: string; details?: Partial<OpenRouterErrorDetails>; retryAfterMs?: number | null; cause?: unknown },
): ApiError {
  const details: OpenRouterErrorDetails = { retryable: false, moderation: false, in_flight: false, ...init.details };
  return new ApiError({
    kind: API_KIND[kind] ?? 'http',
    status: init.status,
    code: kind,
    message: init.message,
    details: details as unknown as Record<string, unknown>,
    retryAfterMs: init.retryAfterMs ?? null,
    cause: init.cause,
  });
}

/** A request that got no answer: offline, CORS or an extension (`network`), its time limit, or a cancel. */
export function transportError(kind: 'network' | 'timeout' | 'aborted', status: number, cause?: unknown): ApiError {
  const message = {
    network: 'the browser could not complete the request to openrouter.ai',
    timeout: 'openrouter.ai did not answer in time',
    aborted: 'the request was cancelled',
  }[kind];
  return openRouterError(kind, { status, message, details: { retryable: kind !== 'aborted' }, cause });
}

/** The OpenRouter error kind of anything a live call threw. */
export function errorKind(err: unknown): OpenRouterErrorKind {
  if (err instanceof ApiError) {
    if ((OPENROUTER_ERROR_KINDS as readonly string[]).includes(err.code)) return err.code as OpenRouterErrorKind;
    if (err.kind === 'network' || err.kind === 'timeout' || err.kind === 'aborted') return err.kind;
    return 'bad_response';
  }
  if (err instanceof DOMException && err.name === 'AbortError') return 'aborted';
  if (err instanceof DOMException && err.name === 'TimeoutError') return 'timeout';
  if (err instanceof TypeError) return 'network';
  return 'bad_response';
}

/** The details of an OpenRouter ApiError (defaults for anything else). */
export function errorDetails(err: unknown): OpenRouterErrorDetails {
  const d = err instanceof ApiError ? (err.details as Partial<OpenRouterErrorDetails>) : {};
  return { retryable: d.retryable === true, moderation: d.moderation === true, in_flight: d.in_flight === true, usage: d.usage, diagnosed: d.diagnosed };
}

// ------------------------------------------------------------------------------------ envelopes

/** What the vendor error envelopes have in common (classify.go `envelope`). */
export interface Envelope {
  message: string;
  errorType: string;
  limitSource: string;
  moderation: boolean;
}

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);
const str = (v: unknown): string => (typeof v === 'string' ? v : '');
const firstNonEmpty = (a: string, b: string): string => (a !== '' ? a : b);

/** Secret patterns a vendor might echo (SPEC §7.5 redaction; classify.go secretPattern). */
const SECRET_PATTERN = /(sk-[A-Za-z0-9_-]{8,}|Bearer\s+\S+|x-api-key:\s*\S+)/gi;
const MAX_MESSAGE = 300;

/** Masks secrets (and the key itself) and bounds the length of a vendor message. */
export function scrub(message: string, secret?: string | null): string {
  let s = message;
  if (secret && secret.length >= 8) s = s.split(secret).join('[redacted]');
  s = s.replace(SECRET_PATTERN, '[redacted]');
  const chars = [...s];
  return chars.length > MAX_MESSAGE ? `${chars.slice(0, MAX_MESSAGE).join('')}…` : s;
}

const firstLine = (s: string): string => s.trim().split(/\r?\n|\r/)[0] ?? '';

/**
 * Reads the vendor error envelopes (classify.go parseEnvelope):
 * OpenRouter `{"error":{"code","message","metadata":{…}}}`, OpenAI-style `{"error":{"message","type"}}`
 * or `{"error":"…"}`, TypeSafe `{"detail":[{loc,msg}]}`, `{"detail":{error_type,message}}` or
 * `{"detail":"…"}`, Vercel `{"message","error_type"}`.
 */
export function parseEnvelope(text: string, secret?: string | null): Envelope {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    return { message: scrub(firstLine(text), secret), errorType: '', limitSource: '', moderation: false };
  }
  const env: Envelope = { message: '', errorType: '', limitSource: '', moderation: false };
  if (!isObj(raw)) return { ...env, message: scrub(firstLine(text), secret) };
  env.message = str(raw.message);
  env.errorType = str(raw.error_type);
  const error = raw.error;
  if (isObj(error)) {
    env.message = firstNonEmpty(str(error.message), env.message);
    env.errorType = firstNonEmpty(str(error.type), env.errorType);
    const meta = isObj(error.metadata) ? error.metadata : {};
    env.limitSource = str(meta.limit_source);
    env.moderation = 'flagged_input' in meta || 'reasons' in meta;
    const detail = str(meta.raw);
    if (detail !== '' && env.message !== '' && !env.message.includes(detail)) env.message += `: ${detail}`;
  } else if (typeof error === 'string') {
    env.message = firstNonEmpty(error, env.message);
  }
  const detail = raw.detail;
  if (Array.isArray(detail)) {
    const parts = detail.map((d) => {
      const o = isObj(d) ? d : {};
      const loc = Array.isArray(o.loc) ? o.loc.map((l) => (typeof l === 'string' ? l : JSON.stringify(l))) : [];
      const msg = str(o.msg);
      return loc.length > 0 ? `${loc.join('.')}: ${msg}` : msg;
    });
    env.message = firstNonEmpty(parts.join('; '), env.message);
  } else if (isObj(detail)) {
    env.message = firstNonEmpty(str(detail.message), env.message);
    env.errorType = firstNonEmpty(str(detail.error_type), env.errorType);
  } else if (typeof detail === 'string') {
    env.message = firstNonEmpty(detail, env.message);
  }
  if (env.message === '' && env.errorType !== '') env.message = env.errorType;
  env.message = scrub(env.message, secret);
  return env;
}

/** The usage block an error body may carry (then the response is billable, D101). */
export function usageFromBody(text: string): CallUsage | undefined {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    return undefined;
  }
  if (!isObj(raw) || !isObj(raw.usage)) return undefined;
  return usageOf(raw.usage);
}

/** A wire usage block (input_tokens, or prompt_tokens from OpenAI-style answers) as CallUsage. */
export function usageOf(u: Obj): CallUsage | undefined {
  const tokens = (v: unknown): number | undefined => (typeof v === 'number' && Number.isSafeInteger(v) && v >= 0 ? v : undefined);
  const input = tokens(u.input_tokens) ?? tokens(u.prompt_tokens);
  const output = tokens(u.output_tokens) ?? tokens(u.completion_tokens);
  const cost = typeof u.cost === 'number' && Number.isFinite(u.cost) && u.cost >= 0 ? u.cost : null;
  if (input === undefined && cost === null) return undefined;
  return { input_tokens: input ?? 0, ...(output === undefined ? {} : { output_tokens: output }), cost };
}

// ------------------------------------------------------------------------------------ classify

/** classify.go sizeMessage and dataPolicyMessage. */
const SIZE_MESSAGE = /token|context|too long|too large/i;
const DATA_POLICY_MESSAGE = /no endpoints found|data policy/i;

/** Status texts for an error without a readable message. */
const statusText = (status: number): string => `HTTP ${status}`;

/** A non-2xx OpenRouter response as an ApiError (classify.go classifyStatus). */
export function classifyStatus(status: number, headers: Headers, text: string, secret?: string | null): ApiError {
  const env = parseEnvelope(text, secret);
  const message = env.message || statusText(status);
  const usage = usageFromBody(text);
  const retryAfterMs = parseRetryAfter(headers.get('retry-after'));
  const base = { status, message, retryAfterMs, details: { usage } };
  if (status === 403 && env.moderation) {
    return openRouterError('bad_request', { ...base, message: `the provider's moderation flagged the input: ${message}`, details: { usage, moderation: true } });
  }
  if (status === 401 || status === 403) return openRouterError('auth', base);
  if (status === 402) {
    const inFlight = env.limitSource === LIMIT_SOURCE_IN_FLIGHT && retryAfterMs !== null;
    return openRouterError('credits', { ...base, details: { usage, in_flight: inFlight, retryable: inFlight } });
  }
  if (status === 404) return openRouterError(DATA_POLICY_MESSAGE.test(text) ? 'data_policy' : 'not_found', base);
  if (status === 413 || (status === 400 && SIZE_MESSAGE.test(env.message))) return openRouterError('too_large', base);
  if (status === 408 || status === 429 || (status >= 500 && status <= 599)) {
    return openRouterError('retryable', { ...base, details: { usage, retryable: true } });
  }
  if (status >= 400 && status <= 499) return openRouterError('bad_request', base);
  return openRouterError('bad_response', base);
}

/** A 2xx that is not JSON (classify.go checkContentType); null when it is. */
export function checkContentType(status: number, headers: Headers, text: string): ApiError | null {
  const ct = headers.get('content-type');
  if (ct === null || ct.trim() === '') {
    if (text.slice(0, 64).trim().startsWith('{')) return null;
    return openRouterError('bad_content_type', { status, message: 'expected JSON, got no Content-Type' });
  }
  const media = ct.split(';')[0].trim().toLowerCase();
  if (media === 'application/json' || (media.startsWith('application/') && media.endsWith('+json'))) return null;
  const message = ct.toLowerCase().includes('html') ? `got a web page (${ct}) instead of JSON` : `expected JSON, got Content-Type ${ct}`;
  return openRouterError('bad_content_type', { status, message });
}

// ------------------------------------------------------------------------------------ diagnosis

/** What the free key check says about the account (keyInfo.ts). */
export interface KeyCheck {
  limit_remaining?: number | null;
}

/**
 * When the browser cannot read OpenRouter's error bodies (ERRORS_READABLE=false), a billable call
 * that failed with a bare `network` error is explained by one free `GET /api/v1/key`: no credits
 * → `credits` (the 402 screen), a refused key → `auth`; anything else keeps the network error.
 * The result keeps status 0: nothing readable came back, so the ledger counts nothing.
 */
export async function diagnoseOpaqueFailure(err: ApiError, checkKey: () => Promise<KeyCheck>, errorsReadable: boolean = ERRORS_READABLE): Promise<ApiError> {
  if (errorsReadable || errorKind(err) !== 'network') return err;
  const diagnosed = (kind: 'credits' | 'auth', cause: unknown) =>
    openRouterError(kind, { status: 0, message: `${err.message}; the key check says ${kind}`, details: { diagnosed: true }, cause });
  try {
    const info = await checkKey();
    if (typeof info.limit_remaining === 'number' && info.limit_remaining <= 0) return diagnosed('credits', err);
    return err;
  } catch (checkErr) {
    const kind = errorKind(checkErr);
    if (kind === 'credits' || kind === 'auth') return diagnosed(kind, checkErr);
    return err;
  }
}

// ------------------------------------------------------------------------------------ the panel

/** A sentence of copy.playground_live (or the playground_static cap line) the panel shows. */
export type LiveMessageId =
  | 'errors.unauthorized'
  | 'errors.no_credits'
  | 'errors.credits_inflight'
  | 'errors.data_policy'
  | 'errors.rate_limited'
  | 'errors.too_large'
  | 'errors.moderation'
  | 'errors.bad_response'
  | 'errors.network'
  | 'errors.timeout'
  | 'retry_busy'
  | 'browser_blocked'
  | 'token_cap'
  | 'rejected_pause'
  | 'cap_reached';

/** What the panel offers next to a failure. */
export type LiveErrorAction = 'paste_new_key' | 'retry' | 'open_credits' | 'open_privacy' | 'forget_key' | 'check_again';

export interface LiveErrorView {
  kind: OpenRouterErrorKind | LedgerRefusal;
  message: LiveMessageId;
  /** A second sentence (the browser-blocked explanation), or null. */
  hint: LiveMessageId | null;
  actions: LiveErrorAction[];
  /** `no_credits`: the 402 screen instead of an inline message. */
  screen: 'none' | 'no_credits';
  /** Nothing to show: the request was cancelled (Forget key, or the page left). */
  silent: boolean;
}

const view = (kind: LiveErrorView['kind'], message: LiveMessageId, actions: LiveErrorAction[], extra: Partial<LiveErrorView> = {}): LiveErrorView => ({
  kind,
  message,
  hint: null,
  actions,
  screen: 'none',
  silent: false,
  ...extra,
});

const isRefusal = (err: unknown): err is Error & { reason: LedgerRefusal } =>
  err instanceof Error && err.name === 'GateRefusedError' && typeof (err as { reason?: unknown }).reason === 'string';

/**
 * What the live panel says about a failure: a copy key, the actions to offer, and whether it is the
 * 402 screen. Takes ApiErrors from the transport and GateRefusedError from the caps.
 */
export function liveErrorView(err: unknown): LiveErrorView {
  if (isRefusal(err)) {
    switch (err.reason) {
      case 'cap_reached':
        return view('cap_reached', 'cap_reached', []);
      case 'token_cap':
        return view('token_cap', 'token_cap', []);
      case 'rejected_pause':
        return view('rejected_pause', 'rejected_pause', ['retry', 'forget_key']);
    }
  }
  const kind = errorKind(err);
  const d = errorDetails(err);
  const status = err instanceof ApiError ? err.status : 0;
  switch (kind) {
    case 'aborted':
      return view(kind, 'errors.network', [], { silent: true });
    case 'auth':
      return view(kind, 'errors.unauthorized', ['paste_new_key']);
    case 'credits':
      return d.in_flight ? view(kind, 'errors.credits_inflight', ['retry']) : view(kind, 'errors.no_credits', ['open_credits', 'check_again'], { screen: 'no_credits' });
    case 'data_policy':
      return view(kind, 'errors.data_policy', ['open_privacy', 'retry']);
    case 'retryable':
      return status === 429 ? view(kind, 'errors.rate_limited', ['retry']) : view(kind, 'retry_busy', ['retry']);
    case 'too_large':
      return view(kind, 'errors.too_large', []);
    case 'bad_request':
      // A refused request other than moderation (400/422) has no sentence of its own yet; it is a
      // reply the page cannot use, so Retry is not offered (it would be refused again).
      return d.moderation ? view(kind, 'errors.moderation', []) : view(kind, 'errors.bad_response', []);
    case 'not_found':
      return view(kind, 'errors.bad_response', []);
    case 'bad_content_type':
    case 'bad_response':
      return view(kind, 'errors.bad_response', ['retry']);
    case 'network':
      // "Nothing was billed" holds only when no response arrived at all.
      return view(kind, 'errors.network', ['retry'], { hint: status === 0 ? 'browser_blocked' : null });
    case 'timeout':
      return view(kind, 'errors.timeout', ['retry']);
  }
}

/** Whether the key must be forgotten (OpenRouter refused it, D100). */
export function forgetsKey(err: unknown): boolean {
  return errorKind(err) === 'auth';
}
