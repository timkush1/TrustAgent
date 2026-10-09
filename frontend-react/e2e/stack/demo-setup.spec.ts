/**
 * The first-run wizard on the demo stack (SPEC §3.3; docs/DEMO.md): `make demo` opens /app/setup
 * with its one-time link, so the wizard is the demo's first page and must never dead-end.
 *
 * Signed in as an admin, step 2 says the demo already runs simulated checks (nothing to connect,
 * no disabled ways in) and Next opens step 3; the refund example is checked there (gated like the
 * Playground, not like a settings write), and "Go to Overview" reaches the dashboard.
 *
 * Needs the demo stack and an admin key (TRUSTAGENT_ADMIN_KEY) or its setup link
 * (TRUSTAGENT_SETUP_URL); without either the test skips with the reason, and fails in CI.
 */
import { expect, test } from '@playwright/test';
import { canSignInAsAdmin, requireStack, seriousViolations, signInAsAdmin, skipOrFail } from './stack';

const TIMEOUT = 30_000;

test.describe('demo setup', () => {
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStack(playwright, testInfo);
  });

  test('demo setup: step 2 needs nothing, step 3 checks the refund answer, then the dashboard', async ({ page }) => {
    const health = await page.request.get('/health', { failOnStatusCode: false });
    const demo = ((await health.json().catch(() => null)) as { demo?: unknown } | null)?.demo === true;
    skipOrFail(!demo, 'this spec needs the demo stack (make demo)');
    skipOrFail(!canSignInAsAdmin(), 'set TRUSTAGENT_ADMIN_KEY (or TRUSTAGENT_SETUP_URL) to sign in as admin');
    await signInAsAdmin(page);
    // The demo promises only what it allows: no keys to connect there.
    await expect(page.getByText(/^Admin actions are available: edit policies, run the Playground/)).toBeVisible();

    await page.goto('/app/setup?step=connect');
    const steps = page.getByRole('navigation', { name: 'Setup steps' });
    await expect(page.getByRole('status').filter({ hasText: 'Nothing to connect: the demo already runs simulated checks.' })).toBeVisible({ timeout: TIMEOUT });
    await expect(page.getByRole('button', { name: 'Connect OpenRouter' })).toHaveCount(0);
    await expect(steps.getByRole('link', { name: /Connect OpenRouter.*Done/ })).toBeVisible();
    expect(await seriousViolations(page)).toEqual([]);
    const next = page.getByRole('button', { name: 'Next' });
    await expect(next).toHaveAttribute('aria-disabled', 'false');
    await next.click();

    await expect(page).toHaveURL(/\/app\/setup\?step=try$/);
    await expect(page.getByText('Checked against', { exact: true })).toBeVisible({ timeout: TIMEOUT });
    const check = page.getByRole('button', { name: 'Check this answer' });
    await expect(check).toHaveAttribute('aria-disabled', 'false');
    await check.click();
    const timeline = page.getByRole('list', { name: 'What happened to the request' });
    await expect(timeline).toBeVisible({ timeout: TIMEOUT });
    // The scripted refund answer (60 days against a 30-day policy) is flagged.
    await expect(timeline).toContainText(/flag/i, { timeout: TIMEOUT });
    await expect(page.getByRole('link', { name: /Open the trace/ })).toHaveAttribute('href', /^\/app\/traffic\//);
    expect(await seriousViolations(page)).toEqual([]);

    await page.getByRole('link', { name: /Go to Overview/ }).click();
    await expect(page).toHaveURL(/\/app\/overview/);
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible({ timeout: TIMEOUT });
  });
});
