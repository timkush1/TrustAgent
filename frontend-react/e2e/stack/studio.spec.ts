/**
 * Evaluator Studio on a running demo-shaped stack (SPEC §7.3, P4-PLAN §12 STW-5), as an admin: draft
 * a check with the mock drafter, see its lints, test it on the draft's six samples, see quick
 * calibration ask for 8 labels, save it as a new check, watch it on support-bot as a shadow
 * evaluator through the policy review, test it on past traffic (and cancel a second run), then roll
 * the policy back the way policy-edit.spec.ts does and archive the check, so the stack is left as
 * it was found.
 *
 * Needs TRUSTAGENT_ADMIN_KEY (a key from TRUTHTABLE_ADMIN_KEYS) or TRUSTAGENT_SETUP_URL. Without
 * either the test skips with the reason, and fails in CI. It first runs live at integration (I-5).
 */
import { expect, test, type Locator, type Page } from '@playwright/test';
import { canSignInAsAdmin, cookieWriteHeaders, requireStack, signInAsAdmin, skipOrFail } from './stack';

const TIMEOUT = 30_000;
const ROUTE = 'support-bot';
const ID_PATTERN = /^[a-z][a-z0-9_]{1,47}$/;

interface PolicyState {
  id: number;
  source: string;
  effective_routes: { name: string; evaluators: { ref: string; stage: string }[] }[];
}

async function policyState(page: Page): Promise<PolicyState> {
  const res = await page.request.get('/api/policies');
  expect(res.ok(), `GET /api/policies answered ${res.status()}`).toBe(true);
  return (await res.json()) as PolicyState;
}

/** Rolls back to `version` from the versions sheet, through its confirmation (as policy-edit.spec.ts). */
async function rollBackTo(page: Page, version: number): Promise<void> {
  await page.goto('/app/policies');
  await page.getByRole('button', { name: 'Versions' }).click();
  const sheet = page.getByRole('dialog', { name: 'Policy versions' });
  await sheet.getByRole('button', { name: new RegExp(`Roll back to version ${version}\\b`) }).click();
  const confirm = page.getByRole('alertdialog', { name: `Roll back to version ${version}?` });
  await confirm.getByRole('button', { name: 'Roll back' }).click();
  await expect.poll(async () => (await policyState(page)).source, { timeout: TIMEOUT }).toBe('rollback');
}

const step = (page: Page, n: number): Locator => page.getByRole('region', { name: new RegExp(`^Step ${n}:`) });

