/**
 * Labels on a running stack (SPEC §3.3 Evaluators and Trace detail; P4-PLAN D59-D63), as an admin:
 *
 * - the label queue: label the first answer with the F key, see the check's calibration progress
 *   move by one, then Undo it and see it move back (also through GET /api/evaluators/:id);
 * - "Was this right?" on a flagged request: False alarm, then Undo;
 * - the glass box on that request: "Show exact Jev request" rebuilds the request and says whether
 *   it matches what was recorded.
 *
 * Every test removes the labels it wrote, so the stack's calibration progress is left as it was.
 * Needs TRUSTAGENT_ADMIN_KEY (or TRUSTAGENT_SETUP_URL) and traffic with stored text (the demo
 * stack's scripted traffic); without them the tests skip with the reason, and fail in CI.
 */
import { expect, test, type Page } from '@playwright/test';
import { canSignInAsAdmin, requireStack, seriousViolations, signInAsAdmin, skipOrFail } from './stack';

const TIMEOUT = 60_000;

interface QueueItem {
  trace_id: string;
  evaluator_ref: string;
  user_request: string | null;
  response_text: string | null;
}

async function queue(page: Page): Promise<QueueItem[]> {
  const res = await page.request.get('/api/labels/queue?window=7d&limit=50', { failOnStatusCode: false });
  expect(res.ok(), `GET /api/labels/queue answered HTTP ${res.status()}`).toBe(true);
  return ((await res.json()) as { items: QueueItem[] }).items;
}

async function labelCount(page: Page, evaluatorId: string): Promise<number> {
  const res = await page.request.get(`/api/evaluators/${encodeURIComponent(evaluatorId)}`);
  expect(res.ok(), `GET /api/evaluators/${evaluatorId} answered HTTP ${res.status()}`).toBe(true);
  return ((await res.json()) as { calibration_progress: { human_labels: { n: number } } }).calibration_progress.human_labels.n;
}

/** Removes a label this spec may have left behind (after a failure). */
async function removeLabel(page: Page, traceId: string, target: string): Promise<void> {
  await page.request.delete('/api/labels', { data: { trace_id: traceId, target }, headers: { 'X-TrustAgent-CSRF': '1' }, failOnStatusCode: false });
}

/** The newest flagged request whose text is stored, with the checks at Elevated or above. */
async function flaggedRequest(page: Page): Promise<{ traceId: string; targets: string[] } | null> {
  let picked: { traceId: string; targets: string[] } | null = null;
  await expect
    .poll(
      async () => {
        const res = await page.request.get('/api/traces?outcome=flagged&limit=50', { failOnStatusCode: false });
        if (!res.ok()) return `GET /api/traces answered HTTP ${res.status()}`;
        const { items } = (await res.json()) as { items: { trace_id: string; redacted: boolean }[] };
        for (const t of items.filter((i) => !i.redacted).slice(0, 10)) {
          const detail = await page.request.get(`/api/traces/${t.trace_id}`);
          if (!detail.ok()) continue;
          const d = (await detail.json()) as {
            body: { available: boolean };
            labels?: { target: string }[];
            decisions: { attempt: number; evaluators: Record<string, { ref: string; status: string; level: string | null }> }[];
          };
          const attempt0 = d.decisions.find((x) => x.attempt === 0);
          const targets = Object.values(attempt0?.evaluators ?? {})
            .filter((e) => e.status === 'assessed' && (e.level === 'warn' || e.level === 'critical'))
            .map((e) => e.ref);
          // "Was this right?" offers its buttons only while none of these checks is labelled yet.
          const labelled = (d.labels ?? []).some((l) => targets.includes(l.target));
          if (d.body.available && targets.length > 0 && !labelled) {
            picked = { traceId: t.trace_id, targets };
            return 'found';
          }
        }
        return 'no unlabelled flagged request with stored text yet';
      },
      { timeout: TIMEOUT, message: 'a flagged request whose text is stored and that has no label yet' },
    )
    .toBe('found');
  return picked;
}

