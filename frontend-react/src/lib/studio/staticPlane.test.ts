// @vitest-environment node
/**
 * The static Studio plane (D107) on fakes: drafting through the tab's chat client (checked
 * as the gateway checks a draft, priced vendor or estimate, refused by the caps), testing through
 * runGate on a one-evaluator route (latest + 1, one slot per sample, rows like gstudio ResultOf,
 * fatal and per-sample failures), samples from the committed recording, quick calibration, and
 * the dry run that stays unavailable.
 */
import { describe, expect, it } from 'vitest';
import prompt from '@contracts/prompts/evaluator-drafter.v1.json';
import { BUILTIN_TEMPLATES } from '@/lib/decision';
import { ApiError, isApiError } from '@/data/errors';
import { testCostSource } from './costSource';
import { renderUserText } from './drafter';
import { chatAnswer, fakeChat, fakeJev, fakeLedger, fakeStudioLive } from './fakeLive';
import { createStaticStudioPlane, refusedError, type StudioTapeDeps } from './staticPlane';
import { staticRegistry } from './staticRegistry';
import { blankSpec } from './templates';
import { committedDetail, committedManifest, scenarioTrace } from '@/test/recording';
import type { EvaluatorSpec, StudioSample } from '@/types/api';

const MESSAGE = 'Run it yourself with make demo.';
const manifest = committedManifest();
const deps: StudioTapeDeps = {
  message: MESSAGE,
  traceIds: async () => manifest.traces.map((t) => t.trace_id),
  trace: async (id) => committedDetail(id),
};
const registry = staticRegistry({ started: new Date('2026-10-01T00:00:00Z') });

async function failure(p: Promise<unknown>): Promise<ApiError> {
  const err = await p.then(
    () => null,
    (e: unknown) => e,
  );
  if (!isApiError(err)) throw new Error(`expected an ApiError, got ${String(err)}`);
  return err;
}

// ------------------------------------------------------------------------------------- draft

const EXAMPLE = prompt.examples[0].output as unknown as Record<string, unknown>;

