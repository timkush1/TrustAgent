/**
 * Fallback on a running demo stack (SPEC §2.12 "Fallback", the M-Demo gate): payments-fallback's
 * answer says refunds are back "within 2 business days" while refund-timing.md says 5 to 10, so the
 * payments-faq route (mode fallback) replaces it with a fallback answer written from the document,
 * which passes its own check. Traffic shows the request as Replaced; the trace shows the fallback
 * answer as what the user received; the deep audit judges the model's original answer and marks
 * "2 business days" as contradicted.
 *
 * Needs a session that may read answers (the demo's public read shows them); otherwise the texts
 * are hidden: the test skips with the reason, and fails in CI.
 */
import { expect, test } from '@playwright/test';
import { requireStack, seriousViolations, skipOrFail } from './stack';

const TIMEOUT = 60_000;
const DOC = 'refund-timing.md';

test.describe('fallback', () => {
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStack(playwright, testInfo);
  });

  test('shows the replaced answer, what the user received instead, and the contradicted claim', async ({ page }) => {
    test.setTimeout(2 * TIMEOUT + 30_000);
    await page.goto('/app/traffic');
    const rows = page.getByRole('table', { name: 'Requests' }).locator('[role="row"][data-trace-id]');
    const replaced = rows.filter({ has: page.getByText('Replaced', { exact: true }) }).filter({ hasText: DOC });
    await expect(replaced.first(), `a Replaced request checked against ${DOC}`).toBeVisible({ timeout: TIMEOUT });
    await page.getByRole('button', { name: 'Live', exact: true }).click();
    await replaced.first().getByRole('link').click();
    await expect(page).toHaveURL(/\/app\/traffic\/[^/?#]+$/);

    const outcome = page.getByRole('region', { name: 'Outcome' });
    await expect(outcome.getByText('Replaced', { exact: true })).toBeVisible();
    await expect(outcome.getByText(`Checked against: ${DOC}`)).toBeVisible();

    const answer = page.getByRole('region', { name: 'Answer' });
    await expect(answer).toBeVisible();
    skipOrFail(await answer.getByText(/^Hidden:/).isVisible(), "this session can't read answers (no content scope)");

    // Delivered: the fallback answer, written from the document.
    const received = answer.getByTestId('received-text');
    await expect(received).toContainText('5 to 10 business days');
    await expect(answer.getByText("Your model's answer (not sent to the user)")).toBeVisible();
    await outcome.getByRole('link', { name: 'See what the user received instead' }).click();
    await expect(received.locator('xpath=..')).toBeFocused();

    // The deep audit judged the model's answer: "2 business days" is contradicted.
    const claims = page.getByRole('list', { name: 'Claims' });
    await expect(claims.getByText('Contradicted', { exact: true })).toBeVisible({ timeout: TIMEOUT });
    const mark = answer.locator('mark[data-v="contradicted"]');
    await expect(mark).toContainText('2 business days');
    await expect(answer.getByRole('link', { name: /2 business days, contradicted claim/ })).toBeVisible();
    await expect(received.locator('mark')).toHaveCount(0);

    expect(await seriousViolations(page), 'trace detail of a replaced answer').toEqual([]);
  });
});
