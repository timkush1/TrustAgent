// @vitest-environment node
import { describe, expect, it } from 'vitest';
import policyYaml from '../../../../config/policies.yaml?raw';
import { parse as parseYaml } from 'yaml';
import scenarioPack from '@contracts/demo/scenarios.v1.json';
import type { RawAnswerInput } from '@/lib/decision/normalize';
import type { JevCall } from '@/lib/openrouter/types';
import { builtinSpecs, liveBlockMessage, liveRoute, liveRouteNames, LIVE_POLICY } from './livePolicy';
import { decisionRequestView, evidenceSummaryOf, gateDecisionView, playgroundResult } from './playgroundResult';
import { NOOP_LEDGER, runGate } from './runner';

interface PackScenario {
  id: string;
  route: string;
  request: unknown;
  response: string;
  mock_answers: Record<string, RawAnswerInput>;
}
const scenario = (id: string) => (scenarioPack as unknown as { scenarios: PackScenario[] }).scenarios.find((s) => s.id === id)!;

function answering(s: PackScenario, cost: number | null): JevCall {
  return {
    backend: 'openrouter-decisions',
    model: 'typesafe/jev-1.13',
    call: async (request) => ({
      answers: Object.fromEntries(Object.keys(request.body.questions).map((q) => [q, s.mock_answers[q]])),
      usage: { input_tokens: 400, cost },
      snapshot: 'typesafe/jev-1.13-20260917',
      latency_ms: 210,
    }),
  };
}

async function check(id: string, cost: number | null = 0.0000168, routeName?: string) {
  const s = scenario(id);
  const call = answering(s, cost);
  let t = 1_000;
  const gate = await runGate({
    route: liveRoute(routeName ?? s.route),
    specs: builtinSpecs(),
    request: s.request,
    response: s.response,
    apiFormat: 'openai_chat',
    call,
    ledger: NOOP_LEDGER,
    maxTokens: 8000,
    clock: () => (t += 150),
  });
  return { s, call, gate };
}

describe('the live policy (vite/plugins/policy.ts, D104)', () => {
  it('is config/policies.yaml, parsed at build time', () => {
    const yaml = parseYaml(policyYaml);
    expect(LIVE_POLICY).toEqual(yaml);
    expect(liveRouteNames()).toEqual(yaml.routes.map((r: { name: string }) => r.name));
  });

  it('resolves the routes the live scenarios run on, and the block messages', () => {
    expect(liveRoute('support-bot')).toMatchObject({ name: 'support-bot', mode: 'flag', fail: 'open' });
    expect(liveRoute('account-bot').evaluators.map((e) => e.ref)).toEqual(['groundedness@1', 'contradiction@1', 'pii_secret_leak@1']);
    expect(liveBlockMessage('support-bot')).toBe("I can't provide a verified answer to that right now.");
    expect(liveBlockMessage('payments-faq')).toBe("I can't confirm that from our policy documents. A person will follow up.");
    expect(liveBlockMessage('no-such-route')).toBeNull();
    expect(() => liveRoute('no-such-route')).toThrow(/no route/);
  });

  it('builtinSpecs holds every template by ref', () => {
    expect([...builtinSpecs().keys()]).toEqual(expect.arrayContaining(['groundedness@1', 'contradiction@1', 'pii_secret_leak@1', 'factual_claims@1']));
    expect(builtinSpecs()).toBe(builtinSpecs());
  });
});

