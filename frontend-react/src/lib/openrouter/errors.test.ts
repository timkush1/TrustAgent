import { describe, expect, it, vi } from 'vitest';
import { ApiError } from '@/data/errors';
import { liveActionLabel, liveErrorWords, liveMessage } from '@/domain/words/live';
import { GateRefusedError } from '@/lib/gate/runner';
import {
  checkContentType,
  classifyStatus,
  diagnoseOpaqueFailure,
  errorDetails,
  errorKind,
  forgetsKey,
  liveErrorView,
  openRouterError,
  parseEnvelope,
  scrub,
  transportError,
  type LiveErrorAction,
  type LiveMessageId,
} from './errors';
import { KEY_URL } from './endpoints';
import { getKeyInfo, hasNoLimit, parseKeyInfo } from './keyInfo';
import { OPENROUTER_ERROR_KINDS, type LedgerRefusal } from './types';
import { isBillable } from './usage';

interface VendorFixture {
  source: string;
  request: { method: string; path: string; headers: Record<string, string>; body?: unknown };
  status: number;
  headers: Record<string, string>;
  body: unknown;
}

const fixtures = import.meta.glob<VendorFixture>('@contracts/fixtures/vendor/openrouter/*.json', { eager: true, import: 'default' });
const byName = Object.fromEntries(Object.entries(fixtures).map(([path, f]) => [path.split('/').pop()!.replace(/\.json$/, ''), f]));

const KEY = 'sk-or-v1-e2e-0123456789abcdef0123456789abcdef';
const NO_PATTERN = 'cmp0e2eZq7Lw4Rt9Yx2Vb6Nm';

const bodyText = (f: VendorFixture) => (typeof f.body === 'string' ? f.body : JSON.stringify(f.body));

/** Classifies a fixture the way the clients do: non-2xx by status, 2xx by Content-Type. */
function classifyFixture(f: VendorFixture): ApiError | null {
  const headers = new Headers(f.headers);
  if (f.status < 200 || f.status > 299) return classifyStatus(f.status, headers, bodyText(f));
  return checkContentType(f.status, headers, bodyText(f));
}

/** Every vendor fixture: its kind (null for a usable 2xx), whether it is retryable and billable (D101). */
const TABLE: Record<string, { kind: string | null; retryable: boolean; billable: boolean; message: LiveMessageId | null }> = {
  'auth_keys.200': { kind: null, retryable: false, billable: true, message: null },
  'auth_keys.400': { kind: 'bad_request', retryable: false, billable: false, message: 'errors.bad_response' },
  'auth_keys.403': { kind: 'auth', retryable: false, billable: false, message: 'errors.unauthorized' },
  'chat.200_usage_cost': { kind: null, retryable: false, billable: true, message: null },
  'decisions.200': { kind: null, retryable: false, billable: true, message: null },
  'decisions.200_html': { kind: 'bad_content_type', retryable: false, billable: true, message: 'errors.bad_response' },
  'decisions.401': { kind: 'auth', retryable: false, billable: false, message: 'errors.unauthorized' },
  'decisions.402_credits': { kind: 'credits', retryable: false, billable: false, message: 'errors.no_credits' },
  'decisions.402_inflight': { kind: 'credits', retryable: true, billable: false, message: 'errors.credits_inflight' },
  'decisions.403_moderation': { kind: 'bad_request', retryable: false, billable: false, message: 'errors.moderation' },
  'decisions.404_data_policy': { kind: 'data_policy', retryable: false, billable: false, message: 'errors.data_policy' },
  'decisions.413': { kind: 'too_large', retryable: false, billable: false, message: 'errors.too_large' },
  'decisions.429': { kind: 'retryable', retryable: true, billable: false, message: 'errors.rate_limited' },
  'key.200': { kind: null, retryable: false, billable: true, message: null },
};

