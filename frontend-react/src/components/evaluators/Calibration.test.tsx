/**
 * Calibrate now, Adopt calibration (D62) and the calibration records, against the real data
 * layer over msw: the fit dialog's honesty note and result, Adopt blocked until Preview impact has
 * loaded, the exact call order of an adoption, deactivation, import, and the page states.
 */
import { cleanup, configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { afterEach, describe, expect, it } from 'vitest';
import { parse } from 'yaml';
import { copy } from '@/domain/presentation';
import { EVAL_CRITICAL_NULL_TEXT } from '@/domain/words/evaluators';
import { axeViolations, formatViolations } from '@/test/axe';
import { calibrationProgress, dryRunResult, evaluatorDetail } from '@/test/control/fixtures';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { API, installMswServer, server } from '@/test/msw';
import { stopLive } from '@/test/renderLive';

installMswServer();
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 4000 });

const escapeRe = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

const dialog = async (name: RegExp | string) => (await screen.findByRole('dialog', { name })) as HTMLElement;

describe('Calibrate now', { timeout: 20_000 }, () => {
  it('says what a fit can claim, fits without activating, and shows what came out', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/evaluators/groundedness');
    await user.click(await screen.findByRole('button', { name: 'Calibrate now for Groundedness' }));
    const d = await dialog('Calibrate Groundedness');
    expect(within(d).getByText(/measures agreement with labels, not ground truth/)).toBeInTheDocument();
    expect(within(d).getByText(/Deep-audit labels make it a Proxy, which can flag but never block/)).toBeInTheDocument();
    expect(within(d).getByText(/You have 43 labels \(31 correct, 12 false alarms\) and 58 deep-audit labels/)).toBeInTheDocument();
    await user.click(within(d).getByRole('radio', { name: /Your labels and the deep-audit sample/ }));
    await user.click(within(d).getByRole('button', { name: 'Fit calibration (free)' }));

    expect(await within(d).findByText('Calibration #7')).toBeInTheDocument();
    const [fit] = control.callsTo('/api/calibration/fit');
    expect(fit.body).toEqual({
      target: 'groundedness@1',
      route: 'support-bot',
      snapshot: 'typesafe/jev-1.13-20260917',
      sources: ['labels', 'uniform_audit'],
      method: 'auto',
      activate: false,
    });
    expect(within(d).getByText('Provisional')).toBeInTheDocument();
    expect(within(d).getByText('0.14 → 0.06')).toBeInTheDocument();
    // No block threshold: said in the dashboard's words, never the server's technical phrase.
    expect(within(d).getByText(EVAL_CRITICAL_NULL_TEXT)).toBeInTheDocument();
    expect(within(d).queryByText(/Fewer than 30 positive labels/)).toBeNull();
    expect(within(d).getByText('agreement with labels, not ground truth', { exact: false })).toBeInTheDocument();

    await user.click(within(d).getByRole('button', { name: 'Activate only' }));
    await waitFor(() => expect(control.callsTo('/api/calibration/7/activate')).toHaveLength(1));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(control.callsTo('/api/policies', 'PUT')).toHaveLength(0);
  });

  it('keeps the dialog open with the server message when the fit fails', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ calibrationFit: errorReply(503, 'persistence_disabled', 'Calibration needs a database.') });
    renderPage('/app/evaluators/groundedness', { control });
    await user.click(await screen.findByRole('button', { name: 'Calibrate now for Groundedness' }));
    const d = await dialog('Calibrate Groundedness');
    await user.click(within(d).getByRole('button', { name: 'Fit calibration (free)' }));
    const alert = await within(d).findByRole('alert');
    expect(alert).toHaveTextContent('The fit failed: Calibration needs a database.');
    expect(alert).toHaveTextContent('persistence_disabled');
  });

  it('says how many more labels a fit needs when the gateway has too few (422), without its refs', async () => {
    const user = userEvent.setup();
    // The gateway's body for #45 below 8 labels or 3 of each kind (cmd/proxy/calibration_api.go).
    const control = createControlApi({
      calibrationFit: errorReply(
        422,
        'validation_failed',
        'Not enough labels to calibrate groundedness@1: 5 (2 should fire, 3 should not); a fit needs 8 with at least 3 of each.',
        undefined,
        { names: ['groundedness@1'], n: 5, pos: 2, neg: 3, needed: { n: 8, per_class: 3 } },
      ),
    });
    renderPage('/app/evaluators/groundedness', { control });
    await user.click(await screen.findByRole('button', { name: 'Calibrate now for Groundedness' }));
    const d = await dialog('Calibrate Groundedness');
    await user.click(within(d).getByRole('button', { name: 'Fit calibration (free)' }));
    const alert = await within(d).findByRole('alert');
    expect(alert).toHaveTextContent(
      'Not enough labels to fit yet: 5 so far (2 correct, 3 false alarms). A fit needs at least 8 labels with 3 of each kind. Label more answers in the label queue, then try again.',
    );
    expect(alert).not.toHaveTextContent(/groundedness@1|should fire|validation_failed|The fit failed/);
  });

  it('offers a fit on the deep-audit labels when your own are too few, preselected and labelled Proxy', async () => {
    const user = userEvent.setup();
    const control = createControlApi();
    control.state.evaluatorDetails.groundedness = evaluatorDetail('groundedness', {
      calibration_progress: calibrationProgress('groundedness@1', { human_labels: { n: 2, pos: 1, neg: 1 }, proxy_labels: 58, can_fit_provisional: false }),
    });
    renderPage('/app/evaluators/groundedness', { control });
    await user.click(await screen.findByRole('button', { name: 'Calibrate now for Groundedness' }));
    const d = await dialog('Calibrate Groundedness');
    expect(within(d).getByRole('radio', { name: /Your labels and the deep-audit sample/ })).toBeChecked();
    expect(within(d).getByText('Not enough of your own labels for a fit yet: it needs 8, with 3 of each kind.')).toBeInTheDocument();
    await user.click(within(d).getByRole('button', { name: 'Fit calibration (free)' }));
    await waitFor(() => expect(control.callsTo('/api/calibration/fit')).toHaveLength(1));
    expect(control.callsTo('/api/calibration/fit')[0].body).toMatchObject({ sources: ['labels', 'uniform_audit'], activate: false });
  });

  it('says why Calibrate now is not available without labels or an admin', async () => {
    const control = createControlApi();
    control.state.evaluatorDetails.groundedness = evaluatorDetail('groundedness', {
      calibration_progress: calibrationProgress('groundedness@1', { human_labels: { n: 2, pos: 1, neg: 1 }, proxy_labels: 5, can_fit_provisional: false }),
    });
    renderPage('/app/evaluators/groundedness', { control });
    const button = await screen.findByRole('button', { name: 'Calibrate now' });
    expect(button).toHaveAttribute('aria-disabled', 'true');
    // Beside Calibrate now, and beside Adopt calibration in the header.
    expect(screen.getAllByText('Not enough labels for a first fit yet: label a few answers in the queue.')).toHaveLength(2);
    expect(screen.getByRole('button', { name: 'Adopt calibration' })).toHaveAttribute('aria-disabled', 'true');
    stopLive();
    cleanup();

    renderPage('/app/evaluators/groundedness', { status: { auth: { ingest: 'keys', read: 'keys', setup_required: false, caller_scopes: ['read'] } } });
    const label = await screen.findByRole('button', { name: 'Label queue' });
    expect(label).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getAllByText('Needs an admin key').length).toBeGreaterThan(0);
  });
});

