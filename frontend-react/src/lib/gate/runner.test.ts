// @vitest-environment node
import { describe, expect, it, vi } from 'vitest';
import goldenText from '../../../../backend-go/internal/jev/testdata/request_openrouter-decisions.golden.json?raw';
import scenarioPack from '@contracts/demo/scenarios.v1.json';
import type { EvaluatorSpec } from '@/lib/contracts/types.gen';
import type { RawAnswerInput } from '@/lib/decision/normalize';
import { classifyStatus, liveErrorView, transportError } from '@/lib/openrouter/errors';
import type { JevCall, JevCallRequest, JevCallResult, UsageLedger } from '@/lib/openrouter/types';
import { memoryStorage } from '@/lib/openrouter/testing';
import { createUsageStore } from '@/lib/openrouter/usage';
import { builtinSpecs, liveRoute, LIVE_POLICY } from './livePolicy';
import { evidenceSummaryOf } from './playgroundResult';
import { resolveRoute, specMap } from './route';
import { GateRefusedError, NOOP_LEDGER, recordFailures, routeThresholds, runGate } from './runner';
import type { PolicyInput, ResolvedRoute, RunGateInput } from './types';

interface PackScenario {
  id: string;
  route: string;
  request: { messages: unknown[] };
  response: string;
  mock_answers: Record<string, RawAnswerInput>;
  expect: { action: string; would_action: string | null; status: string; verdict: string | null; max_risk: number | null; reasons: string[]; deep_audit: string; checked_against: string };
}
const pack = (scenarioPack as unknown as { scenarios: PackScenario[] }).scenarios;
const scenario = (id: string) => pack.find((s) => s.id === id)!;

const SNAPSHOT = 'typesafe/jev-1.13-20260917';

/** A JevCall answering from a table of answers (a fake OpenRouter). */
/** An answer of the question's own type (a low-risk first option). */
function plausibleAnswer(question: unknown): RawAnswerInput {
  const q = question as { type: string; criteria: unknown };
  if (q.type === 'noul') return { type: 'noul', noul: 0.1 };
  if (q.type === 'score') return { type: 'score', score: 0 };
  const options = Object.keys(q.criteria as Record<string, unknown>);
  return { type: 'choice', choice: options[0], probabilities: Object.fromEntries(options.map((o, i) => [o, i === 0 ? 1 : 0])) };
}

function fakeCall(
  answers: Record<string, RawAnswerInput>,
  opts: { fail?: (r: JevCallRequest) => unknown; cost?: number | null; answer?: (question: unknown) => RawAnswerInput } = {},
) {
  const requests: JevCallRequest[] = [];
  const call: JevCall = {
    backend: 'openrouter-decisions',
    model: 'typesafe/jev-1.13',
    call: vi.fn(async (request: JevCallRequest): Promise<JevCallResult> => {
      requests.push(request);
      const failure = opts.fail?.(request);
      if (failure !== undefined) throw failure;
      const out: Record<string, RawAnswerInput> = {};
      for (const [qid, q] of Object.entries(request.body.questions)) {
        if (answers[qid] !== undefined) out[qid] = answers[qid];
        else if (opts.answer) out[qid] = opts.answer(q);
      }
      return { answers: out, usage: { input_tokens: request.est_tokens, cost: opts.cost === undefined ? 0.00002 : opts.cost }, snapshot: SNAPSHOT, latency_ms: 180 };
    }),
  };
  return { call, requests };
}

function input(s: PackScenario, call: JevCall, over: Partial<RunGateInput> = {}): RunGateInput {
  return {
    route: liveRoute(s.route),
    specs: builtinSpecs(),
    request: s.request,
    response: s.response,
    apiFormat: 'openai_chat',
    call,
    ledger: NOOP_LEDGER,
    maxTokens: 8000,
    ...over,
  };
}

const ledger = (maxCalls = 100, maxTokens = 8000) => createUsageStore({ maxCalls, maxTokens, storage: memoryStorage(), target: null, price: () => null });