test.describe('studio', () => {
  test.describe.configure({ mode: 'serial' });

  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStack(playwright, testInfo);
  });

  test.beforeEach(() => {
    skipOrFail(!canSignInAsAdmin(), 'set TRUSTAGENT_ADMIN_KEY (a key from TRUTHTABLE_ADMIN_KEYS) or TRUSTAGENT_SETUP_URL to sign in as admin');
  });

  test('drafts, tests, saves and watches a check, then tests it on past traffic', async ({ page }) => {
    test.setTimeout(8 * TIMEOUT);
    await signInAsAdmin(page);
    const before = await policyState(page);
    const id = `e2e_${Date.now().toString(36)}`;
    expect(id).toMatch(ID_PATTERN);
    let saved = false;
    let archived: number | null = null;

    try {
      // 1. Draft with AI (the mock drafter on the demo stack).
      await page.goto('/app/evaluators/studio');
      const describe = step(page, 1);
      await describe.getByRole('textbox', { name: 'What should TrustAgent check?' }).fill('Flag answers that blame the customer for the problem they describe.');
      await describe.getByRole('button', { name: 'Draft with AI' }).click();
      await expect(describe.getByText(/^Drafted “.+” with /)).toBeVisible({ timeout: TIMEOUT });

      // 2. The lints are shown under the review.
      const review = step(page, 2);
      await expect(review.getByText(/^No problems: the check passes|errors? to fix before saving|suggestions?$/).first()).toBeVisible();

      // 3. Test on the draft's six samples.
      const bench = step(page, 3);
      await expect(bench.getByText("From the AI's draft")).toHaveCount(6);
      await bench.getByRole('button', { name: 'Run test' }).click();
      const results = bench.getByRole('region', { name: 'Test results' });
      await expect(results.getByText(/^(\d+ of \d+ match|No labelled samples)/)).toBeVisible({ timeout: TIMEOUT });
      await expect(results.getByText(/^Sample \d+$/)).toHaveCount(6);
      // On a mock fast check the run says it is simulated, and the mock knows the drafted
      // example's own samples, so every one of them matches its label.
      if ((await results.getByText(/^Simulated: the demo's mock fast check/).count()) > 0) {
        await expect(results.getByText(/^6 of 6 match/)).toBeVisible();
      }

      // 4. Quick calibration asks for more labels (it needs 8, with 3 of each).
      await expect(step(page, 4).getByText(/^Needs 8 labels \(have \d+/)).toBeVisible();

      // 5. Save it as a new check under an e2e id.
      await review.getByRole('tab', { name: 'Advanced' }).click();
      const idBox = review.getByRole('textbox', { name: 'Id', exact: true });
      await idBox.fill(id);
      await idBox.press('Enter');
      const save = step(page, 5);
      await expect(save.getByText(id, { exact: true })).toBeVisible();
      await save.getByRole('textbox', { name: 'What changed and why' }).fill('Created by the studio e2e test');
      const created = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/evaluators');
      await save.getByRole('button', { name: 'Save as a new check' }).click();
      expect((await created).status()).toBe(201);
      saved = true;
      await expect(save.getByText(`${id}@1`)).toBeVisible({ timeout: TIMEOUT });

      // 6. Save and watch on support-bot as a shadow evaluator, through the policy review.
      const watch = step(page, 6);
      await watch.getByRole('button', { name: `Save and watch on route ${ROUTE}` }).click();
      const dialog = page.getByRole('dialog', { name: 'Review and save a new policy version' });
      await expect(dialog.getByText('The server accepts this policy.')).toBeVisible({ timeout: TIMEOUT });
      await dialog.getByRole('textbox', { name: 'What changed and why' }).fill(`Watch ${id} on ${ROUTE} (studio e2e)`);
      await dialog.getByRole('button', { name: 'Save version' }).click();
      await expect(dialog).toBeHidden({ timeout: TIMEOUT });
      await expect(watch.getByText(`Watching on ${ROUTE}`)).toBeVisible({ timeout: TIMEOUT });
      const watching = await policyState(page);
      expect(watching.id).toBeGreaterThan(before.id);
      expect(watching.effective_routes.find((r) => r.name === ROUTE)?.evaluators).toContainEqual(expect.objectContaining({ ref: `${id}@1`, stage: 'shadow' }));

      // 7. Test on past traffic; confirm the cost when the server asks for it (D82).
      await watch.getByRole('radio', { name: '7 days' }).click();
      await watch.getByRole('button', { name: /^Test on past traffic \((paid|simulated)\)$/ }).click();
      const confirm = page.getByRole('alertdialog', { name: /^Run this (paid|simulated) test\?$/ });
      const result = watch.getByText(/^Would have fired (≈ )?[\d,]+ times in the last /);
      await expect(confirm.or(result)).toBeVisible({ timeout: TIMEOUT });
      if (await confirm.isVisible()) await confirm.getByRole('button', { name: 'Run the test' }).click();
      await expect(result).toBeVisible({ timeout: 2 * TIMEOUT });

      // ...and cancel a second run before the stream starts.
      await page.route('**/api/studio/dry-run', async (route) => {
        await new Promise((resolve) => setTimeout(resolve, 3_000));
        await route.continue().catch(() => {});
      });
      await watch.getByRole('button', { name: 'Test again' }).click();
      await watch.getByRole('button', { name: 'Cancel the test' }).click();
      await expect(watch.getByText(/^Test stopped\. You're charged only for the checks that already ran/)).toBeVisible();
      await page.unroute('**/api/studio/dry-run');
    } finally {
      // 8. Roll the policy back as policy-edit.spec.ts does, then archive the e2e check.
      if ((await policyState(page)).id !== before.id) await rollBackTo(page, before.id);
      if (saved) {
        const res = await page.request.post(`/api/evaluators/${id}/archive`, { data: { archived: true }, headers: cookieWriteHeaders() });
        archived = res.status();
      }
    }
    expect(archived, `POST /api/evaluators/${id}/archive answered HTTP ${archived}`).toBe(200);
    const after = await policyState(page);
    expect(after.effective_routes.find((r) => r.name === ROUTE)?.evaluators.some((e) => e.ref.startsWith(`${id}@`))).toBe(false);
  });
});
