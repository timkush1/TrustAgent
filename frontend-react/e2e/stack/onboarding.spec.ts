/**
 * Onboarding on a fresh stack (SPEC §3.3 setup wizard, §4.2; P4-PLAN ON-9, D38, D39, D70, D75,
 * D76, D87): from the proxy's one-time setup link to a checked answer, with every vendor played by
 * mock-upstream, so nothing costs anything.
 *
 * Runs only on the onboarding stack (`make onboarding-up`: no admin key, no decision-model key,
 * public read off, the mock judge set by the environment, OpenRouter on mock-upstream):
 *
 *   env -u TRUSTAGENT_ADMIN_KEY TRUSTAGENT_SETUP_URL="$(make -s onboarding-setup-url)" \
 *     MOCK_UPSTREAM_HOST_PORT=… CI=1 npx playwright test --project=stack --workers=1 -g onboarding
 *
 * The browser cannot resolve the proxy's OpenRouter address (http://mock-upstream:8090), so the
 * spec routes OpenRouter's sign-in page (/auth on any origin but the dashboard's) to mock-upstream's
 * published port (route.fetch without following redirects, then route.fulfill); when the browser
 * does not follow the fulfilled redirect it goes to the callback itself (D70). One page walks every
 * step in order, so a failure stops the rest (serial, no retry: the setup link works once). With
 * P4_EVIDENCE_DIR set it writes /api/credentials, /api/settings and /api/status there, after
 * Connect and at the end, once it has checked they hold no secret.
 *
 * Order: Connect your app (8) runs before the refund example (7). A Playground run is traffic
 * too, so once it has run the Overview no longer waits for a first request.
 */
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, test, type BrowserContext, type Page, type Route } from '@playwright/test';
import type { CredentialList, SettingsView, SetupStatus, SystemStatus } from '../../src/types/api';
import { requireStack, seriousViolations, skipOrFail } from './stack';

const TIMEOUT = 30_000;
const MOCK_UPSTREAM_ON_HOST = `http://127.0.0.1:${process.env.MOCK_UPSTREAM_HOST_PORT || '18090'}`;
/** D87: at most 8 user actions from the setup link to "Ready". */
const CLICK_BUDGET = 8;

/** The words the wizard shows, from the copy contract (contracts/ui/copy.v1.json). */
const COPY = (
  JSON.parse(readFileSync(fileURLToPath(new URL('../../../contracts/ui/copy.v1.json', import.meta.url)), 'utf8')) as {
    copy: {
      setup: Record<'step_connect' | 'step_secure' | 'step_see_it_work', Record<string, string>>;
      connect_app: Record<string, string>;
      tiers: Record<string, string>;
      badges: Record<string, string>;
    };
  }
).copy;
const CONNECT = COPY.setup.step_connect;
const SEE_IT_WORK = COPY.setup.step_see_it_work;
const FAST_CHECK = COPY.tiers.fast_check;

/** Fake keys only (P4-PLAN §0 rule 8). The replacement key matches no redaction pattern. */
const run = Date.now().toString(16);
const NO_CREDITS_KEY = `sk-or-v1-e2e-nocredits-${run}0123456789abcdef`;
const UNMATCHED_KEY = 'cmp0e2eZq7Lw4Rt9Yx2Vb6Nm';
/** What must never appear in an API body or on screen once saved: the I-6 log grep, plus the keys seen here. */
const SECRET_PATTERN = /sk-or-v1-[A-Za-z0-9_-]{8,}|sk-test-[A-Za-z0-9_-]{8,}|cmp0e2eZq7Lw4Rt9Yx2Vb6Nm|mockcode_[0-9a-f]{8,}/;
const shownOnce: string[] = [];

/** Whether `text` holds a secret. Never print the match: the caller asserts on the boolean. */
function holdsSecret(text: string): boolean {
  return SECRET_PATTERN.test(text) || shownOnce.some((s) => s !== '' && text.includes(s));
}

/** The user actions of the happy path, counted (D87). Detours (steps 2 to 4) are not counted. */
const happyPath: string[] = [];
async function step<T>(what: string, action: () => Promise<T>): Promise<T> {
  happyPath.push(what);
  return test.step(`user action ${happyPath.length}: ${what}`, action);
}

