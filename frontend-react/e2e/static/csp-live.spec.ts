/**
 * The live mode's network and secrecy invariants (D100, D102, D138; LV-7) against a mocked
 * openrouter.ai:
 *
 * - nothing reaches openrouter.ai before consent;
 * - the page contacts only itself and https://openrouter.ai;
 * - `Authorization` goes only to openrouter.ai;
 * - the key and the no-pattern secret never appear in a same-origin URL or body, the page text, the
 *   DOM or the console;
 * - localStorage and cookies hold no key;
 * - no CSP violation anywhere, the callback included.
 */
import { expect, test, type Request } from '@playwright/test';
import { mockOpenRouter, vendorFixture, type RecordedRequest, type VendorFixture } from './openrouter';
import { requireStaticSite, useExternalGuard, watchNetwork } from './site';

const KEY = 'sk-or-v1-e2e-0123456789abcdef0123456789abcdef';
const NO_PATTERN = 'cmp0e2eZq7Lw4Rt9Yx2Vb6Nm';
const OPENROUTER = 'https://openrouter.ai';

function answers(request: RecordedRequest): VendorFixture {
  const base = vendorFixture('decisions.200');
  const asked = (JSON.parse(request.body ?? '{}') as { questions?: Record<string, { type: string }> }).questions ?? {};
  return { ...base, body: { ...(base.body as object), answers: Object.fromEntries(Object.entries(asked).map(([q, v]) => [q, { type: v.type, noul: 0.9 }])) } };
}

test.describe('live mode: network and secrets', () => {
  useExternalGuard();
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStaticSite(playwright, testInfo);
  });

  test('nothing reaches openrouter.ai before consent', async ({ page }) => {
    const mock = await mockOpenRouter(page, { decisions: answers, key: vendorFixture('key.200') });
    await page.goto('app/playground/?scenario=pii-leak-flag#live');
    const live = page.getByRole('region', { name: 'Check an answer live, with your own key' });
    await live.getByRole('radio', { name: 'Your own text' }).check();
    await live.getByRole('radio', { name: /Refund window/ }).check();
    await live.getByRole('button', { name: 'Run the check live' }).click();
    await expect(page.getByRole('dialog')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toBeHidden();
    await page.goto('app/overview/');
    await page.goto('connect/callback/');
    expect(mock.requests, 'requests to openrouter.ai before consent').toEqual([]);
  });

  test('the key goes only to openrouter.ai and never into the page, storage or console', async ({ page, baseURL, context }) => {
    const origin = new URL(String(baseURL)).origin;
    const mock = await mockOpenRouter(page, { decisions: answers, key: vendorFixture('key.200'), auth_keys: vendorFixture('auth_keys.200') });
    const net = await watchNetwork(page);
    const requests: Request[] = [];
    page.on('request', (r) => requests.push(r));
    const consoleLines: string[] = [];
    page.on('console', (m) => consoleLines.push(m.text()));
    page.on('pageerror', (e) => consoleLines.push(e.message));

    await page.goto('app/playground/?scenario=refund-contradiction-flag#live');
    const live = page.getByRole('region', { name: 'Check an answer live, with your own key' });
    await live.getByRole('button', { name: 'Run the check live' }).click();
    const input = page.getByRole('dialog').getByLabel('Your OpenRouter key');
    // A secret without the key shape is refused before any request and never echoed.
    await input.fill(NO_PATTERN);
    await input.press('Enter');
    await expect(page.getByRole('dialog').getByRole('alert')).toBeVisible();
    await input.fill(KEY);
    await input.press('Enter');
    await expect(page.getByRole('dialog')).toBeHidden();
    await live.getByRole('button', { name: 'Run the check live' }).click();
    await expect(live.getByTestId('live-result')).toBeVisible();
    await live.getByText('Show exact Jev request').click();
    await expect(live.getByTestId('jev-request')).toBeVisible();
    // With the result and the exact request on screen, as well as after a reload below.
    for (const secret of [KEY, NO_PATTERN]) {
      expect(await page.evaluate(() => document.body.innerText), 'the page text with a result').not.toContain(secret);
      expect(await page.content(), 'the DOM with a result').not.toContain(secret);
    }

    // The violation log is per page load: read it before every navigation.
    expect(await net.violations(), 'CSP violations on the Playground').toEqual([]);
    // The callback, too, runs under the CSP without a violation.
    await page.goto('connect/callback/?code=e2e-code-1');
    await expect(page.getByTestId('callback-state')).toBeVisible();
    expect(await net.violations(), 'CSP violations on the callback').toEqual([]);
    const elsewhere = net.urls.filter((u) => !u.startsWith('data:') && new URL(u).origin !== origin && new URL(u).origin !== OPENROUTER);
    expect(elsewhere, 'requests to anything but this site and openrouter.ai').toEqual([]);

    for (const r of requests) {
      const headers = await r.allHeaders();
      const toOpenRouter = new URL(r.url()).origin === OPENROUTER;
      if (!toOpenRouter) expect(headers.authorization, `Authorization on ${r.url()}`).toBeUndefined();
      if (new URL(r.url()).origin === origin) {
        for (const secret of [KEY, NO_PATTERN]) {
          expect(r.url(), 'a same-origin URL').not.toContain(secret);
          expect(r.postData() ?? '', 'a same-origin body').not.toContain(secret);
        }
      }
    }
    const withKey = mock.requests.filter((r) => r.headers.authorization === `Bearer ${KEY}`).map((r) => r.endpoint);
    expect(withKey).toEqual(expect.arrayContaining(['key', 'decisions']));

    await page.goto('app/playground/#live');
    await expect(page.getByTestId('live-key-chip')).toBeVisible();
    expect(await net.violations(), 'CSP violations after a reload with a key').toEqual([]);
    for (const secret of [KEY, NO_PATTERN]) {
      expect(await page.evaluate(() => document.body.innerText), 'the page text').not.toContain(secret);
      expect(await page.content(), 'the DOM').not.toContain(secret);
      expect(consoleLines.join('\n'), 'the console').not.toContain(secret);
    }
    const stored = await page.evaluate(() => ({ local: JSON.stringify({ ...localStorage }), cookie: document.cookie }));
    expect(stored.local).not.toContain(KEY);
    expect(stored.local).not.toMatch(/ta\.(openrouter|pkce)\./);
    expect(stored.cookie).not.toContain(KEY);
    expect((await context.cookies()).map((c) => c.value).join(' ')).not.toContain(KEY);
    mock.assertClean();
  });
});
