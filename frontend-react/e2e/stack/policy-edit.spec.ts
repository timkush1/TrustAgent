/**
 * Policy edit on a running stack (SPEC §3.3 Policies, §7.2), as an admin (signInAsAdmin: an admin
 * key, or the proxy's one-time setup link): change support-bot's mode as a draft, review it (server
 * validation, free replay) and save a new version with the session cookie; and roll back to the
 * version before an edit from the versions sheet. Each test leaves the stack as it found it, and
 * they run one after the other because both edit the policy.
 *
 * Needs TRUSTAGENT_ADMIN_KEY (a key from TRUTHTABLE_ADMIN_KEYS; preferred, it works under retries)
 * or TRUSTAGENT_SETUP_URL (`make demo-setup-url`). Without either the tests skip with the reason,
 * and fail in CI.
 */
import { expect, test, type Page } from '@playwright/test';
import { canSignInAsAdmin, requireStack, signInAsAdmin, skipOrFail } from './stack';

const TIMEOUT = 30_000;
const ROUTE = 'support-bot';

interface PolicyState {
  id: number;
  source: string;
  effective_routes: { name: string; mode: string }[];
}

async function policyState(page: Page): Promise<PolicyState> {
  const res = await page.request.get('/api/policies');
  expect(res.ok(), `GET /api/policies answered ${res.status()}`).toBe(true);
  return (await res.json()) as PolicyState;
}

function modeOf(state: PolicyState, route: string): string | undefined {
  return state.effective_routes.find((r) => r.name === route)?.mode;
}

/** Drafts a mode change on the Policies page and saves it through the review dialog. */
async function saveMode(page: Page, route: string, mode: RegExp, message: string): Promise<void> {
  await page.goto('/app/policies');
  await page.getByRole('combobox', { name: `Mode for ${route}` }).click();
  await page.getByRole('option', { name: mode }).click();
  const bar = page.getByRole('region', { name: 'Unsaved changes' });
  await bar.getByRole('button', { name: 'Review and save' }).click();

  const dialog = page.getByRole('dialog', { name: 'Review and save a new policy version' });
  await expect(dialog.getByText('The server accepts this policy.')).toBeVisible({ timeout: TIMEOUT });
  await dialog.getByRole('textbox', { name: 'What changed and why' }).fill(message);
  await dialog.getByRole('button', { name: 'Save version' }).click();
  await expect(dialog).toBeHidden({ timeout: TIMEOUT });
}

/** Leaves the stack as it was: rolls back to `before` when the policy moved on (also after a failure). */
async function restore(page: Page, before: PolicyState): Promise<void> {
  if ((await policyState(page)).id !== before.id) await rollBackTo(page, before.id);
}

/** Rolls back to `version` from the versions sheet, through its confirmation. */
async function rollBackTo(page: Page, version: number): Promise<void> {
  await page.goto('/app/policies');
  await page.getByRole('button', { name: 'Versions' }).click();
  const sheet = page.getByRole('dialog', { name: 'Policy versions' });
  await sheet.getByRole('button', { name: new RegExp(`Roll back to version ${version}\\b`) }).click();
  const confirm = page.getByRole('alertdialog', { name: `Roll back to version ${version}?` });
  await confirm.getByRole('button', { name: 'Roll back' }).click();
  await expect.poll(async () => (await policyState(page)).source, { timeout: TIMEOUT }).toBe('rollback');
}

test.describe('policy edit', () => {
  test.describe.configure({ mode: 'serial' });

  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStack(playwright, testInfo);
  });

  test.beforeEach(() => {
    skipOrFail(!canSignInAsAdmin(), 'set TRUSTAGENT_ADMIN_KEY (a key from TRUTHTABLE_ADMIN_KEYS) or TRUSTAGENT_SETUP_URL to sign in as admin');
  });

  test('saves a mode change as admin through the review dialog', async ({ page }) => {
    test.setTimeout(4 * TIMEOUT);
    await signInAsAdmin(page);
    const before = await policyState(page);
    expect(modeOf(before, ROUTE)).toBe('flag');
    try {
      await saveMode(page, ROUTE, /^Observe/, `Observe ${ROUTE} (e2e policy edit)`);
      const saved = await policyState(page);
      expect(saved.id).toBeGreaterThan(before.id);
      expect(saved.source).toBe('dashboard');
      expect(modeOf(saved, ROUTE)).toBe('observe');
    } finally {
      await restore(page, before);
    }
    expect(modeOf(await policyState(page), ROUTE)).toBe('flag');
  });

  test('rollback: restores the version from before an edit', async ({ page }) => {
    test.setTimeout(4 * TIMEOUT);
    await signInAsAdmin(page);
    const before = await policyState(page);
    expect(modeOf(before, ROUTE)).toBe('flag');
    try {
      await saveMode(page, ROUTE, /^Observe/, `Observe ${ROUTE} (e2e rollback)`);
      const edited = await policyState(page);
      expect(modeOf(edited, ROUTE)).toBe('observe');

      await rollBackTo(page, before.id);
      const after = await policyState(page);
      expect(after.id).toBeGreaterThan(edited.id);
      expect(after.source).toBe('rollback');
      expect(modeOf(after, ROUTE)).toBe('flag');
    } finally {
      if (modeOf(await policyState(page), ROUTE) !== 'flag') await restore(page, before);
    }
  });
});
