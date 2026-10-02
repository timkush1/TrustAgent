/**
 * The label queue (EVW-2, P4-PLAN D59-D61) against the real data layer over msw: the C/F/S keys,
 * optimistic calibration progress, focus after each step, notes, Undo, 409 body_expired, a redacted
 * answer, a failed save, and every page state.
 */
import { cleanup, configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { delay, http, HttpResponse } from 'msw';
import { afterEach, describe, expect, it } from 'vitest';
import { copy } from '@/domain/presentation';
import { axeViolations, formatViolations } from '@/test/axe';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { API, installMswServer, server } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import labelQueueFixture from '@contracts/fixtures/api/LabelQueue.default.json';
import type { EvaluatorDetail, LabelPut } from '@/types/api';

installMswServer();
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 4000 });

const FIRST = '01a0e781-0c1d-7a42-9b7e-3f5d2c8a1b64';
const QUESTION = 'Do you ship to Canada?';
const labelsMeter = () => screen.getByRole('meter', { name: 'Labels' });

async function firstItem() {
  expect(await screen.findByText(QUESTION)).toBeInTheDocument();
  await waitFor(() => expect(labelsMeter()).toHaveAttribute('aria-valuenow', '43'));
}

describe('Label queue', { timeout: 20_000 }, () => {
  it('labels with C, moves the progress at once, focuses the next answer and skips with S', async () => {
    const user = userEvent.setup();
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const control = createControlApi();
    const puts: LabelPut[] = [];
    renderPage('/app/evaluators/labels', { control });
    server.use(
      http.put(`${API}/api/labels`, async ({ request }) => {
        puts.push((await request.json()) as LabelPut);
        await gate;
        const detail = control.state.evaluatorDetails.groundedness as EvaluatorDetail;
        detail.calibration_progress = { ...detail.calibration_progress, human_labels: { n: 44, pos: 32, neg: 12 } };
        return new HttpResponse(null, { status: 204 });
      }),
    );
    await firstItem();
    expect(screen.getByRole('heading', { name: 'Should Groundedness flag this answer?' })).toBeInTheDocument();
    expect(screen.getByText('Checked against: shipping.md')).toBeInTheDocument();
    expect(screen.getByText('Answer 1 of 2')).toBeInTheDocument();

    await user.keyboard('c');
    // Before the server answers: the progress has moved and the next answer has the focus.
    expect(labelsMeter()).toHaveAttribute('aria-valuenow', '44');
    expect(await screen.findByText("This answer's text is hidden")).toBeInTheDocument();
    await waitFor(() => expect(document.activeElement).toHaveTextContent('Should Groundedness flag this answer?'));
    await waitFor(() => expect(puts).toEqual([{ trace_id: FIRST, target: 'groundedness@1', label: 'should_fire' }]));

    release();
    expect(await screen.findByText('Correct. Saved. Thanks — 156 more and this check can be calibrated.')).toBeInTheDocument();
    await waitFor(() => expect(labelsMeter()).toHaveAttribute('aria-valuenow', '44'));

    // A redacted answer has nothing to judge: C and F do nothing, S skips it.
    expect(screen.getByRole('button', { name: /^Correct/ })).toHaveAttribute('aria-disabled', 'true');
    await user.keyboard('f');
    expect(puts).toHaveLength(1);
    await user.keyboard('s');
    expect(await screen.findByText("You're through the queue")).toBeInTheDocument();
    expect(screen.getByText('This session: 1 labelled · 1 skipped')).toBeInTheDocument();
  });

  it('never takes a key typed into the note, and sends the note with the label', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/evaluators/labels');
    await firstItem();
    await user.type(screen.getByRole('textbox', { name: /Note/ }), 'Canada ships in 5 days, not next day');
    expect(control.callsTo('/api/labels', 'PUT')).toHaveLength(0);
    await user.click(screen.getByRole('button', { name: /^False alarm/ }));
    await waitFor(() => expect(control.callsTo('/api/labels', 'PUT')).toHaveLength(1));
    expect(control.callsTo('/api/labels', 'PUT')[0].body).toEqual({
      trace_id: FIRST,
      target: 'groundedness@1',
      label: 'should_not_fire',
      note: 'Canada ships in 5 days, not next day',
    });
    expect(await screen.findByText(/^False alarm\. Saved\./)).toBeInTheDocument();
  });

  it('undoes the last label: removes it, puts the progress back and shows the answer again', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/evaluators/labels');
    await firstItem();
    await user.keyboard('c');
    await screen.findByText(/^Correct\. Saved\./);
    await waitFor(() => expect(labelsMeter()).toHaveAttribute('aria-valuenow', '44'));
    await user.click(screen.getByRole('button', { name: 'Undo' }));
    await waitFor(() => expect(control.callsTo('/api/labels', 'DELETE')).toHaveLength(1));
    expect(control.callsTo('/api/labels', 'DELETE')[0].body).toEqual({ trace_id: FIRST, target: 'groundedness@1' });
    expect(await screen.findByText(QUESTION)).toBeInTheDocument();
    expect(screen.getByText('Label removed: Groundedness is back for you to judge again.')).toBeInTheDocument();
    await waitFor(() => expect(labelsMeter()).toHaveAttribute('aria-valuenow', '43'));
    await waitFor(() => expect(document.activeElement).toHaveTextContent('Should Groundedness flag this answer?'));
  });

  it('says an expired answer cannot be labelled, puts the progress back and moves on', async () => {
    const user = userEvent.setup();
    renderPage('/app/evaluators/labels', { control: createControlApi({ expiredTraces: [FIRST] }) });
    await firstItem();
    await user.keyboard('f');
    expect(await screen.findByText(`${copy.labels.body_expired} It was left out.`)).toBeInTheDocument();
    await waitFor(() => expect(labelsMeter()).toHaveAttribute('aria-valuenow', '43'));
    expect(screen.getByText("This answer's text is hidden")).toBeInTheDocument();
  });

  it('goes back to the answer with the server message when the save fails', async () => {
    const user = userEvent.setup();
    renderPage('/app/evaluators/labels', { control: createControlApi({ labelError: errorReply(500, 'internal', 'The database is unavailable.') }) });
    await firstItem();
    await user.keyboard('c');
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('The label was not saved: The database is unavailable.');
    expect(alert).toHaveTextContent('internal');
    expect(screen.getByText(QUESTION)).toBeInTheDocument();
    await waitFor(() => expect(labelsMeter()).toHaveAttribute('aria-valuenow', '43'));
  });

  it('narrows the queue to one check from the URL and the picker', async () => {
    const user = userEvent.setup();
    const { control, router } = renderPage('/app/evaluators/labels?evaluator=groundedness@1');
    await firstItem();
    expect(control.callsTo('/api/labels/queue')[0].query.get('evaluator')).toBe('groundedness@1');
    expect(control.callsTo('/api/labels/queue')[0].query.get('window')).toBe('7d');
    await user.click(screen.getByRole('combobox', { name: 'Check' }));
    await user.click(await screen.findByRole('option', { name: 'All checks' }));
    await waitFor(() => expect(router.state.location.search).toEqual({}));
    await waitFor(() => expect(control.callsTo('/api/labels/queue').at(-1)?.query.get('evaluator')).toBeNull());
  });

  it('shows the empty queue, an older proxy, an error with Retry and the admin requirement', async () => {
    renderPage('/app/evaluators/labels', { control: createControlApi({ labelQueue: { items: [] } }) });
    expect(await screen.findByText(copy.labels.empty)).toBeInTheDocument();
    stopLive();
    cleanup();

    renderPage('/app/evaluators/labels', { control: createControlApi({ labelQueue: undefined }) });
    expect(await screen.findByText('The label queue is not available on this proxy')).toBeInTheDocument();
    stopLive();
    cleanup();

    renderPage('/app/evaluators/labels', { control: createControlApi({ labelQueue: errorReply(500, 'internal', 'Queue query failed.') }) });
    expect(await screen.findByText(/Queue query failed\./)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Retry/ })).toBeInTheDocument();
    stopLive();
    cleanup();

    const { control } = renderPage('/app/evaluators/labels', { status: { auth: { ingest: 'keys', read: 'keys', setup_required: false, caller_scopes: ['read'] } } });
    expect(await screen.findByText('Labelling needs an admin key')).toBeInTheDocument();
    expect(within(screen.getByText('Labelling needs an admin key').closest('div')!.parentElement!).getByRole('link', { name: 'Sign in as an admin' })).toBeInTheDocument();
    expect(control.callsTo('/api/labels/queue')).toHaveLength(0);
  });

  it('shows a loading state after 200 ms, then the first answer', async () => {
    renderPage('/app/evaluators/labels');
    server.use(
      http.get(`${API}/api/labels/queue`, async () => {
        await delay(600);
        return HttpResponse.json(labelQueueFixture);
      }),
    );
    expect(await screen.findByText(copy.states.loading)).toBeInTheDocument();
    expect(await screen.findByText(QUESTION, {}, { timeout: 3000 })).toBeInTheDocument();
  });

  it('shows the Evaluators sections with the queue current', async () => {
    renderPage('/app/evaluators/labels');
    await firstItem();
    expect(screen.getByRole('link', { name: 'Label queue' })).toHaveAttribute('aria-current', 'page');
  });

  it('has no axe violations', async () => {
    const { container } = renderPage('/app/evaluators/labels');
    await firstItem();
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});
