/**
 * The user's journey through a fresh stack, shared by the onboarding, first-run and golden-path
 * specs (D130): the words the wizard shows (from the copy contract), the counted user actions, the
 * secrets a run has seen, and the wizard's steps as one call each.
 *
 * A journey only clicks and types, as a person would: it never runs a command and never reads a
 * log (golden-path checks both). Its setup link comes from the environment, which `make first-run`,
 * `make golden-path` and `make onboarding-setup-url` fill in.
 *
 * D95 (CO-19): binding an upstream key for every caller (bindings.upstream_default) needs an app
 * key with the ingest scope first. No journey here binds one; one that does calls createAppKey
 * before it. Connect OpenRouter binds only the fast check and the judge, which need none.
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { expect, test, type APIRequestContext, type Page, type PlaywrightWorkerArgs, type TestInfo } from '@playwright/test';
import type { SetupStatus } from '../../src/types/api';
import { skipOrFail } from './stack';

export const TIMEOUT = 30_000;

/** The words the wizard shows, from the copy contract (contracts/ui/copy.v1.json). */
export const COPY = (
  JSON.parse(readFileSync(fileURLToPath(new URL('../../../contracts/ui/copy.v1.json', import.meta.url)), 'utf8')) as {
    copy: {
      setup: Record<'step_secure' | 'step_connect' | 'step_see_it_work', Record<string, string>>;
      connect_app: Record<string, string>;
      tiers: Record<string, string>;
      badges: Record<string, string>;
      value_labels: Record<string, string>;
    };
  }
).copy;
export const SECURE = COPY.setup.step_secure;
export const CONNECT = COPY.setup.step_connect;
export const SEE_IT_WORK = COPY.setup.step_see_it_work;
export const FAST_CHECK = COPY.tiers.fast_check;

/** What must never appear in an API body or on screen once saved: the fake-key conventions. */
const SECRET_PATTERN = /sk-or-v1-[A-Za-z0-9_-]{8,}|sk-test-[A-Za-z0-9_-]{8,}|cmp0e2eZq7Lw4Rt9Yx2Vb6Nm|mockcode_[0-9a-f]{8,}/;

/**
 * The user actions of a journey's happy path, counted against a budget (D87, D130, D162): every
 * click and tick a person makes, Copy included; the setup link the stack opens is the start, not
 * an action. Detours (a spec's extra checks) are not counted; an action that happens away from the
 * page (approving at OpenRouter) is counted with `away`.
 */
export class Journey {
  readonly actions: string[] = [];
  /** Keys shown once during the run, kept only to check they never show again. Never printed. */
  readonly shownOnce: string[] = [];
  /** The most user actions the journey's goal may take. */
  readonly budget: number;

  constructor(budget: number) {
    this.budget = budget;
  }

  /**
   * Where the journey starts, not counted: `make up` (and `make first-run`, `make golden-path`)
   * opens the setup link itself (SPEC §1 row 39), so the person's first action is on that page.
   */
  start<T>(what: string, action: () => Promise<T>): Promise<T> {
    return test.step(`start: ${what}`, action);
  }

  /** A counted user action, reported as a Playwright step. */
  step<T>(what: string, action: () => Promise<T>): Promise<T> {
    this.actions.push(what);
    return test.step(`user action ${this.actions.length}: ${what}`, action);
  }

  /** A counted action that happens away from the page (the vendor's approve screen). */
  away(what: string): void {
    this.actions.push(what);
  }

  /**
   * Fails, listing every action, when the journey took more than its budget. The count is printed
   * either way, so a run's log records it (the proof's evidence).
   */
  expectWithinBudget(goal: string): void {
    const summary = `${this.actions.length} user actions to ${goal} (budget ${this.budget}): ${this.actions.join(' → ')}`;
    console.log(summary);
    expect(this.actions.length, summary).toBeLessThanOrEqual(this.budget);
  }

