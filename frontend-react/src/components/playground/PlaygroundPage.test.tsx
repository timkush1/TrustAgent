import { act, configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { axeViolations, formatViolations } from '@/test/axe';
import { playgroundResult } from '@/test/control/fixtures';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { API, installMswServer, server } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import { s0Decision, s0Trace } from '@/test/scenarios';
import { buildRequest, EXAMPLES, formProblem, INITIAL_FORM } from './playgroundModel';
import { usePlaygroundStore } from './playgroundStore';

installMswServer();
beforeEach(() => usePlaygroundStore.getState().reset());
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 4000 });

async function fillChat(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await screen.findByRole('textbox', { name: 'Message 2' }), 'Can I get a refund after 45 days?');
}

describe('Playground model', () => {
  it('drops empty messages and documents and keeps only what the mode uses', () => {
    const req = buildRequest({
      ...INITIAL_FORM,
      mode: 'audit',
      route: 'support-bot',
      messages: [
        { role: 'system', content: '' },
        { role: 'user', content: 'Refund?' },
      ],
      evidence: [{ id: ' refund.md ', text: '30 days.' }, { id: '', text: ' ' }],
      response: '60 days.',
      stream: true,
      modeOverride: 'block',
    });
    expect(req).toEqual({
      mode: 'audit',
      route: 'support-bot',
      messages: [{ role: 'user', content: 'Refund?' }],
      evidence: [{ id: 'refund.md', text: '30 days.' }],
      response: '60 days.',
      mode_override: 'block',
    });
  });

  it('says what is missing before a run', () => {
    expect(formProblem(INITIAL_FORM, null)).toBe('Write a user message first.');
    const withUser = { ...INITIAL_FORM, messages: [{ role: 'user' as const, content: 'Hi' }] };
    expect(formProblem({ ...withUser, mode: 'audit' }, null)).toBe('Paste the answer to check first.');
    expect(formProblem(withUser, 1)).toMatch(/longer than this proxy checks/);
    expect(formProblem(withUser, null)).toBeNull();
  });

  it('turns the demo scenarios into audit examples', () => {
    const refund = EXAMPLES.find((e) => e.id === 'refund-contradiction-flag');
    expect(refund?.form).toMatchObject({ mode: 'audit', route: 'support-bot', response: 'You can get a full refund within 60 days of purchase.' });
  });
});

