/**
 * Trace detail on a running demo stack (SPEC §3.3 "Trace detail"): the flagged refund answer gets
 * its deep audit, and the contradicted claim is marked in the answer at its UTF-16 span ("60
 * days", wavy underline in the contradicted hue, with its icon). The mark is a link to the claim's
 * row in the Claims section. Needs a session that may read answers (the demo's public read shows
 * them); otherwise the answer is hidden and the mark check is skipped with the reason.
 */
import { expect, test } from '@playwright/test';
import { requireStack, seriousViolations } from './stack';

const TIMEOUT = 60_000;
const REFUND_DOC = 'refund-policy.md';

test.describe('trace detail', () => {
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStack(playwright, testInfo);
  });

  test('marks the contradicted "60 days" claim in the answer and links it to its row', async ({ page }) => {
    test.setTimeout(2 * TIMEOUT + 30_000);
    await page.goto('/app/traffic');
    const rows = page.getByRole('table', { name: 'Requests' }).locator('[role="row"][data-trace-id]');
    const flagged = rows.filter({ has: page.getByText('Flagged', { exact: true }) }).filter({ hasText: REFUND_DOC });
    await expect(flagged.first(), 'a Flagged request checked against refund-policy.md').toBeVisible({ timeout: TIMEOUT });
    await page.getByRole('button', { name: 'Live', exact: true }).click();
    await flagged.first().getByRole('link').click();
    await expect(page).toHaveURL(/\/app\/traffic\/[^/?#]+$/);

    const claims = page.getByRole('list', { name: 'Claims' });
    await expect(claims.getByText('Contradicted', { exact: true })).toBeVisible({ timeout: TIMEOUT });

    const answer = page.getByRole('region', { name: 'Answer' });
    test.skip(
      await answer.getByText(/^Hidden:/).isVisible(),
      "this session can't read answers (no content scope), so there is nothing to mark",
    );
    const mark = answer.getByRole('link', { name: '60 days, contradicted claim' });
    await expect(mark).toBeVisible();
    await expect(answer.locator('mark[data-v="contradicted"]')).toHaveText('60 days');
    const style = await answer
      .locator('mark[data-v="contradicted"]')
      .evaluate((el) => getComputedStyle(el).textDecorationStyle);
    expect(style, 'contradicted claims are wavy-underlined (DESIGN.md §3)').toBe('wavy');
    await expect(answer.getByText(/not located in text/)).toHaveCount(0);

    // Keyboard: the mark is one tab stop; Enter moves focus to the claim's row.
    await mark.focus();
    await page.keyboard.press('Enter');
    await expect(claims.getByRole('listitem').first()).toBeFocused();
    expect(await seriousViolations(page), 'trace detail with claim marks').toEqual([]);
  });
});
