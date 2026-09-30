/**
 * The static site's security posture (SPEC §7.5 "Static GitHub Pages demo"): every page carries
 * the CSP meta and `referrer: no-referrer`, has no inline script, reports no CSP violation, and
 * talks only to itself (openrouter.ai joins the allowed list with the P5 live playground; nothing
 * calls it yet). No third-party scripts, fonts or images.
 */
import { expect, test, type Page } from '@playwright/test';
import { STATIC_CSP } from '../../vite.site';
import { recorded, requireStaticSite, watchNetwork } from './site';

async function expectHardened(page: Page, what: string): Promise<void> {
  await expect(page.locator('meta[http-equiv="Content-Security-Policy"]'), `${what}: CSP meta`).toHaveAttribute('content', STATIC_CSP);
  await expect(page.locator('meta[name="referrer"]'), `${what}: referrer meta`).toHaveAttribute('content', 'no-referrer');
  expect(await page.locator('script:not([src])').count(), `${what}: inline scripts`).toBe(0);
}

test.describe('CSP and network', () => {
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStaticSite(playwright, testInfo);
  });

  test('the CSP is the one SPEC §7.5 sets', () => {
    expect(STATIC_CSP).toBe(
      "default-src 'self'; connect-src 'self' https://openrouter.ai; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'",
    );
  });

  test('every page carries the CSP, and the site only contacts itself', async ({ page, baseURL }) => {
    const origin = new URL(String(baseURL)).origin;
    const { refundTraceId } = await recorded(page);
    const net = await watchNetwork(page);

    await page.goto('');
    await expect(page.locator('[data-recording-label]')).toBeVisible();
    await page.locator('#cost').scrollIntoViewIfNeeded();
    await expect(page.getByRole('heading', { name: 'Estimate your monthly bill' })).toBeVisible();
    await expectHardened(page, 'landing');

    await page.goto('app/traffic/');
    await expect(page.locator('[data-replay-status="finished"]')).toBeVisible({ timeout: 30_000 });
    await expectHardened(page, 'app');

    await page.goto('app/overview/');
    await expect(page.getByText(/\d+ requests on \d+ \w+ \d{4}/)).toBeVisible();

    // A deep link is served by 404.html, which must be just as locked down.
    await page.goto(`app/traffic/${refundTraceId}`);
    await expect(page.getByRole('region', { name: 'Answer' }).locator('mark[data-v="contradicted"]')).toBeVisible({ timeout: 30_000 });
    await expectHardened(page, '404.html');

    expect(await net.violations(), 'CSP violations').toEqual([]);
    const elsewhere = net.urls.filter((u) => !u.startsWith('data:') && new URL(u).origin !== origin);
    expect(elsewhere, 'requests that leave the site').toEqual([]);
    expect(net.urls.some((u) => u.includes('/demo/recorded/manifest.json'))).toBe(true);
    expect(net.urls.some((u) => /\.woff2$/.test(u)), 'self-hosted fonts').toBe(true);
  });
});