describe('Adopt calibration', { timeout: 20_000 }, () => {
  it('stays disabled until Preview impact has loaded, then activates and saves, in that order', async () => {
    const user = userEvent.setup();
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const { control } = renderPage('/app/evaluators/groundedness');
    server.use(
      http.post(`${API}/api/policies/dry-run`, async ({ request }) => {
        control.calls.push({ method: 'POST', path: '/api/policies/dry-run', query: new URL(request.url).searchParams, body: await request.json(), headers: request.headers });
        await gate;
        return HttpResponse.json(dryRunResult());
      }),
    );
    await user.click(await screen.findByRole('button', { name: 'Adopt calibration' }));
    const d = await dialog(/Adopt calibration: Groundedness/);
    await user.click(within(d).getByRole('button', { name: 'Fit and preview (free)' }));

    const adopt = await within(d).findByRole('button', { name: 'Adopt calibration' });
    expect(adopt).toHaveAttribute('aria-disabled', 'true');
    expect(within(d).getByText('Wait for Preview impact to finish: Adopt runs it first.')).toBeInTheDocument();
    await user.click(adopt);
    expect(control.callsTo('/api/calibration/7/activate')).toHaveLength(0);

    release();
    await waitFor(() => expect(within(d).getByRole('button', { name: 'Adopt calibration' })).not.toHaveAttribute('aria-disabled', 'true'));
    expect(within(d).getByTestId('preview-impact')).toBeInTheDocument();
    expect(within(d).getByText(/This calibration is provisional/)).toBeInTheDocument();
    await user.click(within(d).getByRole('button', { name: 'Adopt calibration' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

    const order = control.calls.map((c) => `${c.method} ${c.path}`).filter((c) => /calibration|dry-run|PUT \/api\/policies/.test(c));
    expect(order).toEqual(['POST /api/calibration/fit', 'POST /api/policies/dry-run', 'POST /api/calibration/7/activate', 'PUT /api/policies']);
    expect(control.callsTo('/api/calibration/fit')[0].body).toMatchObject({ activate: false });
    expect(control.callsTo('/api/policies/dry-run')[0].body).toMatchObject({ window: '7d', calibration_ids: [7] });
    const put = control.callsTo('/api/policies', 'PUT')[0];
    expect(put.headers.get('if-match')).toBe('"v3"');
    const saved = parse((put.body as { yaml: string }).yaml) as { routes: { name: string; evaluators: { ref: string; thresholds?: object }[] }[] };
    const support = saved.routes.find((r) => r.name === 'support-bot');
    expect(support?.evaluators.find((e) => e.ref === 'groundedness@1')?.thresholds).toEqual({ space: 'calibrated', warn_at: 0.5, critical_at: null });
    expect((put.body as { message: string }).message).toBe('Adopt calibration #7 for groundedness@1 on support-bot');
  });

  it('keeps Adopt off after a failed preview until it is tried again', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ dryRun: errorReply(429, 'rate_limited', 'At most 6 previews a minute.') });
    renderPage('/app/evaluators/groundedness', { control });
    await user.click(await screen.findByRole('button', { name: 'Adopt calibration' }));
    const d = await dialog(/Adopt calibration: Groundedness/);
    await user.click(within(d).getByRole('button', { name: 'Fit and preview (free)' }));
    expect(await within(d).findByText('The preview failed: At most 6 previews a minute.')).toBeInTheDocument();
    expect(within(d).getByRole('button', { name: 'Adopt calibration' })).toHaveAttribute('aria-disabled', 'true');
    control.state.dryRun = dryRunResult();
    await user.click(within(d).getByRole('button', { name: 'Try the preview again' }));
    await waitFor(() => expect(within(d).getByRole('button', { name: 'Adopt calibration' })).not.toHaveAttribute('aria-disabled', 'true'));
  });

  it('says the calibration is active but the policy unchanged when the save is refused', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ save: errorReply(409, 'file_managed', 'The policy is managed by policies.yaml.') });
    renderPage('/app/evaluators/groundedness', { control });
    await user.click(await screen.findByRole('button', { name: 'Adopt calibration' }));
    const d = await dialog(/Adopt calibration: Groundedness/);
    await user.click(within(d).getByRole('button', { name: 'Fit and preview (free)' }));
    await waitFor(() => expect(within(d).getByRole('button', { name: 'Adopt calibration' })).not.toHaveAttribute('aria-disabled', 'true'));
    await user.click(within(d).getByRole('button', { name: 'Adopt calibration' }));
    expect(await within(d).findByRole('alert')).toHaveTextContent('The calibration is active, but the policy was not changed: The policy is managed by policies.yaml.');
    // A retry saves again without activating twice.
    control.state.save = 'echo';
    await user.click(within(d).getByRole('button', { name: 'Adopt calibration' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(control.callsTo('/api/calibration/7/activate')).toHaveLength(1);
  });

  it('says the policy moved on when the save answers 412, and never claims the activation did nothing', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ save: errorReply(412, 'stale_version', 'The policy changed since you loaded it.') });
    renderPage('/app/evaluators/groundedness', { control });
    await user.click(await screen.findByRole('button', { name: 'Adopt calibration' }));
    const d = await dialog(/Adopt calibration: Groundedness/);
    await user.click(within(d).getByRole('button', { name: 'Fit and preview (free)' }));
    await waitFor(() => expect(within(d).getByRole('button', { name: 'Adopt calibration' })).not.toHaveAttribute('aria-disabled', 'true'));
    await user.click(within(d).getByRole('button', { name: 'Adopt calibration' }));
    expect(await within(d).findByRole('alert')).toHaveTextContent(
      'The policy changed while you were reviewing, so it was not saved (the calibration is active, but routes that read raw scores are unchanged). Close this and adopt again to preview the latest policy.',
    );
    expect(control.callsTo('/api/calibration/7/activate')).toHaveLength(1);
  });

  it('cannot adopt for a check that runs on no route', async () => {
    const control = createControlApi();
    control.state.evaluatorDetails.groundedness = evaluatorDetail('groundedness', { attached_routes: [] });
    renderPage('/app/evaluators/groundedness', { control });
    expect(await screen.findByRole('button', { name: 'Adopt calibration' })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByText('Attach this check to a route in Policies first.')).toBeInTheDocument();
  });
});