describe('errors: every vendor fixture (classify.go parity)', () => {
  it('the table covers every fixture, and every fixture is synthetic', () => {
    expect(Object.keys(byName).sort()).toEqual(Object.keys(TABLE).sort());
    for (const f of Object.values(byName)) expect(f.source).toMatch(/^synthetic/);
  });

  it.each(Object.keys(TABLE))('%s', (name) => {
    const f = byName[name];
    const want = TABLE[name];
    const err = classifyFixture(f);
    expect(err === null ? null : err.code).toBe(want.kind);
    expect(errorDetails(err).retryable).toBe(want.retryable);
    expect(isBillable(f.status, errorDetails(err).usage)).toBe(want.billable);
    if (err !== null) {
      expect(err).toBeInstanceOf(ApiError);
      expect(err.status).toBe(f.status);
      const view = liveErrorView(err);
      expect(view.message).toBe(want.message);
      expect(view.silent).toBe(false);
    }
  });

  it('the in-flight 402 carries Retry-After; the credits 402 opens the 402 screen', () => {
    const inflight = classifyFixture(byName['decisions.402_inflight'])!;
    expect(inflight.retryAfterMs).toBe(1000);
    expect(errorDetails(inflight).in_flight).toBe(true);
    expect(liveErrorView(inflight)).toMatchObject({ screen: 'none', actions: ['retry'] });
    expect(liveErrorView(classifyFixture(byName['decisions.402_credits'])!)).toMatchObject({ screen: 'no_credits', actions: ['open_credits', 'check_again'] });
  });

  it('a 402 with the in-flight source but no Retry-After is the account balance', () => {
    const err = classifyStatus(402, new Headers(), JSON.stringify({ error: { message: 'x', metadata: { limit_source: 'openrouter_in_flight_budget' } } }));
    expect(errorDetails(err)).toMatchObject({ in_flight: false, retryable: false });
  });

  it('a moderation 403 is the input, not the key', () => {
    const err = classifyFixture(byName['decisions.403_moderation'])!;
    expect(errorDetails(err).moderation).toBe(true);
    expect(forgetsKey(err)).toBe(false);
    expect(forgetsKey(classifyFixture(byName['decisions.401']))).toBe(true);
  });
});

describe('errors: classifyStatus beyond the fixtures', () => {
  const cls = (status: number, body: unknown = {}, headers: Record<string, string> = {}) =>
    classifyStatus(status, new Headers(headers), typeof body === 'string' ? body : JSON.stringify(body));

  it.each([
    [400, { error: { message: 'This request exceeds the context length' } }, 'too_large'],
    [400, { error: { message: 'bad question type' } }, 'bad_request'],
    [422, { detail: [{ loc: ['body', 'questions'], msg: 'field required' }] }, 'bad_request'],
    [404, { error: { message: 'Model not found' } }, 'not_found'],
    [403, { error: { message: 'Forbidden' } }, 'auth'],
    [408, {}, 'retryable'],
    [500, {}, 'retryable'],
    [502, 'Bad gateway', 'retryable'],
    [503, {}, 'retryable'],
    [418, {}, 'bad_request'],
    [302, {}, 'bad_response'],
  ] as const)('%i %j → %s', (status, body, kind) => {
    expect(cls(status, body).code).toBe(kind);
  });

  it('a 5xx offers Retry with the busy sentence, a 429 the rate-limit one', () => {
    expect(liveErrorView(cls(503))).toMatchObject({ message: 'retry_busy', actions: ['retry'] });
    expect(liveErrorView(cls(429, {}, { 'retry-after': '2' }))).toMatchObject({ message: 'errors.rate_limited', actions: ['retry'] });
    expect(cls(429, {}, { 'retry-after': '2' }).retryAfterMs).toBe(2000);
  });

  it('an error body that reports usage is billable', () => {
    const err = cls(500, { error: { message: 'upstream died' }, usage: { input_tokens: 120, cost: 0.000005 } });
    expect(errorDetails(err).usage).toEqual({ input_tokens: 120, cost: 0.000005 });
    expect(isBillable(err.status, errorDetails(err).usage)).toBe(true);
  });
});

describe('errors: envelopes and scrubbing', () => {
  it.each([
    [{ error: { code: 402, message: 'Insufficient credits' } }, 'Insufficient credits'],
    [{ error: { message: 'Provider returned error', metadata: { raw: 'upstream 500' } } }, 'Provider returned error: upstream 500'],
    [{ error: 'plain string' }, 'plain string'],
    [{ error: { type: 'invalid_request_error' } }, 'invalid_request_error'],
    [{ detail: [{ loc: ['body', 'state', 0], msg: 'bad' }, { msg: 'worse' }] }, 'body.state.0: bad; worse'],
    [{ detail: { error_type: 'quota', message: 'Out of quota' } }, 'Out of quota'],
    [{ detail: 'Not authenticated' }, 'Not authenticated'],
    [{ message: 'Vercel says no', error_type: 'x' }, 'Vercel says no'],
  ])('%j', (body, message) => {
    expect(parseEnvelope(JSON.stringify(body)).message).toBe(message);
  });

  it('reads the first line of a non-JSON body', () => {
    expect(parseEnvelope('<html>\n<body>oops</body>').message).toBe('<html>');
  });

  it('masks secret patterns and the key itself, and bounds the length', () => {
    expect(scrub(`echo sk-or-v1-abcdef0123456789 and Bearer ${NO_PATTERN}`)).toBe('echo [redacted] and [redacted]');
    expect(scrub(`your key ${NO_PATTERN} is wrong`, NO_PATTERN)).toBe('your key [redacted] is wrong');
    expect([...scrub('x'.repeat(500))].length).toBe(301);
    const err = classifyStatus(401, new Headers(), JSON.stringify({ error: { message: `Key ${NO_PATTERN} not found` } }), NO_PATTERN);
    expect(err.message).not.toContain(NO_PATTERN);
    expect(JSON.stringify(err.details)).not.toContain(NO_PATTERN);
  });
});

