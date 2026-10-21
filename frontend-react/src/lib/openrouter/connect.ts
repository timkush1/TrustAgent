/**
 * Connect OpenRouter from the static site (D105, D106): start a PKCE sign-in, and finish it on
 * `/connect/callback/`.
 *
 * Start (the consent dialog's Connect `<button>`): a fresh verifier is remembered in this tab
 * (keyStore.savePending, one-shot, 10 min) with a checked return path, and the browser goes to
 * openrouter.ai/auth with the S256 challenge. When this tab cannot store anything, Connect does not
 * start: the verifier would not survive the redirect.
 *
 * Finish (runCallback, the callback page):
 * 1. `code` and `error` are read from the raw query string;
 * 2. the address bar loses them (`history.replaceState`) before any request;
 * 3. `error` and `error_description` are never shown: any error is the fixed `refused` state;
 * 4. the pending sign-in is taken once, whatever happens next; a code without this tab's verifier is
 *    never exchanged (login CSRF), and one older than 10 minutes is `expired`;
 * 5. `POST /api/v1/auth/keys {code, code_verifier, code_challenge_method:"S256"}`, with no key;
 * 6. the key is stored for this tab;
 * 7. `location.replace(BASE_URL + returnPath)`, so Back never returns to the spent callback.
 *
 * A reload after success (a key in this tab, no pending sign-in) is `already_connected`. The finish
 * runs once per page load (a module-level promise), because React StrictMode runs effects twice.
 * Owned by WEB-LIVE (LV-5).
 */
import { LIVE_SCENARIOS } from '@/domain/liveScenarios';
import { AUTH_KEYS_URL, TIMEOUTS_MS } from './endpoints';
import { checkContentType, classifyStatus, errorKind, openRouterError } from './errors';
import { openRouterFetch, type HttpDeps } from './http';
import { isKeyShape, type KeyStore } from './keyStore';
import { authUrl, challengeS256, KEY_LABEL } from './pkce';
import { createVerifier, type RandomFill } from './random';

/** Where a finished sign-in returns when the stored path is missing or not allowed. */
export const DEFAULT_RETURN = 'app/playground/#live';

/**
 * The return paths a sign-in may come back to, router-relative (no leading slash, so every path
 * stays under the site's base, `/TrustAgent/` on Pages). Anything else falls back to the default:
 * `//evil`, `/\evil`, `app/../x` and `javascript:` never match.
 */
export const RETURN_PATH = new RegExp(`^app/(playground|evaluators/studio)/(\\?scenario=(${LIVE_SCENARIOS.join('|')}))?(#live)?$`);

/** `path` when it is an allowed return path, else the default. */
export function checkReturnPath(path: unknown): string {
  return typeof path === 'string' && RETURN_PATH.test(path) ? path : DEFAULT_RETURN;
}

/** This site's callback URL: the page's origin and base, then `connect/callback/`. */
export function callbackUrl(origin: string, base: string): string {
  return `${origin}${base.endsWith('/') ? base : `${base}/`}connect/callback/`;
}

export interface BeginConnectOptions {
  /** location.origin by default. */
  origin?: string;
  /** import.meta.env.BASE_URL by default. */
  base?: string;
  fill?: RandomFill;
}

export type BeginConnect = { ok: true; url: string } | { ok: false; reason: 'storage_blocked' };

/** Remembers a new sign-in and returns the openrouter.ai/auth URL to go to. */
export function beginConnect(store: KeyStore, returnTo: string, opts: BeginConnectOptions = {}): BeginConnect {
  if (store.getSnapshot().storage_blocked) return { ok: false, reason: 'storage_blocked' };
  const verifier = createVerifier(opts.fill);
  if (!store.savePending(verifier, checkReturnPath(returnTo))) return { ok: false, reason: 'storage_blocked' };
  const origin = opts.origin ?? location.origin;
  const base = opts.base ?? import.meta.env.BASE_URL;
  return { ok: true, url: authUrl({ callbackUrl: callbackUrl(origin, base), challenge: challengeS256(verifier), keyLabel: KEY_LABEL }) };
}

export interface ExchangeOptions {
  signal?: AbortSignal;
  track?: (controller: AbortController) => () => void;
  http?: HttpDeps;
}

