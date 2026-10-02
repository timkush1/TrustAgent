import { configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { afterEach, describe, expect, it } from 'vitest';
import { axeViolations, formatViolations } from '@/test/axe';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { settingsView } from '@/test/control/securityHandlers';
import { API, installMswServer, server } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import { systemStatus } from '@/test/status';
import type { SettingsView } from '@/types/api';

installMswServer();
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 4000 });

const PATH = '/app/settings/budgets';
const spentStatus = (decision: number, llm = 0.41) => ({
  budgets: { ...systemStatus().budgets, decision_usd_day: 2, llm_usd_day: 5, decision_spent_today_usd: decision, llm_spent_today_usd: llm },
});

async function decisionInput() {
  return screen.findByRole('textbox', { name: 'Fast-check budget, USD per day' });
}

describe('Settings → Budgets', { timeout: 20_000 }, () => {
  it('shows both caps with today’s spend and where observe and flag stop', async () => {
    renderPage(PATH, { status: spentStatus(0.031204) });
    expect(await decisionInput()).toHaveValue('2');
    expect(screen.getByRole('textbox', { name: 'Deep-audit budget, USD per day' })).toHaveValue('5');
    const fast = screen.getByRole('meter', { name: 'Fast-check budget: spent today' });
    expect(fast).toHaveAttribute('aria-valuetext', '$0.03 of $2.00 spent today (1.6%)');
    expect(fast.querySelector('[data-marker="observe-line"]')).toHaveStyle({ left: '75%' });
    expect(screen.getByText('Observe and flag stop at 75% ($1.50)')).toBeInTheDocument();
    const deep = screen.getByRole('meter', { name: 'Deep-audit budget: spent today' });
    expect(deep).toHaveAttribute('aria-valuetext', '$0.41 of $5.00 spent today (8.2%)');
    expect(deep.querySelector('[data-marker]')).toBeNull();
    const save = screen.getByRole('button', { name: 'Save budgets' });
    expect(save).toHaveAttribute('aria-disabled', 'true');
    expect(save).toHaveAccessibleDescription('Nothing to save yet');
  });

  it('moves the observe line with the enforcing reserve the proxy reports', async () => {
    const base = settingsView();
    const control = createControlApi({
      settings: { ...base, groups: [...base.groups, { name: 'Budgets', keys: [{ ...base.groups[0].keys[0], key: 'TRUTHTABLE_SPEND_ENFORCING_RESERVE', value: '0.4' }] }] },
    });
    renderPage(PATH, { control });
    expect(await screen.findByText('Observe and flag stop at 60% ($1.20)')).toBeInTheDocument();
  });

  it('saves a changed cap with If-Match and only what changed', async () => {
    const user = userEvent.setup();
    const { control } = renderPage(PATH);
    const input = await decisionInput();
    await user.clear(input);
    await user.type(input, '3.5');
    await user.click(screen.getByRole('button', { name: 'Save budgets' }));
    await waitFor(() => expect(control.callsTo('/api/settings', 'PATCH')).toHaveLength(1));
    const call = control.callsTo('/api/settings', 'PATCH')[0];
    expect(call.body).toEqual({ budgets: { decision_usd_day: 3.5 } });
    expect(call.headers.get('if-match')).toBe('"s7"');
    expect(call.query.get('confirm')).toBeNull();
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save budgets' })).toHaveAttribute('aria-disabled', 'true'));
    expect(await decisionInput()).toHaveValue('3.5');
  });

  it('checks the amount before sending it', async () => {
    const user = userEvent.setup();
    const { control } = renderPage(PATH);
    const input = await decisionInput();
    await user.clear(input);
    await user.type(input, '2.555');
    expect(await screen.findByText('Use dollars and cents, like 2 or 2.50.')).toBeInTheDocument();
    const save = screen.getByRole('button', { name: 'Save budgets' });
    expect(save).toHaveAccessibleDescription('Fix the amounts above first');
    await user.click(save);
    expect(control.callsTo('/api/settings', 'PATCH')).toHaveLength(0);
  });

  it('warns when a cap is below today’s spend, and asks before blocking fail-closed routes (D77)', async () => {
    const user = userEvent.setup();
    const { control } = renderPage(PATH, { status: spentStatus(1.2) });
    const input = await decisionInput();
    await user.clear(input);
    await user.type(input, '1');
    expect(screen.getByText(/Today’s spend \(\$1\.20\) is already above this cap/)).toBeInTheDocument();
    let attempts = 0;
    server.use(
      http.patch(`${API}/api/settings`, async ({ request }) => {
        attempts += 1;
        const url = new URL(request.url);
        if (url.searchParams.get('confirm') !== 'true') {
          return HttpResponse.json(
            { error: { code: 'confirmation_required', message: 'These routes would block every answer.', details: { routes: ['payments-faq', 'help-center'] } } },
            { status: 409 },
          );
        }
        const next: SettingsView = { ...settingsView(), version: 8, etag: '"s8"' };
        next.runtime = { ...next.runtime, budgets: { decision_usd_day: 1, llm_usd_day: 5 } };
        return HttpResponse.json(next);
      }),
    );
    await user.click(screen.getByRole('button', { name: 'Save budgets' }));
    const dialog = await screen.findByRole('alertdialog', { name: 'Save a fast-check cap below today’s spend?' });
    expect(dialog).toHaveTextContent(
      'These routes fail closed and would block every answer without a fast check: payments-faq, help-center. Continue anyway?',
    );
    await user.click(within(dialog).getByRole('button', { name: 'Save anyway' }));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    expect(attempts).toBe(2);
    expect(await decisionInput()).toHaveValue('1');
    expect(control.callsTo('/api/settings', 'PATCH')).toHaveLength(0);
  });

  it('locks a cap set in .env and says where to change it', async () => {
    const control = createControlApi({ settings: settingsView({ env_managed: ['budgets.decision_usd_day'] }) });
    renderPage(PATH, { control });
    const input = await decisionInput();
    expect(input).toHaveAttribute('readonly');
    expect(screen.getByText('Set in .env (TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY); change it there and restart.')).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Deep-audit budget, USD per day' })).not.toHaveAttribute('readonly');
  });

  it('offers a reload when the settings changed in another tab', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ settingsPatch: errorReply(412, 'stale_version', 'The settings changed since you loaded them.') });
    renderPage(PATH, { control });
    const input = await decisionInput();
    await user.clear(input);
    await user.type(input, '4');
    await user.click(screen.getByRole('button', { name: 'Save budgets' }));
    expect(await screen.findByText(/Settings changed in another tab/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Reload settings' }));
    await waitFor(() => expect(control.callsTo('/api/settings', 'GET').length).toBeGreaterThan(1));
  });

  it('says why saving is off on a read-only stack', async () => {
    renderPage(PATH, { status: { readonly: true } });
    expect(await decisionInput()).toHaveAttribute('readonly');
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save budgets' })).toHaveAccessibleDescription('Not available on a read-only instance'));
  });

  it('says why saving is off for a viewer without the admin scope', async () => {
    renderPage(PATH, { status: { auth: { ...systemStatus().auth, read: 'keys', caller_scopes: ['read'] } } });
    expect(await decisionInput()).toHaveAttribute('readonly');
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save prices' })).toHaveAccessibleDescription(/Needs an admin key/));
  });

  it('degrades on a proxy without the settings API', async () => {
    renderPage(PATH, { control: createControlApi({ settings: undefined }) });
    expect(await screen.findByText('Budgets can’t be changed from this dashboard')).toBeInTheDocument();
    expect(screen.getByText(/set TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY and TRUTHTABLE_SPEND_CAP_LLM_USD_DAY in \.env/)).toBeInTheDocument();
  });

  it('has no axe violations', async () => {
    const { container } = renderPage(PATH, { status: spentStatus(1.6) });
    await decisionInput();
    await screen.findByRole('table', { name: /Prices in use/ });
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});

describe('Settings → Budgets → Prices', { timeout: 20_000 }, () => {
  it('lists the prices in use with where each comes from', async () => {
    renderPage(PATH);
    const table = await screen.findByRole('table', { name: 'Prices in use, first match wins (version 2026-09-15)' });
    const rows = within(table).getAllByRole('row').slice(1);
    expect(rows).toHaveLength(4);
    expect(within(rows[0]).getByText('Your price')).toBeInTheDocument();
    expect(within(rows[0]).getByRole('cell', { name: '$0.12 in · $0.50 out' })).toBeInTheDocument();
    expect(within(rows[1]).getByRole('button', { name: 'Vendor: how “typesafe/jev* price” was produced' })).toBeInTheDocument();
  });

  it('adds an override and saves the list labelled as yours', async () => {
    const user = userEvent.setup();
    const { control } = renderPage(PATH);
    await user.click(await screen.findByRole('button', { name: 'Add a price' }));
    const row = document.querySelector('[data-override-row="0"]') as HTMLElement;
    await user.clear(within(row).getByRole('combobox', { name: 'Provider' }));
    await user.type(within(row).getByRole('combobox', { name: 'Provider' }), 'openai');
    await user.type(within(row).getByRole('textbox', { name: /^Model/ }), 'gpt-4o-mini*');
    await user.type(within(row).getByRole('textbox', { name: 'Input, USD per million tokens' }), '0.12');
    await user.type(within(row).getByRole('textbox', { name: 'Output, USD per million tokens' }), '0.5');
    await user.click(screen.getByRole('button', { name: 'Save prices' }));
    await waitFor(() => expect(control.callsTo('/api/settings', 'PATCH')).toHaveLength(1));
    expect(control.callsTo('/api/settings', 'PATCH')[0].body).toEqual({
      pricing_overrides: [{ provider: 'openai', match: 'gpt-4o-mini*', input_per_mtok: 0.12, output_per_mtok: 0.5, label: 'override' }],
    });
    // The effective table is read again.
    await waitFor(() => expect(control.callsTo('/api/pricing').length).toBeGreaterThan(1));
  });

  it('checks a row before sending it', async () => {
    const user = userEvent.setup();
    const { control } = renderPage(PATH);
    await user.click(await screen.findByRole('button', { name: 'Add a price' }));
    await user.click(screen.getByRole('button', { name: 'Save prices' }));
    expect(await screen.findByText(/Name the model/)).toBeInTheDocument();
    expect(screen.getAllByText('A price of 0 or more, in USD per million tokens.')).toHaveLength(2);
    expect(control.callsTo('/api/settings', 'PATCH')).toHaveLength(0);
  });

  it('shows the server’s findings on the row they concern, and its warnings after a save', async () => {
    const user = userEvent.setup();
    renderPage(PATH);
    await user.click(await screen.findByRole('button', { name: 'Add a price' }));
    const row = document.querySelector('[data-override-row="0"]') as HTMLElement;
    await user.clear(within(row).getByRole('combobox', { name: 'Provider' }));
    await user.type(within(row).getByRole('combobox', { name: 'Provider' }), 'openai');
    await user.type(within(row).getByRole('textbox', { name: /^Model/ }), 'gpt-4o*');
    await user.type(within(row).getByRole('textbox', { name: 'Input, USD per million tokens' }), '0');
    await user.type(within(row).getByRole('textbox', { name: 'Output, USD per million tokens' }), '0');
    server.use(
      http.patch(`${API}/api/settings`, () =>
        HttpResponse.json(
          {
            error: {
              code: 'validation_failed',
              message: 'Some settings are not valid.',
              field_errors: [
                {
                  path: 'pricing_overrides[0].input_per_mtok',
                  code: 'price_zero_not_local',
                  message: 'A $0 price is only allowed for local models; openai calls are billed.',
                  details: { provider: 'openai' },
                },
              ],
            },
          },
          { status: 422 },
        ),
      ),
    );
    await user.click(screen.getByRole('button', { name: 'Save prices' }));
    expect(await within(row).findByText('$0 is only allowed for local models. openai bills its calls.')).toBeInTheDocument();

    server.use(
      http.patch(`${API}/api/settings`, () => {
        const view = settingsView({ version: 8, etag: '"s8"' });
        view.runtime = { ...view.runtime, pricing_overrides: [{ provider: 'openai', match: 'gpt-4o*', input_per_mtok: 500, output_per_mtok: 0, label: 'override' }] };
        view.warnings = [
          {
            key: 'pricing_overrides',
            code: 'price_far_above_contracts',
            message: '$500 per million tokens is 200× the built-in price of $2.5; check the units (USD per million tokens).',
            hint: 'Check the price against the provider’s price page.',
          },
        ];
        return HttpResponse.json(view);
      }),
    );
    const input = within(row).getByRole('textbox', { name: 'Input, USD per million tokens' });
    await user.clear(input);
    await user.type(input, '500');
    await user.click(screen.getByRole('button', { name: 'Save prices' }));
    const warnings = await screen.findByRole('list', { name: 'Check these prices' });
    expect(warnings).toHaveTextContent('$500 per million tokens is 200× the built-in price of $2.5');
  });
});