describe('runGate: the three live scenarios (D99) reproduce their expect blocks', () => {
  it.each(['grounded-pass', 'refund-contradiction-flag', 'pii-leak-flag'])('%s', async (id) => {
    const s = scenario(id);
    const { call } = fakeCall(s.mock_answers);
    const r = await runGate(input(s, call));
    const d = r.decision;
    expect({ action: d.action, would_action: d.would_action, status: d.status, verdict: d.verdict, max_risk: d.max_risk, reasons: d.reasons, deep_audit: d.deep_audit }).toEqual({
      action: s.expect.action,
      would_action: s.expect.would_action,
      status: s.expect.status,
      verdict: s.expect.verdict,
      max_risk: s.expect.max_risk,
      reasons: s.expect.reasons,
      deep_audit: s.expect.deep_audit,
    });
    const checked = evidenceSummaryOf(r).used.map((u) => `${u.source}:${u.items}`).join(',') || 'none';
    expect(checked).toBe(s.expect.checked_against);
    expect(r.request_status).toEqual({ '0': 'ok' });
    expect(r.snapshot).toBe(SNAPSHOT);
    expect(r.usage.cost).toBeCloseTo(0.00002, 12);
  });

  it('the live budget is min(24000, maxTokens)', async () => {
    const s = scenario('refund-contradiction-flag');
    const { call, requests } = fakeCall(s.mock_answers);
    const r = await runGate(input(s, call, { maxTokens: 8000 }));
    expect(Math.max(...r.compiled.est_tokens)).toBeLessThanOrEqual(8000);
    expect(requests.map((q) => q.est_tokens)).toEqual(r.compiled.est_tokens);
  });
});

describe('runGate: TS/Go request parity (D109)', () => {
  const golden = JSON.parse(goldenText) as Record<string, unknown>;

  /** Evaluator specs whose questions are the ones internal/jev/http_test.go sends. */
  const goldenSpecs = (): Map<string, ReturnType<typeof builtinSpecs> extends ReadonlyMap<string, infer S> ? S : never> => {
    const q = golden.questions as Record<string, { type: string; instructions: string; criteria: unknown }>;
    const doc = (id: string, fields: string[], requires: string): EvaluatorSpec =>
      ({
        spec_version: 'evaluator/v1',
        id,
        version: 1,
        builtin: false,
        name: id,
        category: 'quality',
        description: `${id} (Go golden)`,
        state_fields: fields,
        question: q[`${id}__v1`],
        evidence: { requires },
        signal: q[`${id}__v1`].type === 'choice' ? { risk_weights: { declines_or_deflects: 1 } } : { invert: false },
        outcomes: { warn_at: 0.8, critical_at: null, block_eligible: false, requires_corroboration: [], implies: [] },
        escalation: { escalate_from: null, numeric_or_date: false, on_error: 'ignore' },
      }) as unknown as EvaluatorSpec;
    return specMap([
      doc('groundedness', ['assistant_response', 'evidence'], 'required'),
      doc('refusal', ['user_request', 'assistant_response'], 'none'),
      doc('quality', ['assistant_response'], 'none'),
    ]);
  };

  it('the body runGate hands the transport equals the Go golden request, minus session_id and provider', async () => {
    const state = golden.state as { user_request: string; assistant_response: string; evidence: { id: string; text: string }[] };
    const policy: PolicyInput = {
      version: 1,
      routes: [{ name: 'golden', mode: 'observe', evaluators: [{ ref: 'groundedness@1' }, { ref: 'refusal@1' }, { ref: 'quality@1' }] }],
    };
    const request = {
      model: 'gpt-4o-mini',
      messages: [
        { role: 'system', content: `<document id="${state.evidence[0].id}">${state.evidence[0].text}</document>` },
        { role: 'user', content: state.user_request },
      ],
    };
    const { call, requests } = fakeCall({});
    await runGate({ route: resolveRoute(policy, 'golden'), specs: goldenSpecs(), request, response: state.assistant_response, apiFormat: 'openai_chat', call, ledger: NOOP_LEDGER, maxTokens: 8000 });
    expect(requests).toHaveLength(1);
    const { session_id: _sid, provider: _provider, ...want } = golden;
    expect(JSON.parse(JSON.stringify(requests[0].body))).toEqual(want);
  });
});