describe('draftEvaluator', () => {
  it('drafts through the tab key with the judge model, the built-ins as the registry and the route description', async () => {
    const chat = fakeChat([chatAnswer(JSON.stringify(EXAMPLE))]);
    const ledger = fakeLedger();
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({ chat, ledger }), registry });
    const route = registry.routes()[1];
    const out = await plane.draftEvaluator({ description: '  Flag answers that recommend a competitor.  ', route });
    expect(out).toMatchObject({ draft: EXAMPLE, errors: [], model: 'anthropic/claude-haiku-4.5', attempts: 1, cost_usd: 0.0054, cost_source: 'reported' });
    expect(chat.calls[0].model).toBe('anthropic/claude-haiku-4.5');
    expect(chat.calls[0].messages[1].content).toBe(
      renderUserText({ description: 'Flag answers that recommend a competitor.', routeName: route, routeDescription: registry.routeDescription(route) }, registry.ids()),
    );
    expect(ledger.reserved).toHaveLength(1);
  });

  it('prices a draft from the table when OpenRouter reports no cost (estimate)', async () => {
    const chat = fakeChat([chatAnswer(JSON.stringify(EXAMPLE), { usage: { input_tokens: 2000, output_tokens: 500, cost: null } })]);
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({ chat }), registry });
    const out = await plane.draftEvaluator({ description: 'Flag answers that recommend a competitor.' });
    // claude-haiku-4.5 on OpenRouter: $1 in and $5 out per million tokens (contracts/pricing).
    expect(out.cost_source).toBe('computed');
    expect(out.cost_usd).toBeCloseTo((2000 * 1 + 500 * 5) / 1e6, 12);
  });

  it('returns no draft, with the schema errors, when even the repair breaks the format', async () => {
    const broken = JSON.stringify({ ...EXAMPLE, category: 'misc' });
    const chat = fakeChat([chatAnswer(broken), chatAnswer(broken)]);
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({ chat }), registry });
    const out = await plane.draftEvaluator({ description: 'Flag answers that recommend a competitor.' });
    expect(out.draft).toBeNull();
    expect(out.attempts).toBe(2);
    expect(out.errors).toContainEqual(expect.objectContaining({ path: 'category' }));
    expect(out.cost_usd).toBeCloseTo(0.0108, 12);
  });

  it('says why when the answer was no JSON object at all', async () => {
    const chat = fakeChat([chatAnswer('I cannot.'), chatAnswer('Still no.')]);
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({ chat }), registry });
    const out = await plane.draftEvaluator({ description: 'Flag answers that recommend a competitor.' });
    expect(out.draft).toBeNull();
    expect(out.errors[0].message).toMatch(/^\(root\): the answer is not valid JSON/);
  });

  it('needs a key: without the chat client nothing is sent', async () => {
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({}), registry });
    const err = await failure(plane.draftEvaluator({ description: 'Flag answers that recommend a competitor.' }));
    expect(err).toMatchObject({ status: 503, code: 'no_judge' });
  });

  it('refuses at the cap with the ledger reason, before any call', async () => {
    const chat = fakeChat([]);
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({ chat, ledger: fakeLedger(0) }), registry });
    const err = await failure(plane.draftEvaluator({ description: 'Flag answers that recommend a competitor.' }));
    expect(err).toMatchObject({ code: 'cap_reached', details: { reason: 'cap_reached' } });
    expect(chat.calls).toHaveLength(0);
  });

  it('checks the description and the route like the gateway', async () => {
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({ chat: fakeChat([]) }), registry });
    expect(await failure(plane.draftEvaluator({ description: '   ' }))).toMatchObject({ status: 422, fieldErrors: [{ path: 'description' }] });
    expect(await failure(plane.draftEvaluator({ description: 'x'.repeat(2001) }))).toMatchObject({ status: 422 });
    expect(await failure(plane.draftEvaluator({ description: 'Flag things.', route: 'nope' }))).toMatchObject({ status: 422, fieldErrors: [{ path: 'route' }] });
  });
});

// -------------------------------------------------------------------------------------- test

/** A Yes/No check of the answer; the fake model says yes (0.95) when the answer contains "BAD". */
const spec: EvaluatorSpec = { ...blankSpec([]), id: 'mentions_bad', name: 'Mentions bad' };
const yesWhenBad = (state: Record<string, unknown>) => ({ type: 'noul' as const, noul: String(state.assistant_response).includes('BAD') ? 0.95 : 0.05 });

const samples: StudioSample[] = [
  { id: 's1', assistant_response: 'This is BAD.', expected: 'fire' },
  { id: 's2', assistant_response: 'This is fine.', expected: 'fire' },
  { id: 's3', assistant_response: 'All good here.' },
];

