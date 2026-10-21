/**
 * The launch video's clips (D210; scripts/launch_video.py runs this spec, never `npm run`): one
 * 1920×1080 recording per beat of docs/launch/demo-script.md, with a sidecar (clips.json) that
 * names each clip, the moment its page was ready (lead_ms) and, where a wait would be dead air,
 * the parts to keep (jump cuts at real speed: nothing is sped up).
 *
 * - `static beats` (VIDEO_BEATS=static): the landing hero, Traffic, the featured trace (D209) and
 *   the Savings tab, on the launch build of the Pages site (vite preview). It refuses the simulated
 *   tape unless VIDEO_ALLOW_MOCK=1, and launch_video.py then writes only to tmp/launch/rehearsal/.
 * - `stack beats` (VIDEO_BEATS=stack): the Studio (draft, test on samples, test on past traffic),
 *   the payments-faq fallback and Savings, on a reviewed attempt's record stack (VIDEO_STACK_URL,
 *   signed in with the attempt's admin key from VIDEO_ADMIN_KEY_FILE). It saves nothing: the
 *   policy's version and content are the same before and after (P5C-27), and its paid calls (the
 *   draft and the dry runs) sit inside the record ledger.
 *
 * VIDEO_CLIPS_DIR is where the clips and clips.json go (launch_video.py chooses it).
 */
import { createHash } from 'node:crypto';
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { basename, resolve } from 'node:path';
import { expect, test, type Browser, type BrowserContext, type Page } from '@playwright/test';
import { blockUnmockedExternal } from '../static/site';
import { captureContext, expectFeaturedTrace, presetPreferences, refuseMock, shippedTape, type Tape } from './capture';

const COPY = (
  JSON.parse(readFileSync(new URL('../../../contracts/ui/copy.v1.json', import.meta.url), 'utf8')) as {
    copy: { trace: { show_jev_request: string } };
  }
).copy;

export const VIDEO_SIZE = { width: 1920, height: 1080 } as const;
const CLIPS_SCHEMA = 'trustagent.video-clips/v1';
const TIMEOUT = 90_000;
/** What the Studio beat types (the demo script's 27–40 s row). */
const STUDIO_TEXT = 'Flag when the bot promises a refund';

interface ClipRecord {
  beat: string;
  file: string;
  lead_ms: number;
  /** Parts of the clip to keep, in ms from its start; absent: from lead_ms on. */
  parts?: [number, number][];
}

/** Lets the viewer read a beat. */
async function hold(page: Page, ms: number): Promise<void> {
  await page.waitForTimeout(ms);
}

/**
 * Lets the recording catch up before a beat's lead is marked: the screencast's frames trail the
 * page under load, so a beat starts on a page that has been still for a moment. Take the clips
 * with nothing else running (P6b, P6c).
 */
const SETTLE_MS = 1_200;

function clipsDir(): string {
  const dir = process.env.VIDEO_CLIPS_DIR;
  if (!dir) throw new Error('VIDEO_CLIPS_DIR is not set: run the recorder through scripts/launch_video.py');
  return resolve(dir);
}

/**
 * Records one beat in its own context, so every clip starts on a clean page. `run` returns when the
 * beat has been shown; `ready()` marks the moment its page was ready (the clip's lead), `part()`
 * keeps a stretch of it.
 */
