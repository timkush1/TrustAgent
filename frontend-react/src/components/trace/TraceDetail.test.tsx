/**
 * The trace detail page against the real data layer (msw + a fake WebSocket), following the S0
 * demo scenarios from the gate decision to the completed deep audit.
 */
import { configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { delay, http, HttpResponse } from 'msw';
import { afterEach, describe, expect, it } from 'vitest';
import { copy } from '@/domain/presentation';
import { createFakeApi } from '@/test/apiHandlers';
import { API, installMswServer, server } from '@/test/msw';
import { p3Detail } from '@/test/p3Fixtures';
import { renderLive, stopLive } from '@/test/renderLive';
import { REFUND_DOC, auditIdOf, s0Audit, s0Decision, s0Detail, s0Trace, scenario, traceIdOf } from '@/test/scenarios';

installMswServer();
afterEach(stopLive);
// The first render of a file loads the lazily split route modules; under a parallel run that can
// take longer than the 1 s default.
configure({ asyncUtilTimeout: 5000 });

const REFUND = 'refund-contradiction-flag' as const;
const NO_EVIDENCE = 'no-evidence-not-assessed' as const;
const PASS = 'grounded-pass' as const;

const region = (name: string) => screen.getByRole('region', { name });

describe('Trace detail', () => {
  it('moves from checking to the gate decision to the deep audit claims', async () => {
    const id = traceIdOf(REFUND);
    const api = createFakeApi({ details: { [id]: s0Detail(REFUND, 'started') } });
    const { ws } = renderLive(`/app/traffic/${id}`, api);
    ws.connect();

    // 1. Stored, not decided yet.
    expect(await screen.findByText(scenario(REFUND).response)).toBeInTheDocument();
    const outcome = region('Outcome');
    expect(within(outcome).getByText('Checking')).toBeInTheDocument();
    expect(within(outcome).getByText('Checking this answer…')).toBeInTheDocument();
    expect(within(region('Checks')).getByRole('status')).toHaveTextContent('Fast check (Jev) is checking this answer…');

    // 2. The gate decides: flagged, with reasons, checks and a queued deep audit.
    api.state.details[id] = s0Detail(REFUND, 'decided');
    ws.send({ type: 'gate_decision', data: { trace: s0Trace(REFUND, 'decided'), decision: s0Decision(REFUND) } }, id);
    await waitFor(() => expect(within(region('Outcome')).getByText('Flagged')).toBeInTheDocument());
    expect(within(region('Outcome')).getByText('Delivered with a risk warning to your app')).toBeInTheDocument();
    expect(within(region('Outcome')).getByText(`Checked against: ${REFUND_DOC}`)).toBeInTheDocument();
    expect(within(region('Outcome')).getByRole('list', { name: 'Reasons' })).toHaveTextContent(
      'Mentions a number not in your documents: 60',
    );

    const checks = within(region('Checks')).getByRole('list', { name: 'Checks' });
    const [groundedness, contradiction] = within(checks).getAllByRole('listitem');
    expect(groundedness).toHaveTextContent('Groundedness');
    expect(groundedness).toHaveTextContent('High risk');
    expect(within(groundedness).getByRole('meter', { name: 'Groundedness risk' })).toHaveAttribute(
      'aria-valuetext',
      '0.96 (flags at 0.80, blocks at 0.95)',
    );
    expect(within(contradiction).getByRole('meter', { name: 'Contradiction risk' })).toHaveAttribute('aria-valuenow', '0.98');

    const claims = region('Claims');
    expect(within(claims).getByRole('status')).toHaveTextContent(copy.trace.claims_pending);
    // The instant check's finding is marked at once, before any claim.
    const early = within(region('Answer')).getByTestId('answer-marked');
    expect(early.querySelector('mark[data-det="numbers_not_in_evidence"]')).toHaveTextContent(/^60$/);
    expect(early.querySelector('mark[data-v]')).toBeNull();

    // 3. The deep audit completes: one contradicted claim.
    api.state.details[id] = s0Detail(REFUND, 'audited');
    ws.send({ type: 'audit_completed', data: { audit: s0Audit(REFUND) } }, id);
    const list = await within(region('Claims')).findByRole('list', { name: 'Claims' });
    const [claim] = within(list).getAllByRole('listitem');
    expect(within(claim).getByText('Contradicted')).toBeInTheDocument();
    expect(claim).toHaveTextContent('A full refund is available within 60 days of purchase.');
    expect(within(region('Claims')).getByText('Hallucinated')).toBeInTheDocument();
    expect(within(region('Claims')).getByText('0 of 1 claims supported · 1 contradicted')).toBeInTheDocument();
    expect(within(region('Claims')).queryByRole('status')).toBeNull();

    // 4. The claim is marked in the answer at its UTF-16 span, and the mark links to its row.
    const answer = region('Answer');
    const mark = within(answer).getByRole('link', {
      name: '60 days, contradicted claim, instant checks: mentions a number not in your documents',
    });
    expect(mark.querySelector('mark[data-det="numbers_not_in_evidence"]')).toHaveTextContent(/^60$/);
    expect(mark.closest('mark')).toHaveAttribute('data-v', 'contradicted');
    // The marked answer still reads as the model's text.
    expect(within(answer).getByTestId('answer-marked')).toHaveTextContent(scenario(REFUND).response);
    await userEvent.click(mark);
    expect(claim).toHaveFocus();
    expect(within(answer).queryByText(/not located in text/)).toBeNull();
  });

  it('says a request without documents was not checked, and why', async () => {
    const id = traceIdOf(NO_EVIDENCE);
    renderLive(`/app/traffic/${id}`, createFakeApi({ details: { [id]: s0Detail(NO_EVIDENCE) } }));
    const outcome = await screen.findByRole('region', { name: 'Outcome' });
    expect(within(outcome).getByText('Not checked')).toBeInTheDocument();
    expect(within(outcome).getByText('Not checked: no documents in this request')).toBeInTheDocument();
    expect(within(outcome).getByText('Checked against: No documents')).toBeInTheDocument();

    const checks = within(region('Checks')).getByRole('list', { name: 'Checks' });
    expect(within(checks).getAllByText('Not checked — no documents')).toHaveLength(2);
    expect(within(region('Checks')).queryByRole('meter')).toBeNull();
    expect(within(region('Claims')).getByText('Not assessed')).toBeInTheDocument();
  });

  it('shows a stored, audited pass with its supported claims', async () => {
    const id = traceIdOf(PASS);
    renderLive(`/app/traffic/${id}`, createFakeApi({ details: { [id]: s0Detail(PASS) } }));
    const outcome = await screen.findByRole('region', { name: 'Outcome' });
    expect(within(outcome).getByText('Passed')).toBeInTheDocument();
    expect(within(outcome).getByText('No problems found')).toBeInTheDocument();
    expect(within(outcome).queryByRole('list', { name: 'Reasons' })).toBeNull();
    const checks = within(region('Checks')).getByRole('list', { name: 'Checks' });
    expect(within(checks).getAllByText('Low risk')).toHaveLength(2);
    const claims = within(region('Claims')).getByRole('list', { name: 'Claims' });
    expect(within(claims).getAllByText('Supported')).toHaveLength(2);
  });

  it('fetches the audit by id when the trace has finished but did not carry it', async () => {
    const id = traceIdOf(REFUND);
    const api = createFakeApi({
      details: { [id]: { ...s0Detail(REFUND), audit_detail: null } },
      audits: { [auditIdOf(REFUND)]: s0Audit(REFUND) },
    });
    renderLive(`/app/traffic/${id}`, api);
    const claims = await screen.findByRole('list', { name: 'Claims' });
    expect(within(claims).getByText('Contradicted')).toBeInTheDocument();
  });

  it('renders from the live stream when the request is not stored yet', async () => {
    const id = traceIdOf(REFUND);
    const { ws } = renderLive(`/app/traffic/${id}`, createFakeApi());
    expect(await screen.findByText('There is no request with this id')).toBeInTheDocument();
    ws.connect();
    ws.send({ type: 'gate_decision', data: { trace: s0Trace(REFUND, 'decided'), decision: s0Decision(REFUND) } }, id);
    const outcome = await screen.findByRole('region', { name: 'Outcome' });
    expect(within(outcome).getByText('Flagged')).toBeInTheDocument();
    // The list preview stands in for the stored answer.
    expect(screen.getByText(scenario(REFUND).response)).toBeInTheDocument();
    expect(screen.getByText('Preview: the full text is not stored for this request.')).toBeInTheDocument();
  });

  it('shows a loading state after 200 ms, then the request', async () => {
    const id = traceIdOf(PASS);
    renderLive(`/app/traffic/${id}`, createFakeApi());
    server.use(
      http.get(`${API}/api/traces/:id`, async () => {
        await delay(600);
        return HttpResponse.json(s0Detail(PASS));
      }),
    );
    expect(await screen.findByText(copy.states.loading)).toBeInTheDocument();
    expect(await screen.findByRole('region', { name: 'Outcome' }, { timeout: 3000 })).toHaveTextContent('Passed');
  });

  it("shows the server's message with Retry", async () => {
    const id = traceIdOf(PASS);
    const api = createFakeApi();
    renderLive(`/app/traffic/${id}`, api);
    server.use(
      http.get(`${API}/api/traces/:id`, () =>
        HttpResponse.json({ error: { code: 'forbidden', message: 'This key cannot read traces.' } }, { status: 403 }),
      ),
    );
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent("Couldn't load this: This key cannot read traces.");
    server.use(http.get(`${API}/api/traces/:id`, () => HttpResponse.json(s0Detail(PASS))));
    await userEvent.click(within(alert).getByRole('button', { name: 'Retry' }));
    expect(await screen.findByRole('region', { name: 'Outcome' })).toHaveTextContent('Passed');
  });

  it('shows a replaced answer: the checks on the model\'s answer, and what the user received instead', async () => {
    const detail = p3Detail('payments-fallback');
    renderLive(`/app/traffic/${detail.trace_id}`, createFakeApi({ details: { [detail.trace_id]: detail } }));
    const outcome = await screen.findByRole('region', { name: 'Outcome' });
    expect(within(outcome).getByText('Replaced')).toBeInTheDocument();
    expect(within(outcome).getByText('Replaced with an answer that sticks to your documents')).toBeInTheDocument();
    expect(within(outcome).getByText('Checked against: refund-timing.md')).toBeInTheDocument();
    // Reasons and checks describe the model's answer (attempt 0), not the fallback check.
    expect(within(outcome).getByRole('list', { name: 'Reasons' })).toHaveTextContent('Mentions a number not in your documents: 2');
    const checks = within(region('Checks')).getByRole('list', { name: 'Checks' });
    expect(within(checks).getAllByText('High risk')).toHaveLength(2);

    const answer = region('Answer');
    const received = within(answer).getByTestId('received-text');
    expect(received).toHaveTextContent('Approved refunds go back to your original payment method within 5 to 10 business days.');
    await userEvent.click(within(outcome).getByRole('link', { name: 'See what the user received instead' }));
    expect(received.parentElement).toHaveFocus();
    // The deep audit judged the model's answer.
    expect(within(region('Claims')).getByText('Hallucinated')).toBeInTheDocument();
  });

  it('shows a blocked answer with the block message the user received', async () => {
    const detail = p3Detail('help-center-link-block');
    renderLive(`/app/traffic/${detail.trace_id}`, createFakeApi({ details: { [detail.trace_id]: detail } }));
    const outcome = await screen.findByRole('region', { name: 'Outcome' });
    expect(within(outcome).getByText('Blocked')).toBeInTheDocument();
    expect(within(outcome).getByRole('list', { name: 'Reasons' })).toHaveTextContent(
      'Links to a URL not in your documents: https://acme.example/reset-password',
    );
    expect(within(region('Answer')).getByTestId('received-text')).toHaveTextContent(
      "I can't verify that answer against our help articles, so I've held it back.",
    );
  });

  it('keeps ids and timings in collapsed Details', async () => {
    const id = traceIdOf(REFUND);
    const { container } = renderLive(`/app/traffic/${id}`, createFakeApi({ details: { [id]: s0Detail(REFUND) } }));
    await screen.findByRole('region', { name: 'Outcome' });
    const details = container.querySelector('details');
    expect(details).not.toBeNull();
    expect(details).not.toHaveAttribute('open');
    await userEvent.click(within(details as HTMLElement).getByText('Details'));
    expect(details).toHaveAttribute('open');
    expect(details).toHaveTextContent(id);
    expect(details).toHaveTextContent(auditIdOf(REFUND));
    expect(details).toHaveTextContent('support-bot · policy version 1');
    expect(details).toHaveTextContent('188 ms');
    expect(details).toHaveTextContent('2.4 s');
    // What each tier ran on, and its cost with an honest label (the mock's cost is simulated).
    expect(details).toHaveTextContent('Fast check (Jev) modelmock/jevMock (simulated)');
    expect(details).toHaveTextContent('Fast check (Jev) cost$0Simulated');
  });
});
