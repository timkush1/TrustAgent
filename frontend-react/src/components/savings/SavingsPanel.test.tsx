import { QueryClient } from '@tanstack/react-query';
import { configure, fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import statusBudgetEvent from '@contracts/fixtures/api/WsEvent.status.budget.json';
import settingsUpdatedEvent from '@contracts/fixtures/api/WsEvent.settings_updated.json';
import { qk, qkc } from '@/data/queries';
import { costSideEffects, qkcCosts, roiBucket } from '@/data/queries/costs';
import type { StreamEvent } from '@/data/stream/types';
import { axeViolations, formatViolations } from '@/test/axe';
import { roiSummary } from '@/test/control/fixtures';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { installMswServer } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import type { Counterfactual, RoiTimeseries } from '@/types/api';

installMswServer();
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

    await user.click(within(chart).getByRole('radio', { name: 'Table' }));
    const table = within(chart).getByRole('region', { name: /Spend per day/ });
    const rows = within(table).getAllByRole('row');
    // A header row plus the seven days of the fixture.
    expect(rows).toHaveLength(8);
    expect(within(table).getByRole('columnheader', { name: 'Your cost (Measured)' })).toBeInTheDocument();
    // 2026-09-22: $0.0081 + $0.142 spent; the same checks $0.39 × [1.05/1.22, 1.40/1.22].
    expect(within(rows[1]).getAllByRole('cell').map((c) => c.textContent)).toEqual(['180', '$0.15', '$0.34–$0.45']);
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

  it('reads each interval with the arrow keys', async () => {
    renderPage('/app/overview?tab=savings&window=7d');
    const plot = await screen.findByRole('group', { name: /Use the arrow keys/ });
    plot.focus();
    fireEvent.keyDown(plot, { key: 'Home' });
    expect(within(plot).getByText(/your cost \$0\.15, same questions LLM \$0\.34–\$0\.45, 180 answers checked/)).toBeInTheDocument();
    fireEvent.keyDown(plot, { key: 'ArrowRight' });
    expect(within(plot).getByText(/: your cost \$0\.17, .*207 answers checked\.$/)).toBeInTheDocument();
    fireEvent.keyDown(plot, { key: 'Escape' });
    expect(within(plot).queryByText(/answers checked\./)).toBeNull();
  });

  it('labels every component with where its number comes from', async () => {
    const control = createControlApi({ roi: roiSummary({ counterfactuals: [...roiSummary().counterfactuals, FULL_AUDIT] }) });
    renderPage('/app/savings', { control });
    const table = await screen.findByRole('table', { name: /What checking cost, and what the alternatives/ });
    const rows = within(table).getAllByRole('row').slice(1);
    expect(rows.map((r) => r.getAttribute('data-row'))).toEqual(['fast_check', 'deep_audit', 'total', 'llm_same_checks', 'llm_full_claim_audit']);
    for (const row of rows) expect(within(row).getByRole('button', { name: /: how “.+” was produced$/ })).toBeInTheDocument();
    expect(within(rows[1]).getByRole('rowheader')).toHaveTextContent('58 deep audits: 44 high risk · 10 sampled · 4 uniform sample');
    expect(within(rows[3]).getAllByRole('cell')[0]).toHaveTextContent('$1.05–$1.40');
  });

  it('keeps stopped (block and fallback) apart from flagged, with speed and sampling', async () => {
    const control = createControlApi({ roi: roiSummary({ outcomes: { flagged: 34, blocked: 4, fallback: 3, not_assessed: 72, label: 'measured' } }) });
    renderPage('/app/savings', { control });
    const stopped = await screen.findByText('Stopped before users saw them');
    expect(stopped.closest('div')).toHaveTextContent('7');
    expect(screen.getByText('Flagged to your app').closest('div')).toHaveTextContent('34');
    expect(badge('Measured', 'Stopped and flagged answers')).toBeInTheDocument();
    expect(screen.getByText('p95').nextElementSibling).toHaveTextContent('480 ms');
    expect(screen.getByText('Missed the deadline').nextElementSibling).toHaveTextContent('0.4%');
    expect(screen.getByText('Uniform sampling: 4 of 50 audits today (1% of answers)')).toBeInTheDocument();
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

  it('has no axe violations', async () => {
    const control = createControlApi({ roi: roiSummary({ counterfactuals: [...roiSummary().counterfactuals, FULL_AUDIT] }) });
    const { container } = renderPage('/app/overview?tab=savings&window=7d', { control });
    await screen.findByRole('figure', { name: 'What checking cost, against asking an LLM' });
    await waitFor(() => expect(container.querySelector('[data-series="measured"]')).not.toBeNull());
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});

describe('cost queries', () => {
  it('draws hours up to a day and days beyond', () => {
    expect(['15m', '1h', '24h', '7d', '30d'].map((w) => roiBucket(w as never))).toEqual(['1h', '1h', '1h', '1d', '1d']);
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
