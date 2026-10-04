import { act, configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { axeViolations, formatViolations } from '@/test/axe';
import { playgroundResult } from '@/test/control/fixtures';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { API, installMswServer, server } from '@/test/msw';
import { p3Detail } from '@/test/p3Fixtures';
import { stopLive } from '@/test/renderLive';
import { s0Decision, s0Trace } from '@/test/scenarios';
import { playgroundCommand } from './playgroundCurl';
import { buildRequest, EXAMPLES, formProblem, INITIAL_FORM, playgroundExamples } from './playgroundModel';
import { usePlaygroundStore } from './playgroundStore';

installMswServer();
beforeEach(() => {
  usePlaygroundStore.getState().reset();
  usePlaygroundStore.getState().setRunUnavailable(false);
});
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
    expect(refund?.form).toMatchObject({
      mode: 'audit',
      route: 'support-bot',
      response: 'You can get a full refund within 60 days of purchase.',
      scenario: 'refund-contradiction-flag',
    });
  });

  it('offers only the scenarios the form holds as they are', () => {
    const ids = EXAMPLES.map((e) => e.id);
    expect(ids.slice(0, 3)).toEqual(['grounded-pass', 'refund-contradiction-flag', 'no-evidence-not-assessed']);
    // Another request shape, tool calls, a developer message, the extension, streaming or knowledge.
    for (const id of [
      'anthropic-system-grounded',
      'completions-extension-grounded',
      'tool-results-grounded',
      'developer-role-unsupported',
      'extension-misquote-flag',
      'stream-passthrough-flag',
      'help-center-link-block',
      'knowledge-only-capped',
    ]) {
      expect(ids).not.toContain(id);
    }
    // The gateway enforces block and fallback: the p3 chat scenarios on those routes are examples.
    expect(ids).toEqual(expect.arrayContaining(['payments-fallback', 'help-center-uncalibrated-flag']));
    // With the knowledge add-on on, its scenarios are examples too; nothing else changes.
    const withKnowledge = playgroundExamples({ knowledge: true }).map((e) => e.id);
    expect(withKnowledge).toContain('knowledge-only-capped');
    expect(withKnowledge.filter((id) => id !== 'knowledge-only-capped')).toEqual(ids);
    expect(playgroundExamples({ knowledge: true }).find((e) => e.id === 'knowledge-only-capped')?.form).toMatchObject({ mode: 'audit', route: 'handbook' });
    expect(playgroundExamples()).toBe(EXAMPLES);
    expect(EXAMPLES.find((e) => e.id === 'payments-fallback')?.form).toMatchObject({
      mode: 'audit',
      route: 'payments-faq',
      scenario: 'payments-fallback',
    });
    for (const e of EXAMPLES) {
      for (const m of e.form.messages) {
        expect(['system', 'user', 'assistant']).toContain(m.role);
        expect(typeof m.content).toBe('string');
      }
    }
  });

  it('sends an example scenario only when asked, and forgets it once the example is edited', () => {
    const refund = EXAMPLES.find((e) => e.id === 'refund-contradiction-flag')!;
    expect(buildRequest(refund.form).scenario).toBeUndefined();
    expect(buildRequest(refund.form, { scenario: true }).scenario).toBe('refund-contradiction-flag');

    const store = usePlaygroundStore.getState();
    store.load(refund.form);
    store.set('modeOverride', 'block');
    store.set('model', 'gpt-4o');
    // Neither the mode to act as nor the model is scripted by the scenario.
    expect(usePlaygroundStore.getState().form.scenario).toBe('refund-contradiction-flag');
    store.set('response', 'Refunds within 30 days.');
    expect(usePlaygroundStore.getState().form.scenario).toBe('');
    store.load(refund.form);
    store.setMessage(1, { content: 'Something else?' });
    expect(usePlaygroundStore.getState().form.scenario).toBe('');
    store.load(refund.form);
    store.addDoc();
    expect(usePlaygroundStore.getState().form.scenario).toBe('');
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

  it('shows the fallback answer the app received in place of the model\'s, and the headers it got', async () => {
    const user = userEvent.setup();
    // payments-fallback on a P3 proxy: the contradicted answer was replaced by a checked fallback.
    const decision = p3Detail('payments-fallback').decisions[0];
    const result = playgroundResult({
      trace_id: decision.trace_id,
      response_text: 'The money will be back on your card within 2 business days.',
      delivered_text: 'Approved refunds go back to your original payment method within 5 to 10 business days.',
      decision,
      headers: {
        'X-TrustAgent-Trace-Id': decision.trace_id,
        'X-Request-ID': decision.trace_id,
        'X-TrustAgent-Action': 'fallback',
        'X-TrustAgent-Would-Action': 'block',
        'X-TrustAgent-Status': 'ok',
        'X-TrustAgent-Source': 'playground',
      },
    });
    renderPage('/app/playground', { control: createControlApi({ playground: result }) });
    await fillChat(user);
    await user.click(screen.getByRole('button', { name: 'Send the request' }));
    const timeline = await screen.findByRole('list', { name: 'What happened to the request' });
    expect(await within(timeline).findByText('Your app received a fallback answer instead')).toBeInTheDocument();
    expect(within(timeline).getByText(result.delivered_text)).toBeInTheDocument();
    expect(within(timeline).getByText('Replaced')).toBeInTheDocument();
    await user.click(within(timeline).getByText('Response headers your app saw'));
    const headers = within(timeline).getByRole('region', { name: '6 headers' });
    expect(headers).toHaveTextContent('X-TrustAgent-Action: fallback');
    expect(headers).toHaveTextContent(`X-Request-ID: ${decision.trace_id}`);
  });

  it('shows the block message the app received in place of a blocked answer', async () => {
    const user = userEvent.setup();
    const decision = p3Detail('help-center-link-block').decisions[0];
    const result = playgroundResult({
      trace_id: decision.trace_id,
      response_text: 'Open https://acme.example/reset-password and follow the steps. The reset link expires after 1 hour.',
      delivered_text: "I can't verify that answer against our help articles, so I've held it back.",
      decision,
      headers: { 'X-TrustAgent-Action': 'block', 'X-TrustAgent-Status': 'ok' },
    });
    renderPage('/app/playground', { control: createControlApi({ playground: result }) });
    await fillChat(user);
    await user.click(screen.getByRole('button', { name: 'Send the request' }));
    expect(await screen.findByText('Your app received the block message instead')).toBeInTheDocument();
    expect(screen.getByText(result.delivered_text)).toBeInTheDocument();
    expect(screen.getByText('Blocked')).toBeInTheDocument();
  });

  it('shows a failed fallback as the block the app received', async () => {
    const user = userEvent.setup();
    const decision = p3Detail('payments-fallback').decisions[0];
    const result = playgroundResult({
      trace_id: decision.trace_id,
      response_text: 'The money will be back on your card within 2 business days.',
      delivered_text: "I can't confirm that from our policy documents. A person will follow up.",
      decision,
      headers: { 'X-TrustAgent-Action': 'block', 'X-TrustAgent-Would-Action': 'block', 'X-TrustAgent-Status': 'ok' },
    });
    renderPage('/app/playground', { control: createControlApi({ playground: result }) });
    await fillChat(user);
    await user.click(screen.getByRole('button', { name: 'Send the request' }));
    const timeline = await screen.findByRole('list', { name: 'What happened to the request' });
    expect(await within(timeline).findByText('Your app received the block message instead')).toBeInTheDocument();
    expect(within(timeline).getByText('Blocked')).toBeInTheDocument();
    expect(within(timeline).queryByText('Replaced')).toBeNull();
  });

  it('shows the answer once when the app received it as the model wrote it', async () => {
    const user = userEvent.setup();
    renderPage('/app/playground');
    await fillChat(user);
    await user.click(screen.getByRole('button', { name: 'Send the request' }));
    const timeline = await screen.findByRole('list', { name: 'What happened to the request' });
    expect(await within(timeline).findAllByText(playgroundResult().delivered_text)).toHaveLength(1);
    expect(within(timeline).queryByText(/instead$/)).toBeNull();
  });

  it('shows the first token and a decision that arrives while the answer still streams (D69)', async () => {
    const user = userEvent.setup();
    const scripted = playgroundResult();
    // A real fast check (OpenRouter), so both times are measurements.
    const result = {
      ...scripted,
      decision: { ...scripted.decision, decision_model: { ...scripted.decision.decision_model!, backend: 'openrouter-decisions' as const } },
    };
    const sse = [
      'event: token',
      'data: {"text":"You can get "}',
      '',
      'event: decision',
      `data: ${JSON.stringify(result.decision)}`,
      '',
      'event: token',
      'data: {"text":"a full refund."}',
      '',
      'event: done',
      `data: ${JSON.stringify(result)}`,
      '',
      '',
    ].join('\n');
    renderPage('/app/playground', { control: createControlApi({ playgroundStream: sse }) });
    await fillChat(user);
    await user.click(screen.getByRole('switch', { name: 'Stream the answer' }));
    await user.click(screen.getByRole('button', { name: 'Send the request' }));
    expect(await screen.findByText(/^First token after \d+ ms$/)).toBeInTheDocument();
    expect(screen.getByText(/^Decided after \d+ ms, while the answer was still streaming$/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Measured: how “Time to first token” was produced' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Measured: how “When the fast check decided” was produced' })).toBeInTheDocument();
    expect(await screen.findByText('Flagged')).toBeInTheDocument();
  });

  it('labels the decision time Simulated when a mock fast check decided', async () => {
    const user = userEvent.setup();
    const mock = playgroundResult();
    expect(mock.decision.decision_model?.backend).toBe('mock');
    const sse = [
      'event: token',
      'data: {"text":"You can get a full refund."}',
      '',
      'event: decision',
      `data: ${JSON.stringify(mock.decision)}`,
      '',
      'event: done',
      `data: ${JSON.stringify(mock)}`,
      '',
      '',
    ].join('\n');
    renderPage('/app/playground', { control: createControlApi({ playgroundStream: sse }) });
    await fillChat(user);
    await user.click(screen.getByRole('switch', { name: 'Stream the answer' }));
    await user.click(screen.getByRole('button', { name: 'Send the request' }));
    expect(await screen.findByText(/^Decided after \d+ ms$/)).toBeInTheDocument();
    // Your model's first token is still a real measurement; the mock's decision time is not.
    expect(screen.getByRole('button', { name: 'Measured: how “Time to first token” was produced' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Simulated: how “When the fast check decided” was produced' })).toBeInTheDocument();
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
    // No decision event before done: the time to first token, but no mid-stream decision.
    expect(screen.getByText(/^First token after \d+ ms$/)).toBeInTheDocument();
    expect(screen.queryByText(/still streaming/)).toBeNull();
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
    // Audit mode has no /v1 equivalent: the command posts the same request to the Playground endpoint.
    const curl = screen.getByRole('region', { name: 'The same request with curl' });
    expect(curl.textContent).toContain('/api/playground/run');
    expect(curl.textContent).not.toContain('/api/audit');
    expect(curl.textContent).toContain('X-TrustAgent-Key: $TRUSTAGENT_ADMIN_KEY');
    expect(curl.textContent).toContain('"mode": "audit"');
    expect(curl.textContent).toContain('"response": "You can get a full refund within 60 days of purchase."');
    expect(screen.getByText(/set TRUSTAGENT_ADMIN_KEY to an admin key/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Check the answer' }));
    expect(await screen.findByText('Checked as written: no model was called to produce it.')).toBeInTheDocument();
    expect(control.callsTo('/api/playground/run')[0].body).toMatchObject({ mode: 'audit', route: 'support-bot' });
    // A live (non-demo) stack runs real checks: no scenario is sent.
    expect(control.callsTo('/api/playground/run')[0].body).not.toHaveProperty('scenario');
  });

  it('offers the knowledge examples only while the knowledge add-on is on', async () => {
    const user = userEvent.setup();
    renderPage('/app/playground', { status: { knowledge: { state: 'up', enabled: true } } });
    await user.click(await screen.findByRole('button', { name: 'Load an example' }));
    expect(await screen.findByRole('menuitem', { name: 'Knowledge-base evidence only: capped at warn' })).toBeInTheDocument();
  });

  it('leaves the knowledge examples out without the add-on', async () => {
    const user = userEvent.setup();
    renderPage('/app/playground', { status: { knowledge: { state: 'disabled', enabled: false } } });
    await user.click(await screen.findByRole('button', { name: 'Load an example' }));
    await screen.findByRole('menuitem', { name: 'Refund window: 60 days vs 30 days' });
    expect(screen.queryByRole('menuitem', { name: 'Knowledge-base evidence only: capped at warn' })).toBeNull();
  });

  it('sends the example scenario to a demo stack, whose mock checks script it', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/playground', { status: { demo: true } });
    await user.click(await screen.findByRole('button', { name: 'Load an example' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Refund window: 60 days vs 30 days' }));
    expect(screen.getByText(/this stack’s mock checks answer it with the scenario’s scripted results \(simulated\)/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Check the answer' }));
    await screen.findByText('Checked as written: no model was called to produce it.');
    expect(control.callsTo('/api/playground/run')[0].body).toMatchObject({ mode: 'audit', scenario: 'refund-contradiction-flag' });
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

  it('builds a command that reproduces each mode', () => {
    const chat = playgroundCommand(
      {
        mode: 'chat',
        route: 'support-bot',
        model: 'gpt-4o-mini',
        messages: [{ role: 'user', content: "It's late: refund?" }],
        evidence: [{ id: 'refund.md', text: '30 days.' }],
        stream: true,
        mode_override: 'block',
      },
      'http://localhost:3000/v1/',
    );
    expect(chat.endpoint).toBe('POST /v1/chat/completions');
    expect(chat.code.split('\n')[0]).toBe('curl -i -N http://localhost:3000/v1/chat/completions \\');
    expect(chat.code).toContain("-H 'X-TrustAgent-Route: support-bot'");
    expect(chat.code).toContain("-H 'X-TrustAgent-Mode: block'");
    // Shell-quoted: the apostrophe in the message survives the paste.
    expect(chat.code).toContain(`It'\\''s late: refund?`);
    expect(chat.code).toContain('"trustagent": {');

    const request = buildRequest(EXAMPLES.find((e) => e.id === 'refund-contradiction-flag')!.form, { scenario: true });
    const audit = playgroundCommand({ ...request, mode_override: 'flag' }, 'http://localhost:3000');
    expect(audit.endpoint).toBe('POST /api/playground/run');
    const body = JSON.parse(audit.code.slice(audit.code.indexOf("-d '") + 4, -1)) as unknown;
    // Exactly the body the page sends, so the run is reproduced as is.
    expect(body).toEqual({ ...request, mode_override: 'flag' });
    expect(audit.code).not.toContain('OPENAI_API_KEY');
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

  it('degrades on an older proxy that does not serve runs, and says why Run is then off', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ playground: errorReply(404, 'not_found', 'No endpoint here: POST /api/playground/run.') });
    renderPage('/app/playground', { control });
    await fillChat(user);
    await user.click(screen.getByRole('button', { name: 'Send the request' }));
    expect(await screen.findByText('This proxy does not run Playground requests')).toBeInTheDocument();
    expect(screen.getByText(/so it is older than this dashboard \(proxy version 2\.0\.0-test\)\. Update the proxy/)).toBeInTheDocument();
    expect(screen.queryByText(/P3/)).toBeNull();
    expect(screen.queryByText(/The run failed/)).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open Traffic' })).toHaveAttribute('href', '/app/traffic');
    const run = screen.getByRole('button', { name: 'Send the request' });
    expect(run).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByText('This proxy is older than the dashboard and does not run Playground requests')).toBeInTheDocument();
    // The curl equivalent still works through the gateway.
    expect(screen.getByRole('region', { name: 'The same request with curl' }).textContent).toContain('/v1/chat/completions');
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
