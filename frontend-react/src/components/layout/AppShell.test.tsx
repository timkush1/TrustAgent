import { screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import { useStreamStore } from '@/data/stream/streamStore';
import { audit } from '@/test/fixtures';
import { fakeDataSource } from '@/test/fakeDataSource';
import { renderApp } from '@/test/renderApp';

beforeEach(() => {
  useStreamStore.getState().reset();
});

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

  it('renders an honest placeholder that names the phase and only links to real pages', async () => {
    renderApp('/app/policies');
    expect(await screen.findByText(/Arrives in P3/)).toBeInTheDocument();
    expect(screen.getByRole('heading', { level: 2 })).toHaveTextContent('What does each route do with a risky answer?');
    const main = screen.getByRole('main');
    expect(within(main).queryAllByRole('button').filter((b) => !b.hasAttribute('aria-haspopup') && !b.getAttribute('aria-expanded'))).toHaveLength(0);
  });

  it('shows the Health pill from /health', async () => {
    renderApp('/app/overview');
    const pill = await screen.findByRole('button', { name: /System health/ });
    await waitFor(() => expect(pill).toHaveTextContent(/issue|ready/));
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
    await screen.findByText(/Arrives in S0/);
    expect(router.state.location.search).toEqual({ live: 1, route: 'support-bot' });
  });
});
