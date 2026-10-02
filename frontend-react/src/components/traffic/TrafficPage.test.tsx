/**
 * The Traffic list against the real data layer: LiveDataSource over msw (REST) and StreamClient
 * over a fake WebSocket (WS v2).
 */
import { configure, fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { delay, http, HttpResponse } from 'msw';
import { afterEach, describe, expect, it } from 'vitest';
import { copy } from '@/domain/presentation';
import { createFakeApi } from '@/test/apiHandlers';
import { API, installMswServer, server } from '@/test/msw';
import { renderLive, stopLive } from '@/test/renderLive';
import { REFUND_DOC, s0Audit, s0Decision, s0Trace, s0Traces, traceIdOf } from '@/test/scenarios';
import { trace } from '@/test/fixtures';

installMswServer();
afterEach(stopLive);
// The first render of a file loads the lazily split route modules; under a parallel run that can
// take longer than the 1 s default.
configure({ asyncUtilTimeout: 5000 });

const REFUND = 'refund-contradiction-flag' as const;

function table() {
  return screen.getByRole('table', { name: 'Requests' });
}

function dataRows() {
  return within(table())
    .getAllByRole('row')
    .filter((r) => r.hasAttribute('data-trace-id'));
}

function rowOf(traceId: string) {
  const row = dataRows().find((r) => r.getAttribute('data-trace-id') === traceId);
  if (!row) throw new Error(`no row for ${traceId}`);
  return row;
}

describe('Traffic list', () => {
  it('shows each request with its outcome, reason, documents and deep-audit state', async () => {
    const api = createFakeApi({ traces: s0Traces() });
    renderLive('/app/traffic', api);
    await waitFor(() => expect(dataRows()).toHaveLength(3));

    const flagged = rowOf(traceIdOf(REFUND));
    expect(within(flagged).getByText('Flagged')).toBeInTheDocument();
    expect(within(flagged).getByText('support-bot')).toBeInTheDocument();
    expect(within(flagged).getByText('gpt-4o-mini')).toBeInTheDocument();
    expect(within(flagged).getByText('Contradicts your documents')).toBeInTheDocument();
    expect(within(flagged).getByText(REFUND_DOC)).toBeInTheDocument();
    expect(within(flagged).getByText('188 ms')).toBeInTheDocument();
    expect(within(flagged).getByText('Hallucinated')).toBeInTheDocument();

    const notChecked = rowOf(traceIdOf('no-evidence-not-assessed'));
    expect(within(notChecked).getByText('Not checked')).toBeInTheDocument();
    expect(within(notChecked).getByText('No documents in this request')).toBeInTheDocument();
    expect(within(notChecked).getByText('No documents')).toBeInTheDocument();
    expect(within(notChecked).getByText('Not assessed')).toBeInTheDocument();

    const passed = rowOf(traceIdOf('grounded-pass'));
    expect(within(passed).getByText('Passed')).toBeInTheDocument();
    expect(within(passed).getByText('Grounded')).toBeInTheDocument();

    // Newest first, and the list asked for one page of the default size.
    expect(dataRows().map((r) => r.getAttribute('data-outcome'))).toEqual(['not_checked', 'flagged', 'passed']);
    expect(api.traceQueries[0].get('limit')).toBe('50');
  });

  it('opens the request when its row is clicked', async () => {
    const api = createFakeApi({ traces: s0Traces() });
    const { router } = renderLive('/app/traffic', api);
    await waitFor(() => expect(dataRows()).toHaveLength(3));
    await userEvent.click(within(rowOf(traceIdOf(REFUND))).getByRole('link', { name: /Flagged, open request/ }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/app/traffic/${traceIdOf(REFUND)}`));
  });

  it('shows the empty state with a next step', async () => {
    renderLive('/app/traffic', createFakeApi());
    expect(await screen.findByText(copy.states.empty_traffic)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open the Playground' })).toHaveAttribute('href', '/app/playground');
  });

  it('shows a loading state only after 200 ms', async () => {
    const api = createFakeApi({ traces: s0Traces() });
    renderLive('/app/traffic', api);
    server.use(
      http.get(`${API}/api/traces`, async () => {
        await delay(600);
        return HttpResponse.json({ items: s0Traces(), next_cursor: null });
      }),
    );
    expect(screen.queryByRole('status')).toBeNull();
    expect(await screen.findByText(copy.states.loading)).toBeInTheDocument();
    await waitFor(() => expect(dataRows()).toHaveLength(3), { timeout: 3000 });
  });

  it("shows the server's message with Retry, and recovers", async () => {
    const api = createFakeApi({
      traces: s0Traces(),
      tracesFailure: { status: 503, code: 'persistence_disabled', message: 'No database is configured.' },
    });
    renderLive('/app/traffic', api);
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent("Couldn't load this: No database is configured.");
    expect(alert).toHaveTextContent('HTTP 503 · persistence_disabled');
    api.state.tracesFailure = null;
    await userEvent.click(within(alert).getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(dataRows()).toHaveLength(3));
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('filters by outcome and reason through the API, and by reason locally too', async () => {
    const api = createFakeApi({ traces: s0Traces() });
    const { router } = renderLive('/app/traffic?outcome=flagged', api);
    await waitFor(() => expect(dataRows()).toHaveLength(1));
    expect(dataRows()[0]).toHaveAttribute('data-outcome', 'flagged');
    expect(api.traceQueries.at(-1)?.get('outcome')).toBe('flagged');

    await userEvent.click(screen.getByRole('link', { name: 'Clear filters' }));
    await waitFor(() => expect(dataRows()).toHaveLength(3));

    await router.navigate({ to: '/app/traffic', search: { reason: 'det:numbers_not_in_evidence' } });
    await waitFor(() => expect(dataRows()).toHaveLength(1));
    expect(screen.getByText('Mentions a number not in your documents')).toBeInTheDocument();
    // The reason goes to the proxy with its 30-day since (D68).
    const q = api.traceQueries.at(-1);
    expect(q?.get('reason')).toBe('det:numbers_not_in_evidence');
    const since = Date.parse(q?.get('since') ?? '');
    expect(Date.now() - since).toBeGreaterThanOrEqual(30 * 86_400_000);
    expect(Date.now() - since).toBeLessThan(30 * 86_400_000 + 120_000);

    await userEvent.click(screen.getByRole('link', { name: 'Remove the reason filter' }));
    await waitFor(() => expect(dataRows()).toHaveLength(3));
  });

  it('loads the next page as the end of the list comes into view', async () => {
    const many = Array.from({ length: 60 }, (_, i) =>
      trace({ trace_id: `t-${String(i).padStart(2, '0')}`, created_at: new Date(Date.UTC(2026, 8, 27, 12, 0, 60 - i)).toISOString() }),
    );
    const api = createFakeApi({ traces: many });
    const tall = window.innerHeight;
    Object.defineProperty(window, 'innerHeight', { value: 60 * 48, configurable: true });
    try {
      renderLive('/app/traffic', api);
      await waitFor(() => expect(dataRows()).toHaveLength(60));
      expect(api.traceQueries.map((q) => q.get('cursor'))).toEqual([null, '50']);
      expect(table()).toHaveAttribute('aria-rowcount', '61');
    } finally {
      Object.defineProperty(window, 'innerHeight', { value: tall, configurable: true });
    }
  });

  it('filters from the menus and says when nothing matches', async () => {
    const api = createFakeApi({ traces: s0Traces() });
    const { router } = renderLive('/app/traffic', api);
    await waitFor(() => expect(dataRows()).toHaveLength(3));

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Outcome' }), 'Not checked');
    await waitFor(() => expect(router.state.location.search).toEqual({ outcome: 'not_checked' }));
    await waitFor(() => expect(dataRows()).toHaveLength(1));

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Outcome' }), 'Blocked');
    expect(await screen.findByText('No requests match these filters')).toBeInTheDocument();

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Outcome' }), 'All outcomes');
    await waitFor(() => expect(dataRows()).toHaveLength(3));
    expect(screen.getByRole('combobox', { name: 'Route' })).toHaveTextContent('support-bot');
  });
});

describe('Traffic list, live', () => {
  it('adds new requests on top and updates them as the gate and the deep audit answer', async () => {
    const api = createFakeApi({ traces: s0Traces().filter((t) => t.trace_id !== traceIdOf(REFUND)) });
    const { ws } = renderLive('/app/traffic', api);
    await waitFor(() => expect(dataRows()).toHaveLength(2));
    ws.connect();

    // The refund scenario happens now, by a server clock 30 s ahead of this one.
    const now = new Date(Date.now() + 30_000).toISOString();
    const started = { ...s0Trace(REFUND, 'started'), created_at: now, updated_at: now };
    ws.send({ type: 'trace_started', data: { trace: started } }, started.trace_id);
    await waitFor(() => expect(dataRows()[0]).toHaveAttribute('data-outcome', 'checking'));
    expect(within(dataRows()[0]).getByText('Checking')).toBeInTheDocument();
    expect(within(dataRows()[0]).getByText('just now')).toBeInTheDocument();

    const decided = { ...s0Trace(REFUND, 'decided'), created_at: now, updated_at: new Date(Date.parse(now) + 5).toISOString() };
    ws.send({ type: 'gate_decision', data: { trace: decided, decision: s0Decision(REFUND) } }, decided.trace_id);
    await waitFor(() => expect(dataRows()[0]).toHaveAttribute('data-outcome', 'flagged'));
    expect(within(dataRows()[0]).getByText('Deep audit queued')).toBeInTheDocument();

    ws.send({ type: 'audit_completed', data: { audit: s0Audit(REFUND) } }, decided.trace_id);
    await waitFor(() => expect(within(dataRows()[0]).getByText('Hallucinated')).toBeInTheDocument());
    expect(dataRows()).toHaveLength(3);
  });

  it('pauses on the button: new requests wait behind the pill, shown rows still update', async () => {
    const api = createFakeApi({ traces: [s0Trace(REFUND, 'decided')] });
    const { ws, router } = renderLive('/app/traffic', api);
    await waitFor(() => expect(dataRows()).toHaveLength(1));
    ws.connect();

    const live = screen.getByRole('button', { name: 'Live' });
    expect(live).toHaveAttribute('aria-pressed', 'true');
    await userEvent.click(live);
    expect(live).toHaveAttribute('aria-pressed', 'false');
    await waitFor(() => expect(router.state.location.search).toEqual({ live: 0 }));
    expect(screen.getByRole('button', { name: 'Paused · Resume' })).toBeInTheDocument();

    const now = new Date().toISOString();
    const incoming = trace({ trace_id: 'incoming', created_at: now, updated_at: now });
    ws.send({ type: 'trace_started', data: { trace: incoming } }, 'incoming');
    // The row on screen keeps updating while paused.
    ws.send({ type: 'audit_completed', data: { audit: s0Audit(REFUND) } }, traceIdOf(REFUND));
    const pill = await screen.findByRole('button', { name: 'Paused · 1 new · Resume' });
    await waitFor(() => expect(within(rowOf(traceIdOf(REFUND))).getByText('Hallucinated')).toBeInTheDocument());
    expect(dataRows()).toHaveLength(1);

    await userEvent.click(pill);
    await waitFor(() => expect(dataRows()).toHaveLength(2));
    expect(dataRows()[0]).toHaveAttribute('data-trace-id', 'incoming');
    expect(live).toHaveAttribute('aria-pressed', 'true');
    expect(router.state.location.search).toEqual({});
    expect(screen.queryByRole('button', { name: /Paused/ })).toBeNull();
  });

  it('toggles with Space unless a control has focus, and opens paused from ?live=0', async () => {
    const api = createFakeApi({ traces: s0Traces() });
    renderLive('/app/traffic?live=0', api);
    await waitFor(() => expect(dataRows()).toHaveLength(3));
    const live = screen.getByRole('button', { name: 'Live' });
    expect(live).toHaveAttribute('aria-pressed', 'false');

    fireEvent.keyDown(document.body, { key: ' ' });
    await waitFor(() => expect(live).toHaveAttribute('aria-pressed', 'true'));

    // Space on a focused control belongs to the control.
    fireEvent.keyDown(screen.getByRole('combobox', { name: 'Outcome' }), { key: ' ' });
    expect(live).toHaveAttribute('aria-pressed', 'true');

    fireEvent.keyDown(document.body, { key: ' ' });
    await waitFor(() => expect(live).toHaveAttribute('aria-pressed', 'false'));
  });

  it('pauses when the reader scrolls away from the top', async () => {
    const api = createFakeApi({ traces: s0Traces() });
    renderLive('/app/traffic', api);
    await waitFor(() => expect(dataRows()).toHaveLength(3));
    const live = screen.getByRole('button', { name: 'Live' });
    Object.defineProperty(window, 'scrollY', { value: 400, configurable: true });
    try {
      fireEvent.scroll(window);
      await waitFor(() => expect(live).toHaveAttribute('aria-pressed', 'false'));
    } finally {
      Object.defineProperty(window, 'scrollY', { value: 0, configurable: true });
    }
  });
});
