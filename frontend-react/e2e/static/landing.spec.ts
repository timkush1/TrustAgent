/**
 * The landing page (SPEC §8.1, D99, D113-D117): the root of the static site, at most seven
 * sections, every number with its label chip, the replay one click away, the live scenarios one
 * click into the Playground, the Studio a real page, absolute share meta, both themes, a 400 px
 * screen and what the first viewport shows.
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { expect, test, type Page } from '@playwright/test';
import { seriousViolations } from '../stack/stack';
import { MOCK_LABEL, recorded, requireStaticSite, useExternalGuard } from './site';

const pack = JSON.parse(readFileSync(fileURLToPath(new URL('../../../contracts/demo/scenarios.v1.json', import.meta.url)), 'utf8')) as {
  scenarios: { id: string; title: string }[];
};
const scenarioTitle = (id: string) => pack.scenarios.find((s) => s.id === id)?.title ?? id;

/** Waits until every lazy part of the page is in (no placeholder left). */
async function settled(page: Page): Promise<void> {
  await expect(page.locator('[data-lazy-placeholder]')).toHaveCount(0);
}

/** Whether the element's box lies inside the first viewport (no scrolling). */
async function inFirstViewport(page: Page, selector: ReturnType<Page['locator']>): Promise<boolean> {
  const box = await selector.boundingBox();
  const size = page.viewportSize();
  if (!box || !size) return false;
  return box.y >= 0 && box.x >= 0 && box.y + box.height <= size.height && box.x + box.width <= size.width;
}

