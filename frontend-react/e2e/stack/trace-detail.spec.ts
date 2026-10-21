/**
 * Trace detail on a running demo stack (SPEC §3.3 "Trace detail"), the inline marks in the answer:
 *
 * - claims: the flagged refund answer (refund-contradiction-flag, picked by its answer: another
 *   flagged support-bot request is checked against the same document) gets its deep audit, and the
 *   contradicted claim is marked at its UTF-16 span ("60 days": wavy underline in the contradicted
 *   hue, with its icon); the mark is a link to the claim's row in the Claims section, and the
 *   instant check's "60" nests inside it;
 * - detectors: help-center-link-block (a buffered stream, blocked) marks the link the article does
 *   not contain as soon as the gate decides (urls_not_in_evidence), shows the block message the
 *   user received instead, and nests the finding in the unsupported claim once the audit lands.
 *
 * Needs a session that may read answers (the demo's public read shows them); otherwise the answer
 * is hidden and the marks cannot be checked: the tests skip with the reason, and fail in CI.
 */
import { expect, test, type Page } from '@playwright/test';
import { requireStack, seriousViolations, skipOrFail } from './stack';

const TIMEOUT = 60_000;

/** A request picked by id, and the Traffic filters (`?route=…&outcome=…`) that list it near the top. */
interface PickedRow {
  traceId: string;
  search: string;
}

/**
 * The newest request on `route` with outcome `outcome` whose answer contains `text` (GET
 * /api/traces, allowed by the demo's public read). Traffic rows show no answer, and on a P3 stack
 * two support-bot scenarios are Flagged against refund-policy.md: refund-contradiction-flag ("60
 * days") and the streamed stream-passthrough-flag ("45 days"), which the demo loop sends after it.
 * Null when the listed answers are hidden from this session (no content scope).
 */
async function newestWithAnswer(page: Page, route: string, outcome: string, text: string): Promise<PickedRow | null> {
  const search = new URLSearchParams({ route, outcome }).toString();
  let picked: PickedRow | null | undefined;
  await expect
    .poll(
      async () => {
        const res = await page.request.get(`/api/traces?${search}&limit=100`, { failOnStatusCode: false });
        if (!res.ok()) return `GET /api/traces answered HTTP ${res.status()}`;
        const { items } = (await res.json()) as { items: { trace_id: string; response_preview: string | null; redacted: boolean }[] };
        const hit = items.find((t) => t.response_preview?.includes(text));
        picked = hit ? { traceId: hit.trace_id, search } : items.some((t) => t.redacted) ? null : undefined;
        return picked === undefined ? 'no such request yet' : 'found';
      },
      { timeout: TIMEOUT, message: `a ${outcome} request on ${route} whose answer says "${text}"` },
    )
    .toBe('found');
  return picked ?? null;
}

/**
 * Opens a Traffic row with the live list paused: the newest with `outcome` checked against `doc`,
 * or with `pick`, that request, listed under its filters.
 */