let context: BrowserContext;
let page: Page;
/** How mock-upstream's /auth should mint the next key (mock_key=nocredits), or null for a working one. */
let mockKey: string | null = null;
/** Where mock-upstream's /auth last redirected: the dashboard's /connect/callback?code=…. Never printed. */
let lastRedirect: string | null = null;

/** Routes the proxy's OpenRouter address to mock-upstream's published port (D70). */
async function routeMockUpstream(route: Route): Promise<void> {
  const from = new URL(route.request().url());
  const to = new URL(`${from.pathname}${from.search}`, MOCK_UPSTREAM_ON_HOST);
  if (from.pathname === '/auth' && mockKey) to.searchParams.set('mock_key', mockKey);
  const response = await route.fetch({ url: to.toString(), maxRedirects: 0 });
  lastRedirect = response.headers()['location'] ?? null;
  await route.fulfill({ response });
}

/**
 * Clicks Connect OpenRouter (`click`) and waits until the browser is back on the dashboard's
 * /connect/callback; when it did not follow the fulfilled redirect, it goes there itself.
 */
async function connectThroughMock(click: () => Promise<void>): Promise<void> {
  lastRedirect = null;
  const atCallback = page
    .waitForEvent('framenavigated', {
      predicate: (frame) => frame === page.mainFrame() && new URL(frame.url()).pathname === '/connect/callback',
      timeout: 15_000,
    })
    .then(
      () => true,
      () => false,
    );
  await click();
  if (await atCallback) return;
  expect(lastRedirect !== null, 'mock-upstream /auth answered with a redirect to /connect/callback').toBe(true);
  await page.goto(lastRedirect ?? '/connect/callback');
}

async function getJson<T>(path: string): Promise<T> {
  const res = await page.request.get(path, { failOnStatusCode: false });
  expect(res.status(), `GET ${path}`).toBe(200);
  const text = await res.text();
  expect(holdsSecret(text), `GET ${path} carries no secret`).toBe(false);
  return JSON.parse(text) as T;
}

const settings = () => getJson<SettingsView>('/api/settings');
const status = () => getJson<SystemStatus>('/api/status');
async function openrouterCredential() {
  const list = await getJson<CredentialList>('/api/credentials');
  return list.items.find((c) => c.name === 'openrouter') ?? null;
}

/** Writes the three API bodies to P4_EVIDENCE_DIR (when set), each checked for secrets first. */
async function writeEvidence(stage: string): Promise<void> {
  const dir = process.env.P4_EVIDENCE_DIR;
  if (!dir) return;
  mkdirSync(dir, { recursive: true });
  for (const [name, path] of [
    ['credentials', '/api/credentials'],
    ['settings', '/api/settings'],
    ['status', '/api/status'],
  ] as const) {
    const body = await getJson<unknown>(path);
    writeFileSync(join(dir, `onboarding-${stage}-${name}.json`), `${JSON.stringify(body, null, 2)}\n`);
  }
}

/** The text of a key shown once (`title` names it), kept only to check it never shows again. */
async function shownOnceSecret(title: string): Promise<string> {
  const value = (await page.getByText(title, { exact: true }).locator('xpath=..').locator('code').textContent())?.trim() ?? '';
  expect(value.length > 0, `${title} is on screen`).toBe(true);
  shownOnce.push(value);
  return value;
}

/** The outcome a key write or a Connect left on the page (CredentialResult). */
const outcome = () => page.getByRole('status').filter({ hasText: /Ready · |no credits|Saved/ }).last();

