/**
 * "Try Jev live" in the static Playground (D99-D106, LV-7), against a mocked openrouter.ai
 * (e2e/static/openrouter.ts: the synthetic vendor fixtures, CORS as the real API answers it, and a
 * check that every request sends only ALLOWED_REQUEST_HEADERS). Nothing reaches the internet.
 *
 * Consent, Connect, the callback and a live check of the refund example; a reload keeps the key and
 * a new context has none; Forget key and a bfcache restore; the Paste path; a 402; an error the
 * browser cannot read; and every live surface at 400 px, in the dark theme and through axe.
 */
import { expect, test, type Page } from '@playwright/test';
import { seriousViolations } from '../stack/stack';
import { mockOpenRouter, vendorFixture, type OpenRouterMock, type RecordedRequest, type VendorFixture } from './openrouter';
import { requireStaticSite, useExternalGuard } from './site';

const KEY = 'sk-or-v1-e2e-0123456789abcdef0123456789abcdef';
const REFUND = 'app/playground/?scenario=refund-contradiction-flag#live';

/** A decisions answer for every question the request asked, all at `noul`. */
function answers(noul: number) {
  return (request: RecordedRequest): VendorFixture => {
    const base = vendorFixture('decisions.200');
    const asked = (JSON.parse(request.body ?? '{}') as { questions?: Record<string, { type: string }> }).questions ?? {};
    return { ...base, body: { ...(base.body as object), answers: Object.fromEntries(Object.entries(asked).map(([q, v]) => [q, { type: v.type, noul }])) } };
  };
}

async function liveMock(page: Page, decisions: Parameters<typeof mockOpenRouter>[1]['decisions'] = answers(0.97)): Promise<OpenRouterMock> {
  return mockOpenRouter(page, { decisions, auth_keys: vendorFixture('auth_keys.200'), key: vendorFixture('key.200') });
}

const live = (page: Page) => page.getByRole('region', { name: 'Check an answer live, with your own key' });
const runButton = (page: Page) => live(page).getByRole('button', { name: 'Run the check live' });

/** Pastes a key through the consent dialog (Enter, no form). */
async function pasteKey(page: Page, key = KEY): Promise<void> {
  await runButton(page).click();
  const dialog = page.getByRole('dialog', { name: 'Run the fast check with your OpenRouter key' });
  await dialog.getByLabel('Your OpenRouter key').fill(key);
  await dialog.getByLabel('Your OpenRouter key').press('Enter');
  await expect(dialog).toBeHidden();
}

