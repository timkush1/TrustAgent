/**
 * The README hero (D115; SPEC §8.2 erratum: this is e2e/launch/hero.spec.ts): `npm run hero` builds
 * the launch site, then this spec records the first-visit story as a 1200×675 video:
 *
 *   the landing hero → Try the demo → Traffic filling up → the featured answer (D209: the trace
 *   manifest.featured names on a recorded tape) with its contradicted words marked → Show exact Jev
 *   request.
 *
 * It writes frontend-react/test-results/launch/hero/hero.webm and hero.json (the tape's kind and
 * label), which scripts/make_hero_gif.py turns into the GIF (at most 8 MB, 1200 px, 12 fps) and an
 * MP4 (`make hero-gif` runs both). It refuses the simulated tape unless HERO_ALLOW_MOCK=1, and
 * make_hero_gif.py then keeps everything in the scratch directory.
 */
import { readFileSync, rmSync } from 'node:fs';
import { relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, test, type Page } from '@playwright/test';
import { blockUnmockedExternal, requireStaticSite } from '../static/site';
import {
  captureContext,
  expectFeaturedTrace,
  HERO_DIR,
  HERO_SIZE,
  presetPreferences,
  refuseMock,
  REPO_ROOT,
  shippedTape,
  writeCapture,
} from './capture';

const COPY = (
  JSON.parse(readFileSync(fileURLToPath(new URL('../../../contracts/ui/copy.v1.json', import.meta.url)), 'utf8')) as {
    copy: { trace: { show_jev_request: string } };
  }
).copy;

/** How long each beat of the story stays on screen, in ms (the GIF's pacing). */
const BEAT = { landing: 2_500, traffic: 3_500, answer: 3_500, glassBox: 3_000 } as const;

/** Lets the viewer read a beat: the recording is the point here, not the test's speed. */
async function hold(page: Page, ms: number): Promise<void> {
  await page.waitForTimeout(ms);
}

test.describe('hero recording', () => {
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStaticSite(playwright, testInfo);
  });

  test('records the first-visit story, from a recorded tape only', async ({ browser, baseURL }, testInfo) => {
    testInfo.setTimeout(120_000);
    const raw = resolve(HERO_DIR, 'raw');
    rmSync(HERO_DIR, { recursive: true, force: true });
    const context = await browser.newContext({
      ...captureContext('light', HERO_SIZE),
      baseURL,
      recordVideo: { dir: raw, size: HERO_SIZE },
    });
    const page = await context.newPage();
    const blocked = await blockUnmockedExternal(page, baseURL);
    const video = page.video();
    if (!video) throw new Error('the context records no video');

    try {
      const tape = await shippedTape(page);
      refuseMock(tape, 'HERO_ALLOW_MOCK', 'npm run hero');
      // The tour shows: it is what a first visit sees.
      await presetPreferences(page, 'light', { showGuide: true });

      await page.goto('');
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      await hold(page, BEAT.landing);

      await page.getByRole('link', { name: 'Try the demo' }).first().click();
      await expect(page).toHaveURL(/\/app\/traffic\/?$/);
      await expect(page.getByRole('table', { name: 'Requests' }).locator('[role="row"][data-trace-id]').first()).toBeVisible({
        timeout: 30_000,
      });
      await hold(page, BEAT.traffic);

      await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Trace' }).click();
      await expectFeaturedTrace(page, tape);
      const answer = page.getByRole('region', { name: 'Answer' });
      const mark = answer.locator('mark[data-v="contradicted"]').first();
      await expect(mark).toBeVisible({ timeout: 30_000 });
      await answer.scrollIntoViewIfNeeded();
      await hold(page, BEAT.answer);

      await page.getByRole('heading', { name: 'Details' }).click();
      await page.getByText(COPY.trace.show_jev_request, { exact: true }).click();
      const glassBox = page.getByTestId('jev-request');
      await expect(glassBox.getByText(/what it was asked$/).first()).toBeVisible({ timeout: 30_000 });
      await glassBox.scrollIntoViewIfNeeded();
      await hold(page, BEAT.glassBox);

      expect(blocked, 'requests to an unmocked external origin').toEqual([]);
      await context.close();
      const webm = resolve(HERO_DIR, 'hero.webm');
      await video.saveAs(webm);
      const sidecar = {
        schema: 'trustagent.hero-capture/v1',
        kind: tape.kind,
        label: tape.label,
        traces: tape.traces,
        featured: tape.featured,
        video: relative(HERO_DIR, webm),
        width: HERO_SIZE.width,
        height: HERO_SIZE.height,
        captured_at: new Date().toISOString(),
      };
      const written = writeCapture(resolve(HERO_DIR, 'hero.json'), `${JSON.stringify(sidecar, null, 2)}\n`);
      console.log(`hero: ${relative(REPO_ROOT, webm)} and ${written} (${tape.kind}); next: python3 scripts/make_hero_gif.py`);
    } finally {
      await context.close();
      rmSync(raw, { recursive: true, force: true });
    }
  });
});
