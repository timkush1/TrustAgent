import { QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRouter, RouterProvider } from '@tanstack/react-router';
import { configure, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { ServicesContext, type AppServices } from '@/app/services';
import { buildRuntimeConfig } from '@/config/runtime';
import { clearDashboardKey, readDashboardKey } from '@/data/http';
import { LiveDataSource } from '@/data/live/liveDataSource';
import { routeTree } from '@/routeTree.gen';
import { axeViolations, formatViolations } from '@/test/axe';
import { createControlApi, errorReply, type ControlState } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { adminSession, CLAIMED_ADMIN_KEY, setupStatus } from '@/test/control/securityHandlers';
import { API, installMswServer, server } from '@/test/msw';
import { createTestQueryClient } from '@/test/renderApp';
import { stopLive } from '@/test/renderLive';
import { createFakeApi } from '@/test/apiHandlers';
import { takeSetupToken } from './setupToken';

installMswServer();
afterEach(() => {
  stopLive();
  clearDashboardKey();
  window.history.replaceState(null, '', '/');
});
configure({ asyncUtilTimeout: 4000 });

/** A proxy with no signed-in browser yet. */
function signedOut(overrides: Partial<ControlState> = {}) {
  return createControlApi({ session: null, setupStatus: setupStatus({ setup_required: true }), ...overrides });
}

/**
 * The dashboard on another origin than its proxy (DASHBOARD_API_BASE): the runtime config says
 * crossOrigin, and the data source sends keys, never cookies.
 */
function renderCrossOrigin(path: string, control = signedOut()) {
  server.use(...createFakeApi({ status: null }, { controlPlane: false }).handlers, ...control.handlers);
  const config = buildRuntimeConfig(
    { apiBase: API },
    { location: { href: 'http://localhost:5173/', origin: 'http://localhost:5173', protocol: 'http:' }, env: { BASE_URL: '/' } },
  );
  expect(config.crossOrigin).toBe(true);
  const dataSource = new LiveDataSource({ apiBase: API, crossOrigin: true, wsUrl: 'ws://proxy.test/ws' });
  const queryClient = createTestQueryClient();
  const services: AppServices = { config, dataSource, queryClient };
  const router = createRouter({ routeTree, context: services, history: createMemoryHistory({ initialEntries: [path] }), defaultPendingMs: 0 });
  render(
    <ServicesContext.Provider value={services}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </ServicesContext.Provider>,
  );
  return { control };
}

describe('takeSetupToken', () => {
  it('reads the percent-encoded token once and removes it from the address bar', () => {
    window.history.replaceState(null, '', '/app/setup#token=a%2Bb%20c');
    expect(takeSetupToken()).toBe('a+b c');
    expect(window.location.hash).toBe('');
    expect(window.location.pathname).toBe('/app/setup');
    expect(takeSetupToken()).toBeNull();
  });
});

describe('Setup page', { timeout: 20_000 }, () => {
  it('claims the setup link, shows the admin key once and signs the browser in', async () => {
    const user = userEvent.setup();
    const control = signedOut();
    window.history.replaceState(null, '', '/app/setup#token=setup-token-123');
    renderPage('/app/setup', { control });
    await user.click(await screen.findByRole('button', { name: 'Create the admin key' }));
    expect(await screen.findByText('Your admin key (shown once)')).toBeInTheDocument();
    expect(screen.getByText(CLAIMED_ADMIN_KEY)).toBeInTheDocument();
    expect(control.callsTo('/api/session', 'POST').map((c) => c.body)).toEqual([{ setup_token: 'setup-token-123' }]);
    expect(await screen.findByText(/Admin actions are available/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Open Policies/ })).toHaveAttribute('href', '/app/policies');
  });

  it('keeps the created key on screen when the browser does not keep the session cookie', async () => {
    const user = userEvent.setup();
    window.history.replaceState(null, '', '/app/setup#token=no-cookie');
    renderPage('/app/setup', { control: signedOut({ keepsCookie: false }) });
    await user.click(await screen.findByRole('button', { name: 'Create the admin key' }));
    expect(await screen.findByText('This browser did not keep the session')).toBeInTheDocument();
    expect(screen.getByText(CLAIMED_ADMIN_KEY)).toBeInTheDocument();
  });

  it('says what to do when the setup link was already used', async () => {
    const user = userEvent.setup();
    const control = signedOut({
      setupStatus: setupStatus({ setup_required: false }),
      signIn: errorReply(410, 'setup_token_used', 'This setup token was already used.'),
    });
    window.history.replaceState(null, '', '/app/setup#token=used');
    renderPage('/app/setup', { control });
    await user.click(await screen.findByRole('button', { name: 'Create the admin key' }));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('The setup link was refused: This setup token was already used.');
    expect(alert.textContent).toContain('make setup-url');
  });

  it('signs in with a key, and signs out', async () => {
    const user = userEvent.setup();
    const control = signedOut({ setupStatus: setupStatus({ setup_required: false }) });
    renderPage('/app/setup', { control });
    const signIn = await screen.findByRole('button', { name: 'Sign in' });
    expect(signIn).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByText('Enter a key first')).toBeInTheDocument();
    await user.type(screen.getByLabelText('Key'), 'ta_admin-key');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByText(/^Signed in as/)).toBeInTheDocument();
    expect(control.callsTo('/api/session', 'POST').map((c) => c.body)).toEqual([{ key: 'ta_admin-key' }]);
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument());
    expect(control.callsTo('/api/session', 'DELETE')).toHaveLength(1);
  });

  it('shows how to print a setup link while no admin exists', async () => {
    renderPage('/app/setup', { control: signedOut() });
    expect(await screen.findByText('No admin yet')).toBeInTheDocument();
    expect(screen.getByText('docker compose exec -T proxy /proxy -new-setup-token')).toBeInTheDocument();
  });

  it('has no axe violations', async () => {
    window.history.replaceState(null, '', '/app/setup#token=t');
    const { container } = renderPage('/app/setup', { control: signedOut() });
    await screen.findByRole('button', { name: 'Create the admin key' });
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});

