/**
 * Evaluator Studio on the static site (D107, D108), against a mocked openrouter.ai
 * (e2e/static/openrouter.ts: the synthetic vendor fixtures, CORS as the real API answers it, and a
 * check that every request sends only ALLOWED_REQUEST_HEADERS). Nothing reaches the internet.
 *
 * A check from a template, edited, tested on the decision model with the tab's key, given a
 * threshold by the quick calibration, then copied and downloaded as a valid evaluator.v1 file that
 * carries neither the key nor any sample text; Draft with AI counting one call per turn on the
 * tab's counter; the online demo offering no Save, Watch or Activate; and the page at 400 px, in
 * the dark theme and through axe.
 *
 * The consent dialog that stores a key belongs to the live Playground, whose spec walks Connect and
 * Paste. Here the viewer arrives with a key already in the tab, as after connecting there.
 */
import { readFileSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';
import { Ajv2020 } from 'ajv/dist/2020.js';
import { seriousViolations } from '../stack/stack';
import { mockOpenRouter, vendorFixture, type RecordedRequest, type VendorFixture } from './openrouter';
import { requireStaticSite, useExternalGuard } from './site';

const STUDIO = 'app/evaluators/studio/';
const KEY = 'sk-or-v1-e2e-0123456789abcdef0123456789abcdef';
const NO_PATTERN = 'cmp0e2eZq7Lw4Rt9Yx2Vb6Nm';

const readJson = (path: string): unknown => JSON.parse(readFileSync(new URL(path, import.meta.url), 'utf8'));
const evaluatorSchema = readJson('../../../contracts/schema/evaluator.v1.schema.json') as Record<string, unknown>;
const drafterPrompt = readJson('../../../contracts/prompts/evaluator-drafter.v1.json') as { examples: { output: unknown }[] };

/** The tab's key, as the live Playground's consent dialog leaves it (the key store's record, D102). */
async function connectKey(page: Page): Promise<void> {
  await page.goto(STUDIO);
  await page.evaluate((key) => sessionStorage.setItem('ta.openrouter.key', JSON.stringify({ key, stored_at: Date.now() })), KEY);
  await page.reload();
}

const asked = (request: RecordedRequest) => JSON.parse(request.body ?? '{}') as { questions?: Record<string, { type: string }>; state?: { assistant_response?: string } };

/** Jev's answer to every question asked: Yes (0.92) for an answer that promises a refund, No (0.08) otherwise. */
function refundDecisions(request: RecordedRequest): VendorFixture {
  const base = vendorFixture('decisions.200');
  const body = asked(request);
  const noul = /refund/i.test(body.state?.assistant_response ?? '') ? 0.92 : 0.08;
  const answers = Object.fromEntries(Object.keys(body.questions ?? {}).map((q) => [q, { type: 'noul', noul }]));
  return { ...base, body: { ...(base.body as object), answers } };
}

/** A chat completion whose message is `content` (the fixture's usage and cost). */
function chatReply(content: string): VendorFixture {
  const base = vendorFixture('chat.200_usage_cost');
  const body = structuredClone(base.body) as { choices: { message: { content: string } }[] };
  body.choices[0].message.content = content;
  return { ...base, body };
}

const step = (page: Page, name: RegExp) => page.getByRole('region', { name });

/** The tab's "{n} of {max} calls to OpenRouter" counter in a step, as a number. */
async function callsUsed(page: Page, region: RegExp): Promise<number> {
  const text = await step(page, region).getByText(/^\d+ of \d+ calls to OpenRouter/).first().textContent();
  return Number(/^(\d+)/.exec(text ?? '')?.[1]);
}

const SAMPLES: { answer: string; label: 'Should fire' | "Shouldn't fire" }[] = [
  { answer: 'We will refund you in full today.', label: 'Should fire' },
  { answer: 'I have issued a refund to your card.', label: 'Should fire' },
  { answer: 'You get a refund of 50 dollars, no questions asked.', label: 'Should fire' },
  { answer: `A full refund is on its way (ticket ${NO_PATTERN}).`, label: 'Should fire' },
  { answer: 'Your order ships on Monday.', label: "Shouldn't fire" },
  { answer: 'You can reset the password in Settings.', label: "Shouldn't fire" },
  { answer: 'Our store opens at nine.', label: "Shouldn't fire" },
  { answer: 'The invoice is attached to this email.', label: "Shouldn't fire" },
];

/** Starts a blank Yes/No check and edits its name and question in the Simple tab. */
async function startBlankCheck(page: Page): Promise<void> {
  const describe = step(page, /Step 1: Describe a check in plain English/);
  await describe.getByRole('combobox', { name: 'Start from a template' }).click();
  await page.getByRole('option', { name: /^Blank Yes\/No check/ }).click();
  await describe.getByRole('button', { name: 'Use this template' }).click();
  const review = step(page, /Step 2: Review the check/);
  await review.getByRole('textbox', { name: 'Name' }).fill('Promises a refund');
  await review.getByRole('textbox', { name: 'The question the fast check answers' }).fill('Does `assistant_response` promise the customer a refund?');
}

/** Pastes every sample with its label in step 3. */
async function addSamples(page: Page): Promise<void> {
  const test3 = step(page, /Step 3: Test on samples/);
  await test3.getByRole('tab', { name: 'Paste your own' }).click();
  for (const s of SAMPLES) {
    await test3.getByRole('textbox', { name: 'The answer to check' }).fill(s.answer);
    await test3.getByRole('radiogroup', { name: 'Your label' }).getByRole('radio', { name: s.label, exact: true }).click();
    await test3.getByRole('button', { name: 'Add sample' }).click();
  }
  await expect(test3.getByRole('list', { name: 'Samples' }).getByRole('listitem')).toHaveCount(SAMPLES.length);
}

async function runTest(page: Page): Promise<void> {
  const test3 = step(page, /Step 3: Test on samples/);
  await test3.getByRole('button', { name: 'Run test' }).click();
  await expect(test3.getByRole('region', { name: 'Test results' })).toBeVisible();
}

test.describe('static Studio', () => {
  useExternalGuard();
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStaticSite(playwright, testInfo);
  });

  test('studio: without a key, templates are the way in and nothing reaches openrouter.ai', async ({ page }) => {
    const mock = await mockOpenRouter(page, {});
    await page.goto(STUDIO);
    const describe = step(page, /Step 1: Describe a check in plain English/);
    await describe.getByRole('textbox', { name: 'What should TrustAgent check?' }).fill('Flag answers that promise a refund our policy does not allow.');
    await expect(describe.getByRole('button', { name: 'Draft with AI' })).toHaveAttribute('aria-disabled', 'true');
    await expect(describe.getByText('Draft with AI calls OpenRouter with your key from this tab. Connect or paste a key first.')).toBeVisible();
    await expect(describe.getByRole('button', { name: 'Connect OpenRouter' })).toBeEnabled();
    await startBlankCheck(page);
    await addSamples(page);
    const test3 = step(page, /Step 3: Test on samples/);
    await expect(test3.getByRole('button', { name: 'Run test' })).toHaveAttribute('aria-disabled', 'true');
    await expect(test3.getByText('Testing calls OpenRouter with your key from this tab. Connect or paste a key first.')).toBeVisible();
    expect(mock.requests, 'nothing reaches openrouter.ai without a key').toEqual([]);
    mock.assertClean();
  });

  test('studio: template, edit, a test on the mocked decision model, quick calibration, then copy and download a valid evaluator.v1', async ({ page, context, baseURL }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: new URL(String(baseURL)).origin });
    const mock = await mockOpenRouter(page, { decisions: refundDecisions });
    await connectKey(page);
    await startBlankCheck(page);
    await addSamples(page);

    // The estimate and the calls left come before Run; nothing has been sent yet.
    const test3 = step(page, /Step 3: Test on samples/);
    await expect(test3.getByText(/^8 samples ·/)).toBeVisible();
    await expect(test3.getByRole('button', { name: /^Estimate: how “Cost of this test” was produced/ })).toBeVisible();
    expect(await callsUsed(page, /Step 3: Test on samples/)).toBe(0);
    expect(mock.requests.filter((r) => r.endpoint === 'decisions')).toEqual([]);

    await runTest(page);
    const results = test3.getByRole('region', { name: 'Test results' });
    await expect(results.getByText(/^8 of 8 match/)).toBeVisible();
    const decisions = mock.requests.filter((r) => r.endpoint === 'decisions');
    expect(decisions, 'one decisions call per sample').toHaveLength(SAMPLES.length);
    for (const r of decisions) {
      expect(r.headers.authorization).toBe(`Bearer ${KEY}`);
      expect(Object.keys(asked(r).questions ?? {})).toHaveLength(1);
    }
    expect(await callsUsed(page, /Step 3: Test on samples/)).toBe(SAMPLES.length);
    await expect(results.getByRole('button', { name: /^Vendor: how “Cost of this run” was produced/ })).toBeVisible();

    // Quick calibration: free, in the browser, from the eight labels.
    const quick = step(page, /Step 4: Suggest a threshold/);
    await expect(quick.getByText('From 8 labels (4 should fire, 4 shouldn\'t)')).toBeVisible();
    await expect(quick.getByTestId('quick-calibration-message')).not.toBeEmpty();
    const suggest = quick.getByRole('button', { name: /^Use the suggested threshold \((0\.\d\d)\)$/ });
    await expect(suggest).toBeVisible();
    const threshold = Number(/\((0\.\d\d)\)/.exec((await suggest.textContent()) ?? '')?.[1]);
    await suggest.click();
    expect(mock.requests.filter((r) => r.endpoint === 'decisions'), 'quick calibration makes no call').toHaveLength(SAMPLES.length);

    // Export: the clipboard and the file hold the same valid evaluator.v1, with no key and no sample text.
    const exported = step(page, /Step 5: Export the check/);
    await exported.getByRole('button', { name: 'Copy JSON' }).click();
    await expect(exported.getByText(/^Copied [a-z0-9_]+\.v1\.json to the clipboard\.$/)).toBeVisible();
    const copied = await page.evaluate(() => navigator.clipboard.readText());
    const [download] = await Promise.all([page.waitForEvent('download'), exported.getByRole('button', { name: 'Download .json' }).click()]);
    // A blank check's id comes from the template, not from the name typed later.
    expect(download.suggestedFilename()).toBe('custom_check.v1.json');
    const downloaded = readFileSync(await download.path(), 'utf8');
    expect(downloaded).toBe(copied);

    const validate = new Ajv2020({ allErrors: true, strict: false }).compile(evaluatorSchema);
    for (const text of [copied, downloaded]) {
      expect(text).not.toContain(KEY);
      expect(text).not.toContain(NO_PATTERN);
      expect(text).not.toContain('refund to your card');
      const doc = JSON.parse(text) as { id: string; version: number; name: string; question: { instructions: string }; outcomes: { warn_at: number } };
      expect(validate(doc), JSON.stringify(validate.errors)).toBe(true);
      expect(doc).toMatchObject({ id: 'custom_check', version: 1, name: 'Promises a refund' });
      expect(doc.question.instructions).toBe('Does `assistant_response` promise the customer a refund?');
      expect(doc.outcomes.warn_at).toBeCloseTo(threshold, 2);
    }
    mock.assertClean();
  });

  test('studio: a mocked draft raises the counter by one call per turn, two with the repair', async ({ page }) => {
    const valid = JSON.stringify(drafterPrompt.examples[0].output);
    // The first draft answers right away; the second needs the repair turn.
    const mock = await mockOpenRouter(page, { chat: [chatReply(valid), chatReply('{"id": "x"}'), chatReply(valid)] });
    await connectKey(page);
    const describe = /Step 1: Describe a check in plain English/;
    await step(page, describe).getByRole('textbox', { name: 'What should TrustAgent check?' }).fill('Flag answers that recommend a competitor.');
    await expect(step(page, describe).getByText(/Uses anthropic\/claude-haiku-4\.5 \(OpenRouter\)/)).toBeVisible();
    const before = await callsUsed(page, describe);

    await step(page, describe).getByRole('button', { name: 'Draft with AI' }).click();
    await expect(step(page, describe).getByText(/^Drafted “.+” with /)).toBeVisible();
    await expect.poll(() => callsUsed(page, describe)).toBe(before + 1);
    expect(mock.requests.filter((r) => r.endpoint === 'chat')).toHaveLength(1);

    await step(page, describe).getByRole('button', { name: 'Draft again with AI' }).click();
    await expect.poll(() => mock.requests.filter((r) => r.endpoint === 'chat').length).toBe(3);
    await expect.poll(() => callsUsed(page, describe)).toBe(before + 3);
    const chats = mock.requests.filter((r) => r.endpoint === 'chat');
    for (const r of chats) expect(r.headers.authorization).toBe(`Bearer ${KEY}`);
    // The repair turn carries the first answer and the problems found in it.
    const repair = JSON.parse(chats[2].body ?? '{}') as { messages: { role: string; content: string }[] };
    expect(repair.messages.map((m) => m.role)).toEqual(['system', 'user', 'assistant', 'user']);
    expect(repair.messages[2].content).toBe('{"id": "x"}');
    mock.assertClean();
  });

  test('studio: the online demo offers no Save, Watch or Activate, only the export and the own-stack card', async ({ page }) => {
    const mock = await mockOpenRouter(page, {});
    await page.goto(STUDIO);
    await startBlankCheck(page);
    for (const name of [/^Save/, /Watch/, /Make active/, /Activate/, /past traffic/, /Calibrate now/]) {
      await expect(page.getByRole('button', { name })).toHaveCount(0);
    }
    await expect(step(page, /Step 5: Export the check/).getByRole('button', { name: 'Copy JSON' })).toBeEnabled();
    const ownStack = page.getByRole('region', { name: 'On your own stack' });
    await expect(ownStack).toContainText('watch it in shadow, test it on past traffic, make it active and calibrate it for blocking');
    await expect(ownStack.getByText('make demo')).toBeVisible();
    mock.assertClean();
  });

  test('studio: fits 400 px in the dark theme with results on screen, and has no serious axe violations', async ({ page }) => {
    await page.emulateMedia({ colorScheme: 'dark', reducedMotion: 'reduce' });
    await page.setViewportSize({ width: 400, height: 860 });
    const mock = await mockOpenRouter(page, { decisions: refundDecisions });
    await connectKey(page);
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
    await startBlankCheck(page);
    await addSamples(page);
    await runTest(page);
    const [scrollWidth, width] = await page.evaluate(() => [document.documentElement.scrollWidth, window.innerWidth]);
    expect(scrollWidth, 'no horizontal page scroll').toBeLessThanOrEqual(width);
    expect(await seriousViolations(page), 'Studio (dark, 400 px)').toEqual([]);

    await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' });
    await page.setViewportSize({ width: 1280, height: 900 });
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
    expect(await seriousViolations(page), 'Studio (light)').toEqual([]);
    mock.assertClean();
  });
});
