/**
 * The replay (SPEC §3.3 "Static build", §7.5 "Replay is the primary experience"): the committed
 * recording plays through ReplayDataSource in compressed time, labelled simulated, and the flagged
 * refund answer opens with "60 days" marked as contradicted. Deep links boot from 404.html, as on
 * GitHub Pages; the pages that need a gateway explain themselves.
 */
import { expect, test } from '@playwright/test';
import { hiddenStaticPage } from '../../src/components/replay/staticPages';
import { STATIC_APP_PAGES } from '../../vite.site';
import { seriousViolations } from '../stack/stack';
import { MOCK_LABEL, recorded, requireStaticSite, useExternalGuard } from './site';

test.describe('replay', () => {
  useExternalGuard();
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStaticSite(playwright, testInfo);
  });

  test('replays the recorded traffic, labelled simulated, to the end', async ({ page }) => {
    const { traces } = await recorded(page);
    const res = await page.goto('app/traffic/');
    expect(res?.status()).toBe(200);

    const banner = page.locator('[data-banner="replay"]');
    await expect(banner).toContainText('Replay of simulated traffic.');
    await expect(banner).toContainText('generated from the zero-key demo’s scripted scenarios');
    await expect(banner.locator('[data-recording-label]')).toHaveText(MOCK_LABEL);
    await expect(page.locator('[data-banner]')).toHaveCount(1);

    const table = page.getByRole('table', { name: 'Requests' });
    const rows = table.locator('[role="row"][data-trace-id]');
    await expect(rows.first()).toBeVisible();
    await expect(page.locator('[data-replay-status="finished"]')).toBeVisible({ timeout: 30_000 });
    await expect(page.locator('[data-replay-status="finished"]')).toContainText(`Replay finished · ${traces} requests`);
    // The table renders a window of its rows and loads the next page on scroll; once every page is
    // in, its row count covers every request (plus the header row).
    const everyRequestListed = async () => {
      await expect(async () => {
        await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
        await expect(table).toHaveAttribute('aria-rowcount', String(traces + 1), { timeout: 1_000 });
      }).toPass({ timeout: 20_000 });
      await page.evaluate(() => window.scrollTo(0, 0));
    };
    await everyRequestListed();

    // Replay again plays the live part of the tape once more.
    await page.getByRole('button', { name: 'Replay the recorded traffic again' }).click();
    await expect(page.locator('[data-replay-status="finished"]')).toBeVisible({ timeout: 30_000 });
    await everyRequestListed();
    expect(await seriousViolations(page), 'replay Traffic').toEqual([]);
  });

  test('opens the flagged refund answer with "60 days" marked as contradicted', async ({ page }) => {
    const { refundTraceId } = await recorded(page);
    await page.goto('app/traffic/');
    const nav = page.getByRole('navigation', { name: 'Main' });
    await nav.getByRole('link', { name: 'Trace' }).click();
    await expect(page).toHaveURL(new RegExp(`/TrustAgent/app/traffic/${refundTraceId}$`));

    await expect(page.getByText('Flagged', { exact: true }).first()).toBeVisible();
    await expect(page.getByText('Checked against: refund-policy.md')).toBeVisible();
    const claims = page.getByRole('list', { name: 'Claims' });
    await expect(claims.getByText('Contradicted', { exact: true })).toBeVisible({ timeout: 30_000 });

    const answer = page.getByRole('region', { name: 'Answer' });
    const mark = answer.locator('mark[data-v="contradicted"]');
    await expect(mark).toHaveText('60 days');
    expect(await mark.evaluate((el) => getComputedStyle(el).textDecorationStyle), 'wavy underline').toBe('wavy');
    await expect(answer.getByRole('link', { name: '60 days, contradicted claim' })).toBeVisible();
    expect(await seriousViolations(page), 'replay trace detail').toEqual([]);
  });

  test('boots a shared deep link from 404.html, as GitHub Pages does', async ({ page }) => {
    const { refundTraceId } = await recorded(page);
    const res = await page.goto(`app/traffic/${refundTraceId}`);
    // Pages answers a path with no file with 404.html (a copy of the app) and status 404.
    expect(res?.status()).toBe(404);
    await expect(page.getByRole('region', { name: 'Answer' }).locator('mark[data-v="contradicted"]')).toHaveText('60 days', {
      timeout: 30_000,
    });
  });

  test('lists only the static pages, and explains the ones that need a gateway', async ({ page }) => {
    await page.goto('app/overview/');
    const nav = page.getByRole('navigation', { name: 'Main' });
    await expect(nav.getByRole('link', { name: 'Run it yourself' })).toBeVisible();
    const labels = await nav.getByRole('link').evaluateAll((links) => links.map((a) => a.getAttribute('aria-label')).filter(Boolean));
    expect(labels).toEqual(['Overview', 'Traffic', 'Trace', 'Playground', 'Studio', 'Run it yourself']);
    await expect(page.getByText(/\d+ requests on \d+ \w+ \d{4}/)).toBeVisible();

    await page.goto('app/policies');
    await expect(page.getByRole('heading', { name: 'Policies is not part of the static demo' })).toBeVisible();
    await expect(page.getByRole('main').getByText('make demo')).toBeVisible();

    await page.goto('app/playground/');
    // One example per outcome: a short tour of what the gateway does.
    const examples = page.getByRole('list', { name: 'Example requests' }).getByRole('listitem');
    await expect(examples).toHaveCount(5);
    expect(await examples.locator('[data-outcome]').evaluateAll((els) => els.map((el) => el.getAttribute('data-outcome')))).toEqual([
      'passed',
      'flagged',
      'not_checked',
      'would_block',
      'blocked',
    ]);

    await nav.getByRole('link', { name: 'Run it yourself' }).click();
    await expect(page).toHaveURL(/\/TrustAgent\/#self-host$/);
    await expect(page.getByRole('heading', { name: 'On your laptop in one command.' })).toBeInViewport();
  });

  test('answers a setup link with why sign-in needs a running stack, without asking a gateway', async ({ page }) => {
    const api: string[] = [];
    page.on('request', (r) => {
      if (new URL(r.url()).pathname.includes('/api/')) api.push(r.url());
    });
    // A hidden page has no copy of its own: GitHub Pages serves it 404.html, which boots the app.
    const res = await page.goto('app/setup#token=not-a-real-token');
    expect(res?.status()).toBe(404);
    await expect(page.getByRole('heading', { name: 'Setup is not part of the static demo' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Create the admin key' })).toHaveCount(0);
    expect(api, 'no session or setup probe').toEqual([]);
  });

  test('gives each main page its own copy of the app, and hides none of them', async ({ request }) => {
    for (const path of STATIC_APP_PAGES) {
      expect(hiddenStaticPage(`/${path}`), `${path} is listed in STATIC_APP_PAGES but hidden`).toBeNull();
      const res = await request.get(`${path}/`);
      expect(res.status(), `${path}/`).toBe(200);
    }
  });

  test('fits a 400 px wide screen', async ({ page }) => {
    await page.setViewportSize({ width: 400, height: 860 });
    await page.goto('app/traffic/');
    await expect(page.locator('[data-banner="replay"]')).toBeVisible();
    const [scrollWidth, width] = await page.evaluate(() => [document.documentElement.scrollWidth, window.innerWidth]);
    expect(scrollWidth, 'no horizontal page scroll').toBeLessThanOrEqual(width);
  });
});
