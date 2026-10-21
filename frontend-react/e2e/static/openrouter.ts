/**
 * A mocked openrouter.ai for the static specs (D138; frozen after Step 0b). `mockOpenRouter(page,
 * fixtures)` routes `https://openrouter.ai/**`:
 *
 * - serves the synthetic vendor fixtures (contracts/fixtures/vendor/openrouter, D122) per endpoint;
 * - answers the `/auth?…` navigation with a 302 to the `callback_url` it carries, plus `?code=`;
 * - answers CORS preflights the way the real API does (Step 0a, SPEC Appendix D);
 * - records each request's method, URL, headers and body;
 * - **fails** any request whose author header names fall outside ALLOWED_REQUEST_HEADERS: a test
 *   mock that fulfils preflights itself would otherwise let a new header pass in tests and break on
 *   GitHub Pages. Call `assertClean()` (or check `violations`) at the end of each test.
 *
 * Register it after blockUnmockedExternal (site.ts): Playwright tries the newest route first.
 */
import { readFileSync } from 'node:fs';
import { expect, type Page, type Request, type Route } from '@playwright/test';
import { ALLOWED_REQUEST_HEADERS } from '../../src/domain/liveScenarios';

export const OPENROUTER_ORIGIN = 'https://openrouter.ai';

/** One contracts/fixtures/vendor/openrouter/*.json file. */
export interface VendorFixture {
  source: string;
  request?: { method: string; path: string; headers?: Record<string, string>; body?: unknown };
  status: number;
  headers: Record<string, string>;
  body: unknown;
}

/** Reads contracts/fixtures/vendor/openrouter/<name>.json (e.g. `decisions.200`). */
export function vendorFixture(name: string): VendorFixture {
  const file = new URL(`../../../contracts/fixtures/vendor/openrouter/${name}.json`, import.meta.url);
  return JSON.parse(readFileSync(file, 'utf8')) as VendorFixture;
}

export type OpenRouterEndpoint = 'decisions' | 'systemone' | 'chat' | 'auth_keys' | 'key';

export interface RecordedRequest {
  method: string;
  url: string;
  endpoint: OpenRouterEndpoint | 'auth' | 'preflight' | 'unknown';
  headers: Record<string, string>;
  body: string | null;
}

/** A fixture, a sequence (one per call; the last repeats), or a function of the request. */
export type FixtureSource = VendorFixture | readonly VendorFixture[] | ((request: RecordedRequest, call: number) => VendorFixture);

export interface OpenRouterMock {
  requests: RecordedRequest[];
  /** One line per problem: a header outside ALLOWED_REQUEST_HEADERS, or an unknown endpoint. */
  violations: string[];
  /** Fails the test when any request broke the network invariant. */
  assertClean(): void;
}

const ENDPOINTS: [string, string, OpenRouterEndpoint][] = [
  ['POST', '/api/alpha/decisions', 'decisions'],
  ['POST', '/api/v1/systemone', 'systemone'],
  ['POST', '/api/v1/chat/completions', 'chat'],
  ['POST', '/api/v1/auth/keys', 'auth_keys'],
  ['GET', '/api/v1/key', 'key'],
];

/** Headers the browser itself sets on a fetch: never the page's choice, so never checked. */
const BROWSER_HEADERS = /^(sec-|accept-language$|accept-encoding$|user-agent$|referer$|origin$|host$|connection$|content-length$|cookie$|priority$|cache-control$|pragma$|upgrade-insecure-requests$|dnt$)/;

const ALLOWED: ReadonlySet<string> = new Set(ALLOWED_REQUEST_HEADERS);

const CORS = {
  'access-control-allow-origin': '*',
  'access-control-allow-methods': 'GET,OPTIONS,PATCH,DELETE,POST,PUT',
  'access-control-allow-headers': 'Authorization, Content-Type, Accept, HTTP-Referer, X-Openrouter-Title',
  'access-control-expose-headers': 'X-Generation-Id,X-Provider-Name,request-id,cf-ray',
};

function pick(source: FixtureSource, request: RecordedRequest, call: number): VendorFixture {
  if (typeof source === 'function') return source(request, call);
  if (Array.isArray(source)) return source[Math.min(call, source.length - 1)] as VendorFixture;
  return source as VendorFixture;
}

export async function mockOpenRouter(
  page: Page,
  fixtures: Partial<Record<OpenRouterEndpoint, FixtureSource>>,
  options: { code?: string } = {},
): Promise<OpenRouterMock> {
  const requests: RecordedRequest[] = [];
  const violations: string[] = [];
  const calls = new Map<OpenRouterEndpoint, number>();

  const record = async (req: Request, endpoint: RecordedRequest['endpoint']): Promise<RecordedRequest> => {
    const headers = await req.allHeaders();
    const entry: RecordedRequest = { method: req.method(), url: req.url(), endpoint, headers, body: req.postData() };
    requests.push(entry);
    return entry;
  };

  const handler = async (route: Route) => {
    const req = route.request();
    const url = new URL(req.url());
    const method = req.method();

    if (method === 'OPTIONS') {
      const asked = (req.headers()['access-control-request-headers'] ?? '')
        .split(',')
        .map((h) => h.trim().toLowerCase())
        .filter(Boolean);
      await record(req, 'preflight');
      for (const h of asked) if (!ALLOWED.has(h)) violations.push(`preflight for ${url.pathname} asks for header ${h}`);
      await route.fulfill({ status: 204, headers: CORS });
      return;
    }

    if (method === 'GET' && url.pathname === '/auth') {
      await record(req, 'auth');
      const callback = url.searchParams.get('callback_url');
      if (!callback) {
        violations.push('the /auth navigation carries no callback_url');
        await route.fulfill({ status: 400, body: 'callback_url is required' });
        return;
      }
      const target = new URL(callback);
      target.searchParams.set('code', options.code ?? 'e2e-code-1');
      await route.fulfill({ status: 302, headers: { location: target.toString() } });
      return;
    }

    const known = ENDPOINTS.find(([m, path]) => m === method && path === url.pathname);
    const recorded = await record(req, known ? known[2] : 'unknown');
    for (const name of Object.keys(recorded.headers)) {
      const h = name.toLowerCase();
      if (!BROWSER_HEADERS.test(h) && !ALLOWED.has(h)) violations.push(`${method} ${url.pathname} sends header ${h}`);
    }
    const source = known ? (fixtures[known[2]] ?? (known[2] === 'systemone' ? fixtures.decisions : undefined)) : undefined;
    if (!known || source === undefined) {
      violations.push(`no fixture for ${method} ${url.pathname}`);
      await route.fulfill({ status: 404, headers: { ...CORS, 'content-type': 'application/json' }, body: JSON.stringify({ error: { code: 404, message: 'Not mocked.' } }) });
      return;
    }
    const n = calls.get(known[2]) ?? 0;
    calls.set(known[2], n + 1);
    const fixture = pick(source, recorded, n);
    await route.fulfill({
      status: fixture.status,
      headers: { ...fixture.headers },
      body: typeof fixture.body === 'string' ? fixture.body : JSON.stringify(fixture.body),
    });
  };

  await page.route(`${OPENROUTER_ORIGIN}/**`, handler);
  return {
    requests,
    violations,
    assertClean() {
      expect(violations, 'requests to openrouter.ai that break the network invariant (D138)').toEqual([]);
    },
  };
}
