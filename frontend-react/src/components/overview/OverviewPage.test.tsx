import { configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { axeViolations, formatViolations } from '@/test/axe';
import { emptyOverview, labelledRate, overviewStats, roiSummary } from '@/test/control/fixtures';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { installMswServer } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import { heroDescription } from './heroChart';

installMswServer();
afterEach(() => stopLive());
// The first render of a page imports its charts; give it longer than the 1 s default.
configure({ asyncUtilTimeout: 4000 });

describe('Overview page', { timeout: 20_000 }, () => {
  it('shows the numbers, the hero chart and what was caught', async () => {
    const { control } = renderPage('/app/overview');
    expect(await screen.findByRole('heading', { name: 'Requests by outcome' })).toBeInTheDocument();
    // The proxy counts would-block answers in the would_flag series: the chart says so.
    expect(screen.getByText(/Would flag also counts answers an observe-only route would have blocked/)).toBeInTheDocument();
    const glance = screen.getByLabelText('At a glance');
    expect(within(glance).getByText('612')).toBeInTheDocument();
    // Blocked 4 + fallback 0 stopped; the flagged count sits under it.
    expect(within(glance).getByText('4')).toBeInTheDocument();
    expect(within(glance).getByText(/34 flagged to your app/)).toBeInTheDocument();
    // Fast check p50 / p95.
    expect(within(glance).getByText(/212 ms/)).toBeInTheDocument();
    // The hallucination rate waits for 30 unbiased samples.
    expect(within(glance).getByText('Needs 30 unbiased samples (have 12)')).toBeInTheDocument();
    // The fast check's latency carries its value label like the counts.
    expect(within(glance).getByRole('button', { name: /Measured: how “Fast check \(Jev\) latency” was produced/ })).toBeInTheDocument();
    // So do the stopped count and the groundedness share (every number says where it comes from).
    expect(within(glance).getByRole('button', { name: /Measured: how “Answers stopped or flagged” was produced/ })).toBeInTheDocument();
    expect(within(glance).getByRole('button', { name: /Measured: how “Groundedness checked” was produced/ })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'What was caught' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Contradicts your documents: show all 21 requests' })).toHaveAttribute(
      'href',
      '/app/traffic?reason=contradiction%3Acritical',
    );
    expect(screen.getAllByRole('link', { name: /^Latest example of / })).toHaveLength(3);
    expect(control.callsTo('/api/overview')[0].query.get('window')).toBe('24h');
  });

  it('writes the savings line with value labels', async () => {
    renderPage('/app/overview');
    expect(await screen.findByText(/Your cost: \$0\.80 per 1,000 answers \(fast check \$0\.05 \+ deep audit \$0\.76\)/)).toBeInTheDocument();
    expect(screen.getByText(/So far: 540 answers checked for \$0\.43\. Asking an LLM the same questions would have cost ≈ \$1\.22 \(estimate\)\./)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Estimate: how “What an LLM would have cost” was produced/ })).toBeInTheDocument();
  });

  it('labels every number simulated on the demo stack and shows the demo connect copy', async () => {
    const control = createControlApi({ overview: emptyOverview(), roi: roiSummary({ simulated: true }) });
    renderPage('/app/overview', { control, status: { demo: true } });
    const card = (await screen.findByText(/This is the demo stack: mock checks, \$0\./)).closest('p');
    expect(within(card as HTMLElement).getByText('make demo-down && make up')).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: 'OpenAI Python' })).not.toBeInTheDocument();
  });

  it('shows the connect card with SDK tabs when no request has arrived', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ overview: emptyOverview() });
    renderPage('/app/overview', { control });
    expect(await screen.findByRole('heading', { name: 'Connect your app' })).toBeInTheDocument();
    expect(screen.getByText('Waiting for your first request…')).toBeInTheDocument();
    const tabs = ['OpenAI Python', 'OpenAI TS', 'Anthropic', 'curl', 'LangChain'];
    for (const name of tabs) expect(screen.getByRole('tab', { name })).toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: 'curl' }));
    await user.keyboard('{Enter}');
    const panel = await screen.findByRole('region', { name: 'curl (Shell)' });
    expect(panel.textContent).toContain('X-TrustAgent-Key: $TRUSTAGENT_KEY');
    expect(panel.textContent).toContain('"trustagent"');
    // The playground link is the empty state's next step.
    expect(screen.getAllByRole('link', { name: /Playground/ }).length).toBeGreaterThan(0);
  });

  it('explains the evidence gaps in the Why not? panel', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/overview');
    const why = await screen.findByRole('button', { name: 'Why not?' });
    expect(why).toHaveAttribute('aria-expanded', 'false');
    await user.click(why);
    expect(why).toHaveAttribute('aria-expanded', 'true');
    expect(await screen.findByText('No documents in the request')).toBeInTheDocument();
    expect(screen.getByText('System prompt documents')).toBeInTheDocument();
    expect(await screen.findByText(/340 of your last 500 requests would gain documents/)).toBeInTheDocument();
    expect(screen.getByRole('region', { name: 'Attach documents to any request' }).textContent).toContain('trustagent');
    await waitFor(() => expect(control.callsTo('/api/evidence/coverage')).toHaveLength(1));
  });

  it('says why suggestions are missing when they need an admin', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ suggestions: errorReply(403, 'forbidden', 'Needs admin.') });
    renderPage('/app/overview', { control });
    await user.click(await screen.findByRole('button', { name: 'Why not?' }));
    expect(await screen.findByText('Suggestions scan stored prompts, so they need an admin sign-in.')).toBeInTheDocument();
  });

  it('shows the rate once enough unbiased samples exist', async () => {
    const control = createControlApi({
      overview: overviewStats({ hallucination_rate: labelledRate({ value: 0.041, n: 212, ci95: [0.029, 0.056] }) }),
    });
    renderPage('/app/overview', { control });
    expect(await screen.findByText('4.1%')).toBeInTheDocument();
    expect(screen.getByText('95% CI 2.9–5.6%')).toBeInTheDocument();
    expect(screen.getByText('n = 212')).toBeInTheDocument();
  });

  it('does not call risk-selected audits unbiased samples', async () => {
    const control = createControlApi({ overview: overviewStats({ audited_hallucination_share: labelledRate({ value: null, n: 7 }) }) });
    renderPage('/app/overview', { control });
    const section = (await screen.findByRole('heading', { name: 'Hallucinations among audited answers' })).closest('section') as HTMLElement;
    expect(within(section).getByText('Needs 30 audited answers (have 7)')).toBeInTheDocument();
    expect(within(section).queryByText(/unbiased/)).not.toBeInTheDocument();
  });

  it('changes the time window through the URL', async () => {
    const user = userEvent.setup();
    const { control, router } = renderPage('/app/overview');
    await screen.findByRole('heading', { name: 'Requests by outcome' });
    await user.click(screen.getByRole('radio', { name: '7d' }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ window: '7d' }));
    await waitFor(() => expect(control.callsTo('/api/overview').some((c) => c.query.get('window') === '7d')).toBe(true));
  });

  it('shows the server message with Retry on error', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ overview: errorReply(500, 'internal', 'Database is restarting.') });
    renderPage('/app/overview', { control });
    expect(await screen.findByText("Couldn't load this: Database is restarting.")).toBeInTheDocument();
    control.state.overview = overviewStats();
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByRole('heading', { name: 'Requests by outcome' })).toBeInTheDocument();
  });

  it('degrades on a proxy that does not serve the overview', async () => {
    const control = createControlApi({ overview: undefined });
    renderPage('/app/overview', { control });
    expect(await screen.findByText('Overview numbers are not available on this proxy')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Open Traffic/ })).toBeInTheDocument();
  });

  it('lists the setup steps with their fixes', async () => {
    renderPage('/app/overview', {
      status: { persistence: { state: 'down', configured: false, migration: null } },
    });
    expect(await screen.findByText('History saved in a database')).toBeInTheDocument();
    expect(screen.getByText(/of 6 done/)).toBeInTheDocument();
  });

  it('opens the setup wizard at the step a setup item needs', async () => {
    renderPage('/app/overview', {
      status: { auth: { ingest: 'keys', read: 'public', setup_required: true, caller_scopes: ['read'] } },
    });
    const link = await screen.findByRole('link', { name: /Open setup/ });
    expect(link.getAttribute('href')).toMatch(/^\/app\/setup\?step=secure$/);
  });

  it('opens the Savings tab from the deep link', async () => {
    renderPage('/app/savings');
    expect(await screen.findByRole('tab', { name: 'Savings', selected: true })).toBeInTheDocument();
    expect(await screen.findByRole('heading', { level: 2, name: /^Your cost: \$0\.80 per 1,000 answers/ })).toBeInTheDocument();
  });

  it('has no axe violations', async () => {
    const { container } = renderPage('/app/overview');
    await screen.findByRole('heading', { name: 'Requests by outcome' });
    await screen.findByText(/Your cost:/);
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});

describe('Overview hero chart', () => {
  it('says that Would flag also counts would-block answers, when it has any', () => {
    const stats = overviewStats();
    expect(stats.outcomes_series.some((p) => p.would_flag > 0)).toBe(true);
    expect(heroDescription(stats, false)).toMatch(/\. Would flag also counts answers an observe-only route would have blocked\.$/);
    const none = { ...stats, outcomes_series: stats.outcomes_series.map((p) => ({ ...p, would_flag: 0 })) };
    expect(heroDescription(none, false)).not.toMatch(/Would flag/);
    expect(heroDescription(none, true)).toMatch(/over the replayed traffic$/);
  });
});
