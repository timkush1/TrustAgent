import { configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import settingsReadScope from '@contracts/fixtures/api/SettingsView.read-scope.json';
import { axeViolations, formatViolations } from '@/test/axe';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { settingsView } from '@/test/control/securityHandlers';
import { installMswServer } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import { warmRoutes } from '@/test/warmRoutes';
import { commandText } from '@/domain/presentation';
import type { SettingKeyView, SettingsView } from '@/types/api';

installMswServer();
warmRoutes('/app/settings/all');
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 4000 });

describe('Settings → Add-ons', { timeout: 20_000 }, () => {
  it('says which add-ons are on and gives the command for the others', async () => {
    renderPage('/app/settings/addons', { status: { knowledge: { state: 'up', enabled: true, kb_claims: 42, embedding_model: 'nomic-embed-text' } } });
    expect(await screen.findByText('42 claims')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Open Knowledge/ })).toHaveAttribute('href', '/app/knowledge');
    expect(screen.queryByText(commandText('knowledge_up')!)).not.toBeInTheDocument();
    expect(screen.getByText(commandText('local_llm_up')!)).toBeInTheDocument();
    expect(screen.getByText(commandText('observability_up')!)).toBeInTheDocument();
    expect(screen.getAllByText('On')).toHaveLength(1);
  });

  it('shows the command for an add-on that is off, and a degraded note when status is unknown', async () => {
    renderPage('/app/settings/addons', { status: null });
    expect(await screen.findByText("Can't tell which add-ons are on")).toBeInTheDocument();
    expect(screen.getByText(commandText('knowledge_up')!)).toBeInTheDocument();
    expect(screen.getAllByText('Unknown').length).toBe(2);
  });

  it('says on the demo stack that add-ons come with your own stack, and still gives the commands', async () => {
    renderPage('/app/settings/addons', { status: { demo: true } });
    expect(await screen.findByText('The demo stack runs without add-ons. Start your own stack first, then add them.')).toBeInTheDocument();
    expect(screen.getByText(commandText('knowledge_up')!)).toBeInTheDocument();
    expect(screen.getByText(commandText('local_llm_up')!)).toBeInTheDocument();
  });

  it('reads the same on a read-only instance: the add-ons are turned on from the shell, not here', async () => {
    renderPage('/app/settings/addons', { status: { readonly: true } });
    expect(await screen.findByRole('heading', { name: 'Local LLM' })).toBeInTheDocument();
    expect(screen.getByText(commandText('knowledge_up')!)).toBeInTheDocument();
    expect(screen.queryByText('The demo stack runs without add-ons. Start your own stack first, then add them.')).toBeNull();
    // Nothing to write: no button is offered, so none sits disabled without a reason.
    expect(screen.queryAllByRole('button', { name: /^(Turn on|Save|Add)/ })).toHaveLength(0);
  });

  it('has no axe violations', async () => {
    const { container } = renderPage('/app/settings/addons');
    await screen.findByRole('heading', { name: 'Local LLM' });
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});

describe('Settings → All configuration', { timeout: 20_000 }, () => {
  it('lists every setting with its value and where it comes from, hiding advanced ones until asked', async () => {
    const user = userEvent.setup();
    renderPage('/app/settings/all');
    const group = await screen.findByRole('heading', { name: 'Decision model' });
    expect(group).toBeInTheDocument();
    expect(screen.getByRole('rowheader', { name: 'TRUTHTABLE_JEV_BACKEND' })).toBeInTheDocument();
    expect(screen.queryByRole('rowheader', { name: 'TRUTHTABLE_JEV_BASE_URL' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('switch', { name: 'Show advanced settings' }));
    expect(screen.getByRole('rowheader', { name: 'TRUTHTABLE_JEV_BASE_URL' })).toBeInTheDocument();
    expect(screen.getByText(/\/app\/\.env/)).toBeInTheDocument();
    await user.type(screen.getByLabelText('Filter by name'), 'no_such_key');
    expect(await screen.findByText(/No setting matches/)).toBeInTheDocument();
  });

  it('never shows a secret: read-scope callers see Set or Not set (D79)', async () => {
    const control = createControlApi({ settings: settingsReadScope as SettingsView });
    renderPage('/app/settings/all', { control });
    await screen.findByRole('heading', { name: 'Decision model' });
    const user = userEvent.setup();
    await user.click(screen.getByRole('switch', { name: 'Show advanced settings' }));
    const secrets = (settingsReadScope as SettingsView).groups.flatMap((g) => g.keys).filter((k) => k.secret);
    expect(secrets.length).toBeGreaterThan(0);
    for (const k of secrets) {
      const row = screen.getByRole('rowheader', { name: k.key }).closest('tr')!;
      expect(within(row).getByText(k.value === 'set' ? 'Set' : 'Not set')).toBeInTheDocument();
    }
  });

  it('shows an admin the masked forms, and never a secret the proxy sent unmasked (D79)', async () => {
    const leaked = 'cmp0e2eZq7Lw4Rt9Yx2Vb6Nm';
    const secret = (key: string, value: string): SettingKeyView => ({
      key,
      env: key,
      value,
      default: '',
      source: 'env',
      secret: true,
      advanced: false,
      restart_required: true,
      editable: false,
      services: ['proxy'],
    });
    const view = settingsView({
      groups: [
        {
          name: 'Access',
          keys: [
            secret('TRUTHTABLE_ADMIN_KEYS', 'ops=sk-…ab12,ci=****'),
            secret('OPENROUTER_API_KEY', 'sk-…3f2a'),
            secret('TYPESAFE_API_KEY', leaked),
          ],
        },
      ],
    });
    renderPage('/app/settings/all', { control: createControlApi({ settings: view }) });
    const row = async (key: string) => (await screen.findByRole('rowheader', { name: key })).closest('tr')!;
    expect(within(await row('TRUTHTABLE_ADMIN_KEYS')).getByText('ops=sk-…ab12,ci=****')).toBeInTheDocument();
    expect(within(await row('OPENROUTER_API_KEY')).getByText('sk-…3f2a')).toBeInTheDocument();
    expect(within(await row('TYPESAFE_API_KEY')).getByText('Set')).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(leaked);
  });

  it('shows the settings warnings in words', async () => {
    const view = settingsView({
      warnings: [{ key: 'TRUTHTABLE_SECRET_KEY', message: 'raw', hint: 'Restore it.', code: 'master_key_changed', details: { names: ['anthropic'] } }],
    });
    renderPage('/app/settings/all', { control: createControlApi({ settings: view }) });
    const warnings = await screen.findByRole('list', { name: 'Settings warnings' });
    expect(within(warnings).getByText(/The master key changed, so Anthropic can't be read/)).toBeInTheDocument();
  });

  it('falls back to the registry defaults when the proxy does not send settings', async () => {
    renderPage('/app/settings/all', { control: createControlApi({ settings: errorReply(404, 'not_found', 'Not served by this proxy.') }) });
    expect(await screen.findByText('Showing the built-in defaults')).toBeInTheDocument();
    expect(screen.getByText(/Not served by this proxy\./)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole('rowheader', { name: 'TRUTHTABLE_JEV_BACKEND' })).toBeInTheDocument());
    expect(screen.getAllByText('Default').length).toBeGreaterThan(5);
  });

  it('has no axe violations', async () => {
    const { container } = renderPage('/app/settings/all');
    await screen.findByRole('heading', { name: 'Decision model' });
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});
