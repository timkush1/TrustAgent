import { QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRouter, RouterProvider } from '@tanstack/react-router';
import { configure, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { afterEach, describe, expect, it, vi } from 'vitest';
import connectNoCredits from '@contracts/fixtures/api/ConnectCompleteResponse.no-credits.json';
import { ServicesContext, type AppServices } from '@/app/services';
import { buildRuntimeConfig } from '@/config/runtime';
import { clearDashboardKey, readDashboardKey, storeDashboardKey } from '@/data/http';
import { LiveDataSource } from '@/data/live/liveDataSource';
import { copy } from '@/domain/presentation';
import { routeTree } from '@/routeTree.gen';
import { createFakeApi } from '@/test/apiHandlers';
import { axeViolations, formatViolations } from '@/test/axe';
import { createControlApi, errorReply, type ControlState } from '@/test/control/handlers';
import { adminStatus, renderPage } from '@/test/control/renderPage';
import { adminSession, CLAIMED_ADMIN_KEY, settingsView, setupStatus } from '@/test/control/securityHandlers';
import { API, installMswServer, server } from '@/test/msw';
import { createTestQueryClient } from '@/test/renderApp';
import { stopLive } from '@/test/renderLive';
import { warmRoutes } from '@/test/warmRoutes';
import type { ConnectCompleteResponse, SettingsView } from '@/types/api';
import { browserNavigation, callbackMissingText, CONNECT_PENDING_KEY, safeReturnPath, simulatedPatch } from './connectFlow';
import { takeSetupToken, tokenFromPaste } from './setupToken';

installMswServer();
warmRoutes('/app/setup', '/connect/callback');
afterEach(() => {
  stopLive();
  clearDashboardKey();
  sessionStorage.clear();
  vi.restoreAllMocks();
  window.history.replaceState(null, '', '/');
});
configure({ asyncUtilTimeout: 4000 });

/** A proxy with no signed-in browser yet. */
function signedOut(overrides: Partial<ControlState> = {}) {
  return createControlApi({ session: null, setupStatus: setupStatus({ setup_required: true }), ...overrides });
}

/** Settings with nothing bound yet: the wizard's starting point after step 1. */
function unbound(overrides: Partial<SettingsView> = {}): SettingsView {
  const s = settingsView(overrides);
  s.runtime.bindings = { decision_model: null, judge: null, upstream_default: null };
  return s;
}

/** The fast check as /api/status reports it on a fresh stack: no key, nothing ready. */
const fresh = () => ({ decision_model: { ...adminStatus().decision_model, state: 'off' as const, backend: 'off' as const, credential: { source: 'none' as const, name: null, masked: null } } });

/** The demo stack's settings: mock fast check, nothing bound (the demo blanks every vendor key). */
function demoSettings(): SettingsView {
  const s = unbound();
  s.runtime.decision_model = { ...s.runtime.decision_model, backend: 'mock' };
  return s;
}

/** /api/status on the demo stack (`make demo`): demo mode, the mock fast check, no key. */
const demoStatus = () => ({
  demo: true,
  decision_model: { ...adminStatus().decision_model, state: 'mock' as const, backend: 'mock' as const, credential: { source: 'none' as const, name: null, masked: null } },
});

/** The dashboard on another origin than its proxy: the runtime config says crossOrigin. */
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
  return { control, router };
}

const panel = async (title: string) => (await screen.findByRole('heading', { level: 2, name: new RegExp(title) })).closest('.rounded-lg') as HTMLElement;

describe('setup helpers', () => {
  it('reads the percent-encoded token once and removes it from the address bar', () => {
    window.history.replaceState(null, '', '/app/setup#token=a%2Bb%20c');
    expect(takeSetupToken()).toBe('a+b c');
    expect(window.location.hash).toBe('');
    expect(takeSetupToken()).toBeNull();
  });

  it('takes the token from a pasted SETUP line, a setup URL or the token alone', () => {
    expect(tokenFromPaste('SETUP: open http://localhost:3000/app/setup#token=abc%2B1')).toBe('abc+1');
    expect(tokenFromPaste('  t0k3n  ')).toBe('t0k3n');
    expect(tokenFromPaste('two words')).toBeNull();
    expect(tokenFromPaste('')).toBeNull();
  });

  it('turns on simulated checks only where the environment does not decide (D39)', () => {
    expect(simulatedPatch([])).toEqual({ decision_model: { backend: 'mock' }, judge: { provider: 'mock' } });
    expect(simulatedPatch(['judge.provider'])).toEqual({ decision_model: { backend: 'mock' } });
    expect(simulatedPatch(['decision_model.backend', 'judge'])).toBeNull();
  });

  it('says where to open the dashboard after a lost sign-in, with no blank when the proxy has no public URL', () => {
    expect(callbackMissingText('http://localhost:3000')).toBe('No sign-in is waiting in this tab. Open the dashboard at http://localhost:3000 and connect again.');
    for (const unset of ['', '  ', null, undefined]) {
      expect(callbackMissingText(unset)).toBe('No sign-in is waiting in this tab. Open the dashboard at its usual address and connect again.');
    }
  });

  it('follows only dashboard paths back from Connect (D38)', () => {
    expect(safeReturnPath('/app/setup?step=connect')).toBe('/app/setup?step=connect');
    for (const bad of ['https://evil.example/app/', '//evil.example/app/', '/app/../x', '/app/%2f%2fevil', '/app\\evil', '/login', null]) {
      expect(safeReturnPath(bad)).toBeNull();
    }
  });
});

