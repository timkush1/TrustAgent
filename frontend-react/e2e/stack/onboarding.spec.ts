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
 * Step 0 (CO-10, D166): on the setup page before sign-in there is no WebSocket ticket request and
 * no WebSocket for 10 s, and no console error but Chrome's network line for each sign-in probe the
 * load sends (GET /api/session and /api/status answer 401 to a signed-out page), none of them
 * after the load; step 1 then checks the stream connects within 5 s of the claim.
 *
 * Order: Connect your app (8) runs before the refund example (7). A Playground run is traffic
 * too, so once it has run the Overview no longer waits for a first request.
 *
 * The journey's words, counted actions, secret checks and wizard steps are shared with the
 * first-run and golden-path specs (journey.ts).
 */
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test, type BrowserContext, type Page, type Route } from '@playwright/test';
import type { CredentialList, SettingsView, SetupStatus, SystemStatus } from '../../src/types/api';
import {
  CONNECT,
  COPY,
  FAST_CHECK,
  Journey,
  SEE_IT_WORK,
  TIMEOUT,
  checkTheRefundAnswer,
  createAppKey,
  openTheTrace,
  outcome,
  requireFreshStack,
  secureWithSetupLink,
} from './journey';
import { requireStack, seriousViolations, skipOrFail } from './stack';

const MOCK_UPSTREAM_ON_HOST = `http://127.0.0.1:${process.env.MOCK_UPSTREAM_HOST_PORT || '18090'}`;
/** D87: at most 8 user actions from the setup link to "Ready". Detours (steps 2 to 4) are not counted. */
const journey = new Journey(8);

/** Fake keys only (P4-PLAN §0 rule 8). The replacement key matches no redaction pattern. */
const run = Date.now().toString(16);
const NO_CREDITS_KEY = `sk-or-v1-e2e-nocredits-${run}0123456789abcdef`;
const UNMATCHED_KEY = 'cmp0e2eZq7Lw4Rt9Yx2Vb6Nm';

let setupUrl = '';
let context: BrowserContext;
let page: Page;
/** How mock-upstream's /auth should mint the next key (mock_key=nocredits), or null for a working one. */
let mockKey: string | null = null;
/** Where mock-upstream's /auth last redirected: the dashboard's /connect/callback?code=…. Never printed. */
let lastRedirect: string | null = null;
/**
 * The live stream as the page asks for it (CO-10): WebSocket ticket requests, sockets (with when
 * each opened and received its first frame, the hello), console errors, and when the setup link
 * was claimed (the first successful POST /api/session).
 */
const live = {
  tickets: 0,
  sockets: [] as { path: string; at: number; firstFrameAt: number | null }[],
  errors: [] as string[],
  /** Every 401 the page received, as "GET /api/session". */
  refused: [] as string[],
  claimedAt: 0,
};
/** Chrome's own console line for a response with an error status (not the app's console). */
const NETWORK_401 = 'Failed to load resource: the server responded with a status of 401 (Unauthorized)';
/** The reads that tell a signed-out page it is signed out (D166): each answers 401 once per load. */
const SIGN_IN_PROBES = new Set(['GET /api/session', 'GET /api/status']);

/** Records what CO-10 asserts on, for the whole run of `target`. */
function watchLiveStream(target: Page): void {
  target.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/ws-ticket') live.tickets += 1;
  });
  target.on('websocket', (ws) => {
    const socket = { path: new URL(ws.url()).pathname, at: Date.now(), firstFrameAt: null as number | null };
    live.sockets.push(socket);
    ws.on('framereceived', () => {
      socket.firstFrameAt ??= Date.now();
    });
  });
  target.on('console', (message) => {
    if (message.type() === 'error') live.errors.push(message.text());
  });
  target.on('pageerror', (error) => live.errors.push(error.message));
  target.on('response', (response) => {
    const call = `${response.request().method()} ${new URL(response.url()).pathname}`;
    if (call === 'POST /api/session' && response.ok() && !live.claimedAt) live.claimedAt = Date.now();
    if (response.status() === 401) live.refused.push(call);
  });
}

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

const getJson = <T>(path: string) => journey.getJson<T>(page, path);
const holdsSecret = (text: string) => journey.holdsSecret(text);

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

