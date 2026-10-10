// @vitest-environment node
import { describe, expect, it, vi } from 'vitest';
import { openRouterError, transportError } from '@/lib/openrouter/errors';
import type { JevCall, JevCallRequest, UsageLedger } from '@/lib/openrouter/types';
import { CUSTOM_LIMITS, CUSTOM_ROUTE, customInput, customProblems, estimateCheck, explainingFailure, liveScenario, runLiveCheck } from './liveCheck';
import { GateRefusedError } from './runner';

const MODEL = 'typesafe/jev-1.13';
const DOCS =
  'Support is open from 9 am to 5 pm, Monday to Friday. There is no phone or chat support on weekends or public holidays; email answers arrive the next working day.';

/** Answers every question at `noul`, reporting `cost` (null: no usage.cost). */
function scripted(noul: number, cost: number | null = 0.00002, fail?: (r: JevCallRequest) => Error | null): JevCall & { requests: JevCallRequest[] } {
  const requests: JevCallRequest[] = [];
  return {
    requests,
    backend: 'openrouter-decisions',
    model: MODEL,
    async call(request) {
      requests.push(request);
      const err = fail?.(request);
      if (err) throw err;
      const answers = Object.fromEntries(Object.keys(request.body.questions).map((q) => [q, { type: 'noul', noul } as never]));
      return { answers, usage: { input_tokens: request.est_tokens, cost }, snapshot: 'typesafe/jev-1.13-20260917', latency_ms: 120 };
    },
  };
}

const ledger = (): UsageLedger & { settled: number[] } => {
  const settled: number[] = [];
  return { settled, reserve: () => ({ ok: true }), settle: (status) => void settled.push(status) };
};

describe('live inputs (D104)', () => {
  it('uses only a scenario’s pack request and response', () => {
    const s = liveScenario('refund-contradiction-flag');
    expect(s.route).toBe('support-bot');
    expect(s.question).toBe('Can I still get a refund on my annual plan? I bought it 45 days ago.');
    expect(s.input.response).toBe('You can get a full refund within 60 days of purchase.');
    expect(Object.keys(s.input)).toEqual(['route', 'request', 'response']);
  });

  it('puts the visitor’s documents in the system prompt on support-bot', () => {
    const input = customInput({ documents: `  ${DOCS} `, question: ' When? ', answer: ' Always. ' });
    expect(input.route).toBe(CUSTOM_ROUTE);
    expect(input.response).toBe('Always.');
    const [system, user] = input.request.messages as { role: string; content: string }[];
    expect(system.role).toBe('system');
    expect(system.content).toContain(`<document id="your-document">${DOCS}</document>`);
    expect(user).toEqual({ role: 'user', content: 'When?' });
  });

  it('says what the custom form is missing or what is too long', () => {
    expect(customProblems({ documents: '', question: '', answer: '' })).toEqual(['documents', 'answer']);
    expect(customProblems({ documents: 'd', question: '', answer: 'a' })).toEqual([]);
    expect(customProblems({ documents: 'd'.repeat(CUSTOM_LIMITS.documents + 1), question: 'q'.repeat(CUSTOM_LIMITS.question + 1), answer: 'a' })).toEqual([
      'documents_long',
      'question_long',
    ]);
  });
});

describe('estimateCheck', () => {
  it('prices compile’s estimate at the Jev input price, rounded up to whole tokens', () => {
    const e = estimateCheck(liveScenario('refund-contradiction-flag').input, MODEL, 8000);
    expect(e.ok).toBe(true);
    if (!e.ok) return;
    expect(e.est_tokens.length).toBeGreaterThan(0);
    const tokens = e.est_tokens.reduce((n, t) => n + Math.ceil(t), 0);
    expect(e.cost_usd).toBeCloseTo((tokens * 0.042) / 1e6, 12);
  });

  it('is the token cap when a question cannot fit, or a request would exceed the cap', () => {
    expect(estimateCheck(liveScenario('pii-leak-flag').input, MODEL, 100)).toEqual({ ok: false, reason: 'token_cap' });
  });

  it('has no cost figure for a model the table does not price', () => {
    const e = estimateCheck(liveScenario('grounded-pass').input, 'typesafe/jev-unpriced', 8000);
    expect(e).toMatchObject({ ok: true, cost_usd: null });
  });

  it('needs no call when the route skips the text', () => {
    expect(estimateCheck(customInput({ documents: 'Too short.', question: '', answer: 'Yes.' }), MODEL, 8000)).toEqual({ ok: true, est_tokens: [], cost_usd: 0 });
  });
});

describe('runLiveCheck', () => {
  it('flags the refund contradiction with raw scores and OpenRouter’s cost (vendor)', async () => {
    const call = scripted(0.97);
    const r = await runLiveCheck(liveScenario('refund-contradiction-flag').input, { call, ledger: ledger(), maxTokens: 8000 });
    expect(r.result.decision.action).toBe('flag');
    expect(r.result.decision.reasons).toContain('contradiction:critical');
    expect(r.result.decision.decision_model).toMatchObject({ backend: 'openrouter-decisions', cost_source: 'reported', snapshot: 'typesafe/jev-1.13-20260917' });
    expect(r.cost).toEqual({ usd: 0.00002 * call.requests.length, label: 'vendor' });
    expect(r.request.requests.map((q) => q.model)).toEqual(call.requests.map(() => MODEL));
    expect(r.result.delivered_text).toBe('You can get a full refund within 60 days of purchase.');
  });

  it('prices the check here when OpenRouter reported no cost (estimate)', async () => {
    const call = scripted(0.05, null);
    const r = await runLiveCheck(liveScenario('grounded-pass').input, { call, ledger: ledger(), maxTokens: 8000 });
    expect(r.cost?.label).toBe('estimate');
    expect(r.result.decision.decision_model?.cost_source).toBe('computed');
    const tokens = call.requests.reduce((n, q) => n + Math.ceil(q.est_tokens), 0);
    expect(r.cost?.usd).toBeCloseTo((tokens * 0.042) / 1e6, 12);
  });

  it('rejects with the vendor failure instead of a fail-open decision', async () => {
    const call = scripted(0.5, 0.00002, () => openRouterError('credits', { status: 402, message: 'no credits' }));
    const l = ledger();
    const err = await runLiveCheck(liveScenario('grounded-pass').input, { call, ledger: l, maxTokens: 8000 }).catch((e: unknown) => e);
    expect(err).toMatchObject({ code: 'credits', status: 402 });
    expect(l.settled.every((s) => s === 402)).toBe(true);
  });

  it('reports the refused key, not the cancels it caused', () => {
    const aborted = transportError('aborted', 0);
    const auth = openRouterError('auth', { status: 401, message: 'refused' });
    expect(explainingFailure(new Map<number, unknown>([[1, auth], [0, aborted]]))).toBe(auth);
    expect(explainingFailure(new Map<number, unknown>([[0, aborted]]))).toBe(aborted);
    expect(explainingFailure(new Map())).toBeUndefined();
  });

  it('refuses before any call when the caps say so', async () => {
    const call = scripted(0.5);
    const refuse: UsageLedger = { reserve: () => ({ ok: false, reason: 'cap_reached' }), settle: vi.fn() };
    await expect(runLiveCheck(liveScenario('grounded-pass').input, { call, ledger: refuse, maxTokens: 8000 })).rejects.toBeInstanceOf(GateRefusedError);
    expect(call.requests).toHaveLength(0);
  });
});
