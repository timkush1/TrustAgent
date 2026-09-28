/**
 * The only place the dashboard calls fetch for REST (SPEC §2.4).
 *
 * - Same origin (the default): cookies ride along, and non-GET requests carry
 *   `X-TrustAgent-CSRF: 1` as cookie-authenticated writes require.
 * - Cross origin (`apiBase` on another origin): no cookies, key-header auth only (P2 adds the key).
 * - Every failure is an ApiError (errors.ts), including network trouble, timeouts and aborts.
 */
import type { RuntimeConfig } from '@/config/runtime';
import { ApiError, apiErrorFromResponse, toApiError } from './errors';

export type QueryValue = string | number | boolean | null | undefined | readonly (string | number)[];
export type Query = Record<string, QueryValue>;

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  query?: Query;
  /** Serialized as JSON. */
  body?: unknown;
  headers?: Record<string, string>;
  signal?: AbortSignal;
  /** Default 15 s. The request is aborted and fails with kind "timeout". */
  timeoutMs?: number;
}

export interface HttpClient {
  request<T>(path: string, options?: RequestOptions): Promise<T>;
  get<T>(path: string, options?: Omit<RequestOptions, 'method' | 'body'>): Promise<T>;
  /** Absolute or origin-relative URL for a REST path, with the query appended. */
  url(path: string, query?: Query): string;
}

export const DEFAULT_TIMEOUT_MS = 15_000;

export function buildQuery(query: Query | undefined): string {
  if (!query) return '';
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === '') continue;
    if (Array.isArray(value)) {
      for (const v of value) params.append(key, String(v));
    } else {
      params.append(key, String(value));
    }
  }
  const s = params.toString();
  return s ? `?${s}` : '';
}

/** One AbortSignal that fires on the caller's signal or after `timeoutMs`. */
function linkedSignal(outer: AbortSignal | undefined, timeoutMs: number) {
  const controller = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs);
  const onAbort = () => controller.abort(outer?.reason);
  if (outer) {
    if (outer.aborted) controller.abort(outer.reason);
    else outer.addEventListener('abort', onAbort, { once: true });
  }
  return {
    signal: controller.signal,
    timedOut: () => timedOut,
    dispose: () => {
      clearTimeout(timer);
      outer?.removeEventListener('abort', onAbort);
    },
  };
}

async function readBody(res: Response): Promise<unknown> {
  const text = await res.text();
  if (!text) return undefined;
  const type = res.headers.get('content-type') ?? '';
  if (!type.includes('json')) return text;
  return JSON.parse(text) as unknown;
}

export function createHttpClient(
  config: Pick<RuntimeConfig, 'apiBase' | 'crossOrigin'>,
  deps: { fetchImpl?: typeof fetch } = {},
): HttpClient {
  const doFetch = deps.fetchImpl ?? ((input, init) => fetch(input, init));

  const url = (path: string, query?: Query) => `${config.apiBase}${path}${buildQuery(query)}`;

  async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const method = options.method ?? 'GET';
    const headers: Record<string, string> = { Accept: 'application/json', ...options.headers };
    let body: string | undefined;
    if (options.body !== undefined) {
      headers['Content-Type'] = 'application/json';
      body = JSON.stringify(options.body);
    }
    if (method !== 'GET' && !config.crossOrigin) headers['X-TrustAgent-CSRF'] = '1';

    const link = linkedSignal(options.signal, options.timeoutMs ?? DEFAULT_TIMEOUT_MS);
    try {
      const res = await doFetch(url(path, options.query), {
        method,
        headers,
        body,
        credentials: config.crossOrigin ? 'omit' : 'same-origin',
        signal: link.signal,
      });
      let parsed: unknown;
      try {
        parsed = await readBody(res);
      } catch (err) {
        if (!res.ok) throw apiErrorFromResponse(res.status, undefined, res.headers.get('retry-after'));
        throw new ApiError({
          kind: 'invalid_response',
          status: res.status,
          code: 'invalid_response',
          message: 'The server sent a response the dashboard could not read.',
          cause: err,
        });
      }
      if (!res.ok) throw apiErrorFromResponse(res.status, parsed, res.headers.get('retry-after'));
      return parsed as T;
    } catch (err) {
      if (err instanceof ApiError) throw err;
      if (link.timedOut()) throw toApiError(err, true);
      if (options.signal?.aborted) {
        throw new ApiError({ kind: 'aborted', status: 0, code: 'aborted', message: 'The request was cancelled.', cause: err });
      }
      throw toApiError(err);
    } finally {
      link.dispose();
    }
  }

  return {
    request,
    get: (path, options) => request(path, { ...options, method: 'GET' }),
    url,
  };
}