describe('testEvaluator', () => {
  it('runs each sample through the gate on a one-evaluator route and reports rows like the gateway', async () => {
    const jev = fakeJev(yesWhenBad, { cost: () => 0.00002 });
    const ledger = fakeLedger();
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({ jev, ledger }), registry });
    const out = await plane.testEvaluator({ spec, samples });

    expect(out.results.map((r) => [r.sample_id, r.status, r.fires, r.agrees])).toEqual([
      ['s1', 'assessed', true, true],
      ['s2', 'assessed', false, false],
      ['s3', 'assessed', false, null],
    ]);
    expect(out.results[0]).toMatchObject({ risk_raw: 0.95, level: 'warn', expected: 'fire', cost_usd: 0.00002 });
    expect(out.summary).toMatchObject({ n: 3, assessed: 3, agreement: 0.5, snapshot: 'jev-1.13-2026-09-01', backend: 'openrouter-decisions' });
    expect(out.summary.cost_usd).toBeCloseTo(0.00006, 12);
    expect(testCostSource(out)).toBe('reported');
    // One request per sample, each asking only the tested question, as a new check's version 1.
    expect(jev.requests).toHaveLength(3);
    for (const r of jev.requests) expect(Object.keys(r.body.questions)).toEqual(['mentions_bad__v1']);
    expect(ledger.reserved).toHaveLength(3);
    expect(ledger.settled.map((s) => s.status)).toEqual([200, 200, 200]);
  });

  it('compiles an edit of a built-in id as its next version (CompileUnsaved: latest + 1)', async () => {
    const jev = fakeJev(() => ({ type: 'choice', choice: 'answers_request', probabilities: { answers_request: 0.9, declines_or_deflects: 0.05, asks_clarifying_question: 0.03, nothing_to_answer: 0.02 } }));
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({ jev }), registry });
    const refusal = structuredClone(BUILTIN_TEMPLATES['refusal@1']);
    delete refusal.builtin;
    const out = await plane.testEvaluator({ spec: refusal, samples: [{ id: 'a', user_request: 'Reset my password', assistant_response: 'Click Forgot password.' }] });
    expect(Object.keys(jev.requests[0].body.questions)).toEqual([`refusal__v${registry.latest('refusal') + 1}`]);
    expect(out.results[0]).toMatchObject({ status: 'assessed', fires: false });
  });

  it('prices from the table when OpenRouter reports no cost (estimate)', async () => {
    const jev = fakeJev(yesWhenBad);
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({ jev }), registry });
    const out = await plane.testEvaluator({ spec, samples: samples.slice(0, 1) });
    // typesafe/jev-1.13 on OpenRouter: $0.042 per million input tokens.
    expect(out.results[0].cost_usd).toBeCloseTo((jev.requests[0].est_tokens * 0.042) / 1e6, 15);
    expect(testCostSource(out)).toBe('computed');
  });

  it('passes the documents and rules of a sample as the request carries them', async () => {
    const docSpec: EvaluatorSpec = {
      ...spec,
      id: 'reads_docs',
      state_fields: ['assistant_response', 'evidence'],
      question: { type: 'noul', instructions: 'Is `assistant_response` unsupported by `evidence`?', criteria: { true: 'Unsupported', false: 'Supported' } },
      evidence: { requires: 'required', min_chars: 10 },
    };
    const jev = fakeJev(() => ({ type: 'noul', noul: 0.1 }));
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({ jev }), registry });
    const out = await plane.testEvaluator({
      spec: docSpec,
      samples: [
        { id: 'with', assistant_response: 'Refunds take 30 days.', evidence: [{ id: 'kb1', text: 'Refunds are processed within 30 days.' }] },
        { id: 'without', assistant_response: 'Refunds take 30 days.' },
      ],
    });
    expect(jev.requests).toHaveLength(1);
    expect(JSON.stringify(jev.requests[0].body.state)).toContain('Refunds are processed within 30 days.');
    expect(out.results.map((r) => r.status)).toEqual(['assessed', 'not_assessed']);
    expect(out.results[1].fires).toBeNull();
  });

  it('gives the gate a sample’s own rules, and the template default without them (D151)', async () => {
    const commitments = structuredClone(BUILTIN_TEMPLATES['commitments@1']);
    const fallback = (commitments.params as Record<string, { default: string }>).allowed_commitments.default;
    const jev = fakeJev(() => ({
      type: 'choice',
      choice: 'permitted_commitment',
      probabilities: { no_commitment: 0.05, describes_policy_only: 0.05, permitted_commitment: 0.85, unapproved_commitment: 0.05 },
    }));
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({ jev }), registry });
    const own = 'Annual plans: refund within 30 days of purchase.';
    const out = await plane.testEvaluator({
      spec: commitments,
      samples: [
        { id: 'own', assistant_response: 'You can get a refund within 30 days.', rules: { allowed_commitments: own } },
        { id: 'default', assistant_response: 'You can get a refund within 30 days.' },
      ],
    });
    expect(out.results.map((r) => r.status)).toEqual(['assessed', 'assessed']);
    const states = jev.requests.map((r) => JSON.stringify(r.body.state));
    expect(states).toHaveLength(2);
    expect(states[0]).toContain(own);
    expect(states[0]).not.toContain(fallback);
    expect(states[1]).toContain(fallback);
  });

  it('marks a sample whose call failed and goes on; a refused key ends the whole test', async () => {
    const timeout = new ApiError({ kind: 'timeout', status: 0, code: 'timeout', message: 'slow' });
    const flaky = fakeJev((state) => (String(state.assistant_response).includes('fine') ? timeout : { type: 'noul', noul: 0.1 }));
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({ jev: flaky }), registry });
    const out = await plane.testEvaluator({ spec, samples });
    // A timed-out request keeps the request status `timeout` (Go studio Runner, decision.RequestTimeout).
    expect(out.results[1]).toMatchObject({ sample_id: 's2', status: 'error', reason: 'timeout', fires: null, cost_usd: 0 });
    expect(out.results.map((r) => r.status)).toEqual(['assessed', 'error', 'assessed']);

    const auth = new ApiError({ kind: 'http', status: 401, code: 'auth', message: 'refused' });
    const refusing = fakeJev(() => auth);
    const err = await failure(createStaticStudioPlane(deps, { live: fakeStudioLive({ jev: refusing }), registry }).testEvaluator({ spec, samples }));
    expect(err.code).toBe('auth');
  });

  it('refuses with the ledger reason when the caps run out, and spends no more', async () => {
    const jev = fakeJev(yesWhenBad);
    const ledger = fakeLedger(1);
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({ jev, ledger }), registry });
    const err = await failure(plane.testEvaluator({ spec, samples }));
    expect(err.code).toBe('cap_reached');
    expect(jev.requests.length).toBeLessThanOrEqual(1);
  });

  it('marks a sample too large for the per-call token cap and still runs the others', async () => {
    const jev = fakeJev(yesWhenBad);
    const settled: number[] = [];
    // The tab's caps: a request estimated over 400 tokens is refused before any call (D101).
    const ledger = {
      reserve: (est: readonly number[]) => (est.some((t) => t > 400) ? ({ ok: false, reason: 'token_cap' } as const) : ({ ok: true } as const)),
      settle: (status: number) => {
        settled.push(status);
      },
    };
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({ jev, ledger }), registry });
    const long: StudioSample = { id: 'long', assistant_response: `This is BAD. ${'More words that make the answer long. '.repeat(80)}`, expected: 'fire' };
    const out = await plane.testEvaluator({ spec, samples: [samples[0], long, samples[2]] });
    expect(out.results.map((r) => [r.sample_id, r.status, r.reason])).toEqual([
      ['s1', 'assessed', null],
      ['long', 'error', 'error:too_large'],
      ['s3', 'assessed', null],
    ]);
    expect(out.results[1]).toMatchObject({ fires: null, cost_usd: 0 });
    expect(out.summary).toMatchObject({ n: 3, assessed: 2 });
    // Nothing was sent for the large sample.
    expect(jev.requests).toHaveLength(2);
    expect(settled).toEqual([200, 200]);
  });

  it('needs a key, valid samples and a valid spec', async () => {
    const noKey = createStaticStudioPlane(deps, { live: fakeStudioLive({}), registry });
    expect(await failure(noKey.testEvaluator({ spec, samples }))).toMatchObject({ status: 503, code: 'decision_model_unavailable' });
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({ jev: fakeJev(yesWhenBad) }), registry });
    expect(await failure(plane.testEvaluator({ spec, samples: [] }))).toMatchObject({ status: 422, fieldErrors: [{ path: 'samples' }] });
    expect(await failure(plane.testEvaluator({ spec, samples: [{ id: 'x', assistant_response: ' ' }] }))).toMatchObject({ status: 422 });
    const badOrder = { ...spec, outcomes: { ...spec.outcomes, warn_at: 0.5 }, escalation: { ...spec.escalation, escalate_from: 0.7 } };
    expect(await failure(plane.testEvaluator({ spec: badOrder, samples }))).toMatchObject({ status: 422, fieldErrors: [expect.objectContaining({ code: 'threshold_order' })] });
  });

  it('stops when the caller aborts', async () => {
    const controller = new AbortController();
    const jev = fakeJev(() => {
      controller.abort();
      return { type: 'noul', noul: 0.1 };
    });
    const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({ jev }), registry });
    expect(await failure(plane.testEvaluator({ spec, samples }, controller.signal))).toMatchObject({ code: 'aborted' });
  });
});