test.describe('live Playground', () => {
  useExternalGuard();
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStaticSite(playwright, testInfo);
  });

  test('consent, Connect, the callback, then a live check of the refund example', async ({ page }) => {
    const mock = await liveMock(page);
    const visited: string[] = [];
    page.on('framenavigated', (f) => {
      if (f === page.mainFrame()) visited.push(f.url());
    });
    await page.goto(REFUND);
    await expect(live(page).getByRole('radio', { checked: true })).toHaveAccessibleName(/Refund window/);
    expect(mock.requests, 'nothing reaches openrouter.ai before consent').toEqual([]);

    await runButton(page).click();
    const dialog = page.getByRole('dialog', { name: 'Run the fast check with your OpenRouter key' });
    await expect(dialog.getByText('Your key stays in this browser tab and is sent only to openrouter.ai.', { exact: false })).toBeVisible();
    await dialog.getByRole('button', { name: 'Connect OpenRouter' }).click();

    // Back on the Playground: the callback exchanged the code and replaced itself.
    await expect(page).toHaveURL(/\/app\/playground\/?\?scenario=refund-contradiction-flag#live$/);
    await expect(page.getByTestId('live-key-chip')).toBeVisible();
    const exchange = mock.requests.find((r) => r.endpoint === 'auth_keys');
    expect(exchange, 'the code exchange').toBeTruthy();
    expect(JSON.parse(exchange?.body ?? '{}')).toMatchObject({ code: 'e2e-code-1', code_challenge_method: 'S256' });
    expect(Object.keys(exchange?.headers ?? {})).not.toContain('authorization');
    expect(visited.some((u) => u.includes('/connect/callback/'))).toBe(true);
    expect(page.url()).not.toContain('code=');

    await runButton(page).click();
    const result = live(page).getByTestId('live-result');
    await expect(result).toBeVisible();
    await expect(result.getByText('Flagged').first()).toBeVisible();
    await expect(result.getByText('Contradicts your documents')).toBeVisible();
    await expect(result.getByTestId('live-check-cost')).toContainText('This check: $0.0000');
    await expect(result.getByTestId('live-label')).toHaveText(/^Live · typesafe\/jev-1\.13-20260917 · uncalibrated · your key$/);
    await expect(live(page).getByTestId('live-counter')).toHaveText('1 of 100 calls to OpenRouter (checks and AI drafts)');
    const decision = mock.requests.find((r) => r.endpoint === 'decisions');
    expect(decision?.headers.authorization).toBe(`Bearer ${(vendorFixture('auth_keys.200').body as { key: string }).key}`);
    expect(JSON.parse(decision?.body ?? '{}')).not.toHaveProperty('session_id');
    mock.assertClean();
  });

  test('a reload keeps the key; a new browser context has none', async ({ page, browser, baseURL }) => {
    await liveMock(page);
    await page.goto(REFUND);
    await pasteKey(page);
    await expect(page.getByTestId('live-key-chip')).toBeVisible();
    await page.reload();
    await expect(page.getByTestId('live-key-chip')).toBeVisible();
    await expect(live(page).getByTestId('live-counter')).toBeVisible();

    const other = await browser.newContext({ baseURL });
    const fresh = await other.newPage();
    await fresh.route((url) => url.hostname !== 'localhost', (route) => route.abort('blockedbyclient'));
    await fresh.goto(REFUND);
    await expect(live(fresh)).toBeVisible();
    await expect(fresh.getByTestId('live-key-chip')).toHaveCount(0);
    expect(await fresh.evaluate(() => sessionStorage.length)).toBe(0);
    await other.close();
  });

  test('Forget key clears sessionStorage, and Back from the bfcache shows no key', async ({ page }) => {
    await liveMock(page);
    await page.goto(REFUND);
    await pasteKey(page);
    await expect(page.getByTestId('live-key-chip')).toBeVisible();
    await page.goto('app/overview/');
    await expect(page.getByTestId('live-key-chip')).toBeVisible();
    await page.getByTestId('live-key-chip').getByRole('button', { name: 'Forget key' }).click();
    await expect(page.getByTestId('live-key-chip')).toHaveCount(0);
    expect(await page.evaluate(() => Object.keys(sessionStorage).filter((k) => k.startsWith('ta.openrouter') || k.startsWith('ta.pkce')))).toEqual([]);
    await page.goBack();
    await expect(page).toHaveURL(/\/app\/playground\/?\?scenario=/);
    await expect(live(page)).toBeVisible();
    await expect(page.getByTestId('live-key-chip')).toHaveCount(0);
    await expect(page.locator('[data-testid="live-panel"]')).toHaveAttribute('data-key-state', 'none');
  });

  test('Paste: Enter checks the key and never navigates', async ({ page }) => {
    const mock = await liveMock(page);
    await page.goto(REFUND);
    await expect(live(page)).toBeVisible();
    const before = page.url();
    await pasteKey(page);
    expect(page.url()).toBe(before);
    expect(mock.requests.filter((r) => r.endpoint === 'key')).toHaveLength(1);
    await expect(live(page).getByTestId('live-no-limit')).toBeVisible();
    mock.assertClean();
  });

  test('a 402 shows the credits screen and leaves the counter unchanged', async ({ page }) => {
    const mock = await liveMock(page, vendorFixture('decisions.402_credits'));
    await page.goto(REFUND);
    await pasteKey(page);
    await runButton(page).click();
    const screen402 = live(page).getByTestId('live-no-credits');
    await expect(screen402).toBeVisible();
    await expect(screen402.getByRole('link', { name: /Add credits/ })).toHaveAttribute('href', 'https://openrouter.ai/settings/credits');
    await expect(live(page).getByTestId('live-counter')).toHaveText('0 of 100 calls to OpenRouter (checks and AI drafts)');
    await expect(page.getByText('Insufficient credits')).toHaveCount(0);
    mock.assertClean();
  });

  test('an error the browser cannot read (no ACAO) says the browser blocked it, nothing billed', async ({ page }) => {
    // A vendor error without Access-Control-Allow-Origin never reaches the page: the browser turns it
    // into a bare TypeError. Playwright does not apply CORS to a fulfilled response, so the request
    // is failed outright, which is what the page sees.
    const mock = await liveMock(page);
    await page.route('https://openrouter.ai/api/alpha/decisions', (route) => (route.request().method() === 'POST' ? route.abort('failed') : route.fallback()));
    await page.goto(REFUND);
    await pasteKey(page);
    await runButton(page).click();
    const error = live(page).getByTestId('live-error');
    await expect(error).toContainText('Could not reach openrouter.ai.');
    await expect(error).toContainText('Nothing was billed.');
    await expect(error.getByRole('button', { name: 'Retry' })).toBeVisible();
    await expect(live(page).getByTestId('live-counter')).toHaveText('0 of 100 calls to OpenRouter (checks and AI drafts)');
    mock.assertClean();
  });

  test('every live surface fits 400 px', async ({ page }) => {
    await liveMock(page);
    await page.setViewportSize({ width: 400, height: 860 });
    const noSideways = async (what: string) => {
      const [scrollWidth, width] = await page.evaluate(() => [document.documentElement.scrollWidth, window.innerWidth]);
      expect(scrollWidth, `${what}: no horizontal page scroll`).toBeLessThanOrEqual(width);
    };
    await page.goto(REFUND);
    await expect(live(page)).toBeVisible();
    await noSideways('the live section');
    await runButton(page).click();
    await expect(page.getByRole('dialog')).toBeVisible();
    await noSideways('the consent dialog');
    await page.keyboard.press('Escape');
    await pasteKey(page);
    await runButton(page).click();
    await expect(live(page).getByTestId('live-result')).toBeVisible();
    await live(page).getByText('Show exact Jev request').click();
    await noSideways('a live result');
    await live(page).getByRole('radio', { name: 'Your own text' }).check();
    await noSideways('the custom form');
    await page.goto('connect/callback/?code=x');
    await expect(page.getByTestId('callback-state')).toBeVisible();
    await noSideways('the callback');
  });

  for (const colorScheme of ['light', 'dark'] as const) {
    test(`has no serious accessibility violations (${colorScheme})`, async ({ page }) => {
      await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' });
      await liveMock(page);
      await page.goto(REFUND);
      await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme);
      await expect(live(page)).toBeVisible();
      expect(await seriousViolations(page), `live section (${colorScheme})`).toEqual([]);
      await runButton(page).click();
      await expect(page.getByRole('dialog')).toBeVisible();
      expect(await seriousViolations(page), `consent dialog (${colorScheme})`).toEqual([]);
      await page.keyboard.press('Escape');
      await pasteKey(page);
      await runButton(page).click();
      await expect(live(page).getByTestId('live-result')).toBeVisible();
      await live(page).getByText('Show exact Jev request').click();
      expect(await seriousViolations(page), `live result (${colorScheme})`).toEqual([]);
      await page.goto('connect/callback/?code=x');
      await expect(page.getByTestId('callback-state')).toBeVisible();
      expect(await seriousViolations(page), `callback (${colorScheme})`).toEqual([]);
    });
  }
});
