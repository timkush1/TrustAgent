/**
 * Helpers for specs that need a running stack (`make demo`).
 */
import AxeBuilder from '@axe-core/playwright';
import { test, type Page, type PlaywrightWorkerArgs, type TestInfo } from '@playwright/test';

/**
 * Checks that the dashboard answers and its /health reaches the proxy. With no stack the specs
 * are skipped with the reason; in CI (where the job starts the stack first) they fail instead,
 * so a stack that died cannot turn the gate green.
 */
export async function requireStack(playwright: PlaywrightWorkerArgs['playwright'], testInfo: TestInfo): Promise<void> {
  const baseURL = String(testInfo.project.use.baseURL ?? '');
  const request = await playwright.request.newContext({ baseURL });
  let problem: string | null = null;
  try {
    const res = await request.get('/health', { timeout: 5_000, failOnStatusCode: false });
    if (!res.ok()) {
      problem = `GET ${baseURL}/health answered HTTP ${res.status()}`;
    } else {
      const body: unknown = await res.json().catch(() => null);
      const status = typeof body === 'object' && body !== null ? (body as { status?: unknown }).status : undefined;
      if (status !== 'ok' && status !== 'healthy') problem = `GET ${baseURL}/health did not report ok`;
    }
  } catch (err) {
    problem = `nothing answers at ${baseURL} (${String(err instanceof Error ? err.message : err).split('\n')[0]})`;
  } finally {
    await request.dispose();
  }
  if (problem === null) return;
  const message =
    `No running TrustAgent stack: ${problem}. Start the demo with \`make demo\`, ` +
    'or point PLAYWRIGHT_BASE_URL at a running dashboard.';
  if (process.env.CI) throw new Error(message);
  // The list reporter does not print skip reasons; say it once per worker.
  console.warn(`[skipped] ${message}`);
  test.skip(true, message);
}

/** axe (WCAG 2.0/2.1/2.2 A and AA) violations of serious or critical impact, one line each. */
export async function seriousViolations(page: Page): Promise<string[]> {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'wcag22aa']).analyze();
  return results.violations
    .filter((v) => v.impact === 'serious' || v.impact === 'critical')
    .map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`);
}