describe('Setup wizard, step 1: secure this dashboard', { timeout: 30_000 }, () => {
  it('claims the setup link, shows the admin key once, and needs “I saved it” before Next', async () => {
    const user = userEvent.setup();
    const control = signedOut({ settings: unbound() });
    window.history.replaceState(null, '', '/app/setup#token=setup-token-123');
    renderPage('/app/setup', { control, status: fresh() });
    expect(await screen.findByRole('heading', { level: 1, name: 'Setup' })).toBeInTheDocument();
    await user.click(await screen.findByRole('button', { name: 'Create the admin key' }));
    expect(await screen.findByText('Your admin key (shown once)')).toBeInTheDocument();
    expect(screen.getByText(CLAIMED_ADMIN_KEY)).toBeInTheDocument();
    expect(control.callsTo('/api/session', 'POST').map((c) => c.body)).toEqual([{ setup_token: 'setup-token-123' }]);
    expect(await screen.findByText(/Admin actions are available/)).toBeInTheDocument();
    const next = screen.getByRole('button', { name: 'Next' });
    expect(next).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByText('Tick “I saved it” first')).toBeInTheDocument();
    // The steps cannot be skipped either while the key waits to be saved.
    const steps = screen.getByRole('navigation', { name: 'Setup steps' });
    expect(within(steps).queryAllByRole('link')).toHaveLength(0);
    await user.click(screen.getByRole('checkbox', { name: copy.setup.step_secure.saved_confirm }));
    expect(within(steps).getAllByRole('link')).toHaveLength(3);
    await user.click(screen.getByRole('button', { name: 'Next' }));
    expect(await panel(copy.setup.step_connect.title)).toBeTruthy();
    expect(screen.getByRole('link', { name: /Secure this dashboard.*Done/ })).toBeInTheDocument();
  });

  it('keeps the admin key on screen while the new admin session reads the settings', async () => {
    const user = userEvent.setup();
    let release: () => void = () => {};
    const held = new Promise<void>((resolve) => (release = resolve));
    const control = signedOut({ settings: unbound() });
    window.history.replaceState(null, '', '/app/setup#token=setup-token-123');
    renderPage('/app/setup', { control, status: fresh() });
    server.use(
      http.get(`${API}/api/settings`, async () => {
        await held;
        return HttpResponse.json(unbound());
      }),
    );
    await user.click(await screen.findByRole('button', { name: 'Create the admin key' }));
    expect(await screen.findByText(/Admin actions are available/)).toBeInTheDocument();
    // The settings are still on their way: the key shown once and its "I saved it" stay put.
    expect(screen.getByText(CLAIMED_ADMIN_KEY)).toBeInTheDocument();
    expect(screen.getByRole('checkbox', { name: copy.setup.step_secure.saved_confirm })).toBeInTheDocument();
    release();
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
    const control = signedOut({ setupStatus: setupStatus({ setup_required: false }), signIn: errorReply(410, 'setup_token_used', 'This setup token was already used.') });
    window.history.replaceState(null, '', '/app/setup#token=used');
    renderPage('/app/setup', { control });
    await user.click(await screen.findByRole('button', { name: 'Create the admin key' }));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('The setup link was refused: This setup token was already used.');
    expect(alert.textContent).toContain('make setup-url');
  });

  it('without a link, shows where to find one and claims a pasted SETUP line', async () => {
    const user = userEvent.setup();
    const control = signedOut();
    renderPage('/app/setup', { control });
    expect(await screen.findByText('No admin yet')).toBeInTheDocument();
    expect(screen.getByText(copy.setup.step_secure.no_token)).toBeInTheDocument();
    expect(screen.getByText('docker compose logs proxy | grep -m1 SETUP')).toBeInTheDocument();
    expect(screen.getByText('docker compose exec -T proxy /proxy -new-setup-token')).toBeInTheDocument();
    await user.type(screen.getByLabelText(copy.setup.step_secure.paste_token), 'SETUP: open http://localhost:3000/app/setup#token=pasted-123');
    await user.click(screen.getByRole('button', { name: 'Create the admin key' }));
    expect(await screen.findByText('Your admin key (shown once)')).toBeInTheDocument();
    expect(control.callsTo('/api/session', 'POST').map((c) => c.body)).toEqual([{ setup_token: 'pasted-123' }]);
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
    expect(screen.getByText(/Admin actions are available/)).toBeInTheDocument();
    expect(control.callsTo('/api/session', 'POST').map((c) => c.body)).toEqual([{ key: 'ta_admin-key' }]);
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument());
    expect(control.callsTo('/api/session', 'DELETE')).toHaveLength(1);
  });

  it('is skipped when an admin session exists', async () => {
    renderPage('/app/setup', { control: createControlApi({ settings: unbound() }), status: fresh() });
    expect(await panel(copy.setup.step_connect.title)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Sign in' })).not.toBeInTheDocument();
  });

  it('has no axe violations', async () => {
    window.history.replaceState(null, '', '/app/setup#token=t');
    const { container } = renderPage('/app/setup', { control: signedOut() });
    await screen.findByRole('button', { name: 'Create the admin key' });
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});

describe('Setup wizard on another origin (D71)', { timeout: 30_000 }, () => {
  it('checks a key with GET /api/session, keeps it for this tab and sends it as X-TrustAgent-Key', async () => {
    const user = userEvent.setup();
    const control = signedOut({ setupStatus: setupStatus({ setup_required: false }), keySessions: { 'ta_admin-key': adminSession({ actor: 'admin', sliding: false }) } });
    renderCrossOrigin('/app/setup', control);
    await user.type(await screen.findByLabelText('Key'), 'ta_admin-key');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByText(/Signed in for this tab as/)).toBeInTheDocument();
    expect(readDashboardKey()).toBe('ta_admin-key');
    expect(localStorage.getItem('ta.dashboard.key')).toBeNull();
    expect(control.callsTo('/api/session', 'POST')).toHaveLength(0);
    expect(control.callsTo('/api/session', 'GET').at(-1)?.headers.get('x-trustagent-key')).toBe('ta_admin-key');
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
    renderCrossOrigin('/app/setup', signedOut({ keepsCookie: false }));
    await user.click(await screen.findByRole('button', { name: 'Create the admin key' }));
    expect(await screen.findByText('Your admin key (shown once)')).toBeInTheDocument();
    expect(await screen.findByText(/Signed in for this tab as/)).toBeInTheDocument();
    expect(readDashboardKey()).toBe(CLAIMED_ADMIN_KEY);
    expect(screen.queryByText('This browser did not keep the session')).not.toBeInTheDocument();
  });

  it('forgets this tab’s key after Sign out everywhere, as the confirmation says', async () => {
    const user = userEvent.setup();
    storeDashboardKey('ta_admin-key');
    const control = signedOut({ keySessions: { 'ta_admin-key': adminSession({ actor: 'admin', sliding: false }) } });
    const { router } = renderCrossOrigin('/app/settings/access', control);
    await user.click(await screen.findByRole('button', { name: copy.settings.sign_out_everywhere }));
    const dialog = await screen.findByRole('alertdialog');
    await user.click(within(dialog).getByRole('button', { name: copy.settings.sign_out_everywhere }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/app/setup'));
    const [call] = control.callsTo('/api/session', 'DELETE');
    expect(call.query.get('all')).toBe('true');
    expect(call.headers.get('x-trustagent-key')).toBe('ta_admin-key');
    expect(readDashboardKey()).toBeNull();
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeInTheDocument();
  });
});

describe('Setup wizard, step 2: connect OpenRouter', { timeout: 30_000 }, () => {
  it('says what is needed and what is sent where, with Connect as the primary way (D87, D88)', async () => {
    renderPage('/app/setup?step=connect', { control: createControlApi({ settings: unbound() }), status: fresh() });
    const step = await panel(copy.setup.step_connect.title);
    expect(await within(step).findByText(copy.setup.step_connect.prerequisite, { exact: false })).toBeInTheDocument();
    expect(within(step).getByRole('link', { name: copy.setup.step_connect.prerequisite_link })).toHaveAttribute('href', 'https://openrouter.ai/settings/credits');
    expect(within(step).getByText(copy.setup.step_connect.privacy)).toBeInTheDocument();
    expect(within(step).getByRole('button', { name: copy.setup.step_connect.primary })).toHaveClass(/bg-fg/);
    expect(within(step).getByRole('button', { name: copy.setup.step_connect.paste_key })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Next' })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByText(copy.setup.step_try.disabled_reason)).toBeInTheDocument();
  });

  it('starts Connect OpenRouter and keeps the nonce for this tab', async () => {
    const user = userEvent.setup();
    const assign = vi.spyOn(browserNavigation, 'assign').mockImplementation(() => {});
    const { control } = renderPage('/app/setup?step=connect', { control: createControlApi({ settings: unbound() }), status: fresh() });
    await user.click(await screen.findByRole('button', { name: copy.setup.step_connect.primary }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith(expect.stringContaining('code_challenge_method=S256')));
    expect(JSON.parse(sessionStorage.getItem(CONNECT_PENDING_KEY)!)).toEqual({ nonce: 'q2Xw9fLr0aZk7TcVb3NmYg' });
    expect(control.callsTo('/api/connect/openrouter/start')[0].body).toEqual({ return_to: '/app/setup?step=connect' });
  });

  it('says where to open the dashboard when Connect refuses its address, without the generic conflict hint (D38)', async () => {
    const user = userEvent.setup();
    const assign = vi.spyOn(browserNavigation, 'assign').mockImplementation(() => {});
    const message = 'This dashboard address is not one the gateway allows. Open the dashboard at http://localhost:3000 and connect again.';
    renderPage('/app/setup?step=connect', {
      control: createControlApi({
        settings: unbound(),
        connectStart: errorReply(409, 'conflict', message, undefined, { public_url: 'http://localhost:3000', reason: 'callback_origin' }),
      }),
      status: fresh(),
    });
    await user.click(await screen.findByRole('button', { name: copy.setup.step_connect.primary }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(message);
    expect(alert).not.toHaveTextContent('Something else changed this first');
    expect(assign).not.toHaveBeenCalled();
    expect(sessionStorage.getItem(CONNECT_PENDING_KEY)).toBeNull();
  });

  it('pastes a key in one call that verifies and binds both checks, then opens step 3', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ settings: unbound() });
    renderPage('/app/setup?step=connect', { control, status: fresh() });
    await user.click(await screen.findByRole('button', { name: copy.setup.step_connect.paste_key }));
    await user.type(screen.getByLabelText('OpenRouter key'), 'sk-or-v1-e2e-ok-2222');
    await user.click(screen.getByRole('button', { name: 'Save and check' }));
    // Each figure of the test call carries its own label: the time is measured, the cost the vendor's.
    const ready = (await screen.findByText('Ready: the test call took 231 ms')).closest('p')!;
    expect(ready).toHaveTextContent(/^Ready: the test call took 231 msMeasuredand cost \$0\.00001Vendor$/);
    expect(within(ready).getByRole('button', { name: 'Measured: how “Test call time” was produced' })).toBeInTheDocument();
    expect(within(ready).getByRole('button', { name: 'Vendor: how “Test call cost” was produced' })).toBeInTheDocument();
    expect(screen.getByText(`Now used for: ${copy.tiers.fast_check} and ${copy.tiers.deep_audit} judge.`)).toBeInTheDocument();
    expect(control.callsTo('/api/credentials/openrouter', 'PUT')).toHaveLength(1);
    expect(control.callsTo('/api/credentials/openrouter', 'PUT')[0].body).toEqual({
      provider: 'openrouter',
      secret: 'sk-or-v1-e2e-ok-2222',
      verify: true,
      bind: ['decision_model', 'judge'],
    });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Next' })).toHaveAttribute('aria-disabled', 'false'));
  });

  it('says a judge set in .env keeps running deep audits, with the way to Settings → Models', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ settings: unbound({ env_managed: ['judge.provider', 'bindings.judge'] }) });
    renderPage('/app/setup?step=connect', { control, status: fresh() });
    await user.click(await screen.findByRole('button', { name: copy.setup.step_connect.paste_key }));
    await user.type(screen.getByLabelText('OpenRouter key'), 'sk-or-v1-e2e-ok-2222');
    await user.click(screen.getByRole('button', { name: 'Save and check' }));
    const note = (await screen.findByText(copy.setup.step_connect.judge_env_managed, { exact: false })).closest('li')!;
    expect(within(note).getByRole('link', { name: 'See the judge in Settings → Models' })).toHaveAttribute('href', '/app/settings/models');
    expect(screen.getByText(`Now used for: ${copy.tiers.fast_check}.`)).toBeInTheDocument();
  });

  it('once a key is connected, leaves Next as the only primary action and offers no simulated checks', async () => {
    renderPage('/app/setup?step=connect', { control: createControlApi({ settings: settingsView() }) });
    const step = await panel(copy.setup.step_connect.title);
    expect(await within(step).findByText(/A key is connected, so answers are checked for real\./)).toBeInTheDocument();
    expect(within(step).getByText('To use a different key:')).toBeInTheDocument();
    expect(within(step).getByRole('button', { name: copy.setup.step_connect.primary })).not.toHaveClass(/bg-fg/);
    expect(within(step).getByRole('button', { name: copy.setup.step_connect.paste_key })).not.toHaveClass(/bg-fg/);
    // Simulated checks would replace the real ones: not offered on this step once connected.
    expect(within(step).queryByRole('button', { name: copy.setup.step_connect.simulated })).toBeNull();
    expect(within(step).queryByText('No key yet?')).toBeNull();
    expect(screen.getByRole('button', { name: 'Next' })).toHaveAttribute('aria-disabled', 'false');
  });

  it('shows no credits with nothing bound, then binds after “check again”, and says simulated checks are off (D75, D76)', async () => {
    const user = userEvent.setup();
    const settings = unbound();
    settings.runtime.decision_model.backend = 'mock';
    settings.runtime.judge.provider = 'mock';
    const control = createControlApi({ settings, keyLimitUsd: null });
    renderPage('/app/setup?step=connect', { control, status: fresh() });
    await user.click(await screen.findByRole('button', { name: copy.setup.step_connect.paste_key }));
    await user.type(screen.getByLabelText('OpenRouter key'), 'sk-or-v1-e2e-nocredits-3333');
    await user.click(screen.getByRole('button', { name: 'Save and check' }));
    expect(await screen.findByText(copy.setup.step_connect.no_credits)).toBeInTheDocument();
    expect(screen.queryByText(/Now used for/)).not.toBeInTheDocument();
    control.state.verifyStatus = { openrouter: 'ok' };
    await user.click(screen.getByRole('button', { name: copy.setup.step_connect.check_again }));
    expect(await screen.findByText(`${copy.setup.step_connect.simulated_off}.`)).toBeInTheDocument();
    expect(screen.getByText(copy.setup.step_connect.no_limit, { exact: false })).toBeInTheDocument();
    expect(control.callsTo('/api/credentials/openrouter/verify', 'POST')[0].body).toEqual({ bind: ['decision_model', 'judge'] });
  });

  it('turns on simulated checks for the paths the environment leaves free (D39)', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ settings: unbound({ env_managed: ['judge.provider', 'bindings.judge'] }) });
    renderPage('/app/setup?step=connect', { control, status: fresh() });
    await user.click(await screen.findByRole('button', { name: copy.setup.step_connect.simulated }));
    expect(await screen.findByText(copy.setup.step_connect.simulated_on)).toBeInTheDocument();
    const call = control.callsTo('/api/settings', 'PATCH')[0];
    expect(call.body).toEqual({ decision_model: { backend: 'mock' } });
    expect(call.headers.get('if-match')).toBe('"s7"');
    await waitFor(() => expect(screen.getByRole('button', { name: 'Next' })).toHaveAttribute('aria-disabled', 'false'));
  });

  it('reads the settings again after a 412, so the next try sends the new version', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ settings: unbound(), settingsPatch: errorReply(412, 'stale_version', 'The settings changed since you loaded them.') });
    renderPage('/app/setup?step=connect', { control, status: fresh() });
    const button = await screen.findByRole('button', { name: copy.setup.step_connect.simulated });
    const gets = control.callsTo('/api/settings', 'GET').length;
    await user.click(button);
    expect(await screen.findByText(copy.settings.stale_version)).toBeInTheDocument();
    await waitFor(() => expect(control.callsTo('/api/settings', 'GET').length).toBeGreaterThan(gets));
  });

  it('says the fast check already runs on a key from .env, and keeps Connect and Paste off', async () => {
    const status = { decision_model: { ...adminStatus().decision_model, credential: { source: 'env' as const, name: 'openrouter', masked: 'sk-…9c1e' } } };
    renderPage('/app/setup?step=connect', { control: createControlApi({ settings: unbound() }), status });
    expect(await screen.findByText('The fast check already uses the OpenRouter key from .env.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: copy.setup.step_connect.primary })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByText(copy.disabled_reasons.env_managed)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Next' })).toHaveAttribute('aria-disabled', 'false');
  });

  it('on the demo stack, says there is nothing to connect and opens step 3: no disabled ways in, no dead end', async () => {
    renderPage('/app/setup?step=connect', { control: createControlApi({ settings: demoSettings() }), status: demoStatus() });
    const step = await panel(copy.setup.step_connect.title);
    expect(await within(step).findByRole('status')).toHaveTextContent(copy.setup.step_connect.demo_ready);
    expect(within(step).getByText(copy.setup.step_connect.demo_note)).toBeInTheDocument();
    // Nothing that cannot work here: no Connect, no paste, no "try it without a key".
    for (const way of [copy.setup.step_connect.primary, copy.setup.step_connect.paste_key, copy.setup.step_connect.simulated]) {
      expect(within(step).queryByRole('button', { name: way })).toBeNull();
    }
    expect(screen.queryByText(copy.setup.step_try.disabled_reason)).toBeNull();
    expect(screen.getByRole('link', { name: /Connect OpenRouter.*Done/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Next' })).toHaveAttribute('aria-disabled', 'false');
    // The demo banner says how to use your own keys; the page does not repeat it.
    expect(screen.queryByText(/This is the demo stack/)).toBeNull();
  });

  it('explains disabled connecting on a read-only instance', async () => {
    renderPage('/app/setup?step=connect', { control: createControlApi({ settings: unbound() }), status: { ...fresh(), readonly: true } });
    expect((await screen.findAllByText(copy.disabled_reasons.readonly)).length).toBeGreaterThan(0);
    expect(screen.getByRole('button', { name: copy.setup.step_connect.primary })).toHaveAttribute('aria-disabled', 'true');
  });

  it('labels the answers $5 of credit covers as an estimate, with its basis', async () => {
    renderPage('/app/setup?step=connect', { control: createControlApi({ settings: unbound() }), status: fresh() });
    const step = await panel(copy.setup.step_connect.title);
    const estimate = await within(step).findByRole('button', { name: 'Estimate: how “Answers $5 of credit covers” was produced' });
    expect(estimate).toHaveAttribute('data-value-label', 'estimate');
    expect(within(step).queryByText(/estimate\)/)).toBeNull();
  });

  it('says why keys cannot be saved without a credentials store, and still offers simulated checks (D32)', async () => {
    renderPage('/app/setup?step=connect', {
      control: createControlApi({ settings: unbound() }),
      status: { ...fresh(), credentials: { store: 'unavailable', undecryptable: [] } },
    });
    expect(await screen.findByRole('button', { name: copy.setup.step_connect.primary })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByRole('button', { name: copy.setup.step_connect.paste_key })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getAllByText("Keys can't be saved yet").length).toBeGreaterThan(0);
    // The fix is on the page, not only the refusal.
    expect(screen.getByText(copy.settings.store_unavailable)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: copy.setup.step_connect.simulated })).toHaveAttribute('aria-disabled', 'false');
    stopLive();
    document.body.innerHTML = '';

    renderPage('/app/setup?step=connect', {
      control: createControlApi({ settings: unbound() }),
      status: { ...fresh(), credentials: { store: 'no_database', undecryptable: [] } },
    });
    expect(await screen.findByRole('button', { name: copy.setup.step_connect.simulated })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getAllByText(copy.disabled_reasons.needs_database).length).toBeGreaterThan(0);
    expect(screen.getByText(copy.settings.no_database)).toBeInTheDocument();
  });

  it('has no axe violations', async () => {
    const { container } = renderPage('/app/setup?step=connect', { control: createControlApi({ settings: unbound() }), status: fresh() });
    await panel(copy.setup.step_connect.title);
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});