test.describe('onboarding', () => {
  test.describe.configure({ mode: 'serial', retries: 0, timeout: 120_000 });

  test.beforeAll(async ({ browser, playwright }, testInfo) => {
    await requireStack(playwright, testInfo);
    const setupUrl = process.env.TRUSTAGENT_SETUP_URL;
    skipOrFail(!setupUrl, 'set TRUSTAGENT_SETUP_URL to the onboarding stack’s setup link (make -s onboarding-setup-url)');
    const baseURL = String(testInfo.project.use.baseURL ?? '');
    const probe = await playwright.request.newContext({ baseURL });
    try {
      // Checked before anything is clicked: on any other stack the spec would spend its setup link.
      const res = await probe.get('/api/setup/status', { failOnStatusCode: false });
      const setup = res.ok() ? ((await res.json()) as Partial<SetupStatus>) : null;
      skipOrFail(setup?.demo !== false, 'this is the demo stack (or its setup status did not answer): the onboarding spec needs the onboarding stack (make onboarding-up)');
      skipOrFail(setup?.persistence !== true || setup.readonly !== false, 'the onboarding spec needs a writable stack with a database, as make onboarding-up starts it');
      skipOrFail(setup?.setup_required !== true, 'this stack already has an admin: the onboarding spec needs a fresh onboarding stack (make onboarding-down, then make onboarding-up)');
      const mock = await probe.get(`${MOCK_UPSTREAM_ON_HOST}/healthz`, { failOnStatusCode: false, timeout: 5_000 }).catch(() => null);
      skipOrFail(!mock?.ok(), `mock-upstream does not answer at ${MOCK_UPSTREAM_ON_HOST}: set MOCK_UPSTREAM_HOST_PORT to its published port`);
    } finally {
      await probe.dispose();
    }
    context = await browser.newContext({ baseURL, permissions: ['clipboard-read', 'clipboard-write'] });
    // OpenRouter's sign-in page, as the proxy builds Connect's auth_url (TRUTHTABLE_OPENROUTER_BASE_URL
    // + /auth): http://mock-upstream:8090/auth on the onboarding stack, which only the stack's network
    // resolves. Matched by path on any origin but the dashboard's, so a proxy started outside compose
    // with a host address for mock-upstream works too.
    const dashboard = new URL(baseURL).origin;
    await context.route((url) => url.origin !== dashboard && url.pathname === '/auth', routeMockUpstream);
    page = await context.newPage();
  });

  test.afterAll(async () => {
    await context?.close();
  });

  test('1 · secure: claim the setup link, copy the admin key, tick I saved it, Next', async () => {
    const setupUrl = new URL(process.env.TRUSTAGENT_SETUP_URL ?? '');
    // Only the fragment matters: the page is served by the dashboard under test.
    await step('open the setup link', () => page.goto(`/app/setup${setupUrl.hash}`));
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Setup');
    await expect(page, 'the token leaves the address bar').not.toHaveURL(/#token=/);
    await step('Create the admin key', () => page.getByRole('button', { name: 'Create the admin key' }).click());
    await expect(page.getByText('Your admin key (shown once)')).toBeVisible({ timeout: TIMEOUT });
    const adminKey = await shownOnceSecret('Your admin key (shown once)');
    await expect(page.getByText(/Admin actions are available/)).toBeVisible();

    const next = page.getByRole('button', { name: 'Next', exact: true });
    await expect(next, 'Next waits for “I saved it”').toBeDisabled();
    await step('Copy the admin key', () => page.getByRole('button', { name: 'Copy the admin key' }).click());
    await expect(page.getByRole('button', { name: 'Copied' })).toBeVisible();
    const clipboard = await page.evaluate(() => navigator.clipboard.readText());
    expect(clipboard === adminKey, 'the clipboard holds the admin key').toBe(true);
    await step('tick I saved it', () => page.getByRole('checkbox', { name: COPY.setup.step_secure.saved_confirm }).check());
    await step('Next', () => next.click());
    await expect(page).toHaveURL(/\/app\/setup\?step=connect$/);
    await expect(page.getByRole('heading', { level: 2, name: `2. ${CONNECT.title}` })).toBeVisible();
    await expect(page.getByText(CONNECT.prerequisite)).toBeVisible();
    await expect(page.getByText(CONNECT.privacy)).toBeVisible();
  });

  test('2 · paste a key without credits: stored, nothing bound, check again offered', async () => {
    await page.getByRole('button', { name: CONNECT.paste_key }).click();
    const form = page.getByRole('form', { name: 'Paste an OpenRouter key' });
    await form.getByLabel('OpenRouter key').fill(NO_CREDITS_KEY);
    const stored = page.waitForResponse((r) => r.request().method() === 'PUT' && new URL(r.url()).pathname === '/api/credentials/openrouter', { timeout: TIMEOUT });
    await form.getByRole('button', { name: 'Save and check' }).click();
    expect((await stored).status(), 'PUT /api/credentials/openrouter').toBe(200);

    await expect(page.getByText(CONNECT.no_credits)).toBeVisible({ timeout: TIMEOUT });
    await expect(page.getByText(/^Now used for:/)).toHaveCount(0);
    const checkAgain = page.getByRole('button', { name: CONNECT.check_again });
    await expect(checkAgain).toBeVisible();
    // #57 with bind: still no credits, so still nothing bound.
    const checked = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/credentials/openrouter/verify', {
      timeout: TIMEOUT,
    });
    await checkAgain.click();
    expect((await checked).status(), 'POST /api/credentials/openrouter/verify').toBe(200);
    await expect(page.getByText(CONNECT.no_credits)).toBeVisible();
    await expect(checkAgain).toBeEnabled();

    const credential = await openrouterCredential();
    expect(credential?.source).toBe('dashboard');
    expect(credential?.last_verify_status).toBe('no_credits');
    expect(credential?.used_by).toEqual([]);
    expect((await settings()).runtime.bindings.decision_model).toBeNull();
    expect(holdsSecret((await page.locator('body').textContent()) ?? ''), 'the pasted key is not on screen').toBe(false);
  });

  test('3 · Connect with a key without credits (402): stored, nothing bound', async () => {
    const pasted = (await openrouterCredential())?.fingerprint;
    expect(pasted, 'the pasted key has a fingerprint').toBeTruthy();
    mockKey = 'nocredits';
    await connectThroughMock(() => page.getByRole('button', { name: CONNECT.primary, exact: true }).click());
    await expect(page).toHaveURL(/\/app\/setup\?step=connect$/, { timeout: TIMEOUT });
    await expect(page.getByText(CONNECT.no_credits)).toBeVisible({ timeout: TIMEOUT });
    await expect(page.getByText(/^Now used for:/)).toHaveCount(0);
    await expect(page.getByRole('button', { name: CONNECT.check_again })).toBeVisible();

    const credential = await openrouterCredential();
    expect(credential?.fingerprint, 'Connect stored the key OpenRouter issued').not.toBe(pasted);
    expect(credential?.last_verify_status).toBe('no_credits');
    expect(credential?.used_by).toEqual([]);
    expect((await settings()).runtime.bindings.decision_model).toBeNull();
  });

  test('4 · try it without a key: simulated checks, labelled, with the top-bar badge', async () => {
    const next = page.getByRole('button', { name: 'Next', exact: true });
    await expect(next, 'step 3 waits for a key or simulated checks').toBeDisabled();
    await page.getByRole('button', { name: CONNECT.simulated }).click();
    await expect(page.getByText(CONNECT.simulated_on)).toBeVisible({ timeout: TIMEOUT });
    await expect(page.getByRole('banner').getByText(COPY.badges.simulated_checks).first()).toBeVisible({ timeout: TIMEOUT });
    await expect(next).toBeEnabled();

    const view = await settings();
    expect(view.runtime.decision_model.backend).toBe('mock');
    await expect.poll(async () => (await status()).decision_model.backend, { timeout: TIMEOUT }).toBe('mock');
  });

  test('5 · Connect OpenRouter: Ready, the fast check bound, the judge left to the environment', async () => {
    mockKey = null;
    await step('Connect OpenRouter', () =>
      connectThroughMock(() => page.getByRole('button', { name: CONNECT.primary, exact: true }).click()),
    );
    // A real OpenRouter asks the viewer to approve; mock-upstream approves on its own.
    happyPath.push('approve at OpenRouter');
    await expect(page).toHaveURL(/\/app\/setup\?step=connect$/, { timeout: TIMEOUT });
    const result = outcome();
    await expect(result).toContainText(/Ready · [\d,]+ ms/, { timeout: TIMEOUT });
    expect(happyPath.length, `happy-path actions: ${happyPath.join(' → ')}`).toBeLessThanOrEqual(CLICK_BUDGET);
    await expect(result).toContainText(`Now used for: ${FAST_CHECK}.`);
    await expect(result).toContainText(CONNECT.simulated_off);
    await expect(result).toContainText(CONNECT.judge_env_managed);
    await expect(page.getByRole('banner').getByText(COPY.badges.simulated_checks)).toHaveCount(0, { timeout: TIMEOUT });

    const view = await settings();
    expect(view.runtime.bindings.decision_model).toBe('openrouter');
    expect(view.runtime.decision_model.backend, 'D76: simulated mode left').toBe('auto');
    expect(view.env_managed).toContain('bindings.judge');
    await expect.poll(async () => (await status()).decision_model.backend, { timeout: TIMEOUT }).not.toBe('mock');
    // The engine applied this very settings version (D34).
    await expect
      .poll(async () => (await status()).engine.runtime_config_version ?? 0, { timeout: TIMEOUT, message: 'engine.runtime_config_version' })
      .toBe((await settings()).version);
    expect((await status()).engine.runtime_config_version ?? 0).toBeGreaterThan(0);

    await page.goto('/app/settings/connections');
    const card = page.locator('[data-credential="openrouter"]');
    // "Used for": the fast check only; the judge chip stays off, because the environment sets the judge.
    await expect(card.getByText(FAST_CHECK, { exact: true })).toBeVisible({ timeout: TIMEOUT });
    await expect(card.getByText(`${COPY.tiers.deep_audit} judge`, { exact: true })).toHaveCount(0);
    await expect(card.getByText('Working', { exact: true })).toBeVisible();
    await writeEvidence('after-connect');
  });

  test('6 · Connections: replace the key with a pasted one, checked and bound', async () => {
    const before = (await openrouterCredential())?.fingerprint;
    expect(before, 'the connected key has a fingerprint').toBeTruthy();
    const card = page.locator('[data-credential="openrouter"]');
    await card.getByRole('button', { name: 'Replace key' }).click();
    const dialog = page.getByRole('dialog', { name: 'OpenRouter key' });
    await dialog.getByLabel('API key').fill(UNMATCHED_KEY);
    await expect(dialog.getByRole('checkbox', { name: FAST_CHECK })).toBeChecked();
    // The judge is set by the environment (LLM_PROVIDER=mock): left as it is.
    const judge = dialog.getByRole('checkbox', { name: /judge/ });
    await expect(judge).toBeDisabled();
    await expect(judge).not.toBeChecked();
    await expect(dialog.getByText('Set by the environment, so it stays as it is.')).toBeVisible();
    await dialog.getByRole('button', { name: 'Save and check' }).click();

    await expect(dialog.getByRole('status')).toContainText(/Ready · [\d,]+ ms/, { timeout: TIMEOUT });
    await expect(dialog.getByRole('status')).toContainText(`Now used for: ${FAST_CHECK}.`);
    await dialog.getByRole('button', { name: 'Done' }).click();
    await expect(dialog).toBeHidden();

    const credential = await openrouterCredential();
    expect(credential?.fingerprint, 'the pasted key replaced the connected one').not.toBe(before);
    expect(credential?.last_verify_status).toBe('ok');
    expect(credential?.used_by).toEqual(['decision_model']);
    expect((await settings()).runtime.bindings.judge, 'the judge stays with the environment').not.toBe('openrouter');
    expect(holdsSecret((await page.locator('body').textContent()) ?? ''), 'the pasted key is shown only masked').toBe(false);
  });

  test('7 · Connect your app: one app key, then waiting, then received after one /v1 request', async ({ request }) => {
    await page.goto('/app/setup?step=try');
    await expect(page.getByRole('heading', { level: 2, name: `3. ${SEE_IT_WORK.title}` })).toBeVisible({ timeout: TIMEOUT });
    await page.getByRole('button', { name: 'Create an app key' }).click();
    await expect(page.getByText('my-app (shown once)')).toBeVisible({ timeout: TIMEOUT });
    const appKey = await shownOnceSecret('my-app (shown once)');
    // Only one app key: the wizard offers none once it exists.
    await page.reload();
    await expect(page.getByText(/with the key “my-app”/)).toBeVisible({ timeout: TIMEOUT });
    await expect(page.getByRole('button', { name: 'Create an app key' })).toHaveCount(0);

    await page.goto('/app/overview');
    await expect(page.getByText(COPY.connect_app.waiting)).toBeVisible({ timeout: TIMEOUT });
    await expect(page.getByText(/the key “my-app”/).first(), 'the card names the app key, never its secret').toBeVisible();
    await expect(page.getByRole('button', { name: 'Create an app key' }), 'no second app key on a guess').toHaveCount(0);

    // One request from "your app": its own key, none of the dashboard's cookies.
    const res = await request.post('/v1/chat/completions', {
      headers: { 'X-TrustAgent-Key': appKey },
      data: { model: 'gpt-4o-mini', messages: [{ role: 'user', content: 'Hello from my app' }] },
      failOnStatusCode: false,
    });
    expect(res.status(), 'POST /v1/chat/completions with the app key').toBe(200);

    // The live stream flips the card; once the totals count the request, it folds under its heading.
    const received = page.getByText(COPY.connect_app.received, { exact: true });
    const folded = page.locator('details > summary', { hasText: COPY.connect_app.title });
    await expect(received.or(folded).first()).toBeVisible({ timeout: TIMEOUT });
    if (!(await received.isVisible())) await folded.click();
    await expect(received).toBeVisible();
  });

  test('8 · see it work: the refund answer is flagged and its contradicted claim marked', async () => {
    await page.goto('/app/setup?step=try');
    await expect(page.getByText(SEE_IT_WORK.body)).toBeVisible({ timeout: TIMEOUT });
    await page.getByRole('button', { name: 'Check this answer' }).click();
    const timeline = page.getByRole('list', { name: 'What happened to the request' });
    await expect(timeline.getByText('Flagged', { exact: true }).first()).toBeVisible({ timeout: TIMEOUT });
    await page.getByRole('link', { name: 'Open the trace', exact: true }).click();
    await expect(page).toHaveURL(/\/app\/traffic\/[^/?#]+$/);
    // The deep audit lands a little later: the 60-day claim is contradicted by refund-policy.md.
    const claims = page.getByRole('list', { name: 'Claims' });
    await expect(claims.getByText('Contradicted', { exact: true })).toBeVisible({ timeout: 60_000 });
    const answer = page.getByRole('region', { name: 'Answer' });
    await expect(answer.locator('mark[data-v="contradicted"]')).toHaveText('60 days');
  });

  test('9 · no serious accessibility violations in the wizard, the callback and Settings', async () => {
    // Fifteen page loads, each with a full axe run: about a minute on a quiet machine, and close to
    // the suite's two minutes on a busy one.
    test.setTimeout(240_000);
    // A callback with no sign-in waiting in this tab: the code leaves the address bar, and the page
    // says where to open the dashboard.
    await page.goto('/connect/callback?code=not-a-real-code');
    await expect(page).toHaveURL(/\/connect\/callback$/);
    const publicUrl = ((await getJson<Partial<SetupStatus>>('/api/setup/status')).public_url ?? '').trim();
    const alert = page.getByRole('alert');
    await expect(alert).toContainText('No sign-in is waiting in this tab.', { timeout: TIMEOUT });
    await expect(alert).toContainText(`Open the dashboard at ${publicUrl || 'its usual address'} and connect again.`);
    expect(await seriousViolations(page), '/connect/callback without a pending sign-in').toEqual([]);

    const pages = ['/app/setup?step=secure', '/app/setup?step=connect', '/app/setup?step=try', '/app/settings/connections', '/app/settings/models', '/app/settings/access', '/app/overview'];
    for (const theme of ['light', 'dark'] as const) {
      await page.evaluate((t) => localStorage.setItem('trustagent.theme', t), theme);
      for (const path of pages) {
        await page.goto(path);
        await expect(page.getByRole('heading', { level: 1 })).toBeVisible({ timeout: TIMEOUT });
        await expect(page.locator('[aria-busy="true"]')).toHaveCount(0, { timeout: TIMEOUT });
        expect(await seriousViolations(page), `${path} (${theme})`).toEqual([]);
      }
    }
    expect(holdsSecret((await page.locator('body').textContent()) ?? ''), 'no secret on screen at the end').toBe(false);
    await writeEvidence('final');
  });
});