describe('Calibration records', { timeout: 20_000 }, () => {
  it('lists this check’s calibrations in the Calibration tab, kept in the URL', async () => {
    const user = userEvent.setup();
    const { router } = renderPage('/app/evaluators/groundedness');
    await user.click(await screen.findByRole('tab', { name: 'Calibration' }));
    await waitFor(() => expect(router.state.location.search).toEqual({ tab: 'calibration' }));
    const list = await screen.findByRole('list', { name: 'Calibrations' });
    const rows = within(list).getAllByRole('listitem');
    expect(rows).toHaveLength(1);
    expect(rows[0]).toHaveTextContent('Calibration #3');
    expect(rows[0]).toHaveTextContent('Measured');
    expect(rows[0]).toHaveTextContent('Active');
    expect(rows[0]).toHaveTextContent(copy.evaluators.calibration_origin.file);
    // A file row is managed on disk.
    expect(within(rows[0]).getByRole('button', { name: 'Deactivate' })).toHaveAttribute('aria-disabled', 'true');
    expect(within(rows[0]).getByText(/remove or replace the file instead/)).toBeInTheDocument();
  });

  it('deactivates a fitted calibration after a confirmation', async () => {
    const user = userEvent.setup();
    const control = createControlApi();
    const list = control.state.calibrations as { items: { target_key: string; active: boolean; origin: string }[] };
    list.items[1] = { ...list.items[1], target_key: 'groundedness@1', active: true, origin: 'fit' };
    list.items[0].active = false;
    renderPage('/app/evaluators/groundedness?tab=calibration', { control });
    const row = (await screen.findByText('Calibration #7')).closest('li') as HTMLElement;
    expect(row).toHaveTextContent(copy.evaluators.calibration_origin.fit);
    await user.click(within(row).getByRole('button', { name: 'Deactivate' }));
    const confirm = await screen.findByRole('alertdialog', { name: 'Deactivate calibration #7?' });
    await user.click(within(confirm).getByRole('button', { name: 'Deactivate' }));
    await waitFor(() => expect(control.callsTo('/api/calibration/7/deactivate')).toHaveLength(1));
    // The list refreshes: the gateway brought the target's file calibration back (D55).
    await waitFor(() => expect(within(row).getByText('Not active')).toBeInTheDocument());
    const fileRow = screen.getByText('Calibration #3').closest('li') as HTMLElement;
    expect(within(fileRow).getByText('Active')).toBeInTheDocument();
  });

  it('adopts an active calibration from the list: Preview impact, then the policy save without activating again', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/evaluators/groundedness?tab=calibration');
    const row = (await screen.findByText('Calibration #3')).closest('li') as HTMLElement;
    await user.click(within(row).getByRole('button', { name: 'Adopt…' }));
    const d = await dialog(/Adopt calibration: Groundedness/);
    expect(await within(d).findByTestId('preview-impact')).toBeInTheDocument();
    await waitFor(() => expect(within(d).getByRole('button', { name: 'Adopt calibration' })).not.toHaveAttribute('aria-disabled', 'true'));
    await user.click(within(d).getByRole('button', { name: 'Adopt calibration' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

    expect(control.callsTo('/api/calibration/3')).toHaveLength(1);
    expect(control.callsTo('/api/policies/dry-run')[0].body).toMatchObject({ calibration_ids: [3] });
    // #3 is already active: only the policy changes.
    expect(control.callsTo('/api/calibration/3/activate')).toHaveLength(0);
    const put = control.callsTo('/api/policies', 'PUT')[0];
    const saved = parse((put.body as { yaml: string }).yaml) as { routes: { name: string; evaluators: { ref: string; thresholds?: object }[] }[] };
    expect(saved.routes.find((r) => r.name === 'support-bot')?.evaluators.find((e) => e.ref === 'groundedness@1')?.thresholds).toMatchObject({ space: 'calibrated' });
  });

  it('does not adopt a calibration fitted for another fast-check version', async () => {
    const control = createControlApi();
    const list = control.state.calibrations as { items: { snapshot: string }[] };
    list.items[0].snapshot = 'typesafe/jev-1.12-20260601';
    renderPage('/app/evaluators/groundedness?tab=calibration', { control });
    const row = (await screen.findByText('Calibration #3')).closest('li') as HTMLElement;
    expect(within(row).getByRole('button', { name: 'Adopt…' })).toHaveAttribute('aria-disabled', 'true');
    expect(within(row).getByText(/This one is for fast-check version typesafe\/jev-1\.12-20260601; the check now runs on typesafe\/jev-1\.13-20260917/)).toBeInTheDocument();
  });

  it('imports a calibration file and says what was refused and why', async () => {
    const user = userEvent.setup();
    const { control, container } = renderPage('/app/evaluators/groundedness?tab=calibration');
    await screen.findByRole('list', { name: 'Calibrations' });
    const input = container.querySelector('input[type="file"]') as HTMLInputElement;
    const file = new File([JSON.stringify({ schema: 'trustagent.calibration/v1', entries: [] })], 'jev.json', { type: 'application/json' });
    await user.upload(input, file);
    const status = (await screen.findByText('Imported 1 calibration; 2 skipped.')).closest('[role="status"]') as HTMLElement;
    // Each skipped entry in the contract's words (copy.evaluators.calibration_import_rejected), never the raw reason.
    expect(within(status).getByText(new RegExp(escapeRe(copy.evaluators.calibration_import_rejected.unknown_version)))).toBeInTheDocument();
    expect(within(status).getByText(new RegExp(escapeRe(copy.evaluators.calibration_import_rejected.mock_snapshot)))).toBeInTheDocument();
    expect(status).not.toHaveTextContent(/unknown_version|mock_snapshot/);
    // The server's own sentence stays behind "Technical detail".
    const details = within(status).getAllByText('Technical detail');
    expect(details).toHaveLength(2);
    expect(details[0].closest('details')).not.toHaveAttribute('open');
    expect(within(status).getByText('refusal has no version 2 on this gateway.').closest('details')).not.toBeNull();
    expect(control.callsTo('/api/calibration', 'POST')[0].body).toMatchObject({ activate: true, file: { schema: 'trustagent.calibration/v1' } });

    await user.upload(input, new File(['not json'], 'notes.txt', { type: 'application/json' }));
    expect(await screen.findByText(/notes.txt is not JSON/)).toBeInTheDocument();
  });

  it('explains a proxy without a database, an older proxy, an error and an empty list', async () => {
    renderPage('/app/evaluators/groundedness?tab=calibration', {
      control: createControlApi({ calibrations: errorReply(503, 'persistence_disabled', 'No database.') }),
    });
    expect(await screen.findByText('Calibration records need a database')).toBeInTheDocument();
    stopLive();
    cleanup();
    renderPage('/app/evaluators/groundedness?tab=calibration', { control: createControlApi({ calibrations: undefined }) });
    expect(await screen.findByText('Calibration records are not available on this proxy')).toBeInTheDocument();
    stopLive();
    cleanup();
    renderPage('/app/evaluators/groundedness?tab=calibration', { control: createControlApi({ calibrations: errorReply(500, 'internal', 'Boom.') }) });
    expect(await screen.findByText(/Boom\./)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Retry/ })).toBeInTheDocument();
    stopLive();
    cleanup();
    // The static build has no gateway: it says so, not that the proxy is old.
    renderPage('/app/evaluators/groundedness?tab=calibration', {
      control: createControlApi({ calibrations: errorReply(404, 'not_implemented', 'The static demo replays a recording, so there is no gateway to ask.') }),
    });
    expect(await screen.findByText('Calibration records need a running gateway')).toBeInTheDocument();
    expect(screen.getByText('The static demo replays a recording, so there is no gateway to ask.')).toBeInTheDocument();
    expect(screen.queryByText(/older version/)).toBeNull();
    stopLive();
    cleanup();
    renderPage('/app/evaluators/groundedness?tab=calibration', { control: createControlApi({ calibrations: { items: [] } }) });
    expect(await screen.findByText('No calibrations for this check yet')).toBeInTheDocument();
  });

  it('has no axe violations in the Calibration tab and the fit dialog', async () => {
    const user = userEvent.setup();
    const { container } = renderPage('/app/evaluators/groundedness?tab=calibration');
    await screen.findByRole('list', { name: 'Calibrations' });
    expect(formatViolations(await axeViolations(container))).toEqual([]);
    await user.click(screen.getAllByRole('button', { name: 'Calibrate now for Groundedness' })[0]);
    const d = await dialog('Calibrate Groundedness');
    expect(formatViolations(await axeViolations(d))).toEqual([]);
  });
});
