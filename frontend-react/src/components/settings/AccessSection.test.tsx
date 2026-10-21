import { configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { axeViolations, formatViolations } from '@/test/axe';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { installMswServer } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import { warmRoutes } from '@/test/warmRoutes';
import { copy } from '@/domain/presentation';
import type { ApiKeyList } from '@/types/api';

installMswServer();
warmRoutes('/app/settings/access');
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 4000 });

const PATH = '/app/settings/access';

// jsdom has no layout: give elements a box so the admin-activity table renders its rows.
const saved = {
  offsetHeight: Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetHeight'),
  offsetWidth: Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetWidth'),
};
beforeAll(() => {
  Object.defineProperty(HTMLElement.prototype, 'offsetHeight', { configurable: true, get: () => 400 });
  Object.defineProperty(HTMLElement.prototype, 'offsetWidth', { configurable: true, get: () => 800 });
});
afterAll(() => {
  for (const [key, descriptor] of Object.entries(saved)) {
    if (descriptor) Object.defineProperty(HTMLElement.prototype, key, descriptor);
    else Reflect.deleteProperty(HTMLElement.prototype, key);
  }
});

describe('Settings → Access', { timeout: 20_000 }, () => {
  it('lists API keys with their scopes in words, revoked ones last', async () => {
    renderPage(PATH);
    const list = await screen.findByRole('list', { name: 'API keys' });
    const rows = within(list).getAllByRole('listitem');
    expect(rows.map((r) => r.textContent)).toEqual([
      expect.stringContaining('setup-admin'),
      expect.stringContaining('ci-ingest'),
      expect.stringContaining('old-dashboard'),
    ]);
    expect(within(rows[0]).getByText('See prompts and answers')).toBeInTheDocument();
    expect(within(rows[0]).getByText('This browser')).toBeInTheDocument();
    expect(within(rows[0]).getByText('ta_Zp4c9W…')).toBeInTheDocument();
    expect(within(rows[2]).getByText(/^Revoked/)).toBeInTheDocument();
    expect(within(rows[2]).queryByRole('button', { name: /Revoke/ })).not.toBeInTheDocument();
    expect(within(rows[1]).getByText(/never used/)).toBeInTheDocument();
  });

  it('marks only the key this session was opened with, though another has the same name', async () => {
    const control = createControlApi();
    const keys = control.state.apiKeys as ApiKeyList;
    control.state.apiKeys = {
      ...keys,
      items: [...keys.items, { ...keys.items[0], id: 'k_02b1f7a2', prefix: 'ta_Qm8d2X', created_at: '2026-09-29T08:00:00Z' }],
    };
    renderPage(PATH, { control });
    const list = await screen.findByRole('list', { name: 'API keys' });
    const marked = within(list)
      .getAllByRole('listitem')
      .filter((row) => within(row).queryByText('This browser'));
    expect(marked).toHaveLength(1);
    expect(within(marked[0]).getByText('ta_Zp4c9W…')).toBeInTheDocument();
  });

  it('creates a key and shows its secret once, closing only after “I saved it”', async () => {
    const user = userEvent.setup();
    const { control } = renderPage(PATH);
    await user.click(await screen.findByRole('button', { name: 'Create API key' }));
    const dialog = await screen.findByRole('dialog', { name: 'Create an API key' });
    expect(within(dialog).getByRole('button', { name: 'Create key' })).toHaveAttribute('aria-disabled', 'true');
    expect(within(dialog).getByText('Name the key first')).toBeInTheDocument();
    await user.type(within(dialog).getByLabelText('Name'), 'checkout-service');
    await user.click(within(dialog).getByRole('checkbox', { name: /See prompts and answers/ }));
    await user.click(within(dialog).getByRole('button', { name: 'Create key' }));
    const shown = await screen.findByRole('dialog', { name: 'Your new API key' });
    expect(within(shown).getByText(/0secret0shown0once/)).toBeInTheDocument();
    expect(within(shown).getByText(copy.settings.secret_once)).toBeInTheDocument();
    expect(control.callsTo('/api/keys', 'POST')[0].body).toEqual({ label: 'checkout-service', scopes: ['ingest', 'content'] });
    const done = within(shown).getByRole('button', { name: 'Done' });
    expect(done).toHaveAttribute('aria-disabled', 'true');
    await user.keyboard('{Escape}');
    expect(screen.getByRole('dialog', { name: 'Your new API key' })).toBeInTheDocument();
    await user.click(within(shown).getByRole('checkbox', { name: copy.setup.step_secure.saved_confirm }));
    await user.click(within(shown).getByRole('button', { name: 'Done' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const list = await screen.findByRole('list', { name: 'API keys' });
    expect(await within(list).findByText('checkout-service')).toBeInTheDocument();
    expect(document.body.textContent).not.toContain('0secret0shown0once');
  });

  it('warns before revoking the key this browser uses and the last admin key, and shows the refusal (D81)', async () => {
    const user = userEvent.setup();
    const { control } = renderPage(PATH);
    const list = await screen.findByRole('list', { name: 'API keys' });
    await user.click(within(list).getByRole('button', { name: 'Revoke setup-admin' }));
    const dialog = await screen.findByRole('alertdialog', { name: 'Revoke “setup-admin”?' });
    expect(within(dialog).getByText(copy.settings.current_session_key)).toBeInTheDocument();
    expect(within(dialog).getByText(copy.settings.last_admin_key)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Revoke key' }));
    expect(await within(dialog).findByText(/last admin key; revoking it would lock everyone out/)).toBeInTheDocument();
    expect(control.callsTo('/api/keys/k_01a0e6f1', 'DELETE')).toHaveLength(1);
  });

  it('revokes another key after the confirmation', async () => {
    const user = userEvent.setup();
    const { control } = renderPage(PATH);
    const list = await screen.findByRole('list', { name: 'API keys' });
    await user.click(within(list).getByRole('button', { name: 'Revoke ci-ingest' }));
    const dialog = await screen.findByRole('alertdialog');
    expect(within(dialog).queryByText(copy.settings.current_session_key)).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Revoke key' }));
    await waitFor(() => expect(within(list).queryByRole('button', { name: 'Revoke ci-ingest' })).not.toBeInTheDocument());
    expect(control.callsTo('/api/keys/k_01a0e6f9', 'DELETE')).toHaveLength(1);
  });

  it('signs out everywhere and goes to the sign-in page', async () => {
    const user = userEvent.setup();
    const { control, router } = renderPage(PATH);
    await user.click(await screen.findByRole('button', { name: copy.settings.sign_out_everywhere }));
    const dialog = await screen.findByRole('alertdialog');
    expect(within(dialog).getByText(copy.settings.sign_out_everywhere_confirm)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: copy.settings.sign_out_everywhere }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/app/setup'));
    expect(control.callsTo('/api/session', 'DELETE')[0].query.get('all')).toBe('true');
  });

  it('shows admin activity in words and opens an entry', async () => {
    const user = userEvent.setup();
    renderPage(PATH);
    const table = await screen.findByRole('grid', { name: 'Admin activity' });
    expect(await within(table).findByText('Changed settings')).toBeInTheDocument();
    expect(within(table).getByText('Saved a key')).toBeInTheDocument();
    expect(within(table).getByText('Claimed the setup link')).toBeInTheDocument();
    expect(within(table).queryByText('settings.update')).not.toBeInTheDocument();
    // What changed, in words: no field names such as budgets.llm_usd_day in the table.
    // The budgets change, in its On and Changed cells.
    expect(within(table).getAllByText('Daily budgets')).toHaveLength(2);
    expect(within(table).getByText('OpenRouter')).toBeInTheDocument();
    expect(within(table).getByText('Send requests')).toBeInTheDocument();
    expect(within(table).queryByText(/[a-z]+_[a-z_]+/)).not.toBeInTheDocument();
    await user.click(within(table).getByText('Changed settings'));
    const dialog = await screen.findByRole('dialog', { name: 'Changed settings' });
    expect(within(dialog).getByText(/budgets\.llm_usd_day/)).toBeInTheDocument();
  });

  it('has the empty, error and loading states', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ apiKeys: { items: [] }, adminAudit: { items: [], next_cursor: null } });
    renderPage(PATH, { control });
    expect(await screen.findByText('No API keys yet')).toBeInTheDocument();
    expect(await screen.findByText('No admin changes yet')).toBeInTheDocument();
    stopLive();
    document.body.innerHTML = '';

    const failing = createControlApi({ apiKeys: errorReply(503, 'persistence_disabled', 'Keys need the database.') });
    renderPage(PATH, { control: failing });
    expect(await screen.findByText(/Keys need the database\./)).toBeInTheDocument();
    failing.state.apiKeys = { items: [] };
    await user.click(screen.getAllByRole('button', { name: copy.states.retry })[0]);
    expect(await screen.findByText('No API keys yet')).toBeInTheDocument();
  });

  it('explains disabled writes on a read-only instance, on the demo stack and without the admin scope', async () => {
    renderPage(PATH, { status: { readonly: true } });
    const create = await screen.findByRole('button', { name: 'Create API key' });
    expect(create).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getAllByText(copy.disabled_reasons.readonly).length).toBeGreaterThan(0);
    stopLive();
    document.body.innerHTML = '';

    renderPage(PATH, { status: { demo: true } });
    expect(await screen.findByRole('button', { name: 'Create API key' })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getAllByText(copy.disabled_reasons.demo).length).toBeGreaterThan(0);
    stopLive();
    document.body.innerHTML = '';

    const { control } = renderPage(PATH, { status: { auth: { ingest: 'keys', read: 'keys', setup_required: false, caller_scopes: ['read'] } } });
    expect(await screen.findByText('Only admins see API keys')).toBeInTheDocument();
    expect(screen.getByText(copy.disabled_reasons.needs_admin)).toBeInTheDocument();
    expect(control.callsTo('/api/keys')).toHaveLength(0);
  });

  it('has no axe violations', async () => {
    const { container } = renderPage(PATH);
    await screen.findByRole('list', { name: 'API keys' });
    await screen.findByText('Changed settings');
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});