  /** Whether `text` holds a secret. Never print the match: the caller asserts on the boolean. */
  holdsSecret(text: string): boolean {
    return SECRET_PATTERN.test(text) || this.shownOnce.some((s) => s !== '' && text.includes(s));
  }

  /** The text of a key shown once (`title` names it), kept only to check it never shows again. */
  async shownOnceSecret(page: Page, title: string): Promise<string> {
    const value = (await page.getByText(title, { exact: true }).locator('xpath=..').locator('code').textContent())?.trim() ?? '';
    expect(value.length > 0, `${title} is on screen`).toBe(true);
    this.shownOnce.push(value);
    return value;
  }

  /** GET `path` as the signed-in page; the body must hold no secret. */
  async getJson<T>(page: Page, path: string): Promise<T> {
    const res = await page.request.get(path, { failOnStatusCode: false });
    expect(res.status(), `GET ${path}`).toBe(200);
    const text = await res.text();
    expect(this.holdsSecret(text), `GET ${path} carries no secret`).toBe(false);
    return JSON.parse(text) as T;
  }
}

/** The outcome a key write or a Connect left on the page (CredentialResult). */
export const outcome = (page: Page) => page.getByRole('status').filter({ hasText: /Ready: |no credits|Saved/ }).last();

/** What a fresh-stack spec needs before it clicks anything (its setup link works once). */
export interface FreshStackNeeds {
  /** Which spec, for the skip messages. */
  spec: string;
  /** How to start the stack it needs, for the skip messages. */
  start: string;
  /** Other environment variables the spec reads, each with what it is for. */
  env?: Record<string, string>;
}

/**
 * Checks, before anything is clicked, that this is a fresh writable stack that is not the demo
 * and that the spec has its setup link: on any other stack it would spend the link. Skips (or
 * fails in CI) with the reason otherwise. Returns the setup link.
 */
export async function requireFreshStack(
  playwright: PlaywrightWorkerArgs['playwright'],
  testInfo: TestInfo,
  needs: FreshStackNeeds,
): Promise<string> {
  const setupUrl = process.env.TRUSTAGENT_SETUP_URL ?? '';
  skipOrFail(!setupUrl, `set TRUSTAGENT_SETUP_URL to the fresh stack’s setup link (${needs.start})`);
  for (const [name, what] of Object.entries(needs.env ?? {})) {
    skipOrFail(!process.env[name], `set ${name} (${what}); ${needs.start} sets it`);
  }
  const probe = await playwright.request.newContext({ baseURL: String(testInfo.project.use.baseURL ?? '') });
  try {
    const setup = await setupStatus(probe);
    skipOrFail(setup?.demo !== false, `this is the demo stack (or its setup status did not answer): the ${needs.spec} spec needs a fresh stack (${needs.start})`);
    skipOrFail(setup?.persistence !== true || setup.readonly !== false, `the ${needs.spec} spec needs a writable stack with a database (${needs.start})`);
    skipOrFail(setup?.setup_required !== true, `this stack already has an admin: the ${needs.spec} spec needs a fresh one (${needs.start})`);
  } finally {
    await probe.dispose();
  }
  return setupUrl;
}

async function setupStatus(request: APIRequestContext): Promise<Partial<SetupStatus> | null> {
  const res = await request.get('/api/setup/status', { failOnStatusCode: false });
  return res.ok() ? ((await res.json()) as Partial<SetupStatus>) : null;
}

export interface SecureOptions {
  /** Click "Copy the admin key" and check the clipboard (the onboarding spec does). */
  copy: boolean;
}

/**
 * Step 1, Secure: opens the setup link, creates the admin key, ticks "I saved it" and goes on.
 * Returns the admin key (kept as a shown-once secret, never printed).
 */