describe('Setup wizard, step 3: see it work', { timeout: 30_000 }, () => {
  it('stays closed, with the reason and the way back, until the fast check can run', async () => {
    renderPage('/app/setup?step=try', { control: createControlApi({ settings: unbound() }), status: fresh() });
    const step = await panel(copy.setup.step_see_it_work.title);
    expect(await within(step).findByText(copy.setup.step_try.disabled_reason)).toBeInTheDocument();
    expect(within(step).getByRole('link', { name: /Back to Connect OpenRouter/ })).toHaveAttribute('href', '/app/setup?step=connect');
    expect(within(step).queryByRole('button', { name: 'Check this answer' })).not.toBeInTheDocument();
  });

  it('runs the refund example flagged in audit mode, links the trace, and points at the app key', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/setup?step=try');
    const step = await panel(copy.setup.step_see_it_work.title);
    expect(await within(step).findByText('You can get a full refund within 60 days of purchase.')).toBeInTheDocument();
    await user.click(within(step).getByRole('button', { name: 'Check this answer' }));
    expect(await within(step).findByRole('list', { name: 'What happened to the request' })).toBeInTheDocument();
    expect(within(step).getByRole('link', { name: /Open the trace/ })).toHaveAttribute('href', expect.stringMatching(/^\/app\/traffic\//));
    const body = control.callsTo('/api/playground/run', 'POST')[0].body as Record<string, unknown>;
    expect(body).toMatchObject({ mode: 'audit', mode_override: 'flag', response: 'You can get a full refund within 60 days of purchase.' });
    // An app key exists already: it is named, never minted again.
    expect(within(step).getByText(/“ci-ingest”/)).toBeInTheDocument();
    expect(within(step).queryByRole('button', { name: 'Create an app key' })).not.toBeInTheDocument();
  });

  it('shows the document the answer is checked against', async () => {
    renderPage('/app/setup?step=try');
    const step = await panel(copy.setup.step_see_it_work.title);
    const against = (await within(step).findByText('Checked against')).nextElementSibling as HTMLElement;
    expect(against).toHaveTextContent('refund-policy.md: “Annual plans can be refunded within 30 days of purchase. Monthly plans are non-refundable.”');
  });

  it('on the demo stack, runs the check like the Playground does and points at more examples', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/setup?step=try', { control: createControlApi({ settings: demoSettings() }), status: demoStatus() });
    const step = await panel(copy.setup.step_see_it_work.title);
    const check = await within(step).findByRole('button', { name: 'Check this answer' });
    // Gated like the Playground's run (an admin on a writable stack), not like a settings write.
    expect(check).toHaveAttribute('aria-disabled', 'false');
    await user.click(check);
    expect(await within(step).findByRole('list', { name: 'What happened to the request' })).toBeInTheDocument();
    // The demo's mock checks play the unedited example's scripted answers.
    expect(control.callsTo('/api/playground/run', 'POST')[0].body).toMatchObject({ mode_override: 'flag', scenario: 'refund-contradiction-flag' });
    expect(within(step).getByRole('link', { name: /Open the Playground/ })).toHaveAttribute('href', '/app/playground');
  });

  it('offers no app key when the key list cannot be read, only another try', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ apiKeys: errorReply(503, 'persistence_disabled', 'API keys need the database.') });
    renderPage('/app/setup?step=try', { control });
    const step = await panel(copy.setup.step_see_it_work.title);
    expect(await within(step).findByText(/The API keys could not be read/)).toBeInTheDocument();
    expect(within(step).queryByRole('button', { name: 'Create an app key' })).not.toBeInTheDocument();
    const gets = control.callsTo('/api/keys', 'GET').length;
    await user.click(within(step).getByRole('button', { name: 'Try again' }));
    await waitFor(() => expect(control.callsTo('/api/keys', 'GET').length).toBeGreaterThan(gets));
    expect(control.callsTo('/api/keys', 'POST')).toHaveLength(0);
  });

  it('mints an app key only when none is active, and shows it once', async () => {
    const user = userEvent.setup();
    const keys = { items: [{ id: 'k_admin', label: 'setup-admin', scopes: ['ingest', 'read', 'content', 'admin'] as const, prefix: 'ta_adm', created_at: '2026-09-28T09:55:40Z', last_used_at: null, revoked_at: null }] };
    const control = createControlApi({ apiKeys: structuredClone(keys) as unknown as ControlState['apiKeys'] });
    renderPage('/app/setup?step=try', { control });
    const step = await panel(copy.setup.step_see_it_work.title);
    await user.click(await within(step).findByRole('button', { name: 'Create an app key' }));
    expect(await within(step).findByText('my-app (shown once)')).toBeInTheDocument();
    expect(within(step).getByText(copy.setup.step_try.ingest_key)).toBeInTheDocument();
    expect(control.callsTo('/api/keys', 'POST')[0].body).toEqual({ label: 'my-app', scopes: ['ingest'] });
  });

  it('has no axe violations', async () => {
    const { container } = renderPage('/app/setup?step=try');
    await panel(copy.setup.step_see_it_work.title);
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});

