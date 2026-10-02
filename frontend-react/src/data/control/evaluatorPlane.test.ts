/**
 * The evaluator plane over HTTP (EVW-1): every method's method, path, query and body, the 204
 * label writes, and the shape checks, against the msw evaluator handlers.
 */
import { QueryClient } from '@tanstack/react-query';
import { http as mswHttp, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';
import { createHttpClient } from '@/data/http';
import { qk, qkc } from '@/data/queries';
import { bumpProgress, evaluatorSideEffects, invalidateAfterLabel, qkcEvaluators } from '@/data/queries/evaluators';
import { calibrationProgress } from '@/test/control/fixtures';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { API, installMswServer, server } from '@/test/msw';
import { liveEvaluatorPlane } from './evaluatorPlane';

installMswServer();

const plane = () => liveEvaluatorPlane(createHttpClient({ apiBase: API, crossOrigin: false }));

function setup(overrides: Parameters<typeof createControlApi>[0] = {}) {
  const api = createControlApi(overrides);
  server.use(...api.handlers);
  return api;
}

describe('evaluator plane', () => {
  it('reads the glass box for one attempt', async () => {
    const api = setup();
    const view = await plane().getDecisionRequest('t-1', { attempt: 1 });
    expect(view.requests[0].model).toBe('typesafe/jev');
    expect(view.matches_recorded).toBe(true);
    const [call] = api.callsTo('/api/traces/t-1/decision-request');
    expect(call.query.get('attempt')).toBe('1');
  });

  it('answers 409 body_expired for a trace whose text expired', async () => {
    setup({ decisionRequests: { 't-2': errorReply(409, 'body_expired', 'The stored text expired.') } });
    await expect(plane().getDecisionRequest('t-2')).rejects.toMatchObject({ status: 409, code: 'body_expired' });
  });

  it('reads the label queue with its filters and writes and removes labels (204)', async () => {
    const api = setup();
    const queue = await plane().getLabelQueue({ evaluator: 'groundedness@1', window: '7d', limit: 50 });
    expect(queue.items).toHaveLength(2);
    const [q] = api.callsTo('/api/labels/queue');
    expect(Object.fromEntries(q.query)).toEqual({ evaluator: 'groundedness@1', window: '7d', limit: '50' });

    const first = queue.items[0];
    await expect(plane().putLabel({ trace_id: first.trace_id, target: first.evaluator_ref, label: 'should_fire', note: 'refund days wrong' })).resolves.toBeUndefined();
    expect(api.callsTo('/api/labels', 'PUT')[0].body).toEqual({ trace_id: first.trace_id, target: 'groundedness@1', label: 'should_fire', note: 'refund days wrong' });
    expect((await plane().getLabelQueue({})).items.map((i) => i.trace_id)).not.toContain(first.trace_id);

    await expect(plane().deleteLabel({ trace_id: first.trace_id, target: first.evaluator_ref })).resolves.toBeUndefined();
    expect(api.callsTo('/api/labels', 'DELETE')[0].body).toEqual({ trace_id: first.trace_id, target: 'groundedness@1' });
    expect((await plane().getLabelQueue({})).items.map((i) => i.trace_id)).toContain(first.trace_id);
  });

  it('refuses a label on an expired body with 409 body_expired', async () => {
    setup({ expiredTraces: ['t-old'] });
    await expect(plane().putLabel({ trace_id: 't-old', target: 'groundedness@1', label: 'should_not_fire' })).rejects.toMatchObject({
      status: 409,
      code: 'body_expired',
    });
  });

  it('lists, reads, imports, fits, activates and deactivates calibrations', async () => {
    const api = setup();
    const list = await plane().listCalibrations({ active: true });
    expect(list.items.map((r) => r.id)).toEqual([3]);
    expect(api.callsTo('/api/calibration')[0].query.get('active')).toBe('true');

    expect((await plane().getCalibration(7)).entry.method).toBe('platt');

    const fit = await plane().fitCalibration({ target: 'groundedness@1', route: 'support-bot', sources: ['labels'], method: 'auto', activate: false });
    expect(fit.record.active).toBe(false);
    expect(fit.recommended.warn_at?.value).toBe(0.5);
    expect(api.callsTo('/api/calibration/fit')[0].body).toMatchObject({ target: 'groundedness@1', activate: false, sources: ['labels'] });

    const activated = await plane().activateCalibration(fit.record.id);
    expect(activated.active).toBe(true);
    // One active record per target and snapshot: activating the fit deactivated the file row.
    const all = await plane().listCalibrations({});
    expect(all.items.filter((r) => r.target_key === 'groundedness@1' && r.active).map((r) => r.id)).toEqual([fit.record.id]);
    expect((await plane().deactivateCalibration(fit.record.id)).active).toBe(false);

    const imported = await plane().importCalibration({ file: { schema: 'trustagent.calibration/v1' } as never, activate: false });
    expect(imported.imported).toHaveLength(1);
    expect(imported.rejected.map((r) => r.reason)).toEqual(['unknown_version', 'mock_snapshot']);
  });

  it('validates and writes evaluators', async () => {
    const api = setup();
    const result = await plane().validateEvaluator({ spec: { id: 'x' } as never });
    expect(result.errors).toEqual([]);
    const created = await plane().createEvaluator({ spec: { id: 'tone_check' } as never, changelog: 'first' });
    expect(created.evaluator_id).toBe('tone_check');
    const next = await plane().createEvaluatorVersion('groundedness', { spec: { id: 'groundedness' } as never, changelog: 'tighter', base_version: 1 });
    expect(next.version).toBe(2);
    const archived = await plane().archiveEvaluator('refusal', { archived: true });
    expect(archived.archived).toBe(true);
    expect(api.callsTo('/api/evaluators/refusal/archive')[0].body).toEqual({ archived: true });
  });

  it('rejects a response without its lists as invalid_response', async () => {
    server.use(mswHttp.get(`${API}/api/labels/queue`, () => HttpResponse.json({ items: [{ trace_id: 1 }] })));
    await expect(plane().getLabelQueue({})).rejects.toMatchObject({ code: 'invalid_response' });
  });
});

describe('evaluator plane queries', () => {
  it('refreshes calibration records on calibration_updated, and nothing else on other events', () => {
    const qc = new QueryClient();
    qc.setQueryData(qkcEvaluators.calibrations({}), { items: [] });
    qc.setQueryData(qkcEvaluators.calibration(3), { id: 3 });
    qc.setQueryData(qkcEvaluators.labelQueue({}), { items: [] });
    evaluatorSideEffects({ type: 'trace_started' } as never, qc);
    expect(qc.getQueryState(qkcEvaluators.calibrations({}))?.isInvalidated).toBe(false);
    evaluatorSideEffects({ type: 'calibration_updated' } as never, qc);
    expect(qc.getQueryState(qkcEvaluators.calibrations({}))?.isInvalidated).toBe(true);
    expect(qc.getQueryState(qkcEvaluators.calibration(3))?.isInvalidated).toBe(true);
    expect(qc.getQueryState(qkcEvaluators.labelQueue({}))?.isInvalidated).toBe(false);
  });

  it('invalidates the queue, the progress and the trace after a label', () => {
    const qc = new QueryClient();
    qc.setQueryData(qkcEvaluators.labelQueue({}), { items: [] });
    qc.setQueryData(qkc.evaluators.detail('groundedness'), {});
    qc.setQueryData(qkc.evaluators.detail('refusal'), {});
    qc.setQueryData(qk.traces.detail('t-1'), {});
    invalidateAfterLabel(qc, { traceId: 't-1', evaluatorIds: ['groundedness'] });
    expect(qc.getQueryState(qkcEvaluators.labelQueue({}))?.isInvalidated).toBe(true);
    expect(qc.getQueryState(qkc.evaluators.detail('groundedness'))?.isInvalidated).toBe(true);
    expect(qc.getQueryState(qkc.evaluators.detail('refusal'))?.isInvalidated).toBe(false);
    expect(qc.getQueryState(qk.traces.detail('t-1'))?.isInvalidated).toBe(true);
  });

  it('moves calibration progress by one label either way', () => {
    const p = calibrationProgress('groundedness@1');
    const up = bumpProgress(p, 'should_not_fire', 1);
    expect(up.human_labels).toEqual({ n: p.human_labels.n + 1, pos: p.human_labels.pos, neg: p.human_labels.neg + 1 });
    expect(bumpProgress(up, 'should_not_fire', -1).human_labels).toEqual(p.human_labels);
  });
});