test.describe('onboarding', () => {
  test.describe.configure({ mode: 'serial', retries: 0, timeout: 120_000 });

  test.beforeAll(async ({ browser, playwright }, testInfo) => {
    await requireStack(playwright, testInfo);
    // Checked before anything is clicked: on any other stack the spec would spend its setup link.
    setupUrl = await requireFreshStack(playwright, testInfo, {
      spec: 'onboarding',
      start: 'make onboarding-down, make onboarding-up, then make -s onboarding-setup-url',
    });
    const baseURL = String(testInfo.project.use.baseURL ?? '');
    const probe = await playwright.request.newContext({ baseURL });
    try {
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
    watchLiveStream(page);
  });

  test.afterAll(async () => {
    await context?.close();
  });

  test('0 · before sign-in: no ticket, no WebSocket and no console error on the setup page for 10 s (CO-10)', async () => {
    await page.goto('/app/setup');
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Setup');
    // A fixed window, as CO-10 asks: the old shell retried a refused ticket with backoff in it.
    // The first 2 s are the page load; the 8 s after it must be silent.
    await page.waitForTimeout(2_000);
    const loaded = { errors: live.errors.length, refused: live.refused.length };
    await page.waitForTimeout(8_000);
    expect(live.tickets, 'no WebSocket ticket request before sign-in').toBe(0);
    expect(live.sockets.map((socket) => socket.path), 'no WebSocket before sign-in').toEqual([]);
    // D166: the load's sign-in probes answer 401, and Chrome logs each as a network line; nothing
    // else may reach the console, and nothing is retried once the page has loaded.
    expect(live.refused.filter((call) => !SIGN_IN_PROBES.has(call)), 'only the sign-in probes are refused').toEqual([]);
    expect(live.errors.filter((text) => text !== NETWORK_401), 'no console error from the app before sign-in').toEqual([]);
    expect(live.errors.length, 'one network line per refused probe, no other').toBe(live.refused.length);
    expect(live.refused.slice(loaded.refused), 'no 401 after the page loaded (nothing retries)').toEqual([]);
    expect(live.errors.slice(loaded.errors), 'no console line after the page loaded').toEqual([]);
    console.log(`CO-10 before sign-in, 10 s: ${live.tickets} ticket requests, ${live.sockets.length} WebSockets, 401s only at load: ${live.refused.join(', ') || 'none'}`);
    // The setup link then loads the page afresh: from /app/setup its #token would change only the fragment.
    await page.goto('about:blank');
  });

  test('1 · secure: claim the setup link, copy the admin key, tick I saved it, Next', async () => {
    await secureWithSetupLink(page, journey, setupUrl, { copy: true });
    // CO-10: once the setup link is claimed, the live stream connects (its hello arrives) within 5 s.
    expect(live.claimedAt, 'the setup link was claimed (POST /api/session)').toBeGreaterThan(0);
    await expect
      .poll(() => live.sockets.find((socket) => socket.path === '/ws' && socket.firstFrameAt !== null)?.firstFrameAt ?? null, {
        message: 'the live stream connects after the claim',
        timeout: 5_000,
      })
      .not.toBeNull();
    const connected = live.sockets.find((socket) => socket.path === '/ws' && socket.firstFrameAt !== null);
    const elapsed = (connected?.firstFrameAt ?? Infinity) - live.claimedAt;
    console.log(`CO-10 after the claim: the stream's first frame in ${elapsed} ms (budget 5000)`);
    expect(elapsed, 'ms from the claim to the stream’s first frame').toBeLessThanOrEqual(5_000);
    expect(live.tickets, 'same origin: no WebSocket ticket').toBe(0);
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
    await journey.step('Connect OpenRouter', () =>
      connectThroughMock(() => page.getByRole('button', { name: CONNECT.primary, exact: true }).click()),
    );
    // A real OpenRouter asks the viewer to approve; mock-upstream approves on its own.
    journey.away('approve at OpenRouter');
    await expect(page).toHaveURL(/\/app\/setup\?step=connect$/, { timeout: TIMEOUT });
    const result = outcome(page);
    await expect(result).toContainText(/Ready: the test call took [\d,]+ ms/, { timeout: TIMEOUT });
    journey.expectWithinBudget('Ready');
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

    await expect(dialog.getByRole('status')).toContainText(/Ready: the test call took [\d,]+ ms/, { timeout: TIMEOUT });
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

  // Connect your app runs before See it work (ON-9 lists them the other way round): the card waits
  // only while the window has no request, and the wizard's check is one, so after it the card
  // could never be seen going from waiting to received.
  test('7 · Connect your app: one app key, then waiting, then received after one /v1 request', async ({ request }) => {
    await page.goto('/app/setup?step=try');
    await expect(page.getByRole('heading', { level: 2, name: `3. ${SEE_IT_WORK.title}` })).toBeVisible({ timeout: TIMEOUT });
    const appKey = await createAppKey(page, journey);
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
    await checkTheRefundAnswer(page, journey);
    await openTheTrace(page, journey);
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
