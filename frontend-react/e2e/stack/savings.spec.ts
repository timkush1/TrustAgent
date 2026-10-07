/**
 * The Savings tab on a running demo-shaped stack (SPEC §7.4 "Panel", §3.3; P4-PLAN §13 SV-8, D70):
 *
 * - the deep link selects the Savings tab, and the headline ("Your cost: $X per 1,000 answers …")
 *   carries its value label: Simulated on the demo stack, whose checks are mock (D46);
 * - the spend chart over the counterfactual band renders, and so does its table view;
 * - every row of the component table carries a value label;
 * - answers stopped (block and fallback) and answers flagged are separate figures;
 * - the sampling status is shown;
 * - no serious accessibility violations, in either theme.
 *
 * Needs checked traffic in the last 24 hours: the demo's traffic generator sends it within a few
 * seconds of the stack starting; the spec waits for the first checked answer.
 */
import { expect, test, type APIRequestContext } from '@playwright/test';
import { requireStack, seriousViolations } from './stack';

const TIMEOUT = 60_000;

/** Whether the stack runs the demo (mock checks: every cost figure is Simulated). */
async function isDemo(request: APIRequestContext): Promise<boolean> {
  const res = await request.get('/health', { failOnStatusCode: false });
  const body = (await res.json().catch(() => null)) as { demo?: unknown } | null;
  return body?.demo === true;
}

/** Waits until the gateway has checked at least one answer in the last 24 hours. */
async function waitForCheckedAnswers(request: APIRequestContext): Promise<void> {
  await expect
    .poll(
      async () => {
        const res = await request.get('/api/roi/summary?window=24h', { failOnStatusCode: false });
        if (!res.ok()) return `GET /api/roi/summary answered HTTP ${res.status()}`;
        const { gated_responses: n } = (await res.json()) as { gated_responses: number };
        return n > 0 ? 'checked' : 'no checked answers yet';
      },
      { timeout: TIMEOUT, message: 'the stack never checked an answer (is the traffic generator running?)' },
    )
    .toBe('checked');
}

