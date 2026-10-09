import { QueryClient } from '@tanstack/react-query';
import { configure, fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import statusBudgetEvent from '@contracts/fixtures/api/WsEvent.status.budget.json';
import settingsUpdatedEvent from '@contracts/fixtures/api/WsEvent.settings_updated.json';
import { OVERVIEW_POLL_MS, qk, qkc, roiSummaryQuery } from '@/data/queries';
import { costSideEffects, qkcCosts, roiBucket, roiTimeseriesQuery } from '@/data/queries/costs';
import type { DataSource } from '@/data/source';
import type { StreamEvent } from '@/data/stream/types';
import { axeViolations, formatViolations } from '@/test/axe';
import { roiSummary } from '@/test/control/fixtures';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { installMswServer } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import { warmRoutes } from '@/test/warmRoutes';
import type { Counterfactual, RoiTimeseries } from '@/types/api';

installMswServer();
warmRoutes('/app/savings', '/app/overview');
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 4000 });

const FULL_AUDIT: Counterfactual = {
  kind: 'llm_full_claim_audit',
  method: 'static_assumptions',
  usd: 19.17,
  low: 15.12,
  high: 24.3,
  per_1k: 35.5,
  n: 540,
  label: 'estimate',
  basis: 'published assumptions: 8 claims per answer verified by the judge',
};

function badge(label: string, subject: string) {
  return screen.getByRole('button', { name: `${label}: how “${subject}” was produced` });
}

