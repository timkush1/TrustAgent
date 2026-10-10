/**
 * The replay's glass box (D109): rebuildDecisionRequest over committed trace files, against the
 * gateway's rules for GET /api/traces/:id/decision-request (decision_request_api.go). The
 * simulated tape's "rebuild is deterministic" check runs through the data source in replay.test.ts.
 */
import { describe, expect, it } from 'vitest';
import { policy } from 'virtual:trustagent/policy';
import type { PolicyInput } from '@/lib/gate/types';
import { committedDetail, scenarioTrace } from '@/test/recording';
import type { TraceDetail } from '@/types/api';
import { ApiError } from '../errors';
import { rebuildDecisionRequest } from './glassBox';

const refund = () => committedDetail(scenarioTrace('refund-contradiction-flag').trace_id);

function withBody(detail: TraceDetail, body: Partial<TraceDetail['body']>): TraceDetail {
  return { ...detail, body: { ...detail.body, ...body } };
}

function rejection(fn: () => unknown): ApiError {
  try {
    fn();
  } catch (err) {
    if (err instanceof ApiError) return err;
    throw err;
  }
  throw new Error('expected an ApiError');
}

describe('rebuildDecisionRequest', () => {
  it('rebuilds the refund check: the documents it read, the questions it was asked, the recorded fingerprints', () => {
    const detail = refund();
    const view = rebuildDecisionRequest(detail, 0, policy);
    const recorded = detail.decisions[0].compile?.request_sha256 ?? [];
    expect(recorded.length).toBeGreaterThan(0);
    expect(view).toMatchObject({ matches_recorded: true, mismatch_reason: null, request_sha256: recorded, cpt: detail.decisions[0].compile?.cpt });
    expect(view.requests).toHaveLength(recorded.length);
    // Addressed to the recorded model, and session_id never shown.
    expect(view.requests[0].model).toBe(detail.decisions[0].decision_model?.model_requested);
    expect(Object.keys(view.requests[0])).toEqual(['model', 'state', 'questions']);
    const asked = view.requests.flatMap((r) => Object.keys(r.questions));
    expect(asked.some((q) => q.startsWith('contradiction'))).toBe(true);
    expect(JSON.stringify(view.requests[0].state)).toContain('30 days');
    expect(JSON.stringify(view.requests[0].state)).toContain('60 days');
  });

  it('says the body differs when a sanitizer changed it, and that it was cut when it was', () => {
    const detail = refund();
    const scrubbed = withBody(detail, { response_text: (detail.body.response_text ?? '').replace('60 days', '[redacted]') });
    expect(rebuildDecisionRequest(scrubbed, 0, policy)).toMatchObject({ matches_recorded: false, mismatch_reason: null });
    const cut = rebuildDecisionRequest({ ...scrubbed, body: { ...scrubbed.body, truncated: true } }, 0, policy);
    expect(cut).toMatchObject({ matches_recorded: false, mismatch_reason: 'body_truncated' });
    // What it rebuilt from the cut text is still shown.
    expect(cut.requests.length).toBeGreaterThan(0);
    // A truncated body that still rebuilds the same requests is a match.
    expect(rebuildDecisionRequest(withBody(detail, { truncated: true }), 0, policy).matches_recorded).toBe(true);
  });

  it('names a route the policy lacks, and an evaluator with no built-in template', () => {
    const detail = refund();
    const others: PolicyInput = { ...policy, routes: (policy.routes ?? []).filter((r) => r.name !== detail.route) };
    expect(rebuildDecisionRequest(detail, 0, others)).toMatchObject({ matches_recorded: false, mismatch_reason: 'policy_missing', requests: [] });
    const custom: PolicyInput = {
      ...policy,
      routes: (policy.routes ?? []).map((r) => (r.name === detail.route ? { ...r, evaluators: [{ ref: 'competitor_mentions@3' }] } : r)),
    };
    expect(rebuildDecisionRequest(detail, 0, custom)).toMatchObject({ matches_recorded: false, mismatch_reason: 'evaluator_missing' });
    // A disabled one is not asked, so it is not missing.
    const disabled: PolicyInput = {
      ...policy,
      routes: (policy.routes ?? []).map((r) =>
        r.name === detail.route ? { ...r, evaluators: [...(r.evaluators as object[]), { ref: 'competitor_mentions@3', enabled: false }] } : r,
      ),
    };
    expect(rebuildDecisionRequest(detail, 0, disabled).mismatch_reason).toBeNull();
  });

  it('rebuilds a fallback attempt from the answer that was delivered', () => {
    const detail = refund();
    const d0 = detail.decisions[0];
    const twice: TraceDetail = { ...withBody(detail, { final_response_text: detail.body.response_text }), decisions: [d0, { ...d0, attempt: 1 }] };
    expect(rebuildDecisionRequest(twice, 1, policy).matches_recorded).toBe(true);
    const other = withBody(twice, { final_response_text: 'Annual plans can be refunded within 30 days of purchase.' });
    expect(rebuildDecisionRequest(other, 1, policy).matches_recorded).toBe(false);
    expect(rebuildDecisionRequest(other, 0, policy).matches_recorded).toBe(true);
  });

  it('answers 404 for an attempt the trace has no decision for, and 409 without a body', () => {
    const detail = refund();
    expect(rejection(() => rebuildDecisionRequest(detail, 1, policy))).toMatchObject({ status: 404, code: 'not_found' });
    expect(rejection(() => rebuildDecisionRequest({ ...detail, decisions: [] }, 0, policy))).toMatchObject({ status: 404 });
    expect(rejection(() => rebuildDecisionRequest(withBody(detail, { available: false, unavailable_reason: 'not_stored' }), 0, policy))).toMatchObject({
      status: 409,
      code: 'body_expired',
    });
  });

  it('asks nothing for an answer the fast check was not asked about', () => {
    const notChecked = committedDetail(scenarioTrace('no-evidence-not-assessed').trace_id);
    expect(notChecked.decisions[0].compile?.request_sha256 ?? []).toEqual([]);
    expect(rebuildDecisionRequest(notChecked, 0, policy)).toMatchObject({ requests: [], request_sha256: [], matches_recorded: true });
  });
});