async function recordBeat(
  browser: Browser,
  beat: string,
  options: { baseURL: string; storageState?: string },
  run: (page: Page, beatTools: { ready: () => void; now: () => number; keep: (from: number, to: number) => void }) => Promise<void>,
): Promise<ClipRecord> {
  const dir = clipsDir();
  const raw = resolve(dir, 'raw', beat);
  const context: BrowserContext = await browser.newContext({
    ...captureContext('light', VIDEO_SIZE),
    baseURL: options.baseURL,
    storageState: options.storageState,
    recordVideo: { dir: raw, size: VIDEO_SIZE },
  });
  // Keep the compositor producing frames: the screencast sends a frame only when the page
  // repaints, and a still page (or a busy machine) leaves the video on a stale frame. A 1 px dot,
  // never visible, moves every animation frame.
  await context.addInitScript(() => {
    const start = () => {
      const dot = document.createElement('div');
      dot.setAttribute('aria-hidden', 'true');
      dot.style.cssText = 'position:fixed;left:0;top:0;width:1px;height:1px;opacity:0.004;pointer-events:none;z-index:2147483647';
      document.documentElement.appendChild(dot);
      let x = 0;
      const tick = () => {
        x = (x + 1) % 2;
        dot.style.transform = `translateX(${x}px)`;
        requestAnimationFrame(tick);
      };
      requestAnimationFrame(tick);
    };
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start, { once: true });
    else start();
  });
  const page = await context.newPage();
  const t0 = Date.now();
  let lead = -1;
  const parts: [number, number][] = [];
  const video = page.video();
  if (!video) throw new Error('the context records no video');
  try {
    await run(page, {
      ready: () => {
        lead = Date.now() - t0;
      },
      now: () => Date.now() - t0,
      keep: (from, to) => {
        parts.push([Math.max(0, from), to]);
      },
    });
  } finally {
    await context.close();
  }
  expect(lead, `the ${beat} beat marked when its page was ready`).toBeGreaterThanOrEqual(0);
  const file = `${beat}.webm`;
  await video.saveAs(resolve(dir, file));
  rmSync(raw, { recursive: true, force: true });
  return parts.length > 0 ? { beat, file, lead_ms: lead, parts } : { beat, file, lead_ms: lead };
}

function writeSidecar(source: 'static' | 'stack', kind: string, label: string, featured: string | null, clips: ClipRecord[]): void {
  const dir = clipsDir();
  mkdirSync(dir, { recursive: true });
  rmSync(resolve(dir, 'raw'), { recursive: true, force: true });
  const doc = { schema: CLIPS_SCHEMA, source, kind, label, featured, clips, captured_at: new Date().toISOString() };
  writeFileSync(resolve(dir, 'clips.json'), `${JSON.stringify(doc, null, 2)}\n`);
  console.log(`video: ${clips.length} ${source} clip(s) in ${dir} (${kind})`);
}

test.describe('static beats', () => {
  test('records the hero, Traffic, the featured trace and Savings from the Pages build', async ({ browser, baseURL }) => {
    test.skip(process.env.VIDEO_BEATS !== 'static', 'run through scripts/launch_video.py static');
    test.setTimeout(6 * TIMEOUT);
    const site = String(baseURL);
    rmSync(clipsDir(), { recursive: true, force: true });
    let tape: Tape | null = null;
    const blockedAll: string[] = [];
    const guard = async (page: Page): Promise<Tape> => {
      blockedAll.push(...(await blockUnmockedExternal(page, site)));
      const t = await shippedTape(page);
      refuseMock(t, 'VIDEO_ALLOW_MOCK', 'make launch-video');
      tape = t;
      return t;
    };
    const replayFinished = (page: Page) => expect(page.locator('[data-replay-status="finished"]')).toBeVisible({ timeout: TIMEOUT });
    const clips: ClipRecord[] = [];

    clips.push(
      await recordBeat(browser, 'landing', { baseURL: site }, async (page, { ready }) => {
        await guard(page);
        await presetPreferences(page, 'light');
        await page.goto('');
        await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
        await hold(page, SETTLE_MS);
        ready();
        await hold(page, 5_500);
      }),
    );
    clips.push(
      await recordBeat(browser, 'traffic', { baseURL: site }, async (page, { ready }) => {
        await guard(page);
        await presetPreferences(page, 'light', { showGuide: true });
        await page.goto('app/traffic/');
        await expect(page.getByRole('table', { name: 'Requests' }).locator('[role="row"][data-trace-id]').first()).toBeVisible({ timeout: TIMEOUT });
        await hold(page, SETTLE_MS);
        ready();
        await hold(page, 10_500);
      }),
    );
    clips.push(
      await recordBeat(browser, 'trace', { baseURL: site }, async (page, { ready }) => {
        const t = await guard(page);
        await presetPreferences(page, 'light');
        if (t.featured !== null) {
          // A recorded tape names its featured trace (D209): open it straight away, without
          // replaying Traffic first (its deep audit is in the tape's opening half, shown on load).
          await page.goto(`app/traffic/${t.featured}`);
        } else {
          await page.goto('app/traffic/');
          await replayFinished(page);
          await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Trace' }).click();
        }
        await expectFeaturedTrace(page, t);
        await expect(page.getByRole('region', { name: 'Answer' }).locator('mark[data-v="contradicted"]').first()).toBeVisible({ timeout: TIMEOUT });
        await hold(page, SETTLE_MS);
        ready();
        await hold(page, 6_000);
        await page.getByRole('heading', { name: 'Details' }).click();
        await page.getByText(COPY.trace.show_jev_request, { exact: true }).click();
        await page.getByTestId('jev-request').scrollIntoViewIfNeeded();
        await hold(page, 9_500);
      }),
    );
    clips.push(
      await recordBeat(browser, 'savings_static', { baseURL: site }, async (page, { ready }) => {
        await guard(page);
        await presetPreferences(page, 'light');
        await page.goto('app/savings/');
        await expect(page).toHaveURL(/\/app\/overview\/?\?tab=savings$/);
        await replayFinished(page);
        await hold(page, SETTLE_MS);
        ready();
        await hold(page, 7_500);
      }),
    );

    expect(blockedAll, 'requests to an unmocked external origin').toEqual([]);
    const shipped = tape as Tape | null;
    if (!shipped) throw new Error('no tape was read');
    writeSidecar('static', shipped.kind, shipped.label, shipped.featured, clips);
  });
});