describe('Savings tab', { timeout: 20_000 }, () => {
  it('opens from the deep link with the labelled headline and the savings range', async () => {
    renderPage('/app/savings');
    expect(await screen.findByRole('tab', { name: 'Savings', selected: true })).toBeInTheDocument();
    const headline = await screen.findByRole('heading', { level: 2, name: /^Your cost: \$0\.80 per 1,000 answers/ });
    expect(headline).toHaveTextContent('Your cost: $0.80 per 1,000 answers (fast check $0.05 + deep audit $0.76)');
    expect(badge('Measured', 'Your cost per 1,000 answers')).toBeInTheDocument();
    expect(screen.getByText(/Saved/)).toHaveTextContent('Saved $0.60–$0.97 against an LLM asking the same questions');
    expect(badge('Estimate', 'Savings against an LLM asking the same questions')).toBeInTheDocument();
    expect(screen.getByText(/So far: 540 answers checked/)).toBeInTheDocument();
    // The alternatives in the headline's unit: here only an LLM asking the same questions, with
    // its own label and how it was worked out; no real-judge figure on a stack with a real judge.
    const others = screen.getByRole('heading', { level: 3, name: 'Against the alternatives, per 1,000 answers' });
    expect(others).toBeInTheDocument();
    const same = document.querySelector<HTMLElement>('[data-alternative="llm_same_checks"]')!;
    expect(within(same).getByText('$2.26')).toBeInTheDocument();
    expect(same).toHaveTextContent('Token model: your fast-check tokens priced at the judge’s rate');
    expect(badge('Estimate', 'An LLM asking the same questions, per 1,000 answers')).toBeInTheDocument();
    expect(document.querySelector('[data-alternative="real_judge"]')).toBeNull();
    expect(screen.queryByText(/mock judge is free/)).toBeNull();
  });

  it('words a savings range that crosses zero as both ways', async () => {
    const control = createControlApi({ roi: roiSummary({ savings_vs_same_checks: { usd_low: -0.1, usd_high: 0.2, label: 'estimate' } }) });
    renderPage('/app/overview?tab=savings', { control });
    expect(await screen.findByText(/^Between/)).toHaveTextContent('Between $0.10 more and $0.20 less than an LLM asking the same questions');
    expect(screen.queryByText(/^Saved/)).toBeNull();
  });

  it('asks for the time series in the window’s bucket and draws the bands with a table', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/overview?tab=savings&window=7d');
    const chart = await screen.findByRole('figure', { name: 'What checking cost, against asking an LLM' });
    const call = control.callsTo('/api/roi/timeseries')[0];
    expect(call.query.get('window')).toBe('7d');
    expect(call.query.get('bucket')).toBe('1d');
    const legend = within(chart).getByRole('list', { name: 'Legend' });
    expect(within(legend).getByText('What checking cost you (Measured)')).toBeInTheDocument();
    expect(within(legend).getByText('An LLM asking the same questions (Estimate, range)')).toBeInTheDocument();
    expect(chart.querySelector('[data-series="measured"]')).not.toBeNull();
    expect(chart.querySelector('[data-band="same_checks"]')).not.toBeNull();
    // A range is an area (a square key, no dashes), and its end label is the range.
    expect(legend.querySelector('[data-series="band"] [data-mark="square"]')).not.toBeNull();
    expect(chart.querySelector('path[data-band="same_checks"]')).not.toHaveAttribute('stroke-dasharray');
    expect(chart.querySelector('[data-end-label="band"]')?.textContent).toMatch(/^LLM \$[\d.]+–\$[\d.]+$/);

    await user.click(within(chart).getByRole('radio', { name: 'Table' }));
    const table = within(chart).getByRole('region', { name: /Spend per day/ });
    const rows = within(table).getAllByRole('row');
    // A header row plus the seven days of the fixture.
    expect(rows).toHaveLength(8);
    expect(within(table).getByRole('columnheader', { name: 'Your cost (Measured)' })).toBeInTheDocument();
    // 2026-09-22: $0.0081 + $0.142 spent; the same checks $0.39 × [1.05/1.22, 1.40/1.22].
    expect(within(rows[1]).getAllByRole('cell').map((c) => c.textContent)).toEqual(['180', '$0.15', '$0.34–$0.45']);
  });

  it('draws a single LLM estimate as a dashed line, labels both lines at their end, and keeps a range as an area', async () => {
    const base = roiSummary();
    const single = base.counterfactuals.map((c) => (c.kind === 'llm_same_checks' ? { ...c, low: null, high: null } : c));
    const control = createControlApi({ roi: roiSummary({ counterfactuals: single }) });
    renderPage('/app/overview?tab=savings&window=7d', { control });
    const chart = await screen.findByRole('figure', { name: 'What checking cost, against asking an LLM' });
    const legend = within(chart).getByRole('list', { name: 'Legend' });
    // Told apart by mark, not only by weight: the legend key for the estimate is dashed too.
    expect(legend.querySelector('[data-series="measured"] [data-mark="line"]')).not.toBeNull();
    expect(legend.querySelector('[data-series="band"] [data-mark="dash"]')).not.toBeNull();
    const line = chart.querySelector('path[data-band="same_checks"]');
    expect(line).toHaveAttribute('stroke-dasharray');
    expect(chart.querySelector('path[data-series="measured"]')).not.toHaveAttribute('stroke-dasharray');
    // Direct labels at the end of each line: the last day's spend and the LLM's estimate.
    expect(chart.querySelector('[data-end-label="measured"]')?.textContent).toMatch(/^You \$/);
    expect(chart.querySelector('[data-end-label="band"]')?.textContent).toMatch(/^LLM \$[\d.]+$/);
  });

  it('labels the spend line like the total when the deep-audit cost is an estimate', async () => {
    const base = roiSummary();
    const control = createControlApi({
      roi: roiSummary({ llm_cost: { ...base.llm_cost, label: 'estimate' }, total_per_1k: { ...base.total_per_1k, label: 'estimate' } }),
    });
    renderPage('/app/overview?tab=savings', { control });
    const chart = await screen.findByRole('figure', { name: 'What checking cost, against asking an LLM' });
    const legend = within(chart).getByRole('list', { name: 'Legend' });
    expect(within(legend).getByText('What checking cost you (Estimate)')).toBeInTheDocument();
  });

  it('switches the band to a full audit of every answer when that estimate exists', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ roi: roiSummary({ counterfactuals: [...roiSummary().counterfactuals, FULL_AUDIT] }) });
    renderPage('/app/savings', { control });
    const chart = await screen.findByRole('figure', { name: 'What checking cost, against asking an LLM' });
    await user.click(within(chart).getByRole('radio', { name: 'Full audit' }));
    expect(within(chart).getByText('An LLM auditing every answer claim by claim (Estimate, range)')).toBeInTheDocument();
    expect(chart.querySelector('[data-band="full_audit"]')).not.toBeNull();
    // The component table says the full-audit figure is not measured.
    const row = screen.getByRole('rowheader', { name: /An LLM auditing every answer/ });
    expect(row).toHaveTextContent('not measured');
  });

  it('says a $0 deep audit comes from the free mock judge, and shows no $0 full audit', async () => {
    const base = roiSummary();
    const freeFull = { ...FULL_AUDIT, method: 'uniform_sample' as const, usd: 0, low: 0, high: 0, per_1k: 0, label: 'simulated' as const };
    const control = createControlApi({
      roi: roiSummary({ simulated: true, llm_cost: { ...base.llm_cost, usd: 0, value: 0 }, counterfactuals: [...base.counterfactuals, freeFull] }),
    });
    renderPage('/app/savings', { control });
    // On a free mock judge the comparison is the published estimates only, on one basis: checking
    // with a real judge at the shipped defaults, an LLM asking the same questions, a full audit.
    // Never this demo's own deep-audit rate scaled up, nor its simulated same-questions figure.
    expect(await screen.findByRole('heading', { level: 3, name: 'With a real judge, per 1,000 answers' })).toBeInTheDocument();
    const real = screen.getByText('TrustAgent with a real judge').closest<HTMLElement>('[data-alternative]')!;
    expect(real).toHaveAttribute('data-alternative', 'real_judge');
    expect(within(real).getByText('$3')).toBeInTheDocument();
    expect(real).toHaveTextContent('At the shipped defaults: about 8 in 100 answers deep-audited');
    expect(badge('Estimate', 'Checking at the shipped defaults with a real judge')).toBeInTheDocument();
    const same = document.querySelector<HTMLElement>('[data-alternative="llm_same_checks"]')!;
    expect(within(same).getByText('$5.75')).toBeInTheDocument();
    expect(same).toHaveTextContent('Published assumptions · not measured');
    expect(badge('Estimate', 'An LLM asking the same questions, published, per 1,000 answers')).toBeInTheDocument();
    const full = document.querySelector<HTMLElement>('[data-alternative="llm_full_claim_audit"]')!;
    expect(within(full).getByText('$35.50')).toBeInTheDocument();
    expect(full).toHaveTextContent('Published assumptions · not measured');
    expect(badge('Estimate', 'An LLM auditing every answer claim by claim, published, per 1,000 answers')).toBeInTheDocument();
    expect([...document.querySelectorAll('[data-alternative]')].map((el) => el.getAttribute('data-alternative'))).toEqual([
      'real_judge',
      'llm_same_checks',
      'llm_full_claim_audit',
    ]);
    // Then this window's own rate (58 audits over 540 answers), apart from the published figures.
    const free = screen.getByText(/^The deep audit reads \$0 because the demo’s mock judge is free\./);
    expect(free).toHaveTextContent(
      'This demo deep-audits about 11 in 100 answers to show the audit flow: at that rate a real judge would add about $3.81 per 1,000 answers.',
    );
    expect(badge('Estimate', 'What a real judge would add at this demo’s rate')).toBeInTheDocument();
    const row = screen.getByRole('rowheader', { name: /An LLM auditing every answer/ }).closest('tr')!;
    expect(row).toHaveTextContent('Not available in the simulated demo: the mock judge is free');
    expect(within(row).getAllByRole('cell').slice(0, 2).map((c) => c.textContent)).toEqual(['—', '—']);
    // No full-audit band to switch to: the chart compares with the same questions only.
    const chart = await screen.findByRole('figure', { name: 'What checking cost, against asking an LLM' });
    expect(within(chart).queryByRole('radio', { name: 'Full audit' })).toBeNull();
  });

  it('reads each day with the arrow keys', async () => {
    renderPage('/app/overview?tab=savings&window=7d');
    const plot = await screen.findByRole('slider', { name: 'What checking cost per day' });
    expect(plot).toHaveAttribute('aria-valuemax', '6');
    // The totals are the summary's (the headline's and the table's), not a sum of the points.
    expect(plot).toHaveAccessibleDescription(
      'Checking 540 answers cost $0.43 in the last 7 days. An LLM asking the same questions would have cost $1.05–$1.40 (estimate).',
    );
    plot.focus();
    fireEvent.keyDown(plot, { key: 'Home' });
    expect(plot).toHaveAttribute('aria-valuenow', '0');
    expect(plot.getAttribute('aria-valuetext')).toMatch(/: your cost \$0\.15, same questions LLM \$0\.34–\$0\.45, 180 answers checked\.$/);
    // The readout shows next to the point.
    expect(within(plot).getByText('180 answers checked')).toBeInTheDocument();
    fireEvent.keyDown(plot, { key: 'ArrowRight' });
    expect(plot.getAttribute('aria-valuetext')).toMatch(/: your cost \$0\.17, .*207 answers checked\.$/);
    fireEvent.keyDown(plot, { key: 'End' });
    expect(plot).toHaveAttribute('aria-valuenow', '6');
    fireEvent.keyDown(plot, { key: 'Escape' });
    expect(within(plot).queryByText(/answers checked$/)).toBeNull();
  });

  it('labels every component with where its number comes from', async () => {
    const control = createControlApi({ roi: roiSummary({ counterfactuals: [...roiSummary().counterfactuals, FULL_AUDIT] }) });
    renderPage('/app/savings', { control });
    const table = await screen.findByRole('table', { name: /What checking cost, and what the alternatives/ });
    const rows = within(table).getAllByRole('row').slice(1);
    expect(rows.map((r) => r.getAttribute('data-row'))).toEqual(['fast_check', 'deep_audit', 'total', 'llm_same_checks', 'llm_full_claim_audit']);
    for (const row of rows) expect(within(row).getByRole('button', { name: /: how “.+” was produced$/ })).toBeInTheDocument();
    expect(within(rows[1]).getByRole('rowheader')).toHaveTextContent('58 deep audits attempted: 44 high risk · 10 pass sample · 4 uniform sample');
    expect(within(rows[3]).getAllByRole('cell')[0]).toHaveTextContent('$1.05–$1.40');
  });

  it('labels the vendor price the alternatives use Vendor, as the Prices table does', async () => {
    const vendor = 'Claude Haiku 4.5 list price $1 / $5 per million tokens (Anthropic, as of 2026-06-24)';
    renderPage('/app/savings', { control: createControlApi({ roi: roiSummary({ third_party: [vendor] }) }) });
    const line = await screen.findByText(`Priced with: ${vendor}`);
    expect(within(line.parentElement!).getByRole('button', { name: 'Vendor: how “Price used for the alternatives” was produced' })).toBeInTheDocument();
    expect(screen.queryByText(/third-party/i)).toBeNull();
  });

  it('never labels your own price override Vendor', async () => {
    const override = 'anthropic claude-haiku-4-5 $0.8 / $4 per million tokens (your price override, as of 2026-09-30)';
    renderPage('/app/savings', { control: createControlApi({ roi: roiSummary({ third_party: [override] }) }) });
    const own = await screen.findByText(`Priced with: ${override}`);
    expect(own.parentElement).toHaveTextContent(/^Your price/);
    expect(within(own.parentElement!).queryByRole('button', { name: /^Vendor:/ })).toBeNull();
  });

  it('keeps stopped (block and fallback) apart from flagged, with speed and sampling', async () => {
    const control = createControlApi({ roi: roiSummary({ outcomes: { flagged: 34, blocked: 4, fallback: 3, not_assessed: 72, label: 'measured' } }) });
    renderPage('/app/savings', { control });
    const stopped = await screen.findByText('Stopped before users saw them');
    expect(stopped.closest('div')).toHaveTextContent('7');
    expect(screen.getByText('Flagged to your app').closest('div')).toHaveTextContent('34');
    expect(badge('Measured', 'Stopped and flagged answers')).toBeInTheDocument();
    expect(screen.getByText(/answers? (was|were) not assessed\.$/)).toHaveTextContent(/72 answers were not assessed\.$/);
    expect(screen.getByText('p95').nextElementSibling).toHaveTextContent('480 ms');
    expect(screen.getByText('Missed the deadline').nextElementSibling).toHaveTextContent('0.4%');
    expect(screen.getByText('Uniform sampling: 4 of 50 audits today (1% of answers)')).toBeInTheDocument();
    // How a uniform sample differs from the pass samples the component table counts.
    expect(screen.getByText(/^Uniform samples are picked before the fast check decides/)).toHaveTextContent(
      'Pass samples are drawn from answers that passed, at each route’s sample rate.',
    );
    expect(screen.queryByRole('link', { name: /Raise the deep-audit budget/ })).toBeNull();
  });

  it('says when uniform sampling paused and the deep-audit budget ran out', async () => {
    const today = new Date();
    today.setHours(14, 2, 0, 0);
    const at = today.toISOString();
    const control = createControlApi({
      roi: roiSummary({ sampling: { uniform_rate: 0.01, uniform_today: 50, uniform_cap: 50, uniform_stopped_at: at, llm_cap_hit_at: at } }),
    });
    renderPage('/app/savings', { control });
    expect(await screen.findByText('Uniform sampling paused at 14:02 (daily cap 50)')).toBeInTheDocument();
    expect(screen.getByText('Deep-audit budget reached at 14:02')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Raise the deep-audit budget/ })).toHaveAttribute('href', '/app/settings/budgets');
  });

  it('labels everything Simulated on a mock stack', async () => {
    const sim = roiSummary({ simulated: true });
    const control = createControlApi({ roi: sim });
    renderPage('/app/savings', { control, status: { demo: true } });
    await screen.findByRole('heading', { level: 2, name: /^Your cost:/ });
    expect(badge('Simulated', 'Your cost per 1,000 answers')).toBeInTheDocument();
    expect(badge('Simulated', 'Stopped and flagged answers')).toBeInTheDocument();
    const table = screen.getByRole('table', { name: /What checking cost/ });
    for (const b of within(table).getAllByRole('button')) expect(b).toHaveAccessibleName(/^Simulated:/);
    expect(await screen.findByText('What checking cost you (Simulated)')).toBeInTheDocument();
  });

  it('labels the published full-audit figure Estimate on a mock stack, in the table and the chart', async () => {
    const user = userEvent.setup();
    const published = { ...FULL_AUDIT, low: null, high: null, usd: 19.17 };
    const control = createControlApi({ roi: roiSummary({ simulated: true, counterfactuals: [...roiSummary().counterfactuals, published] }) });
    renderPage('/app/savings', { control, status: { demo: true } });
    const row = (await screen.findByRole('rowheader', { name: /An LLM auditing every answer/ })).closest('tr')!;
    expect(within(row).getByRole('button', { name: 'Estimate: how “An LLM auditing every answer claim by claim” was produced' })).toBeInTheDocument();
    expect(row).toHaveTextContent('Published assumptions: $35.50 per 1,000 answers × 540 simulated answers · not measured');
    const same = screen.getByRole('rowheader', { name: /An LLM asking the same questions/ });
    expect(within(same).getByRole('button', { name: /^Simulated:/ })).toBeInTheDocument();
    const chart = await screen.findByRole('figure', { name: 'What checking cost, against asking an LLM' });
    await user.click(within(chart).getByRole('radio', { name: 'Full audit' }));
    expect(within(chart).getByText('An LLM auditing every answer claim by claim (Estimate)')).toBeInTheDocument();
  });

  it('has an empty state before the first checked answer', async () => {
    const control = createControlApi({ roi: roiSummary({ gated_responses: 0 }) });
    renderPage('/app/savings', { control });
    expect(await screen.findByText('No checked answers in the last 24 hours')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Try one in the Playground/ })).toHaveAttribute('href', '/app/playground');
  });

  it('degrades on a proxy that does not report costs, and shows its error otherwise', async () => {
    renderPage('/app/savings', { control: createControlApi({ roi: undefined }) });
    expect(await screen.findByText('Costs are not reported by this proxy')).toBeInTheDocument();
    stopLive();

    const control = createControlApi({ roi: errorReply(500, 'internal', 'The cost ledger is unavailable.') });
    renderPage('/app/savings', { control });
    expect(await screen.findByText(/The cost ledger is unavailable\./, undefined, { timeout: 8000 })).toBeInTheDocument();
    control.state.roi = roiSummary();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByRole('heading', { level: 2, name: /^Your cost:/ })).toBeInTheDocument();
  });

  it('keeps the totals when the proxy does not serve the time series', async () => {
    renderPage('/app/savings', { control: createControlApi({ roiTimeseries: undefined }) });
    expect(await screen.findByText('Spend over time is not available on this proxy')).toBeInTheDocument();
    expect(screen.getByRole('heading', { level: 2, name: /^Your cost:/ })).toBeInTheDocument();
  });

  it('says when a window has a single bucket', async () => {
    const one: RoiTimeseries = { window: '1h', bucket: '1h', points: [{ t: '2026-09-28T10:00:00.000Z', decision_usd: 0.01, llm_usd: 0.1, same_checks_usd: 0.3, gated: 40 }] };
    renderPage('/app/overview?tab=savings&window=1h', { control: createControlApi({ roiTimeseries: one }) });
    expect(await screen.findByText(/Only one hour in this window so far/)).toBeInTheDocument();
  });

  it('asks for no time series in a window shorter than an hour, and says why', async () => {
    const { control } = renderPage('/app/overview?tab=savings&window=15m');
    expect(await screen.findByText(/^Spend over time starts at the 1-hour window/)).toBeInTheDocument();
    expect(screen.getByRole('heading', { level: 2, name: /^Your cost:/ })).toBeInTheDocument();
    expect(control.callsTo('/api/roi/timeseries')).toHaveLength(0);
  });

  it('says when a window has no bucket yet', async () => {
    const none: RoiTimeseries = { window: '7d', bucket: '1d', points: [] };
    renderPage('/app/overview?tab=savings&window=7d', { control: createControlApi({ roiTimeseries: none }) });
    expect(await screen.findByText(/^No days in this window so far: switch to Table/)).toBeInTheDocument();
  });

  it('has no axe violations', async () => {
    const control = createControlApi({ roi: roiSummary({ counterfactuals: [...roiSummary().counterfactuals, FULL_AUDIT] }) });
    const { container } = renderPage('/app/overview?tab=savings&window=7d', { control });
    await screen.findByRole('figure', { name: 'What checking cost, against asking an LLM' });
    await waitFor(() => expect(container.querySelector('[data-series="measured"]')).not.toBeNull());
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});

