/**
 * The social card (D115; docs/launch/og-spec.md): `npm run og` builds the launch site, then this spec
 * renders `/og` (the 1280×640 OgCard, only in a VITE_LAUNCH_ROUTES=1 build) and saves a PNG under
 * 1 MB to frontend-react/public/og.png and docs/launch/og.png.
 *
 * It refuses the simulated tape: a social card is a picture of the product's output, so it must be
 * real model output. OG_ALLOW_MOCK=1 captures the simulated tape anyway, with the card's Simulated
 * chip asserted, and writes it only to frontend-react/test-results/launch/og.png.
 *
 * The landing page links the card (og:image) only when public/og.png exists at build time, so
 * build the site again after a capture.
 */
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { expect, test } from '@playwright/test';
import { blockUnmockedExternal, requireStaticSite } from '../static/site';
import {
  captureContext,
  OG_MAX_BYTES,
  OG_SIZE,
  ogTargets,
  pngSize,
  presetPreferences,
  refuseMock,
  REPO_ROOT,
  shippedTape,
  writeCapture,
} from './capture';

/** The `<meta name="robots" content="noindex">` tags in a served HTML document. */
function noindexTags(html: string): number {
  return (html.match(/<meta\b[^>]*>/gi) ?? []).filter((tag) => /\bname=["']robots["']/i.test(tag) && /\bcontent=["']noindex["']/i.test(tag)).length;
}

test.describe('social card', () => {
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStaticSite(playwright, testInfo);
  });
  test.use(captureContext('light', OG_SIZE));

  test('captures /og as a 1280×640 PNG under 1 MB, from a recorded tape only', async ({ page, baseURL }) => {
    const blocked = await blockUnmockedExternal(page, baseURL);
    const tape = await shippedTape(page);
    refuseMock(tape, 'OG_ALLOW_MOCK', 'npm run og');
    await presetPreferences(page, 'light');

    // GitHub Pages has no og/ file: 404.html boots the app, which renders the route.
    const shell = await page.goto('og');
    if (!shell) throw new Error('no response for og');
    const shellNoindex = noindexTags(await shell.text());
    const card = page.locator('[data-og-card]');
    await expect(card, 'the launch build renders the card (VITE_LAUNCH_ROUTES=1)').toBeVisible({ timeout: 30_000 });
    await expect(card).toHaveAttribute('data-og-ready', '', { timeout: 30_000 });
    await expect(card).toHaveAttribute('data-recording-kind', tape.kind);
    // The card page is never indexed: the route adds its own noindex to whatever the served shell
    // (404.html on Pages) already carries.
    await expect(page.locator('meta[name="robots"][content="noindex"]')).toHaveCount(shellNoindex + 1);

    // Honest numbers: a capture of the simulated tape says so on the card, a recorded one does not.
    const simulated = card.locator('[data-og-simulated]');
    if (tape.kind === 'mock') await expect(simulated).toBeVisible();
    else await expect(simulated).toHaveCount(0);

    // The crop is the featured answer: an outcome, what it was checked against, the marked answer.
    await expect(card.locator('[data-og-crop]')).toBeVisible();
    await expect(card.locator('[data-og-checked]')).toContainText('Checked against');
    await expect(card.locator('[data-og-answer] mark[data-v="contradicted"]').first()).toBeVisible();

    const png = await card.screenshot({ animations: 'disabled', caret: 'hide' });
    expect(pngSize(png), 'the card is exactly 1280×640').toEqual(OG_SIZE);
    expect(png.byteLength, 'the card is under 1 MB').toBeLessThan(OG_MAX_BYTES);
    // A page that tried to reach another origin may not look as it should: nothing is written then.
    expect(blocked, 'requests to an unmocked external origin').toEqual([]);

    const written = ogTargets(tape.kind).map((path) => writeCapture(path, png));
    for (const path of written) {
      expect(pngSize(readFileSync(resolve(REPO_ROOT, path))), `${path} was written whole`).toEqual(OG_SIZE);
    }
    console.log(`og: ${tape.kind === 'mock' ? 'scratch copy (simulated)' : 'recorded tape'} -> ${written.join(', ')}`);
  });
});