describe('Setup wizard on the demo stack (make demo opens it with the one-time link)', { timeout: 30_000 }, () => {
  it('goes from the setup link to a checked answer and on to the dashboard, never stopping at step 2', async () => {
    const user = userEvent.setup();
    const control = signedOut({ settings: demoSettings() });
    window.history.replaceState(null, '', '/app/setup#token=setup-token-123');
    renderPage('/app/setup', { control, status: demoStatus() });
    expect(await screen.findByText(/there is nothing to connect/)).toBeInTheDocument();
    await user.click(await screen.findByRole('button', { name: 'Create the admin key' }));
    // Step 1 promises only what the demo allows: no keys to connect or manage there.
    expect(await screen.findByText('Admin actions are available: edit policies, run the Playground and label answers.')).toBeInTheDocument();
    await user.click(screen.getByRole('checkbox', { name: copy.setup.step_secure.saved_confirm }));
    await user.click(screen.getByRole('button', { name: 'Next' }));

    const connect = await panel(copy.setup.step_connect.title);
    expect(await within(connect).findByText(copy.setup.step_connect.demo_ready)).toBeInTheDocument();
    const next = screen.getByRole('button', { name: 'Next' });
    expect(next).toHaveAttribute('aria-disabled', 'false');
    await user.click(next);

    const tryIt = await panel(copy.setup.step_see_it_work.title);
    const check = await within(tryIt).findByRole('button', { name: 'Check this answer' });
    expect(check).toHaveAttribute('aria-disabled', 'false');
    await user.click(check);
    expect(await within(tryIt).findByRole('list', { name: 'What happened to the request' })).toBeInTheDocument();
    expect(within(tryIt).getByRole('link', { name: /Open the trace/ })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Secure this dashboard.*Done/ })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Connect OpenRouter.*Done/ })).toBeInTheDocument();

    await user.click(screen.getByRole('link', { name: /Go to Overview/ }));
    expect(await screen.findByRole('heading', { level: 1, name: 'Overview' })).toBeInTheDocument();
  });

  it('opens a signed-in admin straight at step 3', async () => {
    renderPage('/app/setup', { control: createControlApi({ settings: demoSettings() }), status: demoStatus() });
    expect(await panel(copy.setup.step_see_it_work.title)).toBeTruthy();
    expect(await screen.findByRole('button', { name: 'Check this answer' })).toHaveAttribute('aria-disabled', 'false');
  });

  it('has no axe violations on step 2', async () => {
    const { container } = renderPage('/app/setup?step=connect', { control: createControlApi({ settings: demoSettings() }), status: demoStatus() });
    await screen.findByText(copy.setup.step_connect.demo_ready);
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});