export async function secureWithSetupLink(page: Page, journey: Journey, setupUrl: string, options: SecureOptions): Promise<string> {
  // Only the fragment matters: the page is served by the dashboard under test.
  await journey.start('the setup link, opened by make up', () => page.goto(`/app/setup${new URL(setupUrl).hash}`));
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Setup');
  await expect(page, 'the token leaves the address bar').not.toHaveURL(/#token=/);
  await expect(page.getByText(SECURE.no_token), 'a setup link never needs the logs').toHaveCount(0);
  await journey.step('Create the admin key', () => page.getByRole('button', { name: 'Create the admin key' }).click());
  await expect(page.getByText('Your admin key (shown once)')).toBeVisible({ timeout: TIMEOUT });
  const adminKey = await journey.shownOnceSecret(page, 'Your admin key (shown once)');
  await expect(page.getByText(/Admin actions are available/)).toBeVisible();

  const next = page.getByRole('button', { name: 'Next', exact: true });
  await expect(next, 'Next waits for “I saved it”').toBeDisabled();
  if (options.copy) {
    await journey.step('Copy the admin key', () => page.getByRole('button', { name: 'Copy the admin key' }).click());
    await expect(page.getByRole('button', { name: 'Copied' })).toBeVisible();
    const clipboard = await page.evaluate(() => navigator.clipboard.readText());
    expect(clipboard === adminKey, 'the clipboard holds the admin key').toBe(true);
  }
  await journey.step('tick I saved it', () => page.getByRole('checkbox', { name: SECURE.saved_confirm }).check());
  await journey.step('Next', () => next.click());
  await expect(page).toHaveURL(/\/app\/setup\?step=connect$/);
  await expect(page.getByRole('heading', { level: 2, name: `2. ${CONNECT.title}` })).toBeVisible();
  return adminKey;
}

/** Step 2 without a key: simulated checks ($0, labelled), then Next to step 3. */
export async function trySimulated(page: Page, journey: Journey): Promise<void> {
  await journey.step('Try it without a key', () => page.getByRole('button', { name: CONNECT.simulated }).click());
  await expect(page.getByText(CONNECT.simulated_on)).toBeVisible({ timeout: TIMEOUT });
  await expect(page.getByRole('banner').getByText(COPY.badges.simulated_checks).first()).toBeVisible({ timeout: TIMEOUT });
  await journey.step('Next', () => page.getByRole('button', { name: 'Next', exact: true }).click());
  await expect(page).toHaveURL(/\/app\/setup\?step=try$/);
  await expect(page.getByRole('heading', { level: 2, name: `3. ${SEE_IT_WORK.title}` })).toBeVisible({ timeout: TIMEOUT });
}

/**
 * Step 3, See it work: checks the refund example (60 days in the answer, 30 in the policy) and
 * waits until the timeline shows it flagged.
 */
export async function checkTheRefundAnswer(page: Page, journey: Journey): Promise<void> {
  await expect(page.getByText(SEE_IT_WORK.body)).toBeVisible({ timeout: TIMEOUT });
  await journey.step('Check this answer', () => page.getByRole('button', { name: 'Check this answer' }).click());
  const timeline = page.getByRole('list', { name: 'What happened to the request' });
  await expect(timeline.getByText('Flagged', { exact: true }).first()).toBeVisible({ timeout: TIMEOUT });
}

/** From step 3's result to the trace's detail page. */
export async function openTheTrace(page: Page, journey: Journey): Promise<void> {
  await journey.step('Open the trace', () => page.getByRole('link', { name: 'Open the trace', exact: true }).click());
  await expect(page).toHaveURL(/\/app\/traffic\/[^/?#]+$/);
}

/**
 * Step 3's app key ("Connect your app"): mints one with the ingest scope and returns it (kept as a
 * shown-once secret). The wizard offers it only while no app key exists. Call it before any
 * upstream bind (D95).
 */
export async function createAppKey(page: Page, journey: Journey): Promise<string> {
  await page.getByRole('button', { name: 'Create an app key' }).click();
  await expect(page.getByText('my-app (shown once)')).toBeVisible({ timeout: TIMEOUT });
  return journey.shownOnceSecret(page, 'my-app (shown once)');
}