type Obj = Record<string, unknown>;

/**
 * Exchanges the code for a key (free, sent without a key, never counted by the caps). Rejects with
 * an OpenRouter ApiError; an answer without a key of the OpenRouter shape is `bad_response`.
 */
export async function exchangeCode(code: string, verifier: string, opts: ExchangeOptions = {}): Promise<string> {
  const body = JSON.stringify({ code, code_verifier: verifier, code_challenge_method: 'S256' });
  const res = await openRouterFetch({ url: AUTH_KEYS_URL, method: 'POST', body, key: null, timeoutMs: TIMEOUTS_MS.authKeys, signal: opts.signal, track: opts.track }, opts.http);
  if (res.status < 200 || res.status > 299) throw classifyStatus(res.status, res.headers, res.text);
  const notJson = checkContentType(res.status, res.headers, res.text);
  if (notJson) throw notJson;
  let parsed: unknown;
  try {
    parsed = JSON.parse(res.text);
  } catch (err) {
    throw openRouterError('bad_response', { status: res.status, message: 'the key exchange is not JSON', cause: err });
  }
  const key = typeof parsed === 'object' && parsed !== null ? (parsed as Obj).key : undefined;
  if (typeof key !== 'string' || !isKeyShape(key)) throw openRouterError('bad_response', { status: res.status, message: 'the key exchange returned no OpenRouter key' });
  return key;
}

// ------------------------------------------------------------------------------------ callback

/** The callback page's states (copy.playground_live.callback.*), plus `done` while it navigates away. */
export type CallbackState = 'working' | 'missing_verifier' | 'expired' | 'refused' | 'network' | 'framed' | 'already_connected' | 'done';

export interface CallbackDeps {
  store: KeyStore;
  /** The raw query string (`location.search`), read before anything changes it. */
  search: string;
  /** The page runs inside a frame (window.top !== window.self): nothing is exchanged (D106). */
  framed: boolean;
  /** Takes `code` and `error` out of the address bar (history.replaceState with the bare path). */
  scrubUrl: () => void;
  /** Leaves the callback for good (location.replace). */
  leave: (url: string) => void;
  /** import.meta.env.BASE_URL. */
  base: string;
  exchange?: typeof exchangeCode;
  http?: HttpDeps;
}

/** What a failed exchange shows: no answer at all is `network`, anything OpenRouter said is `refused`. */
export function exchangeFailureState(err: unknown): 'network' | 'refused' {
  const kind = errorKind(err);
  return kind === 'network' || kind === 'timeout' ? 'network' : 'refused';
}

async function finish(deps: CallbackDeps): Promise<CallbackState> {
  const params = new URLSearchParams(deps.search);
  const code = params.get('code');
  const hasError = params.has('error') || params.has('error_description');
  // Before anything else, and before any request: the code leaves the address bar and history.
  if (code !== null || hasError || deps.search !== '') deps.scrubUrl();
  if (deps.framed) return 'framed';

  const connected = deps.store.currentKey() !== null;
  if (code === null && !hasError) return connected ? 'already_connected' : 'missing_verifier';
  // Taken once whatever the outcome: a verifier never serves two codes.
  const pending = deps.store.takePending();
  if (hasError || code === '') return 'refused';
  if (pending.status === 'missing') return connected ? 'already_connected' : 'missing_verifier';
  if (pending.status === 'expired') return 'expired';

  let key: string;
  try {
    key = await (deps.exchange ?? exchangeCode)(code as string, pending.verifier, { track: deps.store.track, http: deps.http });
  } catch (err) {
    return exchangeFailureState(err);
  }
  if (!deps.store.set(key)) return 'refused';
  deps.leave(`${deps.base}${checkReturnPath(pending.returnPath)}`);
  return 'done';
}

let flight: Promise<CallbackState> | null = null;

/**
 * Finishes the sign-in once per page load: a second call (StrictMode's second effect run) gets the
 * first call's promise and sends nothing.
 */
export function runCallback(deps: CallbackDeps): Promise<CallbackState> {
  flight ??= finish(deps);
  return flight;
}

/** Forgets the page-load guard (tests only). */
export function resetCallbackForTests(): void {
  flight = null;
}