// ----------------------------------------------------------------------------------- samples

describe('getStudioSamples', () => {
  const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({}), registry, random: () => 0 });

  it('gives at most six distinct answers of the chosen route, from the recording', async () => {
    const route = scenarioTrace('grounded-pass').route;
    const { samples } = await plane.getStudioSamples({ route, window: '24h', filter: 'random', limit: 20 });
    expect(samples.length).toBeGreaterThan(0);
    expect(samples.length).toBeLessThanOrEqual(6);
    const keys = new Set(samples.map((s) => `${s.user_request}|${s.assistant_response}`));
    expect(keys.size).toBe(samples.length);
    for (const s of samples) {
      const detail = committedDetail(s.trace_id!);
      expect(detail.route).toBe(route);
      expect(s.assistant_response).toBe(detail.body.response_text);
      expect(s.id).toBe(detail.trace_id);
      for (const e of s.evidence ?? []) expect(detail.body.evidence.find((x) => x.text === e.text)?.untrusted).not.toBe(true);
    }
  });

  it('flagged answers are ones the fast check flagged, blocked or fell back on, or would have', async () => {
    const { samples } = await plane.getStudioSamples({ window: '24h', filter: 'flagged' });
    expect(samples.length).toBeGreaterThan(0);
    for (const s of samples) {
      const d = committedDetail(s.trace_id!).decisions.find((x) => x.attempt === 0)!;
      expect(['flag', 'block', 'fallback'].includes(d.action) || ['flag', 'block', 'fallback'].includes(d.would_action ?? '')).toBe(true);
    }
  });

  it('honours a smaller limit and refuses an unknown route', async () => {
    expect((await plane.getStudioSamples({ window: '24h', filter: 'random', limit: 2 })).samples).toHaveLength(2);
    expect(await failure(plane.getStudioSamples({ route: 'nope', window: '24h', filter: 'random' }))).toMatchObject({ status: 422 });
  });
});