describe('cost queries', () => {
  it('polls the spend series with the ROI summary, so the chart keeps up with the headline', () => {
    const ds = {} as DataSource;
    expect(roiTimeseriesQuery(ds, '24h').refetchInterval).toBe(OVERVIEW_POLL_MS);
    expect(roiSummaryQuery(ds, '24h').refetchInterval).toBe(OVERVIEW_POLL_MS);
  });

  it('draws hours up to a day and days beyond', () => {
    // #48 has no bucket for a 15-minute window (it answers 422): the chart asks for none.
    expect(['15m', '1h', '24h', '7d', '30d'].map((w) => roiBucket(w as never))).toEqual([null, '1h', '1h', '1d', '1d']);
  });

  it('refetches status, ROI and spend on a budget event (D49), and prices when overrides change', () => {
    const client = new QueryClient();
    const spy = vi.spyOn(client, 'invalidateQueries');
    costSideEffects(statusBudgetEvent as unknown as StreamEvent, client);
    expect(spy.mock.calls.map((c) => c[0]?.queryKey)).toEqual([qk.status, [...qkc.overview.all, 'roi'], qkcCosts.all]);

    spy.mockClear();
    costSideEffects(settingsUpdatedEvent as unknown as StreamEvent, client);
    expect(spy).not.toHaveBeenCalled();
    const pricing = { ...settingsUpdatedEvent, data: { version: 8, keys: ['pricing_overrides'] } };
    costSideEffects(pricing as unknown as StreamEvent, client);
    expect(spy.mock.calls.map((c) => c[0]?.queryKey)).toEqual([qkcCosts.pricing]);

    spy.mockClear();
    const engine = { ...statusBudgetEvent, data: { component: 'engine', state: 'down' } };
    costSideEffects(engine as unknown as StreamEvent, client);
    expect(spy).not.toHaveBeenCalled();
  });
});
