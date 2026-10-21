/**
 * Helpers for specs against the static GitHub Pages build (`npm run build:static`, served by
 * `vite preview`). Paths are relative to the base URL (…/TrustAgent/): never start one with "/".
 */
import { expect, test, type Page, type PlaywrightWorkerArgs, type TestInfo } from '@playwright/test';

export const MOCK_LABEL = 'Simulated (mock checks + mock judge): not model output';

/**
 * Checks that a static build answers at the base URL. Without one the specs are skipped with the
 * reason; in CI they fail instead, so a missing build cannot turn the gate green.
 */
export async function requireStaticSite(playwright: PlaywrightWorkerArgs['playwright'], testInfo: TestInfo): Promise<void> {
  const baseURL = String(testInfo.project.use.baseURL ?? '');
  const request = await playwright.request.newContext({ baseURL });
  let problem: string | null = null;
  try {
    const [landing, app] = await Promise.all([
      request.get('', { timeout: 5_000, failOnStatusCode: false }),
      request.get('app/', { timeout: 5_000, failOnStatusCode: false }),
    ]);
    if (!landing.ok() || !app.ok()) problem = `${baseURL} answered HTTP ${landing.status()} (landing) and ${app.status()} (app/)`;
    else if (!(await landing.text()).includes('Content-Security-Policy')) problem = `${baseURL} is not a static build (no CSP meta)`;
  } catch (err) {
    problem = `nothing answers at ${baseURL} (${String(err instanceof Error ? err.message : err).split('\n')[0]})`;
  } finally {
    await request.dispose();
  }
  if (problem === null) return;
  const message =
    `No static build: ${problem}. Run \`make test-e2e-static\`, or \`npm run build:static\` then ` +
    '`PLAYWRIGHT_STATIC_PREVIEW=1 npx playwright test --project=static`.';
  if (process.env.CI) throw new Error(message);
  console.warn(`[skipped] ${message}`);
  test.skip(true, message);
}

export interface Recorded {
  refundTraceId: string;
  traces: number;
}

/** The committed recording's refund trace (flagged, contradicted) and trace count. */
export async function recorded(page: Page): Promise<Recorded> {
  const res = await page.request.get('demo/manifest.json');
  const manifest = (await res.json()) as { traces: { trace_id: string; action: string | null; audit: { verdict: string | null } }[] };
  const refund = manifest.traces.find((t) => t.action === 'flag' && t.audit.verdict === 'HALLUCINATED');
  if (!refund) throw new Error('the recording has no flagged, hallucinated trace');
  return { refundTraceId: refund.trace_id, traces: manifest.traces.length };
}

/** Records every request the page makes and every CSP violation it reports. */
export async function watchNetwork(page: Page): Promise<{ urls: string[]; violations: () => Promise<string[]> }> {
  const urls: string[] = [];
  page.on('request', (r) => urls.push(r.url()));
  await page.addInitScript(() => {
    const w = window as unknown as { __csp: string[] };
    w.__csp = [];
    document.addEventListener('securitypolicyviolation', (e) => w.__csp.push(`${e.violatedDirective} ${e.blockedURI}`));
  });
  return {
    urls,
    violations: () => page.evaluate(() => (window as unknown as { __csp?: string[] }).__csp ?? []),
  };
}

/**
 * Aborts every request the page makes to another origin than the site's, unless a route registered
 * later handles it (mockOpenRouter in openrouter.ts: Playwright tries the newest route first), and
 * returns the URLs it blocked. No static spec reaches the internet (Rule 6, D138).
 */
export async function blockUnmockedExternal(page: Page, baseURL?: string): Promise<string[]> {
  const blocked: string[] = [];
  await page.route(
    (url) => {
      const origin = baseURL ? new URL(baseURL).origin : null;
      return url.protocol.startsWith('http') && (origin === null ? !/^(localhost|127\.0\.0\.1)$/.test(url.hostname) : url.origin !== origin);
    },
    async (route) => {
      blocked.push(route.request().url());
      await route.abort('blockedbyclient');
    },
  );
  return blocked;
}

/**
 * blockUnmockedExternal for every test of the calling describe block: a test whose page reached an
 * unmocked external URL fails.
 */
export function useExternalGuard(): void {
  let blocked: string[] = [];
  test.beforeEach(async ({ page, baseURL }) => {
    blocked = await blockUnmockedExternal(page, baseURL);
  });
  test.afterEach(() => {
    expect(blocked, 'requests to an unmocked external origin').toEqual([]);
  });
}
