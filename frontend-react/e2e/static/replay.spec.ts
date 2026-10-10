/**
 * The replay (SPEC §3.3 "Static build", §7.5 "Replay is the primary experience"; D109, D111, D117):
 * the committed recording plays through ReplayDataSource in compressed time, labelled simulated in
 * the contract's words; the six nav items and the hidden pages; Savings labelled Simulated; the
 * tour's four links; the glass box rebuilt in the browser; the outcome mix; deep links from
 * 404.html; 400 px, the dark theme and axe on every new surface. Nothing here depends on where a
 * trace sits on the tape: traces are found by what they are, from the manifest and the page.
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { expect, test, type Page } from '@playwright/test';
import { hiddenStaticPage } from '../../src/components/replay/staticPages';
import { STATIC_APP_PAGES } from '../../vite.site';
import { seriousViolations } from '../stack/stack';
import { recorded, requireStaticSite, useExternalGuard } from './site';

/** The words the replay shows, from the copy contract (contracts/ui/copy.v1.json). */
const COPY = (
  JSON.parse(readFileSync(fileURLToPath(new URL('../../../contracts/ui/copy.v1.json', import.meta.url)), 'utf8')) as {
    copy: {
      recording: {
        banner_simulated: string;
        showcase_caveat: string;
        nav_hint_caught: string;
        nav_hints: { recorded: string; simulated: string; replay: string; trace: string };
        guide: Record<'title' | 'step1' | 'step2' | 'step3' | 'step4' | 'dismiss', string>;
        glassbox: { rebuilt_simulated: string };
      };
      trace: { show_jev_request: string };
    };
  }
).copy;

