import { cleanup, configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { parse } from 'yaml';
import { axeViolations, formatViolations } from '@/test/axe';
import { effectiveRoutes, policyState, policyVersion, policyVersionMeta } from '@/test/control/fixtures';
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

  it('does not replay past answers again when the live stream reconnects during a review', async () => {
    const user = userEvent.setup();
    const { control, ws } = renderPage('/app/policies');
    await card('support-bot');
    await chooseMode(user, 'support-bot', 'Observe');
    await user.click(screen.getByRole('button', { name: 'Review and save' }));
    const dialog = await screen.findByRole('dialog', { name: 'Review and save a new policy version' });
    await within(dialog).findByTestId('preview-impact');
    expect(control.callsTo('/api/policies/dry-run', 'POST')).toHaveLength(1);

    const reads = control.callsTo('/api/policies', 'GET').length;
    ws.connect();
    // The hello refreshes the policy itself, but neither the validation nor the preview.
    await waitFor(() => expect(control.callsTo('/api/policies', 'GET').length).toBeGreaterThan(reads));
    expect(control.callsTo('/api/policies/dry-run', 'POST')).toHaveLength(1);
    expect(control.callsTo('/api/policies/validate', 'POST')).toHaveLength(1);
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
    // Each blocking preset says it starts as Flag until calibrated (the block preset and, since the
    // policy presets grew, the payments one).
    const blockPreset = within(dialog).getByRole('radio', { name: 'Block contradictions' });
    expect(blockPreset).toHaveAccessibleDescription(/Starts as Flag until calibrated/);
    expect(within(dialog).getAllByText(/Starts as Flag until calibrated/).length).toBeGreaterThanOrEqual(1);
    await user.click(blockPreset);
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

  it('explains a diverged policies.yaml that is not applied, shows the edits and adopts the file', async () => {
    // SPEC §7.2 and the gateway: the file changed after a newer dashboard edit, so the edit (version
    // 4) stays in effect and the file waits to be adopted. Nothing to restore: the edit is in effect.
    const user = userEvent.setup();
    const control = createControlApi({
      policies: policyState({
        id: 4,
        etag: '"v4"',
        source: 'dashboard',
        author: 'setup-admin',
        message: 'Block contradictions on payments-faq',
        file: { path: '/etc/trustagent/policies.yaml', sha256: 'b'.repeat(64), status: 'diverged', errors: [] },
      }),
      versions: [policyVersionMeta(4, { source: 'dashboard', author: 'setup-admin' }), policyVersionMeta(3), policyVersionMeta(2), policyVersionMeta(1)],
      versionBodies: { 3: policyVersion(3, 'version: 1\ndefaults:\n  mode: observe\n') },
    });
    renderPage('/app/policies', { control });
    expect(
      await screen.findByText(/policies.yaml changed on disk after your dashboard edit from \d\d:\d\d and is not applied: version 4 stays in effect until you adopt the file/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/replaced your dashboard edit/)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Restore my edit' })).not.toBeInTheDocument();

    await user.click(await screen.findByRole('button', { name: 'View diff' }));
    const diff = await screen.findByRole('dialog', { name: 'Your dashboard edits' });
    expect(within(diff).getByText(/open \/etc\/trustagent\/policies.yaml to read it/)).toBeInTheDocument();
    expect(await within(diff).findByText(/policies.yaml as last applied \(version 3\) → your edits in effect \(version 4\)/)).toBeInTheDocument();
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Your dashboard edits' })).not.toBeInTheDocument());

    await user.click(screen.getByRole('button', { name: 'Adopt file' }));
    const confirm = await screen.findByRole('alertdialog', { name: 'Adopt policies.yaml?' });
    expect(within(confirm).getByText(/The file on disk replaces version 4 as the policy in effect/)).toBeInTheDocument();
    await user.click(within(confirm).getByRole('button', { name: 'Adopt file' }));
    await waitFor(() => expect(control.callsTo('/api/policies/adopt-file', 'POST')).toHaveLength(1));
    expect(control.callsTo('/api/policies/rollback', 'POST')).toHaveLength(0);
    await waitFor(() => expect(screen.queryByText(/is not applied/)).not.toBeInTheDocument());
  });

  it('offers to restore an edit that policies.yaml replaced', async () => {
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

  it('restores the dashboard edit that policies.yaml replaced', async () => {
    const user = userEvent.setup();
    const control = createControlApi({
      policies: policyState({ file: { path: '/etc/trustagent/policies.yaml', sha256: null, status: 'diverged', errors: [] } }),
    });
    renderPage('/app/policies', { control });
    await screen.findByText(/replaced your dashboard edit/);
    await user.click(await screen.findByRole('button', { name: 'Restore my edit' }));
    const confirm = await screen.findByRole('alertdialog', { name: 'Restore your dashboard edit?' });
    await user.click(within(confirm).getByRole('button', { name: 'Restore my edit' }));
    await waitFor(() => expect(control.callsTo('/api/policies/rollback', 'POST')).toHaveLength(1));
    // Version 2 is the dashboard edit the file (version 3, in effect) replaced.
    expect(control.callsTo('/api/policies/rollback')[0].body).toMatchObject({ version: 2 });
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

  it('opens the threshold editor from an Adjust threshold link, previews the change and saves it', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/policies?route=support-bot&evaluator=groundedness@1');
    const support = await card('support-bot');
    const editor = await within(support).findByTestId('threshold-editor');
    await waitFor(() => expect(document.activeElement).toHaveTextContent('Thresholds for Groundedness on support-bot'));
    const preview = within(editor).getByRole('button', { name: 'Preview impact (free — replays past answers)' });
    expect(preview).toHaveAttribute('aria-disabled', 'true');
    expect(within(editor).getByText('Move a threshold first.')).toBeInTheDocument();

    const flag = within(editor).getByRole('slider', { name: 'Flags from' });
    flag.focus();
    await user.keyboard('{ArrowLeft}{ArrowLeft}{ArrowLeft}{ArrowLeft}{ArrowLeft}');
    expect(flag).toHaveAttribute('aria-valuetext', expect.stringMatching(/^0\.75/));
    await user.click(within(editor).getByRole('button', { name: 'Preview impact (free — replays past answers)' }));

    const dialog = await screen.findByRole('dialog', { name: 'Review and save a new policy version' });
    await waitFor(() => expect(control.callsTo('/api/policies/dry-run')).toHaveLength(1));
    const proposed = parse((control.callsTo('/api/policies/dry-run')[0].body as { yaml: string }).yaml) as { routes: { name: string; evaluators: { ref: string; thresholds?: object }[] }[] };
    expect(proposed.routes.find((r) => r.name === 'support-bot')?.evaluators[0]).toEqual({ ref: 'groundedness@1', thresholds: { warn_at: 0.75, critical_at: 0.95 } });
    await user.type(within(dialog).getByRole('textbox', { name: 'What changed and why' }), 'Flag groundedness earlier');
    await user.click(within(dialog).getByRole('button', { name: 'Save version' }));
    await waitFor(() => expect(control.callsTo('/api/policies', 'PUT')).toHaveLength(1));
  });

  it('says so when an Adjust threshold link names a check the route no longer runs', async () => {
    renderPage('/app/policies?route=support-bot&evaluator=groundedness@7');
    const support = await card('support-bot');
    expect(await screen.findByText(/\(groundedness@7\) no longer runs on support-bot, so there is no threshold of it to adjust there\./)).toBeInTheDocument();
    expect(within(support).queryByTestId('threshold-editor')).toBeNull();
    stopLive();
    cleanup();

    // A link that matches says nothing extra.
    renderPage('/app/policies?route=support-bot&evaluator=groundedness@1');
    await within(await card('support-bot')).findByTestId('threshold-editor');
    expect(screen.queryByText(/no longer runs on/)).toBeNull();
  });

  it('opens a check\'s threshold editor from its card, with one thumb for a check that never blocks', async () => {
    const user = userEvent.setup();
    renderPage('/app/policies');
    const support = await card('support-bot');
    await user.click(within(support).getByRole('button', { name: 'Adjust threshold: Off-brand tone' }));
    const editor = await within(support).findByTestId('threshold-editor');
    expect(within(editor).getAllByRole('slider')).toHaveLength(1);
    expect(within(editor).getByText(/This check never blocks on this route/)).toBeInTheDocument();
    await user.click(within(editor).getByRole('button', { name: 'Cancel' }));
    expect(within(support).queryByTestId('threshold-editor')).toBeNull();
    expect(screen.queryByRole('region', { name: 'Unsaved changes' })).toBeNull();
  });

  it('says policy warnings in plain words, with the server text as technical detail', async () => {
    const user = userEvent.setup();
    const raw = 'routes[0].evaluators[2].ref (line 40, col 9): commitments@1 is corroborated by contradiction@2, which this route does not attach';
    renderPage('/app/policies', {
      control: createControlApi({
        policies: policyState({
          warnings: [raw, 'the newest policy version v5 no longer validates (routes[0].mode: unknown value "x"); running v4 until a valid version is saved'],
        }),
      }),
    });
    const list = await screen.findByRole('list', { name: 'Policy warnings' });
    expect(within(list).getByText('support-bot: Unapproved commitments needs Contradiction to agree before it can block, but this route does not run Contradiction. Add Contradiction to the route so Unapproved commitments can block.')).toBeInTheDocument();
    expect(within(list).getByText('Policy version 5 no longer passes validation, so version 4 stays in effect until a valid version is saved.')).toBeInTheDocument();
    expect(within(list).getByText(raw)).not.toBeVisible();
    await user.click(within(list).getAllByText('Technical detail')[0]);
    expect(within(list).getByText(raw)).toBeVisible();
  });

  it('has no axe violations', async () => {
    const { container } = renderPage('/app/policies');
    await card('payments-faq');
    await screen.findByRole('link', { name: /Label 157 more responses/ });
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});