// ------------------------------------------------------------------------- calibrate, dry run

describe('quickCalibrate and the dry run', () => {
  const plane = createStaticStudioPlane(deps, { live: fakeStudioLive({}), registry });

  it('calibrates in the browser and says when there are too few labels', async () => {
    const labeled = [0.1, 0.2, 0.3, 0.35, 0.7, 0.8, 0.9, 0.95].map((risk_raw, i) => ({ risk_raw, label: i >= 4 }));
    const q = await plane.quickCalibrate({ labeled, current: 0.6 });
    expect(q).toMatchObject({ n: 8, n_pos: 4, n_neg: 4, enough: true });
    expect(q.threshold).not.toBeNull();
    expect((await plane.quickCalibrate({ labeled: labeled.slice(0, 3) })).enough).toBe(false);
    expect(await failure(plane.quickCalibrate({ labeled: [{ risk_raw: 2, label: true }] }))).toMatchObject({ status: 422 });
  });

  it('keeps "Test on past traffic" for your own stack', async () => {
    const err = await failure(plane.dryRunEvaluator({ spec, route: 'default', window: '24h' } as never, {}));
    expect(err.message).toBe(MESSAGE);
  });

  it('words a refusal by its reason', () => {
    expect(refusedError('token_cap')).toMatchObject({ code: 'token_cap', details: { reason: 'token_cap' } });
  });
});