describe('Setup page on another origin (P4-PLAN D71)', { timeout: 20_000 }, () => {
  it('checks a key with GET /api/session, keeps it for this tab and sends it as X-TrustAgent-Key', async () => {
    const user = userEvent.setup();
    const control = signedOut({ setupStatus: setupStatus({ setup_required: false }), keySessions: { 'ta_admin-key': adminSession({ actor: 'admin', sliding: false }) } });
    renderCrossOrigin('/app/setup', control);
    await user.type(await screen.findByLabelText('Key'), 'ta_admin-key');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByText(/Signed in for this tab as/)).toBeInTheDocument();
    expect(readDashboardKey()).toBe('ta_admin-key');
    expect(localStorage.getItem('ta.dashboard.key')).toBeNull();
    // No cookie session: nothing was posted, the key went in the header.
    expect(control.callsTo('/api/session', 'POST')).toHaveLength(0);
    expect(control.callsTo('/api/session', 'GET').at(-1)?.headers.get('x-trustagent-key')).toBe('ta_admin-key');
    expect(control.callsTo('/api/setup/status').at(-1)?.headers.get('x-trustagent-csrf')).toBeNull();

    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument());
    expect(readDashboardKey()).toBeNull();
    expect(control.callsTo('/api/session', 'DELETE')).toHaveLength(0);
  });

  it('refuses a key the proxy does not accept, and keeps nothing', async () => {
    const user = userEvent.setup();
    renderCrossOrigin('/app/setup', signedOut({ setupStatus: setupStatus({ setup_required: false }) }));
    await user.type(await screen.findByLabelText('Key'), 'ta_wrong');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect((await screen.findByRole('alert')).textContent).toContain('Sign-in failed');
    expect(readDashboardKey()).toBeNull();
  });

  it('keeps the admin key a setup claim created for this tab', async () => {
    const user = userEvent.setup();
    window.history.replaceState(null, '', '/app/setup#token=claim-cross-origin');
    const control = signedOut({ keepsCookie: false });
    renderCrossOrigin('/app/setup', control);
    await user.click(await screen.findByRole('button', { name: 'Create the admin key' }));
    expect(await screen.findByText('Your admin key (shown once)')).toBeInTheDocument();
    expect(await screen.findByText(/Signed in for this tab as/)).toBeInTheDocument();
    expect(readDashboardKey()).toBe(CLAIMED_ADMIN_KEY);
    expect(screen.queryByText('This browser did not keep the session')).not.toBeInTheDocument();
  });
});
