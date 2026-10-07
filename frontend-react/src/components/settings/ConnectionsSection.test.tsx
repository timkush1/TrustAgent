import { configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import connectOk from '@contracts/fixtures/api/ConnectCompleteResponse.ok.json';
import { browserNavigation, CONNECT_PENDING_KEY } from '@/components/setup/connectFlow';
import { copy } from '@/domain/presentation';
import { axeViolations, formatViolations } from '@/test/axe';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { credentialList, settingsView } from '@/test/control/securityHandlers';
import { installMswServer } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import { warmRoutes } from '@/test/warmRoutes';
import type { ConnectCompleteResponse } from '@/types/api';

installMswServer();
warmRoutes('/app/settings/connections', '/connect/callback');
afterEach(() => {
  stopLive();
  sessionStorage.clear();
  vi.restoreAllMocks();
});
configure({ asyncUtilTimeout: 4000 });

const PATH = '/app/settings/connections';
const card = async (name: string) => (await screen.findAllByRole('heading', { level: 2 })).find((h) => h.textContent?.startsWith(name))!.closest('[data-credential]') as HTMLElement;

describe('Settings → Connections', { timeout: 30_000 }, () => {
  it('shows each provider: connected, set in .env, set in the engine, not connected and unreadable', async () => {
    renderPage(PATH);
    const openrouter = await card('OpenRouter');
    expect(within(openrouter).getByText('Working')).toBeInTheDocument();
    expect(within(openrouter).getByText('sk-…3f2a')).toBeInTheDocument();
    expect(within(openrouter).getByText('https://openrouter.ai')).toBeInTheDocument();
    expect(within(openrouter).getByText(copy.tiers.fast_check)).toBeInTheDocument();
    expect(within(openrouter).getByText(`${copy.tiers.deep_audit} judge`)).toBeInTheDocument();
    expect(within(openrouter).getByRole('button', { name: 'Replace key' })).toBeInTheDocument();

    const typesafe = await card('TypeSafe');
    expect(within(typesafe).getByText(/Set in \.env \(TYPESAFE_API_KEY\)/)).toBeInTheDocument();
    expect(within(typesafe).queryByRole('button', { name: /Replace key|Remove/ })).not.toBeInTheDocument();
    expect(within(typesafe).getByRole('button', { name: 'Test the TypeSafe key' })).toHaveAttribute('aria-disabled', 'false');

    const anthropic = await card('Anthropic');
    expect(within(anthropic).getByText(/Set in the engine's \.env/)).toBeInTheDocument();
    expect(within(anthropic).getByRole('button', { name: 'Test the Anthropic key' })).toHaveAttribute('aria-disabled', 'true');
    expect(within(anthropic).getByText('Check it with make doctor')).toBeInTheDocument();

    const openai = await card('OpenAI');
    expect(within(openai).getByText('Not connected')).toBeInTheDocument();
    expect(within(openai).getByRole('button', { name: 'Paste a key' })).toBeInTheDocument();

    const other = await card('Other gateway');
    expect(within(other).getByText(copy.settings.credential_undecryptable)).toBeInTheDocument();
    expect(within(other).getByText('Paste the key again first')).toBeInTheDocument();

    const upstream = screen.getByRole('list', { name: 'Upstream keys' });
    expect(within(upstream).getByText('Upstream key (your model)')).toBeInTheDocument();
    expect(screen.getByText(copy.setup.step_connect.privacy)).toBeInTheDocument();
  });

  it('says why an OpenRouter key does not run the judge when .env sets the judge, with the way to Models', async () => {
    const settings = settingsView({ env_managed: ['bindings.judge', 'judge.provider'] });
    settings.runtime.bindings = { ...settings.runtime.bindings, judge: null };
    const credentials = credentialList();
    for (const c of credentials.items) if (c.name === 'openrouter') c.used_by = ['decision_model'];
    renderPage(PATH, { control: createControlApi({ settings, credentials }) });
    const openrouter = await card('OpenRouter');
    expect(within(openrouter).getByText(copy.tiers.fast_check)).toBeInTheDocument();
    const note = within(openrouter).getByText(new RegExp(`^Not used for the ${copy.tiers.deep_audit} judge\\.`));
    expect(note).toHaveTextContent(copy.setup.step_connect.judge_env_managed);
    expect(within(note).getByRole('link', { name: 'See the judge in Settings → Models' })).toHaveAttribute('href', '/app/settings/models');
  });

  it('adds no judge note to a key that runs the judge', async () => {
    renderPage(PATH, { control: createControlApi({ settings: settingsView({ env_managed: ['bindings.judge'] }) }) });
    const openrouter = await card('OpenRouter');
    expect(within(openrouter).getByText(`${copy.tiers.deep_audit} judge`)).toBeInTheDocument();
    expect(within(openrouter).queryByText(/^Not used for the/)).toBeNull();
  });

  it('pastes a key, checks it and binds it to the checks ticked (D75)', async () => {
    const user = userEvent.setup();
    const { control } = renderPage(PATH);
    await user.click(within(await card('OpenAI')).getByRole('button', { name: 'Paste a key' }));
    const dialog = await screen.findByRole('dialog', { name: 'OpenAI key' });
    const judge = within(dialog).getByRole('checkbox', { name: `${copy.tiers.deep_audit} judge` });
    // The judge already runs on OpenRouter: switching it is a deliberate choice.
    expect(judge).not.toBeChecked();
    await user.click(judge);
    await user.type(within(dialog).getByLabelText('API key'), 'sk-test-openai-0000ok12');
    await user.click(within(dialog).getByRole('button', { name: 'Save and check' }));
    expect((await within(dialog).findByText('Ready: the test call took 231 ms')).closest('p')).toHaveTextContent('and cost $0.00001');
    expect(within(dialog).getByText(`Now used for: ${copy.tiers.deep_audit} judge.`)).toBeInTheDocument();
    expect(control.callsTo('/api/credentials/openai', 'PUT')[0].body).toEqual({
      provider: 'openai',
      secret: 'sk-test-openai-0000ok12',
      verify: true,
      bind: ['judge'],
    });
    expect(document.body.textContent).not.toContain('sk-test-openai-0000ok12');
    await user.click(within(dialog).getByRole('button', { name: 'Done' }));
    const openai = await card('OpenAI');
    await waitFor(() => expect(within(openai).getByText('sk-…ok12')).toBeInTheDocument());
  });

  it('says a key has no credits, binds nothing, and binds after “check again”, leaving simulated mode (D75, D76, D87)', async () => {
    const user = userEvent.setup();
    const settings = settingsView();
    settings.runtime.decision_model.backend = 'mock';
    settings.runtime.judge.provider = 'mock';
    settings.runtime.bindings = { decision_model: null, judge: null, upstream_default: null };
    const control = createControlApi({ settings, keyLimitUsd: null });
    renderPage(PATH, { control });
    await user.click(within(await card('OpenRouter')).getByRole('button', { name: 'Replace key' }));
    const dialog = await screen.findByRole('dialog', { name: 'OpenRouter key' });
    expect(within(dialog).getByRole('checkbox', { name: copy.tiers.fast_check })).toBeChecked();
    await user.type(within(dialog).getByLabelText('API key'), 'sk-or-v1-e2e-nocredits-1111');
    await user.click(within(dialog).getByRole('button', { name: 'Save and check' }));
    expect(await within(dialog).findByText(copy.setup.step_connect.no_credits)).toBeInTheDocument();
    expect(within(dialog).queryByText(/Now used for/)).not.toBeInTheDocument();

    control.state.verifyStatus = { openrouter: 'ok' };
    await user.click(within(dialog).getByRole('button', { name: copy.setup.step_connect.check_again }));
    expect(await within(dialog).findByText(`${copy.setup.step_connect.simulated_off}.`)).toBeInTheDocument();
    expect(within(dialog).getByText(copy.setup.step_connect.no_limit, { exact: false })).toBeInTheDocument();
    expect(control.callsTo('/api/credentials/openrouter/verify', 'POST')[0].body).toEqual({ bind: ['decision_model', 'judge'] });
  });

  it('tests a saved key and shows a rejected one', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ verifyStatus: { openrouter: 'unauthorized' } });
    renderPage(PATH, { control });
    const openrouter = await card('OpenRouter');
    await user.click(within(openrouter).getByRole('button', { name: 'Test the OpenRouter key' }));
    expect(await within(openrouter).findByText(/Key rejected\. The provider refused this key/)).toBeInTheDocument();
    expect(control.callsTo('/api/credentials/openrouter/verify', 'POST')[0].body).toEqual({});
  });

  it('keeps “Simulated checks are now off” after the card’s check again binds and the settings refresh (D76)', async () => {
    const user = userEvent.setup();
    const settings = settingsView();
    settings.runtime.decision_model.backend = 'mock';
    settings.runtime.judge.provider = 'mock';
    settings.runtime.bindings = { decision_model: null, judge: null, upstream_default: null };
    const control = createControlApi({ settings, verifyStatus: { openrouter: 'no_credits' } });
    renderPage(PATH, { control });
    const openrouter = await card('OpenRouter');
    await user.click(within(openrouter).getByRole('button', { name: 'Test the OpenRouter key' }));
    expect(await within(openrouter).findByText(copy.setup.step_connect.no_credits)).toBeInTheDocument();
    control.state.verifyStatus = { openrouter: 'ok' };
    const gets = control.callsTo('/api/settings', 'GET').length;
    await user.click(within(openrouter).getByRole('button', { name: copy.setup.step_connect.check_again }));
    expect(await within(openrouter).findByText(`${copy.setup.step_connect.simulated_off}.`)).toBeInTheDocument();
    // The settings are read again (no longer simulated); the outcome still says what changed.
    await waitFor(() => expect(control.callsTo('/api/settings', 'GET').length).toBeGreaterThan(gets));
    expect(within(openrouter).getByText(`${copy.setup.step_connect.simulated_off}.`)).toBeInTheDocument();
  });

  it('shows the outcome of a Connect OpenRouter that returned to Connections (D38)', async () => {
    const result = { ...(structuredClone(connectOk) as ConnectCompleteResponse), return_to: '/app/settings/connections' };
    sessionStorage.setItem(CONNECT_PENDING_KEY, JSON.stringify({ nonce: 'q2Xw9fLr0aZk7TcVb3NmYg' }));
    const { router } = renderPage('/connect/callback?code=abc123', { control: createControlApi({ connectComplete: result }) });
    await waitFor(() => expect(router.state.location.pathname).toBe(PATH));
    const openrouter = await card('OpenRouter');
    expect((await within(openrouter).findByText('Ready: the test call took 412 ms')).closest('p')).toHaveTextContent('and cost $0.00001');
    expect(within(openrouter).getByText(/Now used for: Fast check \(Jev\) and Deep audit judge/)).toBeInTheDocument();
  });

  it('names the fail-closed routes before removing the fast-check key, then removes it with confirm=true (D77)', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ enforcingRoutes: ['payments-faq'] });
    renderPage(PATH, { control });
    await user.click(within(await card('OpenRouter')).getByRole('button', { name: 'Remove the OpenRouter key' }));
    const dialog = await screen.findByRole('alertdialog', { name: 'Remove the OpenRouter key?' });
    expect(within(dialog).getByText(/Removing OpenRouter also disconnects it from Fast check \(Jev\) and Deep audit judge\./)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Remove key' }));
    expect(await within(dialog).findByText(/These routes fail closed .*: payments-faq\. Continue anyway\?/)).toBeInTheDocument();
    // The expected 409 is the question above, not a failure.
    expect(within(dialog).queryByRole('alert')).toBeNull();
    await user.click(within(dialog).getByRole('button', { name: 'Remove anyway' }));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument());
    expect(control.callsTo('/api/credentials/openrouter', 'DELETE').map((c) => c.query.get('confirm'))).toEqual([null, 'true']);
    await waitFor(async () => expect(within(await card('OpenRouter')).getByText('Not connected')).toBeInTheDocument());
  });

  it('starts Connect OpenRouter and leaves for OpenRouter with the nonce kept for this tab (D38)', async () => {
    const user = userEvent.setup();
    const assign = vi.spyOn(browserNavigation, 'assign').mockImplementation(() => {});
    const items = credentialList().items.filter((c) => c.name !== 'openrouter');
    const { control } = renderPage(PATH, { control: createControlApi({ credentials: { items } }) });
    await user.click(within(await card('OpenRouter')).getByRole('button', { name: copy.setup.step_connect.primary }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith(expect.stringMatching(/^https:\/\/openrouter\.ai\/auth\?/)));
    expect(JSON.parse(sessionStorage.getItem(CONNECT_PENDING_KEY)!)).toEqual({ nonce: 'q2Xw9fLr0aZk7TcVb3NmYg' });
    expect(control.callsTo('/api/connect/openrouter/start')[0].body).toEqual({ return_to: '/app/settings/connections' });
  });

  it('explains a credentials store that cannot save keys, and keys the master key cannot read (D32)', async () => {
    renderPage(PATH, { status: { credentials: { store: 'unavailable', undecryptable: ['compatible'] } } });
    expect(await screen.findByText("Keys can't be saved yet")).toBeInTheDocument();
    expect(screen.getByText(copy.settings.store_unavailable)).toBeInTheDocument();
    expect(screen.getByText(/The master key changed, so Other gateway can't be read/)).toBeInTheDocument();
    const openai = await card('OpenAI');
    expect(within(openai).getByRole('button', { name: 'Paste a key' })).toHaveAttribute('aria-disabled', 'true');
    // Testing a key set in .env still works.
    expect(within(await card('TypeSafe')).getByRole('button', { name: 'Test the TypeSafe key' })).toHaveAttribute('aria-disabled', 'false');
    stopLive();
    document.body.innerHTML = '';

    renderPage(PATH, { status: { credentials: { store: 'no_database', undecryptable: [] } } });
    expect(await screen.findByText(copy.settings.no_database)).toBeInTheDocument();
    expect(within(await card('OpenAI')).getByText(copy.disabled_reasons.needs_database)).toBeInTheDocument();
  });

  it('has loading, error, demo, read-only and non-admin states', async () => {
    const user = userEvent.setup();
    const failing = createControlApi({ credentials: errorReply(500, 'internal', 'The store failed.') });
    renderPage(PATH, { control: failing });
    expect(await screen.findByText(/The store failed\./)).toBeInTheDocument();
    failing.state.credentials = credentialList();
    await user.click(screen.getByRole('button', { name: copy.states.retry }));
    expect(await card('OpenRouter')).toBeTruthy();
    stopLive();
    document.body.innerHTML = '';

    renderPage(PATH, { status: { demo: true } });
    // The demo reason and its fix are said once for the page; each card keeps a short reason.
    expect(await screen.findByText(/^This demo runs simulated checks/)).toBeInTheDocument();
    expect(screen.getAllByText(copy.disabled_reasons.demo)).toHaveLength(1);
    expect(within(await card('OpenAI')).getByText('Not on the demo stack')).toBeInTheDocument();
    expect(within(await card('OpenAI')).getByRole('button', { name: 'Paste a key' })).toHaveAttribute('aria-disabled', 'true');
    stopLive();
    document.body.innerHTML = '';

    renderPage(PATH, { status: { readonly: true } });
    expect(within(await card('OpenRouter')).getByText(copy.disabled_reasons.readonly)).toBeInTheDocument();
    expect(within(await card('OpenRouter')).getByRole('button', { name: 'Test the OpenRouter key' })).toHaveAttribute('aria-disabled', 'true');
    stopLive();
    document.body.innerHTML = '';

    const { control } = renderPage(PATH, { status: { auth: { ingest: 'keys', read: 'keys', setup_required: false, caller_scopes: ['read'] } } });
    expect(await screen.findByText('Only admins see connections')).toBeInTheDocument();
    expect(control.callsTo('/api/credentials')).toHaveLength(0);
  });

  it('has no axe violations', async () => {
    const { container } = renderPage(PATH);
    await card('Other gateway');
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});
