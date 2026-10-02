/**
 * The Studio plane over HTTP (SPEC §2.4 #34-#38) against the msw Studio handlers: request bodies,
 * shape checks, and the dry run's SSE stream (D82: the first call never confirms, the 409 carries
 * the server's estimate; then estimate, progress and done; an `error` event; cancel by abort).
 */
import { http as mswHttp, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';
import { createHttpClient } from '@/data/http';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { API, installMswServer, server } from '@/test/msw';
import type { StudioDryRunEvent } from '@/types/api';
import { liveStudioPlane } from './studioPlane';

installMswServer();

const plane = () => liveStudioPlane(createHttpClient({ apiBase: API, crossOrigin: false }));

function serve(overrides: Parameters<typeof createControlApi>[0] = {}) {
  const api = createControlApi(overrides);
  server.use(...api.handlers);
  return api;
}

const SPEC_REF = 'blames_customer@1';

describe('Studio plane', () => {
  it('drafts, tests, loads samples and calibrates with POST bodies and checked shapes', async () => {
    const api = serve();
    const p = plane();
    const draft = await p.draftEvaluator({ description: 'Blames the customer', route: 'support-bot' });
    expect(draft.draft?.name).toBe('Blames the customer');
    expect(draft.cost_source).toBe('reported');
    expect(api.callsTo('/api/studio/draft')[0].body).toEqual({ description: 'Blames the customer', route: 'support-bot' });
    expect(api.callsTo('/api/studio/draft')[0].headers.get('x-trustagent-csrf')).toBe('1');

    const test = await p.testEvaluator({ ref: SPEC_REF, samples: [{ id: 'mine', assistant_response: 'Hi', expected: 'no_fire' }] });
    expect(test.results.map((r) => [r.sample_id, r.expected, r.agrees])).toEqual([['mine', 'no_fire', true]]);
    expect(test.summary.n).toBe(1);
    expect(api.callsTo('/api/studio/test')[0].body).toEqual({ ref: SPEC_REF, samples: [{ id: 'mine', assistant_response: 'Hi', expected: 'no_fire' }] });

    const samples = await p.getStudioSamples({ window: '7d', filter: 'flagged', limit: 20 });
    expect(samples.samples.map((s) => s.id)).toEqual(['t1', 't2']);
    expect(api.callsTo('/api/studio/samples')[0].body).toEqual({ window: '7d', filter: 'flagged', limit: 20 });

    const quick = await p.quickCalibrate({ labeled: [{ risk_raw: 0.2, label: false }] });
    expect(quick.unstable).toBe(true);
  });

  it('refuses a response without the fields a page reads', async () => {
    serve({ studioTest: { run_id: 'x', results: [] } as never });
    await expect(plane().testEvaluator({ samples: [] })).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('passes the server errors through with their codes', async () => {
    serve({ studioDraft: errorReply(503, 'no_judge', 'No judge is configured.') });
    await expect(plane().draftEvaluator({ description: 'x' })).rejects.toMatchObject({ status: 503, code: 'no_judge' });
  });

  it('asks for confirmation first: the 409 carries the estimate and the budget left (D82)', async () => {
    const api = serve();
    const error = await plane()
      .dryRunEvaluator({ ref: SPEC_REF, route: 'support-bot', window: '7d', max_samples: 200 }, {})
      .catch((e: unknown) => e);
    expect(error).toMatchObject({ status: 409, code: 'confirmation_required' });
    expect((error as { details: Record<string, unknown> }).details).toEqual({
      estimate_usd: 0.0021,
      samples: 200,
      population: 1840,
      decision_budget_left_usd: 1.84,
    });
    expect(api.callsTo('/api/studio/dry-run')[0].body).not.toHaveProperty('confirm_cost');
  });

  it('streams the estimate and progress, then resolves with the done result', async () => {
    const api = serve();
    const events: StudioDryRunEvent[] = [];
    const result = await plane().dryRunEvaluator(
      { ref: SPEC_REF, route: 'support-bot', window: '7d', max_samples: 200, confirm_cost: true },
      { onEstimate: (data) => events.push({ event: 'estimate', data }), onProgress: (data) => events.push({ event: 'progress', data }) },
    );
    expect(events.map((e) => e.event)).toEqual(['estimate', 'progress', 'progress']);
    expect(events[0].data).toEqual({ est_cost_usd: 0.0021, sampled: 200, population: 1840 });
    expect(result.fired).toBe(11);
    expect(result.fire_rate_ci95).toEqual([0.033, 0.102]);
    expect(api.callsTo('/api/studio/dry-run')[0].headers.get('accept')).toContain('text/event-stream');
  });

  it('turns an error event into an ApiError with the server code', async () => {
    serve({ studioDryRunConfirm: null, studioDryRunStreamError: { error: { code: 'budget_exhausted', message: 'Budget used up.' } } });
    await expect(plane().dryRunEvaluator({ ref: SPEC_REF, route: 'support-bot', window: '24h', max_samples: 50 }, {})).rejects.toMatchObject({
      code: 'budget_exhausted',
      message: 'Budget used up.',
    });
  });

  it('cancels a running test when its signal aborts, after reporting progress', async () => {
    serve({ studioDryRunConfirm: null, studioDryRunHold: true });
    const controller = new AbortController();
    const progress: number[] = [];
    const run = plane().dryRunEvaluator({ ref: SPEC_REF, route: 'support-bot', window: '7d', max_samples: 200 }, {
      onProgress: (data) => {
        progress.push(data.evaluated);
        if (data.evaluated === 200) controller.abort();
      },
    }, controller.signal);
    await expect(run).rejects.toMatchObject({ kind: 'aborted', code: 'aborted' });
    expect(progress).toEqual([100, 200]);
  });

  it('accepts a proxy that answers with the JSON result instead of a stream', async () => {
    const api = serve({ studioDryRunConfirm: null });
    const done = api.state.studioDryRun;
    server.use(mswHttp.post(`${API}/api/studio/dry-run`, () => HttpResponse.json(done)));
    const result = await plane().dryRunEvaluator({ ref: SPEC_REF, route: 'support-bot', window: '7d', max_samples: 200 }, {});
    expect(result.run_id).toBe('dr_01a0e7b4');
  });

  it('refuses a stream whose events are malformed or that ends without a result', async () => {
    const stream = (body: string) =>
      server.use(mswHttp.post(`${API}/api/studio/dry-run`, () => new HttpResponse(body, { headers: { 'Content-Type': 'text/event-stream' } })));
    const request = { ref: SPEC_REF, route: 'support-bot', window: '7d', max_samples: 10 } as const;
    stream('event: progress\ndata: {"evaluated":"ten"}\n\n');
    await expect(plane().dryRunEvaluator(request, { onProgress: () => {} })).rejects.toMatchObject({ code: 'invalid_response' });
    stream('event: estimate\ndata: not json\n\n');
    await expect(plane().dryRunEvaluator(request, {})).rejects.toMatchObject({ code: 'invalid_response' });
    stream('event: estimate\ndata: {"est_cost_usd":0.1,"sampled":10,"population":10}\n\n');
    await expect(plane().dryRunEvaluator(request, {})).rejects.toMatchObject({ code: 'invalid_response', message: expect.stringMatching(/without a result/) });
  });
});
