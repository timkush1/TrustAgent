/**
 * Connect your app (P4-PLAN ON-8, D87): the key line names the app key and links to Settings →
 * Access; an admin with no app key creates one here, shown once; nobody else is asked for keys.
 */
import { configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { copy } from '@/domain/presentation';
import { axeViolations, formatViolations } from '@/test/axe';
import { emptyOverview } from '@/test/control/fixtures';
import { createControlApi, type ControlState } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { installMswServer } from '@/test/msw';
import { stopLive } from '@/test/renderLive';

installMswServer();
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 4000 });

const card = async () => (await screen.findByRole('heading', { name: 'Connect your app' })).closest('.rounded-lg') as HTMLElement;
const adminOnly = {
  items: [{ id: 'k_admin', label: 'setup-admin', scopes: ['ingest', 'read', 'content', 'admin'], prefix: 'ta_adm', created_at: '2026-09-28T09:55:40Z', last_used_at: null, revoked_at: null }],
} as unknown as ControlState['apiKeys'];

describe('Connect your app', { timeout: 20_000 }, () => {
  it('names the active app key by label and prefix and links to Settings → Access', async () => {
    renderPage('/app/overview', { control: createControlApi({ overview: emptyOverview() }) });
    const c = await card();
    expect(await within(c).findByText(/the key “ci-ingest”/)).toBeInTheDocument();
    expect(within(c).getByText('ta_q7Hd2x…')).toBeInTheDocument();
    expect(within(c).getByRole('link', { name: 'Settings → Access' })).toHaveAttribute('href', '/app/settings/access');
    expect(within(c).queryByRole('button', { name: 'Create an app key' })).not.toBeInTheDocument();
  });

  it('creates an app key when none is active, never suggesting the admin key, and shows it once', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ overview: emptyOverview(), apiKeys: structuredClone(adminOnly) });
    renderPage('/app/overview', { control });
    const c = await card();
    expect(within(c).queryByText(/setup-admin/)).not.toBeInTheDocument();
    await user.click(await within(c).findByRole('button', { name: 'Create an app key' }));
    expect(await within(c).findByText('my-app (shown once)')).toBeInTheDocument();
    expect(within(c).getByText(copy.setup.step_try.ingest_key)).toBeInTheDocument();
    expect(control.callsTo('/api/keys', 'POST')[0].body).toEqual({ label: 'my-app', scopes: ['ingest'] });
    await waitFor(() => expect(within(c).getByText(/the key “my-app”/)).toBeInTheDocument());
  });

  it('asks nobody but an admin about keys, and nothing on the demo stack', async () => {
    const { control } = renderPage('/app/overview', {
      control: createControlApi({ overview: emptyOverview() }),
      status: { auth: { ingest: 'keys', read: 'keys', setup_required: false, caller_scopes: ['read'] } },
    });
    const c = await card();
    expect(within(c).getByText(/an app key from/)).toBeInTheDocument();
    expect(control.callsTo('/api/keys')).toHaveLength(0);
    stopLive();
    document.body.innerHTML = '';

    const demo = renderPage('/app/overview', { control: createControlApi({ overview: emptyOverview() }), status: { demo: true } });
    expect(await screen.findByText(/This is the demo stack/)).toBeInTheDocument();
    expect(demo.control.callsTo('/api/keys')).toHaveLength(0);
  });

  it('has no axe violations', async () => {
    const { container } = renderPage('/app/overview', { control: createControlApi({ overview: emptyOverview(), apiKeys: structuredClone(adminOnly) }) });
    await within(await card()).findByRole('button', { name: 'Create an app key' });
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});
