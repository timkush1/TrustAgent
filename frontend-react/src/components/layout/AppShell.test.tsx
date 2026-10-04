import { configure, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useStreamStore } from '@/data/stream/streamStore';
import { audit } from '@/test/fixtures';
import { policyState } from '@/test/control/fixtures';
import { fakeDataSource } from '@/test/fakeDataSource';
import { renderApp } from '@/test/renderApp';
import { systemStatus } from '@/test/status';

beforeEach(() => {
  useStreamStore.getState().reset();
});
// The first render of a file loads the lazily split route modules; under a parallel run that can
// take longer than the 1 s default.
configure({ asyncUtilTimeout: 5000 });

describe('app shell and routes', () => {
  it('redirects / to Overview and shows the six navigation items', async () => {
    const { router } = renderApp('/');
    await waitFor(() => expect(router.state.location.pathname).toBe('/app/overview'));
    const nav = await screen.findByRole('navigation', { name: 'Main' });
    const labels = within(nav)
      .getAllByRole('link')
      .map((a) => a.getAttribute('aria-label'))
      .filter(Boolean);
    expect(labels).toEqual(['Overview', 'Traffic', 'Evaluators', 'Policies', 'Playground', 'Settings']);
    expect(within(nav).getByRole('link', { name: 'Overview' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Overview');
  });

  it('renders a Settings section under the Settings heading with no dead buttons', async () => {
    renderApp('/app/settings/connections');
    expect(await screen.findByRole('heading', { level: 1, name: 'Settings' })).toBeInTheDocument();
    expect(await screen.findByRole('navigation', { name: 'Settings sections' })).toBeInTheDocument();
    const main = screen.getByRole('main');
    // Every button does something: a disabled one says why (DESIGN.md "no dead buttons").
    for (const button of within(main).queryAllByRole('button')) {
      if (button.hasAttribute('disabled') || button.getAttribute('aria-disabled') === 'true') {
        expect(button.getAttribute('aria-describedby')).toBeTruthy();
      }
    }
  });

  it('shows the Health pill from /health', async () => {
    renderApp('/app/overview');
    const pill = await screen.findByRole('button', { name: /System health/ });
    await waitFor(() => expect(pill).toHaveTextContent(/issue|ready/));
  });

  it('says setup is in progress instead of counting issues while the setup wizard is open', async () => {
    const off = systemStatus({ decision_model: { ...systemStatus().decision_model, state: 'off', backend: 'off' } });
    const ds = fakeDataSource({ getStatus: vi.fn(async () => off) });
    const { router } = renderApp('/app/setup', ds);
    const pill = await screen.findByRole('button', { name: /System health/ });
    await waitFor(() => expect(pill).toHaveTextContent('Setup in progress'));
    // Elsewhere the same state is an issue.
    await router.navigate({ to: '/app/overview' });
    await waitFor(() => expect(pill).toHaveTextContent(/\d+ issues?/));
  });

  it('shows the demo badge and banner on the demo stack', async () => {
    const ds = fakeDataSource({
      getHealth: async () => ({ status: 'ok', version: '2.0.0', demo: true, audit_engine: true }),
    });
    const { container } = renderApp('/app/overview', ds);
    expect(await screen.findByText('Demo')).toBeInTheDocument();
    await waitFor(() => expect(container.querySelector('[data-banner="demo"]')).not.toBeNull());
    expect(screen.getByText('make demo-down && make up').tagName).toBe('CODE');
  });

  it('names the enforcing route in the breaker banner, reading the policy only while the breaker is open', async () => {
    const base = systemStatus();
    const policy = policyState();
    const route = policy.effective_routes[0];
    const getPolicies = vi.fn(async () => ({
      ...policy,
      effective_routes: [{ ...route, name: 'payments-faq', mode: 'fallback' as const, effective_mode: 'flag' as const, fail: 'closed' as const }],
    }));
    const ds = fakeDataSource({
      getStatus: vi.fn(async () => systemStatus({ decision_model: { ...base.decision_model, state: 'breaker_open' } })),
      getPolicies,
    });
    const { container } = renderApp('/app/traffic', ds);
    await waitFor(() => expect(container.querySelector('[data-banner="breaker_open"]')).not.toBeNull());
    const banner = container.querySelector('[data-banner="breaker_open"]') as HTMLElement;
    expect(banner).toHaveTextContent('Route payments-faq enforces decisions; until the fast check recovers it follows its fail mode (closed).');
    expect(getPolicies).toHaveBeenCalled();
  });

  it('pauses deep audits in the banner when their budget is used up, with the way to Budgets', async () => {
    const base = systemStatus();
    const ds = fakeDataSource({ getStatus: vi.fn(async () => systemStatus({ budgets: { ...base.budgets, llm_exhausted: true } })) });
    const { container } = renderApp('/app/traffic', ds);
    await waitFor(() => expect(container.querySelector('[data-banner="llm_exhausted"]')).not.toBeNull());
    const banner = within(container.querySelector('[data-banner="llm_exhausted"]') as HTMLElement);
    expect(banner.getByText(/Deep audits paused/)).toBeInTheDocument();
    expect(banner.getByRole('link', { name: 'Open Budgets' })).toHaveAttribute('href', '/app/settings/budgets');
  });

  it('links a no-credits banner to OpenRouter in a new tab', async () => {
    const base = systemStatus();
    const ds = fakeDataSource({ getStatus: vi.fn(async () => systemStatus({ decision_model: { ...base.decision_model, state: 'no_credits' } })) });
    const { container } = renderApp('/app/traffic', ds);
    await waitFor(() => expect(container.querySelector('[data-banner="no_credits"]')).not.toBeNull());
    const link = within(container.querySelector('[data-banner="no_credits"]') as HTMLElement).getByRole('link', { name: /Add credits on OpenRouter/ });
    expect(link).toHaveAttribute('href', 'https://openrouter.ai/settings/credits');
    expect(link).toHaveAttribute('target', '_blank');
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');
  });

  it('marks simulated checks outside the demo stack, next to the read-only badge (D89)', async () => {
    const base = systemStatus();
    const ds = fakeDataSource({
      getStatus: vi.fn(async () => systemStatus({ demo: false, readonly: true, decision_model: { ...base.decision_model, backend: 'mock', state: 'mock' } })),
    });
    const { container } = renderApp('/app/traffic', ds);
    await waitFor(() => expect(container.querySelector('[data-badge="simulated-checks"]')).not.toBeNull());
    const header = within(screen.getByRole('banner'));
    expect(header.getByText('Simulated checks')).toBeInTheDocument();
    expect(header.getByText('Read-only')).toBeInTheDocument();
  });

  it('leaves the simulated badge to the demo badge on the demo stack, and off a real backend', async () => {
    const base = systemStatus();
    const demo = fakeDataSource({
      getHealth: async () => ({ status: 'ok', version: '2.0.0', demo: true, audit_engine: true }),
      getStatus: vi.fn(async () => systemStatus({ demo: true, decision_model: { ...base.decision_model, backend: 'mock', state: 'mock' } })),
    });
    const { container, unmount } = renderApp('/app/traffic', demo);
    expect(await screen.findByText('Demo')).toBeInTheDocument();
    expect(container.querySelector('[data-badge="simulated-checks"]')).toBeNull();
    unmount();
    const real = fakeDataSource({ getStatus: vi.fn(async () => systemStatus({ demo: false, decision_model: { ...base.decision_model, backend: 'openrouter-decisions', state: 'ready' } })) });
    const second = renderApp('/app/traffic', real);
    await screen.findByRole('button', { name: /System health/ });
    expect(second.container.querySelector('[data-badge="simulated-checks"]')).toBeNull();
  });

  it('does not read the policy for the banner while the breaker is closed', async () => {
    const getPolicies = vi.fn(async () => policyState());
    const ds = fakeDataSource({ getStatus: vi.fn(async () => systemStatus()), getPolicies });
    renderApp('/app/traffic', ds);
    await screen.findByRole('button', { name: /System health/ });
    expect(getPolicies).not.toHaveBeenCalled();
  });

  it('redirects old trace links to Traffic', async () => {
    const { router } = renderApp('/app/traces/t-9');
    await waitFor(() => expect(router.state.location.pathname).toBe('/app/traffic/t-9'));
  });

  it('resolves audit links to their trace', async () => {
    const ds = fakeDataSource({ getAudit: async (id) => audit({ audit_id: id, trace_id: 'trace-of-a1' }) });
    const { router } = renderApp('/app/audits/a-1', ds);
    await waitFor(() => expect(router.state.location.pathname).toBe('/app/traffic/trace-of-a1'));
  });

  it('shows the server message with Retry when an audit link cannot be resolved', async () => {
    renderApp('/app/audits/missing');
    expect(await screen.findByRole('alert')).toHaveTextContent('Not served in tests.');
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument();
  });

  it('opens Settings on Connections and rejects unknown sections', async () => {
    const { router } = renderApp('/app/settings');
    await waitFor(() => expect(router.state.location.pathname).toBe('/app/settings/connections'));
    expect(await screen.findByRole('link', { name: 'Connections' })).toHaveAttribute('aria-current', 'page');
    await router.navigate({ to: '/app/settings/$section', params: { section: 'nope' } });
    expect(await screen.findByText('There is no page at this address')).toBeInTheDocument();
  });

  it('drops invalid Traffic search params instead of failing', async () => {
    const { router } = renderApp('/app/traffic?live=1&outcome=bogus&route=support-bot');
    expect(await screen.findByText('No requests match these filters')).toBeInTheDocument();
    expect(router.state.location.search).toEqual({ live: 1, route: 'support-bot' });
  });
});