describe('errors: Content-Type of a 2xx (checkContentType)', () => {
  it.each([
    ['application/json', '{}', null],
    ['application/json; charset=utf-8', '{}', null],
    ['Application/JSON', '{}', null],
    ['application/problem+json', '{}', null],
    ['application/vnd.openrouter.v1+json; charset=utf-8', '{}', null],
    ['text/html; charset=utf-8', '<!DOCTYPE html>', 'bad_content_type'],
    ['text/plain', '{}', 'bad_content_type'],
    [null, '  {"answers":{}}', null],
    [null, '<html>', 'bad_content_type'],
    ['', 'ok', 'bad_content_type'],
  ] as const)('%s', (ct, body, kind) => {
    const err = checkContentType(200, new Headers(ct === null ? {} : { 'content-type': ct }), body);
    expect(err === null ? null : err.code).toBe(kind);
  });

  it('says a web page came back instead of JSON', () => {
    expect(checkContentType(200, new Headers({ 'content-type': 'text/html' }), '<html>')?.message).toMatch(/web page/);
  });
});

describe('errors: transport failures', () => {
  it('maps timeout, abort and TypeError', () => {
    expect(errorKind(transportError('timeout', 0))).toBe('timeout');
    expect(errorKind(transportError('aborted', 0))).toBe('aborted');
    expect(errorKind(new DOMException('x', 'AbortError'))).toBe('aborted');
    expect(errorKind(new DOMException('x', 'TimeoutError'))).toBe('timeout');
    expect(errorKind(new TypeError('Failed to fetch'))).toBe('network');
    expect(errorKind(new Error('bug'))).toBe('bad_response');
    expect(errorKind(new ApiError({ kind: 'http', status: 500, code: 'internal', message: 'x' }))).toBe('bad_response');
  });

  it('shows nothing for a cancelled request, and Retry for the others', () => {
    expect(liveErrorView(transportError('aborted', 0)).silent).toBe(true);
    expect(liveErrorView(transportError('timeout', 0))).toMatchObject({ message: 'errors.timeout', actions: ['retry'] });
    expect(liveErrorView(transportError('network', 0))).toMatchObject({ message: 'errors.network', hint: 'browser_blocked', actions: ['retry'] });
    // After a response arrived, "nothing was billed" is not certain.
    expect(liveErrorView(transportError('network', 200)).hint).toBeNull();
  });

  it('words the cap refusals', () => {
    expect(liveErrorView(new GateRefusedError('cap_reached'))).toMatchObject({ message: 'cap_reached', actions: [] });
    expect(liveErrorView(new GateRefusedError('token_cap'))).toMatchObject({ message: 'token_cap', actions: [] });
    expect(liveErrorView(new GateRefusedError('rejected_pause'))).toMatchObject({ message: 'rejected_pause', actions: ['retry', 'forget_key'] });
  });
});

describe('errors: an unreadable failure (ERRORS_READABLE=false)', () => {
  const opaque = transportError('network', 0, new TypeError('Failed to fetch'));

  it('a /key 402 turns it into the 402 screen, counted as nothing', async () => {
    const check = vi.fn(async () => {
      throw classifyStatus(402, new Headers(), JSON.stringify({ error: { message: 'Insufficient credits' } }));
    });
    const err = await diagnoseOpaqueFailure(opaque, check, false);
    expect(err).toMatchObject({ code: 'credits', status: 0 });
    expect(errorDetails(err).diagnosed).toBe(true);
    expect(liveErrorView(err).screen).toBe('no_credits');
    expect(isBillable(err.status, errorDetails(err).usage)).toBe(false);
  });

  it('a spent limit on the key is no credits too; a refused key is auth', async () => {
    expect((await diagnoseOpaqueFailure(opaque, async () => ({ limit_remaining: 0 }), false)).code).toBe('credits');
    const auth = await diagnoseOpaqueFailure(opaque, async () => Promise.reject(classifyStatus(401, new Headers(), '{}')), false);
    expect(auth.code).toBe('auth');
    expect(forgetsKey(auth)).toBe(true);
  });

  it('keeps the network error when the key check finds nothing, or fails too', async () => {
    expect(await diagnoseOpaqueFailure(opaque, async () => ({ limit_remaining: 4.2 }), false)).toBe(opaque);
    expect(await diagnoseOpaqueFailure(opaque, async () => Promise.reject(transportError('network', 0)), false)).toBe(opaque);
  });

  it('runs no key check when errors are readable, or for another failure', async () => {
    const check = vi.fn(async () => ({}));
    expect(await diagnoseOpaqueFailure(opaque, check, true)).toBe(opaque);
    const timeout = transportError('timeout', 0);
    expect(await diagnoseOpaqueFailure(timeout, check, false)).toBe(timeout);
    expect(check).not.toHaveBeenCalled();
  });
});

