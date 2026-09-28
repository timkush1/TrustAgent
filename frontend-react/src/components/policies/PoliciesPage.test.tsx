import { configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { axeViolations, formatViolations } from '@/test/axe';
import { effectiveRoutes, policyState } from '@/test/control/fixtures';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { installMswServer } from '@/test/msw';
import { stopLive } from '@/test/renderLive';

installMswServer();
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 4000 });

const card = async (route: string) => (await screen.findByRole('article', { name: new RegExp(route) })) as HTMLElement;

async function chooseMode(user: ReturnType<typeof userEvent.setup>, route: string, mode: string) {
  await user.click(screen.getByRole('combobox', { name: `Mode for ${route}` }));
  await user.click(await screen.findByRole('option', { name: new RegExp(`^${mode}`) }));
}

describe('Policies page', { timeout: 20_000 }, () => {
  it('shows each route with its match, mode and what it really acts as', async () => {
    renderPage('/app/policies');
    const support = await card('support-bot');
    expect(within(support).getByText('Header X-TrustAgent-Route is support-bot')).toBeInTheDocument();
    expect(within(support).getByText('Watching')).toBeInTheDocument();
    const payments = await card('payments-faq');
    expect(within(payments).getByText('Fallback → currently acting as Flag')).toBeInTheDocument();
    expect(within(payments).getByText(/Blocking stays off until every blocking check is calibrated/)).toBeInTheDocument();
    // "Label N more responses" comes from the evaluator's calibration progress (43 of 200).
    expect(await within(payments).findByRole('link', { name: /Label 157 more responses/ })).toHaveAttribute('href', '/app/evaluators/groundedness');
    expect(within(payments).getAllByText('not ready to block')).toHaveLength(2);
    expect(screen.getByText(/Version 3 · From policies.yaml/)).toBeInTheDocument();
  });

  it('drafts a mode change, previews its impact and saves it with If-Match', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/policies');
    await card('support-bot');
    await chooseMode(user, 'support-bot', 'Observe');
    const bar = await screen.findByRole('region', { name: 'Unsaved changes' });
    expect(within(bar).getByText(/Unsaved changes · 2 lines/)).toBeInTheDocument();
    await user.click(within(bar).getByRole('button', { name: 'Review and save' }));

    const dialog = await screen.findByRole('dialog', { name: 'Review and save a new policy version' });
    expect(await within(dialog).findByText('The server accepts this policy.')).toBeInTheDocument();
    expect(await within(dialog).findByText(/140/)).toBeInTheDocument();
    expect(within(dialog).getByText('No longer flagged')).toBeInTheDocument();
    const validate = control.callsTo('/api/policies/validate')[0].body as { yaml: string };
    expect(validate.yaml).toMatch(/name: support-bot\n\s+match: \{headers: \{X-TrustAgent-Route: support-bot\}\}\n\s+mode: observe/);
    expect((control.callsTo('/api/policies/dry-run')[0].body as { window: string }).window).toBe('7d');

    const save = within(dialog).getByRole('button', { name: 'Save version' });
    expect(save).toHaveAttribute('aria-disabled', 'true');
    expect(within(dialog).getByText('Say what changed and why first.')).toBeInTheDocument();
    await user.type(within(dialog).getByRole('textbox', { name: 'What changed and why' }), 'Observe support-bot while we tune');
    // The button is re-rendered once it has no reason to be disabled: query it again.
    await user.click(within(dialog).getByRole('button', { name: 'Save version' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const put = control.callsTo('/api/policies', 'PUT')[0];
    expect(put.headers.get('if-match')).toBe('"v3"');
    expect(put.body).toMatchObject({ message: 'Observe support-bot while we tune' });
    expect(await screen.findByText(/Version 4 · Edited in the dashboard/)).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Unsaved changes' })).not.toBeInTheDocument();
  });

  it('asks to reload when the policy changed meanwhile (412)', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ save: errorReply(412, 'stale_version', 'The policy changed since you loaded it.') });
    renderPage('/app/policies', { control });
    await card('support-bot');
    await chooseMode(user, 'support-bot', 'Block');
    await user.click(screen.getByRole('button', { name: 'Review and save' }));
    const dialog = await screen.findByRole('dialog');
    await within(dialog).findByText('The server accepts this policy.');
    await user.type(within(dialog).getByRole('textbox', { name: 'What changed and why' }), 'Block');
    await user.click(within(dialog).getByRole('button', { name: 'Save version' }));
    expect(await within(dialog).findByText(/Someone changed the policy while you were editing/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: /Load the latest policy/ }));
    await waitFor(() => expect(screen.queryByRole('region', { name: 'Unsaved changes' })).not.toBeInTheDocument());
  });

  it('saves with the version the draft started from, so an edit saved elsewhere is never overwritten', async () => {
    const user = userEvent.setup();
    const { control, ws } = renderPage('/app/policies');
    await card('support-bot');
    await chooseMode(user, 'support-bot', 'Observe');
    await screen.findByRole('region', { name: 'Unsaved changes' });

    // Someone else saves version 4; the live stream refreshes the page while the draft is open.
    control.state.policies = policyState({ id: 4, etag: '"v4"', source: 'dashboard', author: 'other-admin', message: 'Edited elsewhere' });
    ws.connect();
    ws.send({ type: 'policy_updated', data: { version: 4, source: 'dashboard', author: 'other-admin', message: 'Edited elsewhere' } });
    expect(await screen.findByText(/Version 4 · Edited in the dashboard/)).toBeInTheDocument();
    expect(screen.getByText(/The policy changed on the server since you started/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Review and save' }));
    const dialog = await screen.findByRole('dialog', { name: 'Review and save a new policy version' });
    await within(dialog).findByText('The server accepts this policy.');
    await within(dialog).findByTestId('preview-impact');
    await user.type(within(dialog).getByRole('textbox', { name: 'What changed and why' }), 'Observe support-bot');
    await user.click(within(dialog).getByRole('button', { name: 'Save version' }));

    expect(await within(dialog).findByText(/Someone changed the policy while you were editing/)).toBeInTheDocument();
    const put = control.callsTo('/api/policies', 'PUT')[0];
    expect(put.headers.get('if-match')).toBe('"v3"');
    expect(control.state.policies).toMatchObject({ id: 4, message: 'Edited elsewhere' });
  });

  it('shows server validation errors and blocks the save', async () => {
    const user = userEvent.setup();
    const control = createControlApi({
      validate: { ok: false, errors: [{ path: 'routes[0].hedge_after_ms', line: 34, message: 'must be below deadline_ms' }], warnings: [], block_readiness: {}, diff: '' },
    });
    renderPage('/app/policies', { control });
    await card('support-bot');
    await chooseMode(user, 'support-bot', 'Block');
    await user.click(screen.getByRole('button', { name: 'Review and save' }));
    const dialog = await screen.findByRole('dialog');
    expect(await within(dialog).findByText('must be below deadline_ms')).toBeInTheDocument();
    expect(within(dialog).getByText('Fix the errors above first.')).toBeInTheDocument();
  });

  it('applies the one-click fixes to the draft', async () => {
    const user = userEvent.setup();
    const routes = effectiveRoutes();
    routes[0] = {
      ...routes[0],
      mode: 'block',
      effective_mode: 'flag',
      downgrades: [
        { code: 'stream_not_buffered', message: 'passthrough', fix: { kind: 'action', target: 'streaming:buffer' } },
        { code: 'uncalibrated', message: 'uncalibrated', fix: null },
      ],
      block_readiness: [{ evaluator: 'groundedness@1', ready: false, missing: ['calibration', 'buffering'] }],
    };
    const control = createControlApi({ policies: policyState({ effective_routes: routes }) });
    renderPage('/app/policies', { control });
    const support = await card('support-bot');
    expect(within(support).getByText('Block → currently acting as Flag')).toBeInTheDocument();
    await user.click(within(support).getByRole('button', { name: 'Buffer streams' }));
    expect(await screen.findByRole('region', { name: 'Unsaved changes' })).toBeInTheDocument();

    await user.click(within(support).getByRole('button', { name: 'Allow uncalibrated blocking' }));
    const confirm = await screen.findByRole('alertdialog', { name: /Allow uncalibrated blocking on support-bot/ });
    const allow = within(confirm).getByRole('button', { name: 'Allow uncalibrated blocking' });
    expect(allow).toHaveAttribute('aria-disabled', 'true');
    await user.type(within(confirm).getByRole('textbox'), 'support-bot');
    await user.click(allow);
    await user.click(screen.getByRole('button', { name: 'Review and save' }));
    await screen.findByRole('dialog', { name: 'Review and save a new policy version' });
    const yaml = (control.callsTo('/api/policies/validate').at(-1)?.body as { yaml: string }).yaml;
    expect(yaml).toContain('streaming: buffer');
    expect(yaml).toContain('require_calibration: false');
  });

  it('applies a preset from the picker', async () => {
    const user = userEvent.setup();
    renderPage('/app/policies');
    await card('support-bot');
    await user.click(screen.getByRole('button', { name: 'Presets' }));
    const dialog = await screen.findByRole('dialog', { name: 'Start from a preset' });
    expect(within(dialog).getByText(/Starts as Flag until calibrated/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('radio', { name: 'Block contradictions' }));
    await user.click(within(dialog).getByRole('button', { name: 'Apply to draft' }));
    const support = await card('support-bot');
    expect(await within(support).findByText(/Unsaved: Flag → Block/)).toBeInTheDocument();
  });

  it('lists versions, shows a diff and rolls back', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/policies');
    await card('support-bot');
    await user.click(screen.getByRole('button', { name: 'Versions' }));
    const sheet = await screen.findByRole('dialog', { name: 'Policy versions' });
    expect(await within(sheet).findByText('Version 2')).toBeInTheDocument();
    expect(within(sheet).getByText('In effect')).toBeInTheDocument();
    await user.click(within(sheet).getByRole('button', { name: /View diff of version 2/ }));
    expect(await within(sheet).findByText('Same as the policy in effect.')).toBeInTheDocument();
    await user.click(within(sheet).getByRole('button', { name: /Roll back to version 2/ }));
    const confirm = await screen.findByRole('alertdialog', { name: 'Roll back to version 2?' });
    await user.click(within(confirm).getByRole('button', { name: 'Roll back' }));
    await waitFor(() => expect(control.callsTo('/api/policies/rollback', 'POST')).toHaveLength(1));
    expect(control.callsTo('/api/policies/rollback')[0].body).toMatchObject({ version: 2 });
  });

  it('explains a diverged policies.yaml and adopts the file', async () => {
    const user = userEvent.setup();
    const control = createControlApi({
      policies: policyState({ file: { path: '/etc/trustagent/policies.yaml', sha256: null, status: 'diverged', errors: [] } }),
    });
    renderPage('/app/policies', { control });
    expect(await screen.findByText(/policies.yaml changed at \d\d:\d\d and replaced your dashboard edit from \d\d:\d\d/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Adopt file' }));
    const confirm = await screen.findByRole('alertdialog', { name: 'Adopt policies.yaml?' });
    await user.click(within(confirm).getByRole('button', { name: 'Adopt file' }));
    await waitFor(() => expect(control.callsTo('/api/policies/adopt-file', 'POST')).toHaveLength(1));
    await waitFor(() => expect(screen.queryByText(/replaced your dashboard edit/)).not.toBeInTheDocument());
  });

  it('says why nothing can be changed on a read-only instance', async () => {
    renderPage('/app/policies', { status: { readonly: true } });
    const support = await card('support-bot');
    expect(within(support).getByRole('combobox', { name: 'Mode for support-bot' })).toHaveAttribute('data-disabled');
    expect(within(support).getAllByText('Not available on a read-only instance').length).toBeGreaterThan(0);
    expect(screen.getByRole('button', { name: 'View YAML' })).toBeInTheDocument();
  });

  it('shows the file-managed reason from the server', async () => {
    const control = createControlApi({ policies: policyState({ editable: false, editable_reason: 'file-managed (no database)', persisted: false }) });
    renderPage('/app/policies', { control });
    expect((await screen.findAllByText(/This proxy has no database, so the policy is file-managed/)).length).toBeGreaterThan(0);
    expect(screen.getByText('Applies until restart (no database configured)')).toBeInTheDocument();
  });

  it('shows the server message with Retry, and degrades on an older proxy', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ policies: errorReply(500, 'internal', 'Policy store unavailable.') });
    renderPage('/app/policies', { control });
    expect(await screen.findByText("Couldn't load this: Policy store unavailable.")).toBeInTheDocument();
    control.state.policies = policyState();
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await card('support-bot')).toBeInTheDocument();
  });

  it('degrades when the proxy does not serve policies', async () => {
    renderPage('/app/policies', { control: createControlApi({ policies: undefined }) });
    expect(await screen.findByText('Policies cannot be read from this proxy')).toBeInTheDocument();
  });

  it('has no axe violations', async () => {
    const { container } = renderPage('/app/policies');
    await card('payments-faq');
    await screen.findByRole('link', { name: /Label 157 more responses/ });
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});
