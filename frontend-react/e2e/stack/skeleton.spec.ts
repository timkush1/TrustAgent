/**
 * The S0 walking skeleton on a running demo stack (SPEC §4.2 exit gate:
 * `npx playwright test --project=stack -g skeleton`). The demo's trafficgen sends the three
 * scenarios of contracts/demo/scenarios.v1.json with zero keys:
 * - grounded-pass → Passed; refund-contradiction-flag → Flagged, checked against refund-policy.md,
 *   and a deep audit that finds the "60 days" claim Contradicted; no-evidence-not-assessed →
 *   Not checked.
 *
 * Web-first assertions only (no sleeps): the list and the audit arrive when they arrive, within
 * the timeouts below. Nothing here reads prompt or answer text, so the spec holds whether or not
 * the dashboard's session may see content. Skipped with the reason when no stack answers (failed
 * in CI).
 */
import { expect, test, type Page } from '@playwright/test';
import { requireStack, seriousViolations } from './stack';

/** Scripted traffic starts once the stack is healthy; the first audit follows in seconds. */
const TRAFFIC_TIMEOUT = 60_000;
const AUDIT_TIMEOUT = 60_000;
const REFUND_DOC = 'refund-policy.md';

function requestRows(page: Page) {
  return page.getByRole('table', { name: 'Requests' }).locator('[role="row"][data-trace-id]');
}

function rowsWithPill(page: Page, label: string) {
  return requestRows(page).filter({ has: page.getByText(label, { exact: true }) });
}

test.describe('skeleton', () => {
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStack(playwright, testInfo);
  });

  test('Traffic shows the gated demo requests, and the flagged one gets its deep audit', async ({ page }) => {
    test.setTimeout(TRAFFIC_TIMEOUT + AUDIT_TIMEOUT + 30_000);
    await page.goto('/app/traffic');

    await expect
      .poll(() => requestRows(page).count(), {
        message: 'waiting for 3 requests (is the demo trafficgen running?)',
        timeout: TRAFFIC_TIMEOUT,
      })
      .toBeGreaterThanOrEqual(3);

    const flagged = rowsWithPill(page, 'Flagged').filter({ hasText: REFUND_DOC });
    await expect(flagged.first(), 'a Flagged request checked against refund-policy.md').toBeVisible({
      timeout: TRAFFIC_TIMEOUT,
    });
    const notChecked = rowsWithPill(page, 'Not checked');
    await expect(notChecked.first(), 'a Not checked request').toBeVisible({ timeout: TRAFFIC_TIMEOUT });
    await expect(notChecked.first()).toContainText('No documents');
    await expect(rowsWithPill(page, 'Passed').first(), 'a Passed request').toBeVisible({ timeout: TRAFFIC_TIMEOUT });

    // Pause the list so rows hold still, then open the flagged request.
    const live = page.getByRole('button', { name: 'Live', exact: true });
    await live.click();
    await expect(live).toHaveAttribute('aria-pressed', 'false');
    await expect(page.getByRole('button', { name: /^Paused/ })).toBeVisible();
    await flagged.first().getByRole('link').click();

    await expect(page).toHaveURL(/\/app\/traffic\/[^/?#]+$/);
    const outcome = page.getByRole('region', { name: 'Outcome' });
    await expect(outcome.getByText('Flagged', { exact: true })).toBeVisible();
    await expect(outcome.getByText(`Checked against: ${REFUND_DOC}`)).toBeVisible();
    await expect(page.getByRole('region', { name: 'Answer' })).toBeVisible();
    await expect(page.getByRole('list', { name: 'Checks' }).getByRole('meter')).toHaveCount(2);

    // "Deep audit running…" until audit_completed arrives, then the claims.
    const claims = page.getByRole('list', { name: 'Claims' });
    await expect(claims.getByText('Contradicted', { exact: true }), 'a Contradicted claim').toBeVisible({
      timeout: AUDIT_TIMEOUT,
    });
    expect(await seriousViolations(page), 'trace detail').toEqual([]);
  });

  test('Traffic has no serious or critical accessibility violations', async ({ page }) => {
    await page.goto('/app/traffic');
    await expect(requestRows(page).first()).toBeVisible({ timeout: TRAFFIC_TIMEOUT });
    expect(await seriousViolations(page), '/app/traffic').toEqual([]);
  });
});