test.describe('labels', () => {
  test.describe.configure({ mode: 'serial' });

  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStack(playwright, testInfo);
  });

  test.beforeEach(() => {
    skipOrFail(!canSignInAsAdmin(), 'set TRUSTAGENT_ADMIN_KEY (a key from TRUTHTABLE_ADMIN_KEYS) or TRUSTAGENT_SETUP_URL to sign in as admin');
  });

  test('labels an answer in the queue with a key, moves the progress, and undoes it', async ({ page }) => {
    test.setTimeout(3 * TIMEOUT);
    await signInAsAdmin(page);
    // The page narrowed to one check shows that check's answers in queue order, so pick a check
    // whose first answer has text (a redacted one offers only Skip).
    const items = await queue(page);
    const first = items.find((item) => item.response_text !== null && items.find((i) => i.evaluator_ref === item.evaluator_ref) === item);
    skipOrFail(first === undefined, 'the label queue has no answer with stored text first in line: run traffic with stored text (make demo runs it)');
    if (!first) return;
    const evaluatorId = first.evaluator_ref.split('@')[0];
    const before = await labelCount(page, evaluatorId);

    await page.goto(`/app/evaluators/labels?evaluator=${encodeURIComponent(first.evaluator_ref)}`);
    await expect(page.getByRole('heading', { name: /^Should .+ flag this answer\?$/ })).toBeVisible({ timeout: TIMEOUT });
    const meter = page.getByRole('meter', { name: 'Labels' });
    await expect(meter).toHaveAttribute('aria-valuenow', String(Math.min(before, 200)), { timeout: TIMEOUT });
    expect(await seriousViolations(page)).toEqual([]);

    try {
      await page.keyboard.press('f');
      await expect(page.getByText(/^False alarm\. Saved\./)).toBeVisible({ timeout: TIMEOUT });
      await expect.poll(() => labelCount(page, evaluatorId), { timeout: TIMEOUT }).toBe(before + 1);

      await page.getByRole('button', { name: 'Undo' }).click();
      await expect(page.getByText(/^Label removed:/)).toBeVisible({ timeout: TIMEOUT });
      await expect.poll(() => labelCount(page, evaluatorId), { timeout: TIMEOUT }).toBe(before);
    } finally {
      await removeLabel(page, first.trace_id, first.evaluator_ref);
    }
  });

  test('says whether a flagged answer was right, and undoes it', async ({ page }) => {
    test.setTimeout(3 * TIMEOUT);
    await signInAsAdmin(page);
    const picked = await flaggedRequest(page);
    skipOrFail(picked === null, 'no flagged request with stored text on this stack');
    const { traceId, targets } = picked!;

    await page.goto(`/app/traffic/${traceId}`);
    const outcome = page.getByRole('region', { name: 'Outcome' });
    const group = outcome.getByRole('group', { name: 'Was this right?' });
    try {
      await group.getByRole('button', { name: 'False alarm' }).click();
      await expect(outcome.getByText(/Labelled false alarm/)).toBeVisible({ timeout: TIMEOUT });
      await outcome.getByRole('button', { name: 'Undo' }).click();
      await expect(group.getByRole('button', { name: 'Correct' })).toBeVisible({ timeout: TIMEOUT });
    } finally {
      for (const target of targets) await removeLabel(page, traceId, target);
    }
  });

  test('shows the exact Jev request of a flagged answer and whether it matches', async ({ page }) => {
    test.setTimeout(2 * TIMEOUT);
    await signInAsAdmin(page);
    const picked = await flaggedRequest(page);
    skipOrFail(picked === null, 'no flagged request with stored text on this stack');

    await page.goto(`/app/traffic/${picked!.traceId}`);
    await page.getByRole('heading', { level: 2, name: 'Details' }).click();
    await page.getByText('Show exact Jev request').first().click();
    const box = page.getByTestId('jev-request').first();
    await expect(box.getByText(/Jev calls? · \d+ questions?/)).toBeVisible({ timeout: TIMEOUT });
    await expect(box.getByText(/^(Matches|Differs from) a recompile of this request$/)).toBeVisible({ timeout: TIMEOUT });
    expect(await seriousViolations(page)).toEqual([]);
  });
});
