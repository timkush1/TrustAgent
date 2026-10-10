/**
 * The dashboard screenshots (D115; I-5b): `npm run screenshots` builds the launch site, then this
 * spec captures each page the static site shows, in both themes, to
 * docs/images/v2/{page}-{light,dark}.png (1440×900) for the README's screenshot block, and the trace
 * page again as frontend-react/public/landing/trace-{light,dark}.jpg for the landing's replay preview.
 *
 * The simulated tape is allowed here: every picture then carries the replay's Simulated banner, and
 * the README captions them "Simulated". On a recorded tape the banner carries the recording's label.
 *
 * The pages come from the static build, which a capture cannot change (CO-20: captures of a stack
 * once left test checks saved, and the glass box listed a question twice). The trace capture opens
 * the glass box and fails when any question appears twice in one request.
 */
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, test, type Page } from '@playwright/test';
import { blockUnmockedExternal, requireStaticSite } from '../static/site';
import {
  captureContext,
  duplicatedQuestions,
  pngSize,
  presetPreferences,
  REPO_ROOT,
  SCREENSHOT_DIR,
  shippedTape,
  type Tape,
  type Theme,
  writeCapture,
} from './capture';

const COPY = (
  JSON.parse(readFileSync(fileURLToPath(new URL('../../../contracts/ui/copy.v1.json', import.meta.url)), 'utf8')) as {
    copy: { trace: { show_jev_request: string } };
  }
).copy;

const SIZE = { width: 1440, height: 900 } as const;
/** The landing shows this page as its replay preview, from frontend-react/public/landing/<name>-<theme>.jpg. */
const LANDING_PREVIEW = 'trace';
const LANDING_DIR = 'frontend-react/public/landing';
const THEMES: readonly Theme[] = ['light', 'dark'];

interface Shot {
  /** The file name stem: docs/images/v2/<name>-<theme>.png. */
  name: string;
  /** Opens the page and waits until it shows what the picture is for. */
  open(page: Page): Promise<void>;
}

/** The replay has played every request of the tape: the top bar then reads the same on every run. */
async function replayFinished(page: Page): Promise<void> {
  await expect(page.locator('[data-replay-status="finished"]')).toBeVisible({ timeout: 60_000 });
}

const SHOTS: readonly Shot[] = [
  {
    name: 'traffic',
    async open(page) {
      await page.goto('app/traffic/');
      await replayFinished(page);
      await expect(page.getByRole('table', { name: 'Requests' }).locator('[role="row"][data-trace-id]').first()).toBeVisible();
    },
  },
  {
    name: 'trace',
    async open(page) {
      await page.goto('app/traffic/');
      await replayFinished(page);
      await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Trace' }).click();
      await expect(page.getByRole('region', { name: 'Answer' }).locator('mark[data-v="contradicted"]').first()).toBeVisible({
        timeout: 30_000,
      });
      await page.getByRole('heading', { name: 'Details' }).click();
      await page.getByText(COPY.trace.show_jev_request, { exact: true }).click();
      await expect(page.getByTestId('jev-request').getByText(/what it was asked$/).first()).toBeVisible({ timeout: 30_000 });
      expect(await duplicatedQuestions(page), 'a question listed twice in the glass box (CO-20)').toEqual([]);
      await page.evaluate(() => window.scrollTo(0, 0));
    },
  },
  {
    name: 'playground',
    async open(page) {
      await page.goto('app/playground/');
      await replayFinished(page);
      await expect(page.getByRole('list', { name: 'Example requests' }).getByRole('listitem').first()).toBeVisible({ timeout: 30_000 });
    },
  },
  {
    name: 'studio',
    async open(page) {
      await page.goto('app/evaluators/studio/');
      await replayFinished(page);
      await expect(page.getByLabel('What should TrustAgent check?')).toBeVisible({ timeout: 30_000 });
    },
  },
  {
    name: 'savings',
    async open(page) {
      // The static site answers app/savings/ with the Overview's Savings tab.
      await page.goto('app/savings/');
      await expect(page).toHaveURL(/\/app\/overview\/?\?tab=savings$/);
      await replayFinished(page);
    },
  },
];

/** The replay's banner says where the numbers come from, in the tape's own words. */
async function expectTapeLabel(page: Page, tape: Tape): Promise<void> {
  const banner = page.locator('[data-banner="replay"]');
  await expect(banner).toBeVisible();
  await expect(banner).toHaveAttribute('data-recording-kind', tape.kind);
  if (tape.kind === 'mock') await expect(banner).toContainText(/simulated/i);
}

test.describe('dashboard screenshots', () => {
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStaticSite(playwright, testInfo);
  });

  for (const theme of THEMES) {
    for (const shot of SHOTS) {
      test(`${shot.name}, ${theme} theme`, async ({ browser, baseURL }) => {
        const context = await browser.newContext({ ...captureContext(theme, SIZE), baseURL });
        try {
          const page = await context.newPage();
          const blocked = await blockUnmockedExternal(page, baseURL);
          const tape = await shippedTape(page);
          await presetPreferences(page, theme);

          await shot.open(page);
          await expectTapeLabel(page, tape);
          await expect(page.locator('html')).toHaveAttribute('data-theme', theme);

          const png = await page.screenshot({ animations: 'disabled', caret: 'hide' });
          expect(pngSize(png)).toEqual(SIZE);
          // A page that tried to reach another origin may not look as it should: nothing is written then.
          expect(blocked, 'requests to an unmocked external origin').toEqual([]);
          const written = writeCapture(resolve(REPO_ROOT, SCREENSHOT_DIR, `${shot.name}-${theme}.png`), png);
          console.log(`screenshots: ${written} (${tape.kind === 'mock' ? 'Simulated' : tape.label})`);
          if (shot.name === LANDING_PREVIEW) {
            // The landing's replay preview (ReplayPreview): the same picture as a JPEG it can afford.
            const jpeg = await page.screenshot({ type: 'jpeg', quality: 80, animations: 'disabled', caret: 'hide' });
            console.log(`screenshots: ${writeCapture(resolve(REPO_ROOT, LANDING_DIR, `${shot.name}-${theme}.jpg`), jpeg)}`);
          }
        } finally {
          await context.close();
        }
      });
    }
  }
});