/** The replay's featured answer: the nav's Trace item opens it. */
async function featuredId(page: Page): Promise<string> {
  const href = await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Trace' }).getAttribute('href');
  const id = /\/app\/traffic\/([^/?#]+)$/.exec(href ?? '')?.[1];
  if (!id) throw new Error(`the Trace nav item does not open a request (${href})`);
  return id;
}

async function finishReplay(page: Page): Promise<void> {
  await expect(page.locator('[data-replay-status="finished"]')).toBeVisible({ timeout: 30_000 });
}

async function noHorizontalScroll(page: Page, what: string): Promise<void> {
  const [scrollWidth, width] = await page.evaluate(() => [document.documentElement.scrollWidth, window.innerWidth]);
  expect(scrollWidth, `${what}: no horizontal page scroll`).toBeLessThanOrEqual(width);
}

test.describe('replay', () => {
  useExternalGuard();
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStaticSite(playwright, testInfo);
  });

  test('replays the recorded traffic, labelled simulated once, to the end', async ({ page }) => {
    const { traces } = await recorded(page);
    const res = await page.goto('app/traffic/');
    expect(res?.status()).toBe(200);

    const banner = page.locator('[data-banner="replay"]');
    await expect(banner).toContainText(COPY.recording.banner_simulated);
    expect((await banner.textContent())?.match(/simulated/gi), 'the banner says “simulated” once').toHaveLength(1);
    await expect(banner.getByRole('link', { name: 'Run it yourself' })).toHaveAttribute('href', '/TrustAgent/#self-host');
    await expect(page.locator('[data-banner]')).toHaveCount(1);

    const table = page.getByRole('table', { name: 'Requests' });
    const rows = table.locator('[role="row"][data-trace-id]');
    await expect(rows.first()).toBeVisible();
    await finishReplay(page);
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
    await finishReplay(page);
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
    // The policy line it contradicts (a <q>: the quote marks are the browser's).
    await expect(claims.getByRole('list', { name: 'Evidence' })).toContainText('refund-policy.md: Annual plans can be refunded within 30 days of purchase.');

    const answer = page.getByRole('region', { name: 'Answer' });
    const mark = answer.locator('mark[data-v="contradicted"]');
    await expect(mark).toHaveText('60 days');
    expect(await mark.evaluate((el) => getComputedStyle(el).textDecorationStyle), 'wavy underline').toBe('wavy');
    await expect(answer.getByRole('link', { name: '60 days, contradicted claim' })).toBeVisible();
    // The trace's actions stay in view on the static site, disabled with the reason (RP-4).
    const outcome = page.getByRole('region', { name: 'Outcome' });
    for (const name of [/^Open evaluator/, /^Adjust threshold/]) {
      const action = outcome.getByRole('button', { name });
      await expect(action).toHaveAttribute('aria-disabled', 'true');
      await expect(action).toHaveAccessibleDescription(/^Not available on the online demo: run TrustAgent yourself to use it/);
    }
    expect(await seriousViolations(page), 'replay trace detail').toEqual([]);
  });

  test('shows the exact Jev request of the featured answer, rebuilt in the browser (D109)', async ({ page }) => {
    await page.goto('app/traffic/');
    await page.goto(`app/traffic/${await featuredId(page)}`);
    await page.getByRole('heading', { name: 'Details' }).click();
    await page.getByText(COPY.trace.show_jev_request, { exact: true }).click();
    const panel = page.getByTestId('jev-request');
    await expect(panel.locator('[data-glassbox="simulated"]')).toHaveText(COPY.recording.glassbox.rebuilt_simulated);
    // What it read (the refund policy) and every question it was asked.
    await expect(panel.getByText(/what it was asked$/).first()).toBeVisible();
    expect(await panel.locator('h4:has-text("what it was asked") + ul > li').count()).toBeGreaterThan(0);
    await expect(panel).toContainText('Annual plans can be refunded within 30 days of purchase.');
    expect(await seriousViolations(page), 'the glass box').toEqual([]);
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

  test('lists six nav items, and explains the pages that need a gateway (D111)', async ({ page }) => {
    await page.goto('app/overview/');
    const nav = page.getByRole('navigation', { name: 'Main' });
    await expect(nav.getByRole('link', { name: 'Run it yourself' })).toBeVisible();
    const labels = await nav.getByRole('link').evaluateAll((links) => links.map((a) => a.getAttribute('aria-label')).filter(Boolean));
    expect(labels).toEqual(['Overview', 'Traffic', 'Trace', 'Playground', 'Studio', 'Run it yourself']);
    // The Trace item says where the tour starts.
    await expect(nav.getByRole('link', { name: 'Trace' })).toHaveAccessibleDescription(COPY.recording.nav_hint_caught);
    await expect(nav.getByRole('link', { name: 'Trace' })).toContainText(COPY.recording.nav_hints.trace);
    // The Overview's hint names the tape the site ships, never "recorded" for the simulated one (D164).
    const { kind } = (await (await page.request.get('demo/manifest.json')).json()) as { kind: string };
    const hints = COPY.recording.nav_hints;
    await expect(nav.getByRole('link', { name: 'Overview' })).toContainText(kind === 'recorded' ? hints.recorded : hints.simulated);
    if (kind !== 'recorded') await expect(nav.getByRole('link', { name: 'Overview' })).not.toContainText(hints.recorded);
    await expect(page.getByText(/\d+ requests on \d+ \w+ \d{4}/)).toBeVisible();

    for (const [path, name] of [
      ['app/policies', 'Policies'],
      ['app/settings/models', 'Settings'],
      ['app/evaluators/benchmarks', 'Evaluators'],
      ['app/knowledge', 'Knowledge'],
    ] as const) {
      await page.goto(path);
      await expect(page.getByRole('heading', { name: `${name} is not part of the static demo` }), path).toBeVisible();
      await expect(page.getByRole('main').getByText('make demo'), path).toBeVisible();
    }

    await page.goto('app/playground/');
    // One example per outcome the tape shows, in the order it first shows them: a short tour of
    // what the gateway does.
    const examples = page.getByRole('list', { name: 'Example requests' }).getByRole('listitem');
    await expect(examples).toHaveCount(6);
    expect(await examples.locator('[data-outcome]').evaluateAll((els) => els.map((el) => el.getAttribute('data-outcome')))).toEqual([
      'passed',
      'flagged',
      'not_checked',
      'would_block',
      'replaced',
      'blocked',
    ]);

    await page.goto('app/overview/');
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Run it yourself' }).click();
    await expect(page).toHaveURL(/\/TrustAgent\/#self-host$/);
    await expect(page.getByRole('heading', { name: 'On your laptop in one command.' })).toBeInViewport();
  });

  test('labels the Savings tab Simulated, and answers app/savings/ with it', async ({ page, request }) => {
    const res = await request.get('app/savings/');
    expect(res.status()).toBe(200);
    await page.goto('app/savings/');
    await expect(page).toHaveURL(/\/TrustAgent\/app\/overview\/?\?tab=savings$/);
    await expect(page.getByRole('tab', { name: 'Savings' })).toHaveAttribute('aria-selected', 'true');
    const panel = page.getByRole('tabpanel');
    await expect(panel.locator('[data-value-label="simulated"]').first()).toBeVisible();
    await expect(panel.locator('[data-value-label="measured"]')).toHaveCount(0);
    await expect(panel.getByRole('figure', { name: 'What checking cost, against asking an LLM' })).toBeVisible();
    expect(await seriousViolations(page), 'Savings').toEqual([]);
  });

  test('shows the whole outcome mix of the tape, Blocked and Fallback included, with the showcase caveat', async ({ page }) => {
    const { traces } = await recorded(page);
    await page.goto('app/traffic/');
    await finishReplay(page);
    // Every outcome the replayed traffic shows (every row: the table renders a window, so scroll)...
    const table = page.getByRole('table', { name: 'Requests' });
    const outcomes = new Set<string>();
    await expect(async () => {
      for (const o of await table.locator('[data-outcome]').evaluateAll((els) => els.map((el) => el.getAttribute('data-outcome') ?? ''))) outcomes.add(o);
      await page.evaluate(() => window.scrollBy(0, 600));
      await expect(table).toHaveAttribute('aria-rowcount', String(traces + 1), { timeout: 500 });
      for (const o of await table.locator('[data-outcome]').evaluateAll((els) => els.map((el) => el.getAttribute('data-outcome') ?? ''))) outcomes.add(o);
      expect(await page.evaluate(() => window.scrollY + window.innerHeight >= document.documentElement.scrollHeight - 2)).toBe(true);
    }).toPass({ timeout: 30_000 });
    // Blocked and Fallback (outcome "Replaced": the fallback answer went out instead) among them.
    expect([...outcomes]).toEqual(expect.arrayContaining(['blocked', 'replaced', 'flagged', 'passed', 'not_checked', 'would_block']));

    // ...is the Overview's outcome mix.
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Overview' }).click();
    const legend = page.getByRole('figure', { name: /Requests by outcome/ }).getByRole('list', { name: 'Legend' });
    await expect(legend).toBeVisible();
    const series = await legend.locator('[data-series]').evaluateAll((els) => els.map((el) => el.getAttribute('data-series')));
    expect(new Set(series)).toEqual(outcomes);
    await expect(legend.locator('[data-series="blocked"]')).toHaveText('Blocked');
    await expect(legend.locator('[data-series="replaced"]')).toHaveText('Replaced');
    // A scripted showcase mix: its rates are not a traffic rate (D117).
    await expect(page.getByText(COPY.recording.showcase_caveat)).toBeVisible();
    expect(await seriousViolations(page), 'Overview').toEqual([]);
  });

  test('follows the tour’s four links from Traffic', async ({ page }) => {
    await page.goto('app/traffic/');
    const guide = page.getByRole('region', { name: COPY.recording.guide.title });
    await expect(guide).toBeVisible();
    const featured = await featuredId(page);
    const step = (n: 1 | 2 | 3 | 4) => guide.getByRole('link', { name: COPY.recording.guide[`step${n}`] });

    await step(1).click();
    await expect(page).toHaveURL(new RegExp(`/app/traffic/${featured}$`));
    await expect(page.getByRole('region', { name: 'Outcome' }).getByText('Flagged', { exact: true })).toBeVisible();

    await page.goBack();
    await step(2).click();
    await expect(page).toHaveURL(new RegExp(`/app/traffic/${featured}#answer-title$`));
    await expect(page.getByRole('heading', { name: 'Answer' })).toBeInViewport();
    await expect(page.getByRole('region', { name: 'Answer' }).locator('mark[data-v="contradicted"]')).toHaveText('60 days');

    await page.goBack();
    await step(3).click();
    await expect(page).toHaveURL(new RegExp(`/app/traffic/${featured}#jev-request$`));
    const panel = page.locator('details#jev-request');
    await expect(panel).toHaveAttribute('open', '');
    // Brought into view, open, with the request rebuilt below it.
    await expect(panel.locator('summary')).toBeInViewport();
    await expect(panel.locator('summary')).toBeFocused();
    await expect(panel.locator('[data-glassbox="simulated"]')).toBeVisible();

    await page.goBack();
    await step(4).click();
    await expect(page).toHaveURL(/\/app\/overview\/?\?tab=savings$/);
    await expect(page.getByRole('tab', { name: 'Savings' })).toHaveAttribute('aria-selected', 'true');
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

  for (const colorScheme of ['light', 'dark'] as const) {
    test(`fits a 400 px wide screen with no serious accessibility violation (${colorScheme})`, async ({ page }) => {
      await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' });
      await page.setViewportSize({ width: 400, height: 860 });
      await page.goto('app/traffic/');
      await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme);
      await expect(page.locator('[data-banner="replay"]')).toBeVisible();
      await expect(page.getByRole('region', { name: COPY.recording.guide.title })).toBeVisible();
      await noHorizontalScroll(page, 'Traffic');
      expect(await seriousViolations(page), `Traffic (${colorScheme})`).toEqual([]);

      await page.goto(`app/traffic/${await featuredId(page)}#jev-request`);
      await expect(page.locator('details#jev-request [data-glassbox]')).toBeVisible();
      await noHorizontalScroll(page, 'Trace');
      expect(await seriousViolations(page), `Trace (${colorScheme})`).toEqual([]);

      await page.goto('app/overview/');
      await expect(page.getByText(COPY.recording.showcase_caveat)).toBeVisible();
      await noHorizontalScroll(page, 'Overview');
      expect(await seriousViolations(page), `Overview (${colorScheme})`).toEqual([]);

      await page.goto('app/overview/?tab=savings');
      await expect(page.getByRole('figure', { name: 'What checking cost, against asking an LLM' })).toBeVisible();
      await noHorizontalScroll(page, 'Savings');
      expect(await seriousViolations(page), `Savings (${colorScheme})`).toEqual([]);
    });
  }
});
