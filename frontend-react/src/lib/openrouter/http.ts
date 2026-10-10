/**
 * The browser's one door to openrouter.ai (D100, D138): the only live-mode module allowed to call
 * `fetch`. Every request
 *
 * - goes to `https://openrouter.ai/` and nowhere else (anything else is refused before any fetch);
 * - sends exactly the header names in ALLOWED_REQUEST_HEADERS, with the key in `Authorization`
 *   only when the URL's host is exactly `openrouter.ai`;
 * - says where it comes from with `HTTP-Referer: location.origin + BASE_URL` (never VITE_SITE_URL)
 *   and `X-OpenRouter-Title: TrustAgent demo`;
 * - carries no cookies, no referrer, follows no redirect and is never cached;
 * - has a time limit, joins the caller's AbortSignal and registers with the key store, so Forget key
 *   aborts it.
 *
 * Nothing is retried here (D100: a retryable error offers Retry). The answer comes back whatever its
 * status; a request that got no answer rejects with an ApiError coded `network`, `timeout` or
 * `aborted` (its status is the response's when the body failed after the headers arrived, so the
 * ledger can still tell a billed call). Owned by WEB-LIVE (LV-2).
 */
import { ALLOWED_REQUEST_HEADERS } from '@/domain/liveScenarios';
import { APP_TITLE, OPENROUTER_HOST } from './endpoints';
import { transportError } from './errors';

export type AllowedHeader = (typeof ALLOWED_REQUEST_HEADERS)[number];

export interface OpenRouterRequest {
  url: string;
  method: 'GET' | 'POST';
  /** A JSON body, already encoded. */
  body?: string;
  /** The key; attached only for https://openrouter.ai. */
  key?: string | null;
  timeoutMs: number;
  signal?: AbortSignal;
  /** Registers the request so Forget key can abort it (KeyStore.track); returns the unregister. */
  track?: (controller: AbortController) => () => void;
}

export interface OpenRouterResponse {
  status: number;
  headers: Headers;
  text: string;
  latency_ms: number;
}

export interface HttpDeps {
  fetch?: typeof fetch;
  now?: () => number;
  /** The HTTP-Referer value; location.origin + BASE_URL by default. */
  referer?: string;
}

/** The attribution URL: this site's origin and base path (D100). */
export function siteReferer(): string {
  const origin = typeof location === 'undefined' ? '' : location.origin;
  return `${origin}${import.meta.env.BASE_URL}`;
}

/** Whether `url` is on https://openrouter.ai exactly (no other host, port, scheme or credentials). */
export function isOpenRouterUrl(url: string): boolean {
  let u: URL;
  try {
    u = new URL(url);
  } catch {
    return false;
  }
  return u.protocol === 'https:' && u.host === OPENROUTER_HOST && u.username === '' && u.password === '';
}

/** The request headers (lower-case names, all in ALLOWED_REQUEST_HEADERS). */
export function requestHeaders(url: string, opts: { key?: string | null; body: boolean; referer: string }): Record<AllowedHeader, string> {
  const h: Partial<Record<AllowedHeader, string>> = {};
  if (opts.key && isOpenRouterUrl(url)) h.authorization = `Bearer ${opts.key}`;
  if (opts.body) h['content-type'] = 'application/json';
  h.accept = 'application/json';
  h['http-referer'] = opts.referer;
  h['x-openrouter-title'] = APP_TITLE;
  return h as Record<AllowedHeader, string>;
}

export async function openRouterFetch(req: OpenRouterRequest, deps: HttpDeps = {}): Promise<OpenRouterResponse> {
  if (!isOpenRouterUrl(req.url)) throw new Error('openRouterFetch: only https://openrouter.ai/ may be called');
  const doFetch = deps.fetch ?? globalThis.fetch.bind(globalThis);
  const now = deps.now ?? (() => performance.now());
  if (req.signal?.aborted) throw transportError('aborted', 0, req.signal.reason);

  const controller = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort(new DOMException('the request took too long', 'TimeoutError'));
  }, req.timeoutMs);
  const onAbort = () => controller.abort(req.signal?.reason);
  req.signal?.addEventListener('abort', onAbort, { once: true });
  const untrack = req.track?.(controller);

  const started = now();
  let status = 0;
  try {
    const res = await doFetch(req.url, {
      method: req.method,
      headers: requestHeaders(req.url, { key: req.key, body: req.body !== undefined, referer: deps.referer ?? siteReferer() }),
      body: req.body,
      mode: 'cors',
      credentials: 'omit',
      referrerPolicy: 'no-referrer',
      redirect: 'error',
      cache: 'no-store',
      signal: controller.signal,
    });
    status = res.status;
    const text = await res.text();
    return { status, headers: res.headers, text, latency_ms: Math.max(0, Math.round(now() - started)) };
  } catch (err) {
    if (timedOut) throw transportError('timeout', status, err);
    if (controller.signal.aborted) throw transportError('aborted', status, err);
    throw transportError('network', status, err);
  } finally {
    clearTimeout(timer);
    req.signal?.removeEventListener('abort', onAbort);
    untrack?.();
  }
}