describe('playgroundResult: a browser check as the Playground shows it', () => {
  it('builds a full decision/v1 view from runGate output', async () => {
    const { gate, call, s } = await check('refund-contradiction-flag');
    const view = gateDecisionView(gate, call, { traceId: 'live-1', createdAt: Date.UTC(2026, 9, 10), policyVersion: 1 });
    expect(view).toMatchObject({
      spec: 'decision/v1',
      trace_id: 'live-1',
      attempt: 0,
      created_at: '2026-10-10T00:00:00.000Z',
      route: 'support-bot',
      policy_version: 1,
      mode: 'flag',
      fail: 'open',
      action: 'flag',
      would_action: 'block',
      verdict: 'contradicted',
      late: false,
      gate_ms: 150,
    });
    expect(view.decision_model).toEqual({
      backend: 'openrouter-decisions',
      model_requested: 'typesafe/jev-1.13',
      snapshot: 'typesafe/jev-1.13-20260917',
      calls: 1,
      attempts: 1,
      hedged: false,
      latency_ms: 150,
      connect_ms: null,
      input_tokens: 400,
      cost_usd: 0.0000168,
      cost_source: 'reported',
    });
    expect(view.compile).toEqual({
      cpt: gate.compiled.cpt,
      requests: 1,
      question_set_sha256: gate.compiled.question_set_sha256,
      request_sha256: gate.compiled.request_sha256,
      packed_evidence_ids: gate.compiled.packed_ids,
      truncated_ids: [],
      est_tokens: gate.compiled.est_tokens,
    });
    expect(view.evidence_summary).toMatchObject({ configured: ['request_system', 'request_context', 'tool_results', 'extension'], knowledge: 'not_requested', truncated: false });
    expect(view.evidence_summary?.used).toEqual([{ source: 'request_system', items: 1, labels: [expect.any(String)], tokens: expect.any(Number) }]);
    expect(view.detectors.find((d) => d.id === 'numbers_not_in_evidence')?.fired).toBe(true);

    const result = playgroundResult(gate, call, s.response, { traceId: 'live-1' });
    expect(result).toMatchObject({ trace_id: 'live-1', upstream: null, response_text: s.response, delivered_text: s.response, headers: {}, audit_status: 'none' });
  });

  it('labels the cost reported, computed when priced here, or unknown', async () => {
    const reported = await check('grounded-pass', 0.00001);
    expect(gateDecisionView(reported.gate, reported.call).decision_model).toMatchObject({ cost_usd: 0.00001, cost_source: 'reported' });
    const none = await check('grounded-pass', null);
    expect(gateDecisionView(none.gate, none.call).decision_model).toMatchObject({ cost_usd: null, cost_source: 'unknown' });
    expect(gateDecisionView(none.gate, none.call, { computedCostUsd: 0.0000171 }).decision_model).toMatchObject({ cost_usd: 0.0000171, cost_source: 'computed' });
  });

  it('a check that asked nothing has no model usage', async () => {
    const s = scenario('grounded-pass');
    const gate = await runGate({ route: liveRoute('support-bot'), specs: builtinSpecs(), request: { model: 'm', messages: [{ role: 'user', content: 'hi' }] }, response: 'Hello!', apiFormat: 'openai_chat', call: answering(s, null), ledger: NOOP_LEDGER, maxTokens: 8000 });
    const view = gateDecisionView(gate, answering(s, null));
    expect(view.decision_model).toBeNull();
    expect(view.status).toBe('not_assessed');
    expect(evidenceSummaryOf(gate).used).toEqual([]);
    expect(decisionRequestView(gate.compiled).requests).toEqual([]);
  });

  it('a blocked answer delivers the route block message', async () => {
    const { gate, call, s } = await check('refund-contradiction-flag');
    const blocked = { ...gate, decision: { ...gate.decision, action: 'block' as const } };
    expect(playgroundResult(blocked, call, s.response, { blockMessage: liveBlockMessage('support-bot') }).delivered_text).toBe("I can't provide a verified answer to that right now.");
  });

  it('"Show exact Jev request" shows the bodies sent', async () => {
    const { gate } = await check('pii-leak-flag');
    const view = decisionRequestView(gate.compiled);
    expect(view.requests).toEqual(gate.compiled.requests);
    expect(view).toMatchObject({ request_sha256: gate.compiled.request_sha256, matches_recorded: true, mismatch_reason: null, cpt: gate.compiled.cpt });
    expect(JSON.stringify(view)).not.toContain('session_id');
  });

  it('a random trace id when none is given', async () => {
    const { gate, call } = await check('grounded-pass');
    expect(gateDecisionView(gate, call).trace_id).toMatch(/^[0-9a-f-]{36}$/);
  });
});