/** GET /api/policies as the signed-in admin: its version and a hash of its content (P5C-27). */
async function policyFingerprint(page: Page): Promise<{ id: unknown; sha256: string }> {
  const res = await page.request.get('/api/policies');
  expect(res.ok(), `GET /api/policies answered ${res.status()}`).toBe(true);
  const body = (await res.json()) as { id?: unknown; effective_routes?: unknown; policy?: unknown };
  const content = JSON.stringify(body.policy ?? body.effective_routes ?? null);
  return { id: body.id, sha256: createHash('sha256').update(content).digest('hex') };
}

test.describe('stack beats', () => {
  test('records the Studio, the payments-faq fallback and Savings on the record stack', async ({ browser }) => {
    test.skip(process.env.VIDEO_BEATS !== 'stack', 'run through scripts/launch_video.py stack');
    test.setTimeout(8 * TIMEOUT);
    const url = process.env.VIDEO_STACK_URL;
    const keyFile = process.env.VIDEO_ADMIN_KEY_FILE;
    const kind = process.env.VIDEO_TAPE_KIND ?? '';
    if (!url || !keyFile) throw new Error('VIDEO_STACK_URL and VIDEO_ADMIN_KEY_FILE come from scripts/launch_video.py stack');
    if (kind !== 'recorded' && process.env.VIDEO_ALLOW_MOCK !== '1') {
      throw new Error(`the stack's tape is ${kind || 'unknown'}, not recorded: the video is made from a recorded tape only (VIDEO_ALLOW_MOCK=1 for a rehearsal)`);
    }
    rmSync(clipsDir(), { recursive: true, force: true });
    mkdirSync(clipsDir(), { recursive: true });

    // Sign in once, outside any recording; every beat reuses the session.
    const state = resolve(clipsDir(), `.${basename(keyFile)}.state.json`);
    const signIn = await browser.newContext({ baseURL: url });
    const admin = await signIn.newPage();
    await admin.goto('/app/setup');
    await admin.getByRole('textbox', { name: 'Key', exact: true }).fill(readFileSync(keyFile, 'utf8').trim());
    await admin.getByRole('button', { name: 'Sign in', exact: true }).click();
    await expect(admin.getByText(/Admin actions are available/)).toBeVisible({ timeout: TIMEOUT });
    await signIn.storageState({ path: state });
    const before = await policyFingerprint(admin);
    const clips: ClipRecord[] = [];

    try {
      clips.push(
        await recordBeat(browser, 'studio', { baseURL: url, storageState: state }, async (page, { ready, now, keep }) => {
          await presetPreferences(page, 'light');
          await page.goto('/app/evaluators/studio');
          const describe = page.getByRole('region', { name: /^Step 1:/ });
          const box = describe.getByRole('textbox', { name: 'What should TrustAgent check?' });
          await expect(box).toBeVisible({ timeout: TIMEOUT });
          await hold(page, SETTLE_MS);
          ready();
          let from = now();
          await box.pressSequentially(STUDIO_TEXT, { delay: 45 });
          await describe.getByRole('button', { name: 'Draft with AI' }).click();
          keep(from, now() + 800);
          await expect(describe.getByText(/^Drafted “.+” with /)).toBeVisible({ timeout: TIMEOUT });
          from = now() - 300;
          await hold(page, 1_800);
          const bench = page.getByRole('region', { name: /^Step 3:/ });
          await bench.scrollIntoViewIfNeeded();
          await bench.getByRole('button', { name: 'Run test' }).click();
          keep(from, now() + 600);
          const results = bench.getByRole('region', { name: 'Test results' });
          await expect(results.getByText(/^(\d+ of \d+ match|No labelled samples)/)).toBeVisible({ timeout: TIMEOUT });
          from = now() - 300;
          await hold(page, 1_800);
          const watch = page.getByRole('region', { name: /^Step 6:/ });
          await watch.scrollIntoViewIfNeeded();
          await watch.getByRole('radio', { name: '24 h' }).click();
          await watch.getByRole('button', { name: /^Test on past traffic \((paid|simulated)\)$/ }).click();
          const confirm = page.getByRole('alertdialog', { name: /^Run this (paid|simulated) test\?$/ });
          const fired = watch.getByText(/^Would have fired (≈ )?[\d,]+ times in the last /);
          await expect(confirm.or(fired)).toBeVisible({ timeout: TIMEOUT });
          if (await confirm.isVisible()) await confirm.getByRole('button', { name: 'Run the test' }).click();
          keep(from, now() + 600);
          await expect(fired).toBeVisible({ timeout: 2 * TIMEOUT });
          from = now() - 300;
          await hold(page, 3_000);
          keep(from, now());
        }),
      );
      clips.push(
        await recordBeat(browser, 'policies', { baseURL: url, storageState: state }, async (page, { ready, now, keep }) => {
          await presetPreferences(page, 'light');
          await page.goto('/app/policies');
          const route = page.getByText('payments-faq', { exact: true }).first();
          await expect(route).toBeVisible({ timeout: TIMEOUT });
          await route.scrollIntoViewIfNeeded();
          await hold(page, SETTLE_MS);
          ready();
          let from = now();
          await hold(page, 4_000);
          keep(from, now());
          await page.goto('/app/traffic');
          const replaced = page
            .getByRole('table', { name: 'Requests' })
            .locator('[role="row"][data-trace-id]')
            .filter({ has: page.getByText('Replaced', { exact: true }) });
          await replaced.first().getByRole('link').click();
          const outcome = page.getByRole('region', { name: 'Outcome' });
          await expect(outcome.getByText('Replaced', { exact: true })).toBeVisible({ timeout: TIMEOUT });
          from = now() - 200;
          await hold(page, 2_500);
          await outcome.getByRole('link', { name: 'See what the user received instead' }).click();
          await hold(page, 3_500);
          keep(from, now());
        }),
      );
      clips.push(
        await recordBeat(browser, 'savings', { baseURL: url, storageState: state }, async (page, { ready }) => {
          await presetPreferences(page, 'light');
          await page.goto('/app/overview?tab=savings');
          await expect(page.getByRole('tab', { name: /Savings/, selected: true })).toBeVisible({ timeout: TIMEOUT });
          await hold(page, SETTLE_MS);
          ready();
          await hold(page, 5_500);
        }),
      );
    } finally {
      // P5C-27: the beats saved nothing. The policy's version and content are unchanged.
      const after = await policyFingerprint(admin);
      await signIn.close();
      rmSync(state, { force: true });
      expect(after, 'the stack beats changed the policy').toEqual(before);
    }
    writeSidecar('stack', kind, process.env.VIDEO_TAPE_LABEL ?? '', null, clips);
  });
});