describe('runGate: error paths (gstudio Runner.Evaluate parity)', () => {
  it('a missing answer leaves that evaluator in error and the check partial', async () => {
    const s = scenario('refund-contradiction-flag');
    const { contradiction__v1: _dropped, ...rest } = s.mock_answers;
    const { call } = fakeCall(rest);
    const r = await runGate(input(s, call));
    expect(r.request_status).toEqual({ '0': 'ok' });
    expect(r.answers).not.toHaveProperty('contradiction__v1');
    expect(r.decision.status).toBe('partial');
    expect(r.decision.evaluators['contradiction@1']).toMatchObject({ status: 'error', reason: 'missing_answer' });
    expect(r.decision.evaluators['groundedness@1'].status).toBe('assessed');
  });

  it('a request that fails becomes its request status and the fail policy decides', async () => {
    const s = scenario('refund-contradiction-flag');
    const failures: [unknown, string][] = [
      [classifyStatus(402, new Headers(), JSON.stringify({ error: { message: 'Insufficient credits' } })), 'error:credits'],
      [classifyStatus(401, new Headers(), '{}'), 'error:auth'],
      [classifyStatus(429, new Headers({ 'retry-after': '1' }), '{}'), 'error:retryable'],
      [transportError('network', 0, new TypeError('Failed to fetch')), 'error:network'],
      [transportError('timeout', 0), 'timeout'],
      [new Error('a bug in the transport'), 'error:bad_response'],
    ];
    for (const [failure, status] of failures) {
      const { call } = fakeCall(s.mock_answers, { fail: () => failure });
      const r = await runGate(input(s, call));
      expect(r.request_status).toEqual({ '0': status });
      expect(r.decision.status).toBe(status === 'timeout' ? 'timeout' : 'error');
      // support-bot fails open: the answer passes, marked degraded.
      expect(r.route.fail).toBe('open');
      expect(r.decision.action).toBe('pass');
      expect(r.calls).toEqual([null]);
      expect(r.usage).toEqual({ input_tokens: 0, cost: null });
      expect(r.snapshot).toBeNull();
    }
  });

  it('a 402 mid-run: the other request still answers, nothing is counted for the failed one', async () => {
    // The rubric-only route reads no evidence, so a per-call cap below the one-request size splits
    // its questions over two requests (the state is repeated in each, the questions are not).
    const s = { ...scenario('grounded-pass'), route: 'assistant' };
    const ok = fakeCall({}, { answer: plausibleAnswer });
    const one = await runGate(input(s, ok.call));
    expect(one.compiled.requests).toHaveLength(1);
    let cap = one.compiled.est_tokens[0] - 1;
    while (cap > 400 && (await runGate(input(s, ok.call, { maxTokens: cap }))).compiled.requests.length < 2) cap -= 10;

    const credits = classifyStatus(402, new Headers(), JSON.stringify({ error: { code: 402, message: 'Insufficient credits' } }));
    const { call: inner } = fakeCall({}, { answer: plausibleAnswer, fail: (r) => (r.index === 1 ? credits : undefined) });
    const recorded = recordFailures(inner);
    const usage = ledger();
    const r = await runGate(input(s, recorded.call, { ledger: usage, maxTokens: cap }));
    expect(r.compiled.requests).toHaveLength(2);
    expect(r.request_status).toEqual({ '0': 'ok', '1': 'error:credits' });
    expect(r.decision.status).toBe('partial');
    expect(r.calls[0]).not.toBeNull();
    expect(r.calls[1]).toBeNull();
    expect(recorded.failures.get(1)).toBe(credits);
    expect(recorded.failures.has(0)).toBe(false);
    expect(liveErrorView(recorded.failures.get(1)).screen).toBe('no_credits');
    // One billable call; the 402 released its slot and is not a rejected response.
    expect(usage.getSnapshot()).toMatchObject({ calls: 1, rejected: 0, inflight: 0 });
  });

  it('over the cap: refused before any call', async () => {
    const s = scenario('grounded-pass');
    const usage = ledger(1);
    const { call } = fakeCall(s.mock_answers);
    await runGate(input(s, call, { ledger: usage }));
    expect(usage.getSnapshot().calls).toBe(1);
    const second = fakeCall(s.mock_answers);
    const err = await runGate(input(s, second.call, { ledger: usage })).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(GateRefusedError);
    expect((err as GateRefusedError).reason).toBe('cap_reached');
    expect(second.call.call).not.toHaveBeenCalled();
  });

  it('over the token cap: refused before any call', async () => {
    // A rubric question with its state cannot shrink below the cap (no evidence to pack away).
    const s = { ...scenario('grounded-pass'), route: 'assistant' };
    const { call } = fakeCall({}, { answer: plausibleAnswer });
    const usage = ledger(100, 300);
    const err = await runGate(input(s, call, { ledger: usage, maxTokens: 300 })).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(GateRefusedError);
    expect((err as GateRefusedError).reason).toBe('token_cap');
    expect(call.call).not.toHaveBeenCalled();
    expect(usage.getSnapshot()).toMatchObject({ calls: 0, inflight: 0 });
  });

  it('the rejected pause refuses before any call', async () => {
    const s = scenario('grounded-pass');
    const usage = ledger();
    for (let i = 0; i < 10; i++) {
      usage.reserve([1]);
      usage.settle(503);
    }
    const { call } = fakeCall(s.mock_answers);
    await expect(runGate(input(s, call, { ledger: usage }))).rejects.toMatchObject({ reason: 'rejected_pause' });
    expect(call.call).not.toHaveBeenCalled();
  });

  it('a cancelled check rejects with aborted and counts nothing', async () => {
    const s = scenario('grounded-pass');
    const usage = ledger();
    const forget = fakeCall(s.mock_answers, { fail: () => transportError('aborted', 0) });
    await expect(runGate(input(s, forget.call, { ledger: usage }))).rejects.toMatchObject({ code: 'aborted' });

    const controller = new AbortController();
    const slow: JevCall = {
      backend: 'openrouter-decisions',
      model: 'typesafe/jev-1.13',
      call: (_req, signal) => new Promise((_resolve, reject) => signal?.addEventListener('abort', () => reject(transportError('aborted', 0)))),
    };
    const p = runGate(input(s, slow, { ledger: usage, signal: controller.signal }));
    controller.abort();
    await expect(p).rejects.toMatchObject({ code: 'aborted' });

    const before = new AbortController();
    before.abort();
    const never = fakeCall(s.mock_answers);
    await expect(runGate(input(s, never.call, { ledger: usage, signal: before.signal }))).rejects.toMatchObject({ code: 'aborted' });
    expect(never.call.call).not.toHaveBeenCalled();
    expect(usage.getSnapshot()).toMatchObject({ calls: 0, rejected: 0, inflight: 0 });
  });

  it('settles every request once with its status and usage', async () => {
    const s = scenario('grounded-pass');
    const settle = vi.fn();
    const spy: UsageLedger = { reserve: () => ({ ok: true }), settle };
    const { call } = fakeCall(s.mock_answers);
    await runGate(input(s, call, { ledger: spy }));
    expect(settle.mock.calls).toEqual([[200, { input_tokens: expect.any(Number), cost: 0.00002 }]]);

    settle.mockClear();
    const withUsage = classifyStatus(500, new Headers(), JSON.stringify({ usage: { input_tokens: 77, cost: 0.000003 } }));
    const failing = fakeCall(s.mock_answers, { fail: () => withUsage });
    await runGate(input(s, failing.call, { ledger: spy }));
    expect(settle.mock.calls).toEqual([[500, { input_tokens: 77, cost: 0.000003 }]]);

    settle.mockClear();
    const empty = fakeCall({}, {});
    await runGate({ ...input(s, empty.call, { ledger: spy }), request: { model: 'm', messages: [{ role: 'user', content: 'hi' }] } });
    // support-bot asks nothing without documents: nothing reserved, nothing settled.
    expect(settle).not.toHaveBeenCalled();
  });
});

describe('runGate: route threshold overrides reach decide()', () => {
  it('a route warn_at below the score raises the level (Go Runner.Thresholds)', async () => {
    const s = scenario('grounded-pass');
    const base = liveRoute('support-bot');
    const route: ResolvedRoute = {
      ...base,
      evaluators: base.evaluators.map((e) => (e.ref === 'groundedness@1' ? { ...e, thresholds: { warn_at: 0.05 } } : e)) as ResolvedRoute['evaluators'],
    };
    expect(routeThresholds(route)).toEqual({ 'groundedness@1': { warn_at: 0.05 } });
    const { call } = fakeCall(s.mock_answers);
    const r = await runGate({ ...input(s, call), route });
    expect(r.decision.evaluators['groundedness@1'].level).toBe('warn');
    expect(r.decision.reasons).toContain('groundedness:warn');
    expect(r.decision.action).toBe('flag');
  });

  it('the live policy is the one the recording was built with', () => {
    expect(LIVE_POLICY.routes?.map((r) => r.name)).toEqual(expect.arrayContaining(['support-bot', 'account-bot']));
    expect(routeThresholds(liveRoute('support-bot'))).toEqual({});
  });
});
