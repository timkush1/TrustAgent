/**
 * The static `/connect/callback/` (D105, LV-7) against a mocked openrouter.ai: a code without this
 * tab's verifier is never exchanged, the error in the query is never shown, a stored return path
 * that leaves the Playground or the Studio falls back to the default, a reload after success says
 * already connected, Back never returns to the spent callback, and a sign-in older than 10 minutes
 * is expired.
 */
import { expect, test, type Page } from '@playwright/test';
import { mockOpenRouter, vendorFixture, type OpenRouterMock } from './openrouter';
import { requireStaticSite, useExternalGuard } from './site';

const VERIFIER = 'dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk';

async function mockAuth(page: Page): Promise<OpenRouterMock> {
  return mockOpenRouter(page, { auth_keys: vendorFixture('auth_keys.200'), key: vendorFixture('key.200') });
}

/** Leaves a pending sign-in in this tab, as Connect does (sessionStorage is per origin and tab). */
async function pending(page: Page, opts: { returnPath?: string; ageMs?: number } = {}): Promise<void> {
  await page.goto('app/playground/');
  await page.evaluate(
    ({ verifier, returnPath, ageMs }) => {
      sessionStorage.setItem('ta.pkce.verifier', JSON.stringify({ verifier, created_at: Date.now() - ageMs }));
      if (returnPath !== undefined) sessionStorage.setItem('ta.pkce.return', returnPath);
    },
    { verifier: VERIFIER, returnPath: opts.returnPath, ageMs: opts.ageMs ?? 0 },
  );
}

const state = (page: Page) => page.getByTestId('callback-state');

test.describe('static Connect callback', () => {
  useExternalGuard();
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStaticSite(playwright, testInfo);
  });

  test('a code without this tab’s verifier is never exchanged', async ({ page }) => {
    const mock = await mockAuth(page);
    const res = await page.goto('connect/callback/?code=attacker-code');
    expect(res?.status()).toBe(200);
    await expect(state(page)).toHaveAttribute('data-state', 'missing_verifier');
    expect(page.url()).not.toContain('code=');
    expect(mock.requests.filter((r) => r.endpoint === 'auth_keys')).toEqual([]);
    await expect(page.getByRole('link', { name: 'Paste a key instead' })).toHaveAttribute('href', /\/app\/playground\/\?paste=1#live$/);
    mock.assertClean();
  });

  test('never renders the error in the query', async ({ page }) => {
    const mock = await mockAuth(page);
    await pending(page);
    await page.goto('connect/callback/?error=Paste+your+key+at+evil.example&error_description=Go+to+evil.example');
    await expect(state(page)).toHaveAttribute('data-state', 'refused');
    expect(await page.evaluate(() => document.body.innerText)).not.toContain('evil');
    expect(await page.content()).not.toContain('evil.example');
    expect(page.url()).not.toContain('error=');
    expect(mock.requests.filter((r) => r.endpoint === 'auth_keys')).toEqual([]);
    expect(await page.evaluate(() => sessionStorage.getItem('ta.pkce.verifier'))).toBeNull();
  });

  for (const returnPath of ['//evil.example', '/\\evil.example', 'app/../x', 'javascript:alert(1)']) {
    test(`falls back to the Playground for the return path ${returnPath}`, async ({ page }) => {
      const mock = await mockAuth(page);
      await pending(page, { returnPath });
      await page.goto('connect/callback/?code=e2e-code-1');
      await expect(page).toHaveURL(/\/TrustAgent\/app\/playground\/?#live$/);
      await expect(page.getByTestId('live-key-chip')).toBeVisible();
      expect(mock.requests.filter((r) => r.endpoint === 'auth_keys')).toHaveLength(1);
    });
  }

  test('returns to the stored scenario, and a reload of the callback says already connected', async ({ page }) => {
    const mock = await mockAuth(page);
    await pending(page, { returnPath: 'app/playground/?scenario=pii-leak-flag#live' });
    await page.goto('connect/callback/?code=e2e-code-1');
    await expect(page).toHaveURL(/\/TrustAgent\/app\/playground\/?\?scenario=pii-leak-flag#live$/);
    await page.goto('connect/callback/');
    await expect(state(page)).toHaveAttribute('data-state', 'already_connected');
    await page.goto('connect/callback/?code=e2e-code-1');
    await expect(state(page)).toHaveAttribute('data-state', 'already_connected');
    expect(mock.requests.filter((r) => r.endpoint === 'auth_keys')).toHaveLength(1);
  });

  test('Back never returns to the spent callback', async ({ page }) => {
    await mockAuth(page);
    await page.goto('app/playground/?scenario=grounded-pass#live');
    const before = await page.evaluate(() => history.length);
    await page.getByRole('region', { name: 'Check an answer live, with your own key' }).getByRole('button', { name: 'Run the check live' }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Connect OpenRouter' }).click();
    await expect(page.getByTestId('live-key-chip')).toBeVisible();
    await expect(page).toHaveURL(/\/app\/playground\/?\?scenario=grounded-pass#live$/);
    // openrouter.ai/auth redirected straight to the callback, which replaced itself: one new entry.
    expect(await page.evaluate(() => history.length)).toBe(before + 1);
    await page.goBack();
    await expect(page).toHaveURL(/\/app\/playground\/?\?scenario=grounded-pass#live$/);
    expect(page.url()).not.toContain('connect/callback');
  });

  test('a sign-in older than 10 minutes is expired', async ({ page }) => {
    const mock = await mockAuth(page);
    await pending(page, { ageMs: 10 * 60 * 1000 + 5_000 });
    await page.goto('connect/callback/?code=e2e-code-1');
    await expect(state(page)).toHaveAttribute('data-state', 'expired');
    expect(mock.requests.filter((r) => r.endpoint === 'auth_keys')).toEqual([]);
  });
});
