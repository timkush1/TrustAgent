import { configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import settingsEnvManaged from '@contracts/fixtures/api/SettingsView.env-managed.json';
import { copy } from '@/domain/presentation';
import { axeViolations, formatViolations } from '@/test/axe';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { adminStatus, renderPage } from '@/test/control/renderPage';
import { settingsView } from '@/test/control/securityHandlers';
import { installMswServer } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import type { SettingsView } from '@/types/api';

installMswServer();
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 4000 });

const PATH = '/app/settings/models';

const withLastError = () => {
  const base = adminStatus();
  return {
    decision_model: {
      ...base.decision_model,
      state: 'breaker_open' as const,
      last_error: { kind: 'timeout', message: 'OpenRouter did not answer within 4 s.', hint: 'It retries on its own; nothing to do.', at: new Date().toISOString() },
    },
  };
};

describe('Settings → Models', { timeout: 30_000 }, () => {
  it('shows the fast check state, its last error and the backend in use', async () => {
    renderPage(PATH, { status: withLastError() });
    expect(await screen.findByText('Paused after repeated errors')).toBeInTheDocument();
    const lastError = screen.getByRole('region', { name: /Last error/ });
    expect(lastError).toHaveAttribute('id', 'last-error');
    expect(within(lastError).getByText('OpenRouter did not answer within 4 s.')).toBeInTheDocument();
    expect(within(lastError).getByText('It retries on its own; nothing to do.')).toBeInTheDocument();
    const backend = screen.getByRole('radiogroup', { name: 'Backend' });
    expect(within(backend).getByRole('radio', { name: /OpenRouter/ })).toBeChecked();
    // The undecryptable gateway key cannot be chosen.
    await waitFor(() => expect(within(backend).getByRole('radio', { name: /Other gateway/ })).toHaveAttribute('aria-disabled', 'true'));
    expect(within(backend).getByText(/Connect its key first/)).toBeInTheDocument();
    expect(screen.getAllByText('Change something first').length).toBeGreaterThan(0);
  });

  it('switches the backend with If-Match and binds its key', async () => {
    const user = userEvent.setup();
    const { control } = renderPage(PATH);
    const backend = await screen.findByRole('radiogroup', { name: 'Backend' });
    await user.click(within(backend).getByRole('radio', { name: /TypeSafe direct/ }));
    await user.click(screen.getAllByRole('button', { name: 'Save' })[0]);
    await waitFor(() => expect(control.callsTo('/api/settings', 'PATCH')).toHaveLength(1));
    const call = control.callsTo('/api/settings', 'PATCH')[0];
    expect(call.headers.get('if-match')).toBe('"s7"');
    expect(call.body).toEqual({ decision_model: { backend: 'typesafe' }, bindings: { decision_model: 'typesafe' } });
  });

  it('pins a model under Advanced without touching the backend', async () => {
    const user = userEvent.setup();
    const { control } = renderPage(PATH);
    await user.click(await screen.findByRole('button', { name: 'Advanced' }));
    await user.type(screen.getByLabelText(/Model pin/), 'typesafe/jev-1.13');
    await user.click(screen.getAllByRole('button', { name: 'Save' })[0]);
    await waitFor(() => expect(control.callsTo('/api/settings', 'PATCH')).toHaveLength(1));
    expect(control.callsTo('/api/settings', 'PATCH')[0].body).toEqual({ decision_model: { model: 'typesafe/jev-1.13' } });
  });

  it('offers to reload when someone saved first (412)', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ settingsPatch: errorReply(412, 'stale_version', 'The settings changed since you loaded them.') });
    renderPage(PATH, { control });
    const backend = await screen.findByRole('radiogroup', { name: 'Backend' });
    await user.click(within(backend).getByRole('radio', { name: /TypeSafe direct/ }));
    await user.click(screen.getAllByRole('button', { name: 'Save' })[0]);
    const alert = await screen.findByRole('alert');
    expect(within(alert).getByText(copy.settings.stale_version)).toBeInTheDocument();
    const gets = control.callsTo('/api/settings', 'GET').length;
    await user.click(within(alert).getByRole('button', { name: 'Reload settings' }));
    await waitFor(() => expect(control.callsTo('/api/settings', 'GET').length).toBeGreaterThan(gets));
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(within(backend).getByRole('radio', { name: /OpenRouter/ })).toBeChecked();
  });

  it('tests the fast check, and waits as long as the proxy asks after a 429', async () => {
    const user = userEvent.setup();
    const control = createControlApi();
    renderPage(PATH, { control });
    await user.click(await screen.findByRole('button', { name: 'Test the fast check' }));
    expect(await screen.findByText('Ready · 231 ms · $0.00001')).toBeInTheDocument();
    expect(screen.getByText('typesafe/jev-1.13-20260917')).toBeInTheDocument();
    control.state.decisionModelTest = errorReply(429, 'rate_limited', 'Wait a few seconds.', 8000);
    await user.click(screen.getByRole('button', { name: 'Test the fast check' }));
    expect(await screen.findByText(/Checked a moment ago: try again in \d s/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Test the fast check' })).toHaveAttribute('aria-disabled', 'true');
  });

  it('disconnects after naming the fail-closed routes (D77)', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ enforcingRoutes: ['payments-faq'] });
    renderPage(PATH, { control });
    await user.click(await screen.findByRole('button', { name: 'Disconnect' }));
    const dialog = await screen.findByRole('alertdialog');
    await user.click(within(dialog).getByRole('button', { name: 'Turn off' }));
    expect(await within(dialog).findByText(/These routes fail closed .*: payments-faq/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Turn off anyway' }));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument());
    const calls = control.callsTo('/api/settings', 'PATCH');
    expect(calls.map((c) => c.query.get('confirm'))).toEqual([null, 'true']);
    expect(calls[1].body).toEqual({ decision_model: { backend: 'off' } });
    expect(await screen.findByText(/is off: answers pass through unchecked/)).toBeInTheDocument();
  });

  it('sets the judge provider and the key it uses', async () => {
    const user = userEvent.setup();
    const settings = settingsView();
    settings.runtime.judge = { provider: 'auto', model: null };
    const { control } = renderPage(PATH, { control: createControlApi({ settings }) });
    expect(await screen.findByText('Uses the OpenRouter key.')).toBeInTheDocument();
    await user.click(screen.getByRole('combobox', { name: 'Provider' }));
    await user.click(await screen.findByRole('option', { name: 'Anthropic' }));
    expect(await screen.findByText("Uses the Anthropic key from the engine's .env.")).toBeInTheDocument();
    await user.click(screen.getAllByRole('button', { name: 'Save' })[1]);
    await waitFor(() => expect(control.callsTo('/api/settings', 'PATCH')).toHaveLength(1));
    expect(control.callsTo('/api/settings', 'PATCH')[0].body).toEqual({ judge: { provider: 'anthropic', model: null }, bindings: { judge: null } });
  });

  it('locks what the environment sets (D33)', async () => {
    renderPage(PATH, { control: createControlApi({ settings: settingsEnvManaged as SettingsView }) });
    const backend = await screen.findByRole('radiogroup', { name: 'Backend' });
    expect(within(backend).getByRole('radio', { name: /TypeSafe direct/ })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByText(/Set in \.env \(TRUTHTABLE_JEV_BACKEND\)/)).toBeInTheDocument();
    expect(screen.getByText(/Set in \.env \(LLM_PROVIDER\)/)).toBeInTheDocument();
    expect(screen.getByRole('combobox', { name: 'Provider' })).toHaveAttribute('data-disabled');
    expect(screen.getByText(/The master key changed, so Anthropic can't be read/)).toBeInTheDocument();
  });

  it('has error, demo and read-only states', async () => {
    const user = userEvent.setup();
    const failing = createControlApi({ settings: errorReply(503, 'persistence_disabled', 'Settings need the database.') });
    renderPage(PATH, { control: failing });
    expect(await screen.findByText(/Settings need the database\./)).toBeInTheDocument();
    failing.state.settings = settingsView();
    await user.click(screen.getByRole('button', { name: copy.states.retry }));
    expect(await screen.findByRole('radiogroup', { name: 'Backend' })).toBeInTheDocument();
    stopLive();
    document.body.innerHTML = '';

    renderPage(PATH, { status: { demo: true } });
    expect(await screen.findByRole('button', { name: 'Disconnect' })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getAllByText(copy.disabled_reasons.demo).length).toBeGreaterThan(0);
    stopLive();
    document.body.innerHTML = '';

    renderPage(PATH, { status: { readonly: true } });
    expect(await screen.findByRole('button', { name: 'Test the fast check' })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getAllByText(copy.disabled_reasons.readonly).length).toBeGreaterThan(0);
  });

  it('has no axe violations', async () => {
    const { container } = renderPage(PATH, { status: withLastError() });
    await screen.findByRole('radiogroup', { name: 'Backend' });
    await screen.findByText('Uses the OpenRouter key (sk-…3f2a).');
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});
