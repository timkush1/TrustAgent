import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';
import { ApiError } from '@/data/errors';
import { createHttpClient } from '@/data/http';
import { policyState } from '@/test/control/fixtures';
import { createControlApi } from '@/test/control/handlers';
import { API, installMswServer, server } from '@/test/msw';
import { s0Decision } from '@/test/scenarios';
import { LiveControlPlane } from './liveControlPlane';

installMswServer();

const plane = () => new LiveControlPlane(createHttpClient({ apiBase: API, crossOrigin: false }));

describe('LiveControlPlane', () => {
  it('reads the overview with the window in the query', async () => {
    const api = createControlApi();
    server.use(...api.handlers);
    const stats = await plane().getOverview({ window: '7d' });
    expect(stats.totals.requests).toBe(612);
    expect(api.callsTo('/api/overview')[0].query.get('window')).toBe('7d');
  });

  it('reads nil lists deep inside a policy state as empty lists', async () => {
    const raw = policyState();
    const broken = {
      ...raw,
      warnings: null,
      effective_routes: raw.effective_routes.map((r) => ({ ...r, downgrades: null, block_readiness: [{ evaluator: 'x', ready: false, missing: null }] })),
      file: { ...raw.file, errors: null },
    };
    server.use(http.get(`${API}/api/policies`, () => HttpResponse.json(broken)));
    const state = await plane().getPolicies();
    expect(state.warnings).toEqual([]);
    expect(state.effective_routes[0].downgrades).toEqual([]);
    expect(state.effective_routes[0].block_readiness[0].missing).toEqual([]);
    expect(state.file.errors).toEqual([]);
  });

  it('rejects a pre-v2 body as invalid_response', async () => {
    server.use(http.get(`${API}/api/policies`, () => HttpResponse.json({ routes: [] })));
    await expect(plane().getPolicies()).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('saves with If-Match and the CSRF header', async () => {
    const api = createControlApi();
    server.use(...api.handlers);
    const saved = await plane().savePolicy({ yaml: 'version: 1\n', message: 'Tighten support-bot' }, '"v3"');
    expect(saved.etag).toBe('"v4"');
    const call = api.callsTo('/api/policies', 'PUT')[0];
    expect(call.headers.get('if-match')).toBe('"v3"');
    expect(call.headers.get('x-trustagent-csrf')).toBe('1');
    expect(call.body).toEqual({ yaml: 'version: 1\n', message: 'Tighten support-bot' });
  });

  it('surfaces a stale version as a 412 ApiError', async () => {
    const api = createControlApi();
    server.use(...api.handlers);
    const err = await plane().savePolicy({ yaml: 'version: 1\n', message: 'x' }, '"v1"').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 412, code: 'stale_version' });
  });

  it('streams a playground run: tokens, the decision, then the result', async () => {
    const decision = s0Decision('refund-contradiction-flag');
    const done = { ...createControlApi().state.playground, decision };
    const sse = [
      ': trustagent {"action":"flag"}',
      '',
      'event: token',
      'data: {"text":"You can "}',
      '',
      'event: token',
      'data: {"text":"get a refund."}',
      '',
      'event: decision',
      `data: ${JSON.stringify(decision)}`,
      '',
      'event: done',
      `data: ${JSON.stringify(done)}`,
      '',
      '',
    ].join('\n');
    const api = createControlApi({ playgroundStream: sse });
    server.use(...api.handlers);
    const tokens: string[] = [];
    const comments: string[] = [];
    let decided = false;
    const result = await plane().streamPlayground(
      { mode: 'chat', messages: [{ role: 'user', content: 'Hi' }], stream: true },
      { onToken: (t) => tokens.push(t), onDecision: () => (decided = true), onComment: (c) => comments.push(c) },
    );
    expect(tokens.join('')).toBe('You can get a refund.');
    expect(decided).toBe(true);
    expect(comments).toEqual(['trustagent {"action":"flag"}']);
    expect(result.trace_id).toBe(decision.trace_id);
    expect((api.callsTo('/api/playground/run')[0].body as { stream: boolean }).stream).toBe(true);
  });

  it('turns an SSE error event into an ApiError with the server code', async () => {
    const api = createControlApi({
      playgroundStream: 'event: error\ndata: {"error":{"code":"upstream_error","message":"The model timed out."}}\n\n',
    });
    server.use(...api.handlers);
    await expect(plane().streamPlayground({ mode: 'chat', messages: [], stream: true }, {})).rejects.toMatchObject({
      code: 'upstream_error',
      message: 'The model timed out.',
    });
  });

  it('accepts a plain JSON answer to a stream request', async () => {
    const api = createControlApi();
    server.use(...api.handlers);
    const result = await plane().streamPlayground({ mode: 'chat', messages: [], stream: true }, {});
    expect(result.audit_status).toBe('queued');
  });

  it('reports a 200 that is neither a stream nor JSON as invalid_response', async () => {
    server.use(http.post(`${API}/api/playground/run`, () => new HttpResponse('<html>proxy error</html>', { headers: { 'Content-Type': 'text/html' } })));
    const err = await plane()
      .streamPlayground({ mode: 'chat', messages: [], stream: true }, {})
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ code: 'invalid_response' });
  });

  it('fails a stream that ends without a result', async () => {
    const api = createControlApi({ playgroundStream: 'event: token\ndata: {"text":"x"}\n\n' });
    server.use(...api.handlers);
    await expect(plane().streamPlayground({ mode: 'chat', messages: [], stream: true }, {})).rejects.toMatchObject({
      code: 'invalid_response',
    });
  });

  it('passes a 409 knowledge_disabled through', async () => {
    const api = createControlApi({ kbStats: { error: { status: 409, code: 'knowledge_disabled', message: 'The knowledge add-on is off.' } } });
    server.use(...api.handlers);
    await expect(plane().getKbStats()).rejects.toMatchObject({ status: 409, code: 'knowledge_disabled' });
  });
});