describe('/connect/callback (D38)', { timeout: 30_000 }, () => {
  it('finishes the sign-in once, takes the code out of the address bar, and goes to the server’s return_to', async () => {
    sessionStorage.setItem(CONNECT_PENDING_KEY, JSON.stringify({ nonce: 'q2Xw9fLr0aZk7TcVb3NmYg' }));
    window.history.replaceState(null, '', '/connect/callback?code=abc123');
    const { control, router } = renderPage('/connect/callback?code=abc123');
    await waitFor(() => expect(router.state.location.href).toBe('/app/setup?step=try'));
    expect(window.location.search).toBe('');
    expect(sessionStorage.getItem(CONNECT_PENDING_KEY)).toBeNull();
    expect(control.callsTo('/api/connect/openrouter/complete', 'POST').map((c) => c.body)).toEqual([{ code: 'abc123', nonce: 'q2Xw9fLr0aZk7TcVb3NmYg' }]);
  });

  it('hands a no-credits outcome to step 2, with “check again”', async () => {
    sessionStorage.setItem(CONNECT_PENDING_KEY, JSON.stringify({ nonce: 'q2Xw9fLr0aZk7TcVb3NmYg' }));
    const control = createControlApi({ settings: unbound(), connectComplete: structuredClone(connectNoCredits) as ConnectCompleteResponse });
    const { router } = renderPage('/connect/callback?code=nocredits', { control, status: fresh() });
    await waitFor(() => expect(router.state.location.href).toBe('/app/setup?step=connect'));
    expect(await screen.findByText(copy.setup.step_connect.no_credits)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: copy.setup.step_connect.check_again })).toBeInTheDocument();
  });

  it('shows the result in place when return_to is not a dashboard path', async () => {
    sessionStorage.setItem(CONNECT_PENDING_KEY, JSON.stringify({ nonce: 'q2Xw9fLr0aZk7TcVb3NmYg' }));
    const result = { ...(structuredClone(connectNoCredits) as ConnectCompleteResponse), return_to: 'https://evil.example/app/' };
    const { router } = renderPage('/connect/callback?code=x', { control: createControlApi({ connectComplete: result }) });
    expect(await screen.findByText(copy.setup.step_connect.no_credits)).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/connect/callback');
    expect(screen.getByRole('link', { name: /Continue setup/ })).toHaveAttribute('href', '/app/setup?step=connect');
  });

  it('explains a callback with no sign-in waiting in this tab, with the public URL', async () => {
    const { control } = renderPage('/connect/callback?code=abc', { control: createControlApi({ setupStatus: setupStatus({ public_url: 'http://localhost:3000' }) }) });
    expect(await screen.findByText(/No sign-in is waiting in this tab\. Open the dashboard at http:\/\/localhost:3000/)).toBeInTheDocument();
    expect(control.callsTo('/api/connect/openrouter/complete')).toHaveLength(0);
  });

  it('explains an expired or used sign-in (invalid_nonce)', async () => {
    sessionStorage.setItem(CONNECT_PENDING_KEY, JSON.stringify({ nonce: 'an-old-nonce' }));
    renderPage('/connect/callback?code=abc');
    expect(await screen.findByText(copy.setup.step_connect.callback_expired)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Connect again/ })).toHaveAttribute('href', '/app/setup?step=connect');
  });

  it('says OpenRouter did not accept the sign-in when the code exchange fails (502, exchange_failed)', async () => {
    sessionStorage.setItem(CONNECT_PENDING_KEY, JSON.stringify({ nonce: 'q2Xw9fLr0aZk7TcVb3NmYg' }));
    const message = 'POST /api/v1/auth/keys answered 403';
    renderPage('/connect/callback?code=abc', {
      control: createControlApi({ connectComplete: errorReply(502, 'upstream_error', message, undefined, { reason: 'exchange_failed', status: 403 }) }),
    });
    const alert = await screen.findByRole('alert');
    expect(within(alert).getByText(copy.setup.step_connect.exchange_failed)).toBeInTheDocument();
    expect(within(alert).getByText(message)).toBeInTheDocument();
    expect(within(alert).getByRole('link', { name: /Connect again/ })).toHaveAttribute('href', '/app/setup?step=connect');
  });

  it('has no axe violations', async () => {
    const { container } = renderPage('/connect/callback?code=abc');
    await screen.findByRole('alert');
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});
