/**
 * Frame-busting (D106, LV-7): GitHub Pages sends no X-Frame-Options and a meta CSP cannot set
 * frame-ancestors, so the key surfaces check for themselves. Inside an iframe the live section and
 * the callback offer only "Open in its own tab": no key input, no Connect, no Run, no exchange.
 */
import { expect, test } from '@playwright/test';
import { mockOpenRouter, vendorFixture } from './openrouter';
import { requireStaticSite, useExternalGuard } from './site';

test.describe('framed', () => {
  useExternalGuard();
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStaticSite(playwright, testInfo);
  });

  test('the live section offers only Open in its own tab', async ({ page, baseURL }) => {
    const mock = await mockOpenRouter(page, { auth_keys: vendorFixture('auth_keys.200'), key: vendorFixture('key.200') });
    await page.goto('');
    await page.setContent(`<iframe title="framed" src="${baseURL}app/playground/#live" style="width:1000px;height:900px"></iframe>`);
    const frame = page.frameLocator('iframe[title="framed"]');
    const live = frame.getByRole('region', { name: 'Check an answer live, with your own key' });
    await expect(live.getByTestId('live-framed')).toBeVisible({ timeout: 30_000 });
    await expect(live.getByRole('link', { name: 'Open in its own tab' })).toHaveAttribute('target', '_blank');
    await expect(live.getByRole('link', { name: 'Open in its own tab' })).toHaveAttribute('rel', 'noopener noreferrer');
    await expect(live.getByRole('button', { name: 'Run the check live' })).toHaveCount(0);
    await expect(live.getByRole('radiogroup')).toHaveCount(0);
    await expect(frame.locator('input[type="password"]')).toHaveCount(0);
    expect(mock.requests).toEqual([]);
  });

  test('the callback exchanges nothing', async ({ page, baseURL }) => {
    const mock = await mockOpenRouter(page, { auth_keys: vendorFixture('auth_keys.200') });
    await page.goto('');
    await page.setContent(`<iframe title="framed" src="${baseURL}connect/callback/?code=e2e-code-1"></iframe>`);
    const frame = page.frameLocator('iframe[title="framed"]');
    await expect(frame.getByTestId('callback-state')).toHaveAttribute('data-state', 'framed', { timeout: 30_000 });
    await expect(frame.getByRole('link', { name: 'Open in its own tab' })).toHaveAttribute('rel', 'noopener noreferrer');
    expect(mock.requests).toEqual([]);
  });
});
