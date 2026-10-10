/**
 * The free key check (D100, D101): `GET /api/v1/key` with the key, which costs nothing and never
 * counts against the caps. It tells the panel whether the key has a credit limit (a key without
 * one gets the `key_no_limit` advice) and explains an unreadable failure (errors.ts
 * diagnoseOpaqueFailure). Every field stays optional until P6 captures the live shape. Owned by
 * WEB-LIVE (LV-2).
 */
import { checkContentType, classifyStatus, openRouterError } from './errors';
import { KEY_URL, TIMEOUTS_MS } from './endpoints';
import { openRouterFetch, type HttpDeps } from './http';

export interface KeyInfo {
  label?: string;
  /** The key's credit limit in USD; null when it has none. */
  limit?: number | null;
  limit_remaining?: number | null;
  /** USD spent with the key so far. */
  usage?: number;
  is_free_tier?: boolean;
}

export interface KeyInfoOptions {
  signal?: AbortSignal;
  track?: (controller: AbortController) => () => void;
  http?: HttpDeps;
}

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);
const money = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v);

/** The fields of `{data:{…}}` that have the expected types; anything else is left out. */
export function parseKeyInfo(body: unknown): KeyInfo | null {
  if (!isObj(body) || !isObj(body.data)) return null;
  const d = body.data;
  const info: KeyInfo = {};
  if (typeof d.label === 'string') info.label = d.label;
  if (d.limit === null || money(d.limit)) info.limit = d.limit;
  if (d.limit_remaining === null || money(d.limit_remaining)) info.limit_remaining = d.limit_remaining;
  if (money(d.usage)) info.usage = d.usage;
  if (typeof d.is_free_tier === 'boolean') info.is_free_tier = d.is_free_tier;
  return info;
}

/** True when OpenRouter says the key has no credit limit (`limit: null`), so `key_no_limit` applies. */
export function hasNoLimit(info: KeyInfo): boolean {
  return info.limit === null;
}

/** Reads the key's info; rejects with an OpenRouter ApiError (a refused key is `auth`). */
export async function getKeyInfo(key: string, opts: KeyInfoOptions = {}): Promise<KeyInfo> {
  const res = await openRouterFetch({ url: KEY_URL, method: 'GET', key, timeoutMs: TIMEOUTS_MS.key, signal: opts.signal, track: opts.track }, opts.http);
  if (res.status < 200 || res.status > 299) throw classifyStatus(res.status, res.headers, res.text, key);
  const notJson = checkContentType(res.status, res.headers, res.text);
  if (notJson) throw notJson;
  let body: unknown;
  try {
    body = JSON.parse(res.text);
  } catch (err) {
    throw openRouterError('bad_response', { status: res.status, message: 'the key check is not JSON', cause: err });
  }
  const info = parseKeyInfo(body);
  if (info === null) throw openRouterError('bad_response', { status: res.status, message: 'the key check has no data object' });
  return info;
}