async function openRow(page: Page, outcome: string, doc: string, pick?: PickedRow): Promise<void> {
  await page.goto(pick ? `/app/traffic?${pick.search}` : '/app/traffic');
  const rows = page.getByRole('table', { name: 'Requests' }).locator('[role="row"][data-trace-id]');
  let match = rows.filter({ has: page.getByText(outcome, { exact: true }) }).filter({ hasText: doc });
  if (pick) match = match.and(page.locator(`[data-trace-id="${pick.traceId}"]`));
  await expect(match.first(), `a ${outcome} request checked against ${doc}`).toBeVisible({ timeout: TIMEOUT });
  await page.getByRole('button', { name: 'Live', exact: true }).click();
  await match.first().getByRole('link').click();
  await expect(page).toHaveURL(pick ? new RegExp(`/app/traffic/${pick.traceId}$`) : /\/app\/traffic\/[^/?#]+$/);
}

async function requireReadableAnswer(page: Page): Promise<void> {
  const answer = page.getByRole('region', { name: 'Answer' });
  await expect(answer).toBeVisible();
  skipOrFail(
    await answer.getByText(/^Hidden:/).isVisible(),
    "this session can't read answers (no content scope), so there is nothing to mark",
  );
}

test.describe('trace detail', () => {
  test.beforeAll(async ({ playwright }, testInfo) => {
    await requireStack(playwright, testInfo);
  });

  test('inline claims: marks the contradicted "60 days" claim and links it to its row', async ({ page }) => {
    test.setTimeout(3 * TIMEOUT + 30_000);
    const pick = await newestWithAnswer(page, 'support-bot', 'flagged', '60 days');
    skipOrFail(pick === null, "this session can't read answers (no content scope), so there is nothing to mark");
    await openRow(page, 'Flagged', 'refund-policy.md', pick ?? undefined);

    const claims = page.getByRole('list', { name: 'Claims' });
    await expect(claims.getByText('Contradicted', { exact: true })).toBeVisible({ timeout: TIMEOUT });
    await requireReadableAnswer(page);
    const answer = page.getByRole('region', { name: 'Answer' });
    const mark = answer.getByRole('link', { name: /^60 days, contradicted claim/ });
    await expect(mark).toBeVisible();
    const claimMark = answer.locator('mark[data-v="contradicted"]');
    await expect(claimMark).toHaveText('60 days');
    const style = await claimMark.evaluate((el) => getComputedStyle(el).textDecorationStyle);
    expect(style, 'contradicted claims are wavy-underlined (DESIGN.md §3)').toBe('wavy');
    // The instant check's finding nests inside the claim's mark.
    await expect(claimMark.locator('mark[data-det~="numbers_not_in_evidence"]')).toHaveText('60');
    await expect(answer.getByText(/not located in text/)).toHaveCount(0);

    // Keyboard: the mark is one tab stop; Enter moves focus to the claim's row.
    await mark.focus();
    await page.keyboard.press('Enter');
    await expect(claims.getByRole('listitem').first()).toBeFocused();
    expect(await seriousViolations(page), 'trace detail with claim marks').toEqual([]);
  });

  test('inline claims: marks the link an instant check found in the blocked help-center answer', async ({ page }) => {
    test.setTimeout(2 * TIMEOUT + 30_000);
    await openRow(page, 'Blocked', 'password-reset.md');
    await requireReadableAnswer(page);
    const answer = page.getByRole('region', { name: 'Answer' });

    // urls_not_in_evidence: marked from the gate decision, before any claim.
    const finding = answer.locator('mark[data-det~="urls_not_in_evidence"]');
    await expect(finding).toHaveText('https://acme.example/reset-password');
    const line = await finding.evaluate((el) => {
      const s = getComputedStyle(el);
      return { style: s.textDecorationStyle, thickness: s.textDecorationThickness };
    });
    expect(line, 'instant-check marks are a solid 2px line (DESIGN.md §3)').toEqual({ style: 'solid', thickness: '2px' });
    await expect(answer.getByRole('button', { name: 'Marked by the instant checks' })).toBeVisible();

    // What the user received instead: the route's block message.
    await expect(answer.getByTestId('received-text')).toHaveText("I can't verify that answer against our help articles, so I've held it back.");

    // Once the audit lands, the finding nests in the unsupported claim that covers it.
    const claims = page.getByRole('list', { name: 'Claims' });
    await expect(claims.getByText('Unsupported', { exact: true })).toBeVisible({ timeout: TIMEOUT });
    await expect(answer.locator('mark[data-v="unsupported"] mark[data-det~="urls_not_in_evidence"]')).toHaveText(
      'https://acme.example/reset-password',
    );
    await expect(
      answer.getByRole('link', { name: /links to a URL not in your documents$/ }),
      'the claim link names the finding',
    ).toBeVisible();
    expect(await seriousViolations(page), 'trace detail with detector marks').toEqual([]);
  });
});
