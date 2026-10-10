/**
 * First run (SPEC §4.2, D130): a fresh stack, from `docker compose up` to the first flagged
 * answer on screen, in under 5 minutes.
 *
 * `make first-run` starts the stack itself and passes what it measured (FIRST_RUN_* below): the
 * time before `up` (T0), then from T0 the image pull (CI mode only), every service healthy, and
 * the stack ready (doctor --expect fresh passes). This spec walks the wizard as a new user without
 * a key would (the setup link, the admin key, simulated checks, "Check this answer"), measures
 * the first flag from the same T0, and writes first-run.json with every timing and its label:
 * measured, locally with prebuilt images (FIRST_RUN_LOCAL=1, D130) or in CI with pulled ones
 * (release-images.yml, FIRST_RUN_TAG). The file is written before the 5-minute assertion, so a
 * slow run still leaves its numbers.
 *
 * Without its environment (a plain `--project=stack` run) it skips with the reason, and fails in
 * CI (skipOrFail).
 */
import { mkdirSync, writeFileSync } from 'node:fs';
import { cpus, totalmem } from 'node:os';
import { dirname } from 'node:path';
import { expect, test, type BrowserContext, type Page } from '@playwright/test';
import { COPY, Journey, checkTheRefundAnswer, requireFreshStack, secureWithSetupLink, trySimulated } from './journey';
import { requireStack, skipOrFail } from './stack';

/** D130: the first flagged answer within 5 minutes of T0. */
const BUDGET_MS = 5 * 60_000;
const ENV = {
  FIRST_RUN_T0: 'the epoch milliseconds before `docker compose up`',
  FIRST_RUN_HEALTHY_MS: 'ms from T0 until every service was healthy',
  FIRST_RUN_READY_MS: 'ms from T0 until doctor --expect fresh passed',
  FIRST_RUN_OUT: 'where to write first-run.json',
  FIRST_RUN_MODE: 'local (prebuilt images) or pull',
};

/** The first-run.json record (schema trustagent.first-run/v1). */
interface FirstRunRecord {
  schema: 'trustagent.first-run/v1';
  /** The value label (contracts/ui value_labels): always measured, with where and how. */
  label: string;
  value_label: 'measured';
  mode: 'local' | 'pull';
  images: string;
  started_at: string;
  timings_ms: { pull: number | null; healthy: number; ready: number; first_flag: number };
  budget_ms: number;
  within_budget: boolean;
  actions: string[];
  machine: { platform: string; cpus: number; memory_gb: number };
}

/** A non-negative whole number from the environment, or null when unset (pull in local mode). */
function msFromEnv(name: string): number | null {
  const raw = process.env[name];
  if (raw === undefined || raw === '' || raw === 'null') return null;
  const value = Number(raw);
  if (!Number.isInteger(value) || value < 0) throw new Error(`${name} must be a whole number of milliseconds, not ${JSON.stringify(raw)}`);
  return value;
}

let context: BrowserContext;
let page: Page;
let setupUrl = '';

test.describe('first-run', () => {
  test.describe.configure({ mode: 'serial', retries: 0, timeout: BUDGET_MS + 60_000 });

  test.beforeAll(async ({ browser, playwright }, testInfo) => {
    await requireStack(playwright, testInfo);
    setupUrl = await requireFreshStack(playwright, testInfo, {
      spec: 'first-run',
      start: 'make first-run FIRST_RUN_LOCAL=1, or FIRST_RUN_TAG=<tag>',
      env: ENV,
    });
    const mode = process.env.FIRST_RUN_MODE;
    skipOrFail(mode !== 'local' && mode !== 'pull', 'FIRST_RUN_MODE must be local or pull (make first-run sets it)');
    context = await browser.newContext({ baseURL: String(testInfo.project.use.baseURL ?? '') });
    page = await context.newPage();
  });

  test.afterAll(async () => {
    await context?.close();
  });

  test('first-run: a fresh stack shows its first flagged answer within 5 minutes', async () => {
    const t0 = msFromEnv('FIRST_RUN_T0');
    const healthy = msFromEnv('FIRST_RUN_HEALTHY_MS');
    const ready = msFromEnv('FIRST_RUN_READY_MS');
    expect(t0 !== null && healthy !== null && ready !== null, 'make first-run passes T0, healthy and ready').toBe(true);
    const journey = new Journey(Number.POSITIVE_INFINITY);

    await secureWithSetupLink(page, journey, setupUrl, { copy: false });
    await trySimulated(page, journey);
    await checkTheRefundAnswer(page, journey);
    const firstFlag = Date.now() - (t0 ?? 0);
    // Simulated checks are labelled as such on the result: the timing is real, the verdict mock.
    await expect(page.getByRole('banner').getByText(COPY.badges.simulated_checks).first()).toBeVisible();

    const mode = process.env.FIRST_RUN_MODE === 'pull' ? 'pull' : 'local';
    const record: FirstRunRecord = {
      schema: 'trustagent.first-run/v1',
      label: mode === 'pull' ? 'measured in CI, images pulled' : 'measured locally, images prebuilt',
      value_label: 'measured',
      mode,
      images: process.env.FIRST_RUN_IMAGES ?? '',
      started_at: new Date(t0 ?? 0).toISOString(),
      timings_ms: { pull: msFromEnv('FIRST_RUN_PULL_MS'), healthy: healthy ?? 0, ready: ready ?? 0, first_flag: firstFlag },
      budget_ms: BUDGET_MS,
      within_budget: firstFlag < BUDGET_MS,
      actions: journey.actions,
      machine: { platform: process.platform, cpus: cpus().length, memory_gb: Math.round((totalmem() / 1024 ** 3) * 10) / 10 },
    };
    const out = process.env.FIRST_RUN_OUT ?? '';
    mkdirSync(dirname(out), { recursive: true });
    writeFileSync(out, `${JSON.stringify(record, null, 2)}\n`);
    test.info().annotations.push({ type: 'first-run', description: `${record.label}: first flag after ${(firstFlag / 1000).toFixed(1)} s` });

    expect(record.timings_ms.healthy, 'healthy after T0').toBeLessThanOrEqual(record.timings_ms.ready);
    expect(record.timings_ms.ready, 'ready before the first flag').toBeLessThanOrEqual(firstFlag);
    expect(firstFlag, `first flagged answer ${(firstFlag / 1000).toFixed(1)} s after T0 (${record.label})`).toBeLessThan(BUDGET_MS);
  });
});
