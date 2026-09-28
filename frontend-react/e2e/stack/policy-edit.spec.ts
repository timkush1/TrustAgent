/**
 * Policy edit on a running stack (SPEC §3.3 Policies, §7.2), as an admin signed in through the
 * proxy's one-time setup link: claim it on /app/setup, change support-bot's mode as a draft,
 * review it (server validation, free replay) and save a new version with the session cookie,
 * then roll back to the version before so the stack is left as it was.
 *
 * Needs a way to become admin: TRUSTAGENT_SETUP_URL (the link of the proxy's
 * "SETUP: open …/app/setup#token=…" line; `make setup-url` prints a new one) or
 * TRUSTAGENT_ADMIN_KEY (a key from TRUTHTABLE_ADMIN_KEYS). Without either the spec is skipped with
 * the reason.
 */
import { expect, test, type Page } from '@playwright/test';
import { requireStack } from './stack';

const TIMEOUT = 30_000;

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

async function signInAsAdmin(page: Page): Promise<void> {
  const setupUrl = process.env.TRUSTAGENT_SETUP_URL;
  const adminKey = process.env.TRUSTAGENT_ADMIN_KEY;
  if (setupUrl) {
    // Only the fragment matters: the page is served by the dashboard under test.
    await page.goto(`/app/setup${new URL(setupUrl).hash}`);
    await page.getByRole('button', { name: 'Create the admin key' }).click();
    await expect(page.getByText('Your admin key (shown once)')).toBeVisible({ timeout: TIMEOUT });
  } else {
    await page.goto('/app/setup');
    await page.getByLabel('Key').fill(adminKey ?? '');
    await page.getByRole('button', { name: 'Sign in' }).click();
  }
  await expect(page.getByText(/Admin actions are available/)).toBeVisible({ timeout: TIMEOUT });
}

test.describe('policy edit', () => {
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStack(playwright, testInfo);
  });

  test('saves a mode change as admin through the setup link, then rolls it back', async ({ page }) => {
    test.skip(
      !process.env.TRUSTAGENT_SETUP_URL && !process.env.TRUSTAGENT_ADMIN_KEY,
      'set TRUSTAGENT_SETUP_URL (the proxy SETUP link) or TRUSTAGENT_ADMIN_KEY to sign in as admin',
    );
    test.setTimeout(3 * TIMEOUT);
    await signInAsAdmin(page);
    const before = await policyState(page);
    expect(modeOf(before, 'support-bot')).toBe('flag');

    await page.goto('/app/policies');
    await page.getByRole('combobox', { name: 'Mode for support-bot' }).click();
    await page.getByRole('option', { name: /^Observe/ }).click();
    const bar = page.getByRole('region', { name: 'Unsaved changes' });
    await bar.getByRole('button', { name: 'Review and save' }).click();

    const dialog = page.getByRole('dialog', { name: 'Review and save a new policy version' });
    await expect(dialog.getByText('The server accepts this policy.')).toBeVisible({ timeout: TIMEOUT });
    await dialog.getByRole('textbox', { name: 'What changed and why' }).fill('Observe support-bot (e2e policy edit)');
    await dialog.getByRole('button', { name: 'Save version' }).click();
    await expect(dialog).toBeHidden({ timeout: TIMEOUT });

    const saved = await policyState(page);
    expect(saved.id).toBeGreaterThan(before.id);
    expect(saved.source).toBe('dashboard');
    expect(modeOf(saved, 'support-bot')).toBe('observe');

    // Leave the stack as it was: roll back to the version before the edit.
    await page.getByRole('button', { name: 'Versions' }).click();
    const sheet = page.getByRole('dialog', { name: 'Policy versions' });
    await sheet.getByRole('button', { name: new RegExp(`Roll back to version ${before.id}\\b`) }).click();
    const confirm = page.getByRole('alertdialog', { name: `Roll back to version ${before.id}?` });
    await confirm.getByRole('button', { name: 'Roll back' }).click();
    await expect.poll(async () => modeOf(await policyState(page), 'support-bot'), { timeout: TIMEOUT }).toBe('flag');
    expect((await policyState(page)).source).toBe('rollback');
  });
});