test.describe('savings', () => {
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStack(playwright, testInfo);
  });

  test('opens on the Savings tab with a labelled headline, the chart and its table', async ({ page }) => {
    await waitForCheckedAnswers(page.request);
    const demo = await isDemo(page.request);
    await page.goto('/app/savings');
    await expect(page).toHaveURL(/\/app\/overview\?tab=savings$/);
    await expect(page.getByRole('tab', { name: 'Savings', selected: true })).toBeVisible();

    const headline = page.getByRole('heading', { level: 2, name: /^Your cost: .+ per 1,000 answers \(fast check .+ \+ deep audit .+\)/ });
    await expect(headline).toBeVisible({ timeout: TIMEOUT });
    const label = headline.getByRole('button', { name: /: how “Your cost per 1,000 answers” was produced$/ });
    await expect(label).toHaveAccessibleName(demo ? /^Simulated:/ : /^(Measured|Estimate|Vendor|Third-party|Simulated):/);

    const chart = page.getByRole('figure', { name: 'What checking cost, against asking an LLM' });
    await expect(chart).toBeVisible({ timeout: TIMEOUT });
    await expect(chart.getByRole('list', { name: 'Legend' })).toContainText('What checking cost you');
    await chart.getByRole('radio', { name: 'Table' }).click();
    const table = chart.getByRole('region', { name: /^Spend per (hour|day)/ });
    await expect(table).toBeVisible();
    expect(await table.getByRole('row').count()).toBeGreaterThan(1);
    await expect(table.getByRole('columnheader', { name: demo ? 'Your cost (Simulated)' : /^Your cost \(/ })).toBeVisible();
  });

  test('labels every component, keeps stopped apart from flagged, and shows sampling', async ({ page }) => {
    await waitForCheckedAnswers(page.request);
    const demo = await isDemo(page.request);
    // The full audit from the published assumptions is an estimate even on the demo stack; every
    // other figure there is simulated.
    const roi = (await (await page.request.get('/api/roi/summary?window=24h')).json()) as { counterfactuals: { kind: string; method: string }[] };
    const publishedFullAudit = roi.counterfactuals.some((c) => c.kind === 'llm_full_claim_audit' && c.method === 'static_assumptions');
    await page.goto('/app/overview?tab=savings');

    const components = page.getByRole('table', { name: 'What checking cost, and what the alternatives would have cost' });
    await expect(components).toBeVisible({ timeout: TIMEOUT });
    const rows = components.locator('tbody tr');
    expect(await rows.count()).toBeGreaterThanOrEqual(3);
    for (const row of await rows.all()) {
      const badge = row.getByRole('button', { name: /: how “.+” was produced$/ });
      await expect(badge).toHaveCount(1);
      const estimate = publishedFullAudit && (await row.getAttribute('data-row')) === 'llm_full_claim_audit';
      if (demo) await expect(badge).toHaveAccessibleName(estimate ? /^Estimate:/ : /^Simulated:/);
    }

    const stopped = page.getByText('Stopped before users saw them', { exact: true });
    const flagged = page.getByText('Flagged to your app', { exact: true });
    await expect(stopped).toBeVisible();
    await expect(flagged).toBeVisible();
    // Two figures, each with its own count.
    await expect(stopped.locator('xpath=ancestor::div[1]').locator('dd')).toHaveText(/^\d[\d,]*$/);
    await expect(flagged.locator('xpath=ancestor::div[1]').locator('dd')).toHaveText(/^\d[\d,]*$/);

    const sampling = page.getByRole('region', { name: 'Sampling status' });
    await expect(sampling).toBeVisible();
    await expect(sampling.locator('[data-sampling="uniform"]')).toHaveText(/^Uniform sampling/);
  });

  test('on the demo, states a real judge’s cost at the shipped defaults, apart from the demo’s own rate', async ({ page }) => {
    await waitForCheckedAnswers(page.request);
    test.skip(!(await isDemo(page.request)), 'only the demo stack has the free mock judge');
    await page.goto('/app/overview?tab=savings');
    const headline = page.getByRole('region', { name: /^Your cost:/ });
    // The published figures lead: about $3 per 1,000 at the shipped defaults against $35.50 for a
    // full audit of every answer, each an estimate; never the demo's audit rate scaled up.
    const shipped = headline.getByText(/^At the shipped defaults/);
    await expect(shipped).toContainText(/about 8 in 100 answers deep-audited.*about \$3 per 1,000 answers.*\$35\.50 to audit every answer claim by claim/, { timeout: TIMEOUT });
    await expect(shipped.getByRole('button', { name: /^Estimate: / })).toHaveCount(2);
    await expect(headline.getByText(/^The deep audit above reads \$0/)).toContainText(/This demo deep-audits (every answer|about \d+ in 100 answers) to show the audit flow/);
    // One window, one cost: the chart's sentence states the headline's totals.
    const early = await headline.getByText(/^So far: /).textContent();
    const [, answers, spent] = /^So far: ([\d,]+) answers checked for (\$[\d.]+)\./.exec(early ?? '') ?? [];
    const chart = page.getByRole('figure', { name: 'What checking cost, against asking an LLM' });
    await expect(chart).toContainText(`Checking ${answers} answers cost ${spent}`);
  });

  for (const theme of ['light', 'dark'] as const) {
    test(`has no serious accessibility violations (${theme})`, async ({ page }) => {
      await waitForCheckedAnswers(page.request);
      await page.addInitScript((t) => localStorage.setItem('trustagent.theme', t), theme);
      await page.goto('/app/overview?tab=savings');
      await expect(page.getByRole('figure', { name: 'What checking cost, against asking an LLM' })).toBeVisible({ timeout: TIMEOUT });
      expect(await seriousViolations(page)).toEqual([]);
    });
  }
});