describe('errors: what the panel says', () => {
  const allViews = () => {
    const errs: unknown[] = [
      ...OPENROUTER_ERROR_KINDS.map((k) => openRouterError(k, { status: k === 'retryable' ? 429 : 0, message: 'x' })),
      openRouterError('retryable', { status: 503, message: 'x' }),
      openRouterError('credits', { status: 402, message: 'x', details: { in_flight: true } }),
      openRouterError('bad_request', { status: 403, message: 'x', details: { moderation: true } }),
      ...(['cap_reached', 'token_cap', 'rejected_pause'] as LedgerRefusal[]).map((r) => new GateRefusedError(r)),
    ];
    return errs.map((e) => liveErrorView(e));
  };

  it('every sentence comes from copy, filled, and never sends a visitor to a server setting', () => {
    const banned = /TRUTHTABLE_|Settings →|\.env|make /;
    const actions = new Set<LiveErrorAction>();
    for (const v of allViews()) {
      const words = liveErrorWords(v, { max_calls: 100, max_tokens: 8000 });
      for (const s of [words.message, words.hint ?? '']) {
        expect(s).not.toMatch(banned);
        expect(s).not.toMatch(/[{}]/);
      }
      expect(words.message.length).toBeGreaterThan(10);
      v.actions.forEach((a) => actions.add(a));
    }
    for (const a of actions) {
      expect(liveActionLabel(a)).toMatch(/^[A-Z]/);
      expect(liveActionLabel(a)).not.toMatch(banned);
    }
    expect(liveMessage('token_cap', { max_tokens: 8000 })).toContain('8000');
    expect(liveMessage('cap_reached', { max_calls: 100 })).toContain('100');
  });

  it('every OpenRouter kind has a view', () => {
    for (const k of OPENROUTER_ERROR_KINDS) expect(liveErrorView(openRouterError(k, { status: 0, message: 'x' })).kind).toBe(k);
  });
});

describe('keyInfo: the free key check', () => {
  const respond = (status: number, body: unknown, headers: Record<string, string> = { 'content-type': 'application/json' }) => {
    const calls: { url: string; init: RequestInit }[] = [];
    const fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), init: init ?? {} });
      return new Response(typeof body === 'string' ? body : JSON.stringify(body), { status, headers });
    }) as typeof globalThis.fetch;
    return { fetch, calls };
  };

  it('reads the fixture: a key without a credit limit', async () => {
    const f = byName['key.200'];
    const r = respond(200, f.body);
    const info = await getKeyInfo(KEY, { http: { fetch: r.fetch } });
    expect(info).toEqual({ label: 'sk-or-v1-e…key', limit: null, limit_remaining: null, usage: 0.0123, is_free_tier: false });
    expect(hasNoLimit(info)).toBe(true);
    expect(r.calls).toHaveLength(1);
    expect(r.calls[0].url).toBe(KEY_URL);
    expect(r.calls[0].url).not.toContain(KEY);
    expect(r.calls[0].init.method).toBe('GET');
    expect(r.calls[0].init.headers).toMatchObject({ authorization: `Bearer ${KEY}` });
    expect(r.calls[0].init.headers).not.toHaveProperty('content-type');
  });

  it('keeps only well-typed fields (the live shape is not captured yet)', () => {
    expect(parseKeyInfo({ data: { limit: 5, limit_remaining: '1', usage: null, label: 3 } })).toEqual({ limit: 5 });
    expect(hasNoLimit({ limit: 5 })).toBe(false);
    expect(hasNoLimit({})).toBe(false);
    expect(parseKeyInfo({ limit: null })).toBeNull();
  });

  it('rejects with the OpenRouter kind', async () => {
    await expect(getKeyInfo(KEY, { http: { fetch: respond(401, { error: { message: 'User not found.' } }).fetch } })).rejects.toMatchObject({ code: 'auth' });
    await expect(getKeyInfo(KEY, { http: { fetch: respond(200, '<html>', { 'content-type': 'text/html' }).fetch } })).rejects.toMatchObject({ code: 'bad_content_type' });
    await expect(getKeyInfo(KEY, { http: { fetch: respond(200, { nope: 1 }).fetch } })).rejects.toMatchObject({ code: 'bad_response' });
  });
});
