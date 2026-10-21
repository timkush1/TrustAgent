/**
 * Helpers for specs that need a running stack (`make demo`). In CI every prerequisite is provided,
 * so a check that cannot run fails there instead of skipping (D18): a green gate always tested
 * something.
 */
import AxeBuilder from '@axe-core/playwright';
import { expect, test, type Page, type PlaywrightWorkerArgs, type TestInfo } from '@playwright/test';

const SIGN_IN_TIMEOUT = 30_000;

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

/**
 * Skips the running test with `reason` when `condition` holds, or fails it in CI, like
 * requireStack. Call it inside a test body.
 */
export function skipOrFail(condition: boolean, reason: string): void {
  if (!condition) return;
  if (process.env.CI) throw new Error(`This check cannot run: ${reason}`);
  console.warn(`[skipped] ${reason}`);
  test.skip(true, reason);
}

/**
 * Headers for a cookie-authenticated write sent through `page.request` (a spec's cleanup): the
 * CSRF header and the dashboard's own Origin. Playwright's request context sends no Origin, and
 * the proxy refuses a cookie write without an allow-listed one (403 csrf_failed), so a cleanup
 * without it fails quietly and leaves the stack changed.
 */
export function cookieWriteHeaders(): Record<string, string> {
  return { 'X-TrustAgent-CSRF': '1', Origin: new URL(String(test.info().project.use.baseURL)).origin };
}

/** Whether signInAsAdmin has a way in: an admin key or the one-time setup link. */
export function canSignInAsAdmin(): boolean {
  return Boolean(process.env.TRUSTAGENT_ADMIN_KEY || process.env.TRUSTAGENT_SETUP_URL);
}

/** The admin key this worker created by claiming the setup link, for its later sign-ins. */
let claimedAdminKey: string | null = null;

/**
 * Signs `page` in as an admin through /app/setup. TRUSTAGENT_ADMIN_KEY (a key from
 * TRUTHTABLE_ADMIN_KEYS) is preferred: it works any number of times. TRUSTAGENT_SETUP_URL (the link
 * of the proxy's one-time `SETUP: open …/app/setup#token=…` line; `make demo-setup-url` prints a
 * fresh one) works once per stack: the admin key it creates is kept for this worker's later
 * sign-ins, but a retry in a new worker cannot claim it again and needs TRUSTAGENT_ADMIN_KEY.
 */
export async function signInAsAdmin(page: Page): Promise<void> {
  const key = process.env.TRUSTAGENT_ADMIN_KEY || claimedAdminKey;
  const setupUrl = process.env.TRUSTAGENT_SETUP_URL;
  if (key) {
    await page.goto('/app/setup');
    // Exact names: the form itself is labelled "Sign in with a key".
    await page.getByRole('textbox', { name: 'Key', exact: true }).fill(key);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  } else if (setupUrl) {
    // Only the fragment matters: the page is served by the dashboard under test.
    await page.goto(`/app/setup${new URL(setupUrl).hash}`);
    await page.getByRole('button', { name: 'Create the admin key' }).click();
    const created = page.getByText('Your admin key (shown once)');
    await expect(created, 'the setup link works once per stack; set TRUSTAGENT_ADMIN_KEY to sign in again').toBeVisible({
      timeout: SIGN_IN_TIMEOUT,
    });
    claimedAdminKey = (await created.locator('xpath=..').locator('code').textContent())?.trim() || null;
  } else {
    throw new Error('Set TRUSTAGENT_ADMIN_KEY (a key from TRUTHTABLE_ADMIN_KEYS) or TRUSTAGENT_SETUP_URL to sign in as admin.');
  }
  await expect(page.getByText(/Admin actions are available/)).toBeVisible({ timeout: SIGN_IN_TIMEOUT });
}