describe('Playground page', { timeout: 20_000 }, () => {
  it('runs a chat request and shows each step, then keeps the form', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/playground');
    await fillChat(user);
    await user.click(screen.getByRole('button', { name: 'Send the request' }));
    const timeline = await screen.findByRole('list', { name: 'What happened to the request' });
    expect(await within(timeline).findByText('HTTP 200 · 842 ms')).toBeInTheDocument();
    expect(within(timeline).getByText('Flagged')).toBeInTheDocument();
    expect(within(timeline).getByText('Contradicts your documents')).toBeInTheDocument();
    expect(within(timeline).getByText(/^Checked against: refund-policy.md/)).toBeInTheDocument();
    expect(within(timeline).getByRole('meter', { name: 'Contradiction risk' })).toBeInTheDocument();
    expect(within(timeline).getByText('Deep audit queued')).toBeInTheDocument();
    expect(within(timeline).getByRole('link', { name: /Open the full trace/ })).toHaveAttribute(
      'href',
      `/app/traffic/${playgroundResult().trace_id}`,
    );
    const body = control.callsTo('/api/playground/run')[0].body as { mode: string; messages: unknown[]; stream: boolean };
    expect(body).toMatchObject({ mode: 'chat', stream: false, messages: [{ role: 'user', content: 'Can I get a refund after 45 days?' }] });
    // The form is still filled for the next run.
    expect(screen.getByRole('textbox', { name: 'Message 2' })).toHaveValue('Can I get a refund after 45 days?');
  });

  it('follows the deep audit on the live stream', async () => {
    const user = userEvent.setup();
    const { ws } = renderPage('/app/playground');
    ws.connect();
    await fillChat(user);
    await user.click(screen.getByRole('button', { name: 'Send the request' }));
    await screen.findByText('Deep audit queued');
    act(() => {
      ws.send({ type: 'gate_decision', data: { trace: s0Trace('refund-contradiction-flag', 'audited'), decision: s0Decision('refund-contradiction-flag') } });
    });
    expect(await screen.findByText('Hallucinated')).toBeInTheDocument();
  });

  it('shows what the app received instead when the answer was replaced', async () => {
    const user = userEvent.setup();
    const control = createControlApi({
      playground: playgroundResult({ delivered_text: "I can't confirm that from our policy documents." }),
    });
    renderPage('/app/playground', { control });
    await fillChat(user);
    await user.click(screen.getByRole('button', { name: 'Send the request' }));
    expect(await screen.findByText('Your app received instead')).toBeInTheDocument();
    expect(screen.getByText("I can't confirm that from our policy documents.")).toBeInTheDocument();
  });

  it('streams tokens, then the decision and the result', async () => {
    const user = userEvent.setup();
    const result = playgroundResult();
    const sse = [
      'event: token',
      'data: {"text":"You can get "}',
      '',
      'event: token',
      'data: {"text":"a full refund."}',
      '',
      'event: done',
      `data: ${JSON.stringify(result)}`,
      '',
      '',
    ].join('\n');
    const control = createControlApi({ playgroundStream: sse });
    renderPage('/app/playground', { control });
    await fillChat(user);
    await user.click(screen.getByRole('switch', { name: 'Stream the answer' }));
    await user.click(screen.getByRole('button', { name: 'Send the request' }));
    expect(await screen.findByText('Flagged')).toBeInTheDocument();
    expect((control.callsTo('/api/playground/run')[0].body as { stream: boolean }).stream).toBe(true);
  });

  it('stops a streamed run when the viewer leaves the page', async () => {
    const user = userEvent.setup();
    let signal: AbortSignal | null = null;
    const { router } = renderPage('/app/playground');
    server.use(
      http.post(`${API}/api/playground/run`, ({ request }) => {
        signal = request.signal;
        // A stream that never ends on its own.
        const body = new ReadableStream<Uint8Array>({
          start(c) {
            c.enqueue(new TextEncoder().encode('event: token\ndata: {"text":"You can get "}\n\n'));
          },
        });
        return new HttpResponse(body, { headers: { 'Content-Type': 'text/event-stream' } });
      }),
    );
    await fillChat(user);
    await user.click(screen.getByRole('switch', { name: 'Stream the answer' }));
    await user.click(screen.getByRole('button', { name: 'Send the request' }));
    expect(await screen.findByText('You can get')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Stop' })).toBeInTheDocument();
    expect(signal!.aborted).toBe(false);
    await act(() => router.navigate({ to: '/app/overview' }));
    await waitFor(() => expect(signal!.aborted).toBe(true));
  });

  it('checks a pasted answer from an example, with no model call', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/playground');
    await user.click(await screen.findByRole('button', { name: 'Load an example' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Refund window: 60 days vs 30 days' }));
    expect(screen.getByRole('textbox', { name: 'The answer to check' })).toHaveValue('You can get a full refund within 60 days of purchase.');
    const curl = screen.getByRole('region', { name: 'The same request with curl' });
    expect(curl.textContent).toContain('/api/audit');
    expect(curl.textContent).toContain('$TRUSTAGENT_KEY');
    await user.click(screen.getByRole('button', { name: 'Check the answer' }));
    expect(await screen.findByText('Audit mode: no model call.')).toBeInTheDocument();
    expect(control.callsTo('/api/playground/run')[0].body).toMatchObject({ mode: 'audit', route: 'support-bot' });
  });

  it('shows the curl equivalent of the form with the key masked', async () => {
    const user = userEvent.setup();
    renderPage('/app/playground');
    await fillChat(user);
    await user.click(screen.getByRole('button', { name: 'Add document' }));
    await user.type(screen.getByRole('textbox', { name: 'Document 1 text' }), 'Refunds within 30 days.');
    const curl = screen.getByRole('region', { name: 'The same request with curl' });
    expect(curl.textContent).toContain('/v1/chat/completions');
    expect(curl.textContent).toContain('X-TrustAgent-Key: $TRUSTAGENT_KEY');
    expect(curl.textContent).toContain('Refunds within 30 days.');
  });

  it('explains a rate limit with the wait', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ playground: errorReply(429, 'rate_limited', 'Too many playground runs.', 6000) });
    renderPage('/app/playground', { control });
    await fillChat(user);
    await user.click(screen.getByRole('button', { name: 'Send the request' }));
    const alert = await screen.findByRole('alert');
    expect(within(alert).getByText('The run failed: Too many playground runs.')).toBeInTheDocument();
    expect(within(alert).getByText(/10 runs a minute; try again in 6 s/)).toBeInTheDocument();
  });

  it('links to Connections when the fast check has no credentials', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ playground: errorReply(503, 'credentials_unavailable', 'No fast-check key.') });
    renderPage('/app/playground', { control });
    await fillChat(user);
    await user.click(screen.getByRole('button', { name: 'Send the request' }));
    expect(await screen.findByRole('link', { name: 'Open Connections' })).toBeInTheDocument();
  });

  it('says why Run is unavailable', async () => {
    renderPage('/app/playground', { status: { readonly: true } });
    const run = await screen.findByRole('button', { name: 'Send the request' });
    expect(run).toHaveAttribute('aria-disabled', 'true');
    expect(await screen.findByText('Not available on a read-only instance')).toBeInTheDocument();
  });

  it('needs an admin, and a user message', async () => {
    renderPage('/app/playground', { status: { auth: { ingest: 'keys', read: 'keys', setup_required: false, caller_scopes: ['read'] } } });
    expect(await screen.findByText('Needs an admin key')).toBeInTheDocument();
    stopLive();
  });

  it('lists the policy routes and falls back to free text without them', async () => {
    const user = userEvent.setup();
    renderPage('/app/playground');
    const route = await screen.findByRole('combobox', { name: 'Route' });
    await user.click(route);
    expect(await screen.findByRole('option', { name: 'payments-faq' })).toBeInTheDocument();
  });

  it('uses a text field for the route when the policy cannot be read', async () => {
    renderPage('/app/playground', { control: createControlApi({ policies: errorReply(500, 'internal', 'x') }) });
    await waitFor(() => expect(screen.getByRole('textbox', { name: 'Route' })).toBeInTheDocument());
  });

  it('has no axe violations, before and after a run', async () => {
    const user = userEvent.setup();
    const { container } = renderPage('/app/playground');
    await screen.findByRole('combobox', { name: 'Route' });
    expect(formatViolations(await axeViolations(container))).toEqual([]);
    await fillChat(user);
    await user.click(screen.getByRole('button', { name: 'Send the request' }));
    await screen.findByText('Deep audit queued');
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});
