/**
 * The landing page (SPEC §8.1): the root of the static site, at most seven sections, every number
 * with its label chip, the replay one click away, both themes and a 400 px screen.
 */
import { expect, test } from '@playwright/test';
import { seriousViolations } from '../stack/stack';
import { MOCK_LABEL, requireStaticSite } from './site';

test.describe('landing', () => {
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStaticSite(playwright, testInfo);
  });

  test('renders the promise, the refund card and at most seven sections', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (e) => errors.push(e.message));
    await page.goto('');
    await expect(page).toHaveTitle(/TrustAgent/);
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Catch hallucinations before your users do.');
    const sections = page.locator('main > section');
    expect(await sections.count()).toBeGreaterThanOrEqual(5);
    expect(await sections.count()).toBeLessThanOrEqual(7);

    // The hero card's third beat: "60 days" gets its mark next to the evidence.
    await expect(page.locator('mark.hero-mark[data-on]')).toHaveText('60 days', { timeout: 10_000 });
    await expect(page.getByText('refund-policy.md').first()).toBeVisible();

    // Every number chip names its kind; ours are "not yet measured" until a benchmark run.
    const chips = page.locator('[data-value-label]');
    expect(await chips.count()).toBeGreaterThan(10);
    await expect(page.locator('[data-value-label="measured"]')).toHaveCount(0);
    await expect(page.getByText('not yet measured').first()).toBeVisible();

    // The replay card reads the recording's own label.
    await expect(page.locator('[data-recording-label]')).toHaveText(MOCK_LABEL);
    expect(errors).toEqual([]);
  });

  test('"Try the demo" opens the replay', async ({ page }) => {
    await page.goto('');
    await page.getByRole('main').getByRole('link', { name: 'Try the demo' }).click();
    await expect(page).toHaveURL(/\/TrustAgent\/app\/traffic\/?$/);
    await expect(page.getByRole('table', { name: 'Requests' })).toBeVisible();
  });

  for (const colorScheme of ['light', 'dark'] as const) {
    test(`has no serious accessibility violations (${colorScheme})`, async ({ page }) => {
      await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' });
      await page.goto('');
      await expect(page.locator('[data-recording-label]')).toBeVisible();
      await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme);
      // Open the sources list too, so its text is checked.
      await page.getByText('Where every number on this page comes from').click();
      await expect(page.locator('#sources')).toHaveAttribute('open', '');
      expect(await seriousViolations(page), `landing (${colorScheme})`).toEqual([]);
    });
  }

  test('fits a 400 px wide screen', async ({ page }) => {
    await page.setViewportSize({ width: 400, height: 860 });
    await page.goto('');
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    await page.locator('footer').scrollIntoViewIfNeeded();
    const [scrollWidth, width] = await page.evaluate(() => [document.documentElement.scrollWidth, window.innerWidth]);
    expect(scrollWidth, 'no horizontal page scroll').toBeLessThanOrEqual(width);
  });
});