test.describe('landing', () => {
  useExternalGuard();
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStaticSite(playwright, testInfo);
  });

  test('renders the promise, the refund card and at most seven sections', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (e) => errors.push(e.message));
    await page.goto('');
    await expect(page).toHaveTitle(/TrustAgent/);
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Catch hallucinations before your users do.');
    await settled(page);
    const sections = page.locator('main > section');
    expect(await sections.count()).toBe(7);

    // The hero card's third beat: "60 days" gets its mark next to the evidence.
    await expect(page.locator('mark.hero-mark[data-on]')).toHaveText('60 days', { timeout: 10_000 });
    await expect(page.locator('[data-checked-against]')).toHaveText('Checked against: refund-policy.md');

    // Every number chip names its kind; ours are "not yet measured" until a benchmark run.
    const chips = page.locator('[data-value-label]');
    expect(await chips.count()).toBeGreaterThan(10);
    await expect(page.locator('[data-value-label="measured"]')).toHaveCount(0);
    await expect(page.getByRole('main').getByText('not yet measured')).toHaveCount(1);

    // The replay card is built with the page from the shipped tape: no manifest request.
    await expect(page.locator('[data-recording-label]')).toHaveText(MOCK_LABEL);
    expect(errors).toEqual([]);
  });

  test('builds the replay card without fetching the recording', async ({ page }) => {
    const asked: string[] = [];
    page.on('request', (r) => asked.push(new URL(r.url()).pathname));
    await page.goto('');
    await settled(page);
    await expect(page.locator('[data-tape-card]')).toContainText('requests');
    expect(asked.filter((p) => p.includes('/demo/'))).toEqual([]);
  });

  test('"Try the demo" opens the replay', async ({ page }) => {
    await page.goto('');
    await page.getByRole('main').getByRole('link', { name: 'Try the demo' }).click();
    await expect(page).toHaveURL(/\/TrustAgent\/app\/traffic\/?$/);
    await expect(page.getByRole('table', { name: 'Requests' })).toBeVisible();
  });

  test('a live scenario link opens the Playground with that scenario picked', async ({ page }) => {
    await page.goto('');
    await settled(page);
    const link = page.locator('a[data-live-scenario="refund-contradiction-flag"]');
    await expect(link).toHaveAttribute('href', /app\/playground\/\?scenario=refund-contradiction-flag#live$/);
    await link.click();
    await expect(page).toHaveURL(/\/TrustAgent\/app\/playground\/?\?scenario=refund-contradiction-flag#live$/);
    const panel = page.getByTestId('live-panel');
    await expect(panel).toBeVisible();
    await expect(panel.getByRole('radio', { name: scenarioTitle('refund-contradiction-flag') })).toBeChecked();
  });

  test('"What Jev was asked" opens the same refund request on the replay', async ({ page }) => {
    const { refundTraceId } = await recorded(page);
    await page.goto('');
    await settled(page);
    const link = page.locator('[data-glassbox-link]');
    await expect(link).toHaveText('See this request on the replay');
    await link.click();
    await expect(page).toHaveURL(new RegExp(`/TrustAgent/app/traffic/${refundTraceId}$`));
    await expect(page.getByRole('region', { name: 'Answer' }).locator('mark[data-v="contradicted"]')).toHaveText('60 days', { timeout: 30_000 });
  });

  test('the Studio link answers 200 and opens the Studio', async ({ page }) => {
    await page.goto('');
    await settled(page);
    const studio = page.getByRole('link', { name: 'Open the Studio' });
    const href = (await studio.getAttribute('href')) ?? '';
    expect(href).toMatch(/\/TrustAgent\/app\/evaluators\/studio\/$/);
    const res = await page.request.get(new URL(href, page.url()).href);
    expect(res.status()).toBe(200);
    await studio.click();
    await expect(page).toHaveURL(/\/app\/evaluators\/studio\/$/);
  });

  test('carries absolute canonical and share meta, and no og:image before a capture', async ({ page }) => {
    await page.goto('');
    const canonical = await page.locator('link[rel="canonical"]').getAttribute('href');
    expect(canonical).toMatch(/^https:\/\/[^/]+\/.*\/$/);
    await expect(page.locator('meta[property="og:url"]')).toHaveAttribute('content', canonical ?? '');
    for (const selector of ['meta[property="og:title"]', 'meta[property="og:description"]', 'meta[name="twitter:card"]']) {
      expect(await page.locator(selector).getAttribute('content'), selector).toBeTruthy();
    }
    expect(await page.locator('meta[name="theme-color"]').count()).toBe(2);
    // public/og.png is written only by `npm run og` from a recorded tape (D115): none in this build.
    await expect(page.locator('meta[property="og:image"]')).toHaveCount(0);
    for (const url of await page.locator('meta[property^="og:"][content*="/"]').evaluateAll((els) => els.map((e) => e.getAttribute('content')))) {
      expect(url).toMatch(/^https:\/\//);
    }
  });

  for (const colorScheme of ['light', 'dark'] as const) {
    test(`has no serious accessibility violations (${colorScheme})`, async ({ page }) => {
      await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' });
      await page.goto('');
      await settled(page);
      await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme);
      // Open the sources list too, so its text is checked.
      await page.getByText('Where every number on this page comes from').click();
      await expect(page.locator('#sources')).toHaveAttribute('open', '');
      expect(await seriousViolations(page), `landing (${colorScheme})`).toEqual([]);
    });
  }

  test('fits a 400 px wide screen, the comparison included', async ({ page }) => {
    await page.setViewportSize({ width: 400, height: 860 });
    await page.goto('');
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    await settled(page);
    const width = 400;
    for (const anchor of ['#compare', '#try', '#cost', 'footer']) {
      await page.locator(anchor).first().scrollIntoViewIfNeeded();
      const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
      expect(scrollWidth, `no horizontal page scroll at ${anchor}`).toBeLessThanOrEqual(width);
    }
    // A phone gets one card per kind of tool, not a table wider than the page.
    await expect(page.getByRole('table')).toBeHidden();
    const cards = page.getByRole('list', { name: 'Comparison' });
    await expect(cards).toBeVisible();
    const box = await cards.boundingBox();
    expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(width);
  });

  test('shows the promise, the subhead, "Try the demo" and the flagged beat in the first desktop viewport', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await page.goto('');
    const hero = page.locator('section[aria-labelledby="hero-title"]');
    await expect(hero.getByRole('heading', { level: 1 })).toBeVisible();
    const beat2 = page.locator('.beat').first();
    await expect(beat2).toHaveAttribute('data-on', '');
    for (const [name, locator] of [
      ['H1', hero.getByRole('heading', { level: 1 })],
      ['subhead', hero.locator('p.text-lead')],
      ['Try the demo', hero.getByRole('link', { name: 'Try the demo' })],
      ['hero beat 2', beat2],
    ] as const) {
      expect(await inFirstViewport(page, locator), `${name} is in the first viewport`).toBe(true);
    }
  });

  test('a link into a lower section lands on it once every lazy part is in', async ({ page }) => {
    // The replay's "Run it yourself" opens #self-host; the calculator and the sections above it load
    // after the first paint, so the scroll has to wait for them (on a phone they are tallest).
    await page.setViewportSize({ width: 400, height: 860 });
    await page.goto('#self-host');
    await settled(page);
    const heading = page.getByRole('heading', { name: 'On your laptop in one command.' });
    // Below the sticky header, near the top: not left above the viewport or pushed below it.
    await expect
      .poll(
        async () => {
          const y = (await heading.boundingBox())?.y ?? -1;
          return y >= 0 && y < 300;
        },
        { message: 'the #self-host heading is at the top of the viewport' },
      )
      .toBe(true);
  });

  test('shows the promise and "Try the demo" in the first phone viewport', async ({ page }) => {
    await page.setViewportSize({ width: 400, height: 860 });
    await page.goto('');
    const hero = page.locator('section[aria-labelledby="hero-title"]');
    await expect(hero.getByRole('heading', { level: 1 })).toBeVisible();
    expect(await inFirstViewport(page, hero.getByRole('heading', { level: 1 })), 'H1').toBe(true);
    expect(await inFirstViewport(page, hero.getByRole('link', { name: 'Try the demo' })), 'Try the demo').toBe(true);
  });
});
