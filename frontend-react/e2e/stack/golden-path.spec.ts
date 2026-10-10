/**
 * The golden path (SPEC §4.2, D130): from the setup link `make up` opens to the detail of the
 * first flagged trace in at most 8 user actions, with no key ($0, simulated checks, labelled), no
 * shell command and no log.
 *
 *   open the setup link → Create the admin key → tick I saved it → Next → Try it without a key →
 *   Next → Check this answer → Open the trace
 *
 * Copying the admin key is not counted: "I saved it" is the action that stands for saving it, and
 * a person may save it any way they like (the onboarding spec checks the Copy button).
 *
 * `make golden-path` starts a fresh stack (the onboarding stack's configuration, its own project
 * and ports) and passes its setup link; the journey itself only clicks. On failure the message
 * lists every action taken. Without the setup link it skips with the reason, and fails in CI.
 */
import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { Journey, SECURE, TIMEOUT, checkTheRefundAnswer, openTheTrace, requireFreshStack, secureWithSetupLink, trySimulated } from './journey';
import { requireStack } from './stack';

/** D130: at most 8 user actions from the setup link to the flagged trace's detail. */
const BUDGET = 8;
/** A request for logs, of any kind: the golden path never needs one. */
const LOGS = /\/logs?(\/|$)/i;
/** The module that runs commands, spelled so this file does not name it. */
const PROCESS_MODULE = ['child', 'process'].join('_');

/** The modules a source file imports or requires. */
function importedModules(source: string): string[] {
  return [...source.matchAll(/(?:\bfrom\s+|\bimport\s*\(\s*|\brequire\s*\(\s*|^\s*import\s+)['"]([^'"]+)['"]/gm)].map((m) => m[1]);
}

test.describe('golden-path', () => {
  test.describe.configure({ mode: 'serial', retries: 0, timeout: 180_000 });

  test('golden-path: the journey runs no command and reads no log', () => {
    // The journey is clicks only: neither the spec nor the helpers it walks with can start a process.
    for (const file of ['golden-path.spec.ts', 'journey.ts', 'stack.ts']) {
      const modules = importedModules(readFileSync(new URL(`./${file}`, import.meta.url), 'utf8'));
      expect(modules.length, `${file} imports something`).toBeGreaterThan(0);
      for (const name of modules) expect(name.replace(/^node:/, ''), `${file} imports ${name}`).not.toBe(PROCESS_MODULE);
    }
  });

  test('golden-path: from the setup link to the flagged trace in 8 actions or fewer', async ({ page, playwright }, testInfo) => {
    await requireStack(playwright, testInfo);
    const setupUrl = await requireFreshStack(playwright, testInfo, {
      spec: 'golden-path',
      start: 'make golden-path FIRST_RUN_LOCAL=1, or FIRST_RUN_TAG=<tag>',
    });
    const logRequests: string[] = [];
    page.on('request', (request) => {
      const path = new URL(request.url()).pathname;
      if (LOGS.test(path)) logRequests.push(path);
    });
    const journey = new Journey(BUDGET);

    await secureWithSetupLink(page, journey, setupUrl, { copy: false });
    await trySimulated(page, journey);
    await checkTheRefundAnswer(page, journey);
    await openTheTrace(page, journey);

    // The detail of the flagged trace: its outcome, and the claim the deep audit contradicted.
    await expect(page.getByRole('region', { name: 'Outcome' })).toContainText('Flagged', { timeout: TIMEOUT });
    const claims = page.getByRole('list', { name: 'Claims' });
    await expect(claims.getByText('Contradicted', { exact: true })).toBeVisible({ timeout: 60_000 });

    journey.expectWithinBudget('the flagged trace’s detail');
    expect(logRequests, 'no request for logs').toEqual([]);
    await expect(page.getByText(SECURE.no_token), 'never the find-it-in-the-logs fallback').toHaveCount(0);
    expect(journey.holdsSecret((await page.locator('body').textContent()) ?? ''), 'no secret on screen').toBe(false);
  });
});
