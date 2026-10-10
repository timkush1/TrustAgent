/**
 * A first visit (D117, RP-7): from the landing page's "Try the demo", the answer TrustAgent caught
 * is at most three actions away and reads "Flagged", with "60 days" marked in the answer and the
 * "30 days" line of the policy it contradicts, and the site contacts nothing but itself. The tour
 * opens on the first visit, its first step anchored to the featured row of Traffic, which it never
 * covers at 400 px; hidden, it stays hidden.
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { expect, test, type Locator, type Page } from '@playwright/test';
import { seriousViolations } from '../stack/stack';
import { recorded, requireStaticSite, useExternalGuard, watchNetwork } from './site';

const GUIDE = (
  JSON.parse(readFileSync(fileURLToPath(new URL('../../../contracts/ui/copy.v1.json', import.meta.url)), 'utf8')) as {
    copy: { recording: { guide: Record<'title' | 'step1' | 'dismiss', string> } };
  }
).copy.recording.guide;

/** Scrolls the Traffic list until the featured row is rendered (the table renders a window of rows). */
async function featuredRow(page: Page): Promise<Locator> {
  const row = page.locator('[role="row"][data-featured]');
  await expect(async () => {
    if ((await row.count()) === 0) await page.evaluate(() => window.scrollBy(0, 400));
    await expect(row).toHaveCount(1, { timeout: 300 });
  }).toPass({ timeout: 30_000 });
  await row.scrollIntoViewIfNeeded();
  return row;
}

function overlaps(a: { x: number; y: number; width: number; height: number }, b: { x: number; y: number; width: number; height: number }): boolean {
  return a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;
}

test.describe('first-visit', () => {
  useExternalGuard();
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStaticSite(playwright, testInfo);
  });

  test('reaches the caught answer from "Try the demo" in three actions, contacting only the site', async ({ page, baseURL }) => {
    const origin = new URL(String(baseURL)).origin;
    const net = await watchNetwork(page);
    const { refundTraceId } = await recorded(page);
    let actions = 0;
    const act = async (step: () => Promise<void>) => {
      actions += 1;
      await step();
    };

    await page.goto('');
    await act(() => page.getByRole('main').getByRole('link', { name: 'Try the demo' }).first().click());
    await expect(page).toHaveURL(/\/TrustAgent\/app\/traffic\/?$/);
    await act(() => page.getByRole('region', { name: GUIDE.title }).getByRole('link', { name: GUIDE.step1 }).click());
    expect(actions, 'actions from the landing page').toBeLessThanOrEqual(3);

    await expect(page).toHaveURL(new RegExp(`/TrustAgent/app/traffic/${refundTraceId}$`));
    const outcome = page.getByRole('region', { name: 'Outcome' });
    await expect(outcome.getByText('Flagged', { exact: true })).toBeVisible();
    await expect(page.getByRole('region', { name: 'Answer' }).locator('mark[data-v="contradicted"]')).toHaveText('60 days');
    const evidence = page.getByRole('list', { name: 'Claims' }).getByRole('list', { name: 'Evidence' });
    await expect(evidence).toContainText('30 days');
    await expect(evidence).toContainText('refund-policy.md');

    // Nothing beyond the site itself: no fonts, analytics or APIs elsewhere.
    expect(net.urls.filter((u) => u.startsWith('http') && new URL(u).origin !== origin)).toEqual([]);
    expect(await net.violations()).toEqual([]);
  });

  test('opens the tour on a first visit, anchored to the featured row and never over it at 400 px', async ({ page }) => {
    await page.setViewportSize({ width: 400, height: 860 });
    await page.goto('app/traffic/');
    const guide = page.getByRole('region', { name: GUIDE.title });
    await expect(guide).toBeVisible();
    const step1 = guide.getByRole('link', { name: GUIDE.step1 });
    const anchor = await step1.getAttribute('data-guide-anchor');

    const row = await featuredRow(page);
    expect(await row.getAttribute('data-trace-id'), 'step 1 is anchored to the featured row').toBe(anchor);
    expect(await step1.getAttribute('href')).toBe(`/TrustAgent/app/traffic/${anchor}`);
    await expect(row).toBeInViewport();
    const [rowBox, guideBox] = [await row.boundingBox(), await guide.boundingBox()];
    if (!rowBox || !guideBox) throw new Error('the featured row or the tour has no box');
    expect(overlaps(rowBox, guideBox), 'the tour covers the featured row').toBe(false);
    // What is on top at the row's centre is the row itself.
    const onTop = await page.evaluate(
      ([x, y]) => document.elementFromPoint(x, y)?.closest('[data-featured]') !== null,
      [rowBox.x + rowBox.width / 2, rowBox.y + rowBox.height / 2],
    );
    expect(onTop, 'nothing covers the featured row').toBe(true);
    expect(await seriousViolations(page), 'the tour at 400 px').toEqual([]);

    // Hidden, it stays hidden on the next visit.
    await page.evaluate(() => window.scrollTo(0, 0));
    await guide.getByRole('button', { name: GUIDE.dismiss }).click();
    await expect(guide).toHaveCount(0);
    await page.reload();
    await expect(page.getByRole('table', { name: 'Requests' })).toBeVisible();
    await expect(page.getByRole('region', { name: GUIDE.title })).toHaveCount(0);
  });
});
