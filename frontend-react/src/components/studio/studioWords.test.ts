// @vitest-environment node
/**
 * Studio's sentences filled from contracts/ui copy.studio: the dry-run result counts fires over the
 * whole window (extrapolated, "≈", when only a sample was tested), and the D82 confirmation says
 * what is left of today's budget, or that there is no daily budget.
 */
import { describe, expect, it } from 'vitest';
import dryRunFixture from '@contracts/fixtures/api/DryRunResult.done.json';
import type { DryRunResult } from '@/types/api';
import { copy } from '@/domain/presentation';
import { ApiError } from '@/data/errors';
import { openRouterError, transportError } from '@/lib/openrouter/errors';
import { OPENROUTER_ERROR_KINDS } from '@/lib/openrouter/types';
import {
  riskLevelsLine,
  studioCallsUsedText,
  studioCapReachedText,
  studioDraftFailureText,
  studioDryRunConfirmText,
  studioDryRunExtrapolated,
  studioDryRunResultText,
  studioGroupExamples,
  studioJudgeText,
  studioLiveFailure,
  studioQuestionParts,
  studioRowReasonText,
} from './studioWords';

const done = dryRunFixture as DryRunResult;

describe('studioJudgeText', () => {
  it('names the model and its provider', () => {
    expect(studioJudgeText({ provider: 'openrouter', model: 'anthropic/claude-haiku-4.5' })).toBe('Uses anthropic/claude-haiku-4.5 (OpenRouter)');
    expect(studioJudgeText({ provider: 'openrouter', model: null })).toBe('Uses the AI judge (OpenRouter)');
    expect(studioJudgeText(null)).toBe('Uses the AI judge');
    expect(studioJudgeText({ provider: null, model: 'anthropic/claude-haiku-4.5' })).toBe('Uses anthropic/claude-haiku-4.5');
  });

  it('calls the demo judge the mock model once, never "mock (Mock (simulated))"', () => {
    expect(studioJudgeText({ provider: 'mock', model: 'mock' })).toBe('Uses the mock model (simulated)');
  });
});

describe('studioDryRunResultText', () => {
  it('gives the extrapolated count for the window when only a sample of it was tested', () => {
    expect(studioDryRunExtrapolated(done)).toBe(true);
    expect(studioDryRunResultText(done)).toBe('Would have fired ≈ 108 times in the last 7 days (5.9% of answers, likely between 3.3% and 10%).');
  });

  it('gives the count itself when every answer of the window was tested', () => {
    const all = { ...done, window: '24h' as const, sampled: 120, population: 120, fired: 9, extrapolated_fires: 9, fire_rate: 0.075, fire_rate_ci95: [0.04, 0.137] as [number, number] };
    expect(studioDryRunExtrapolated(all)).toBe(false);
    expect(studioDryRunResultText(all)).toBe('Would have fired 9 times in the last 24 h (7.5% of answers, likely between 4% and 14%).');
  });
});

describe('studioDryRunConfirmText', () => {
  const details = { estimate_usd: 0.0021, samples: 200, population: 1840 };

  it("says what is left of today's fast-check budget", () => {
    expect(studioDryRunConfirmText({ ...details, decision_budget_left_usd: 1.84 })).toBe(
      "This test costs about $0.0021 (200 of 1,840 past answers). $1.84 of today's fast-check budget is left.",
    );
  });

  it('says there is no daily budget when the gateway sends none, instead of a number it does not have', () => {
    expect(studioDryRunConfirmText({ ...details, decision_budget_left_usd: null })).toBe(
      'This test costs about $0.0021 (200 of 1,840 past answers). No daily fast-check budget is set.',
    );
  });
});

describe('studioGroupExamples', () => {
  it('shows the same answer once with its count, in the order the examples came', () => {
    const ex = ['a', 'b', 'c', 'd', 'e', 'f'].map((id) => ({ trace_id: id }));
    const groups = studioGroupExamples(ex, ['Refund in 30 days', 'Refund in 30 days', null, 'Call us', 'Refund in 30 days', '']);
    expect(groups.map((g) => [g.example.trace_id, g.count, g.text])).toEqual([
      ['a', 3, 'Refund in 30 days'],
      // Without its text an example stays a row of its own.
      ['c', 1, null],
      ['d', 1, 'Call us'],
      ['f', 1, null],
    ]);
    expect(studioGroupExamples([], [])).toEqual([]);
  });
});

describe('studioQuestionParts', () => {
  it('names each part of the request in words, and a setting by its own label', () => {
    const parts = studioQuestionParts('Does `assistant_response` name one of `rules.competitors` or `rules.other_brand`, given `user_request`?', {
      competitors: { label: 'Competitor names' },
    });
    expect(parts.map((p) => (p.kind === 'name' ? `[${p.label}]` : p.text)).join('')).toBe(
      'Does [your model’s answer] name one of [Competitor names] or [Other brand], given [the user’s message]?',
    );
    expect(parts.filter((p) => p.kind === 'name').map((p) => (p.kind === 'name' ? p.raw : ''))).toEqual([
      'assistant_response',
      'rules.competitors',
      'rules.other_brand',
      'user_request',
    ]);
    expect(studioQuestionParts('No names here.')).toEqual([{ kind: 'text', text: 'No names here.' }]);
    expect(studioQuestionParts('`evidence` and `system_instructions`', null).map((p) => (p.kind === 'name' ? p.label : p.text))).toEqual([
      'your documents',
      ' and ',
      'your system prompt',
    ]);
  });
});

describe('riskLevelsLine', () => {
  it('names the thresholds the risk words follow, with or without a block level', () => {
    expect(riskLevelsLine({ warn_at: 0.6, critical_at: 0.98 })).toBe('Risk words follow this check’s own thresholds: Elevated risk from 0.60, High risk from 0.98.');
    expect(riskLevelsLine({ warn_at: 0.7, critical_at: null })).toBe('Risk words follow this check’s own thresholds: Elevated risk from 0.70; it has no High risk level.');
  });
});

describe('the static Studio’s failure words (D101)', () => {
  const limits = { maxCalls: 100, maxTokens: 8000 };
  /** A refusal by the tab's caps, as the static plane reports it (staticPlane refusedError). */
  const refusedError = (reason: string) => new ApiError({ kind: 'http', status: 429, code: reason, message: 'refused' });

  it('words the tab’s caps as the live Playground does', () => {
    expect(studioCapReachedText(100)).toBe('This demo stops after 100 calls to OpenRouter (checks and AI drafts). Run it yourself for more.');
    expect(studioLiveFailure(refusedError('cap_reached'), limits)).toEqual({ text: studioCapReachedText(100), hint: null });
    expect(studioLiveFailure(refusedError('token_cap'), limits)?.text).toBe("This check is larger than this demo's limit of 8,000 tokens per call. Shorten the documents or the answer.");
    expect(studioLiveFailure(refusedError('rejected_pause'), limits)?.text).toBe(copy.playground_live.rejected_pause);
    expect(studioCallsUsedText(3, 100)).toBe('3 of 100 calls to OpenRouter (checks and AI drafts)');
  });

  it('words each OpenRouter failure as the live Playground does (liveErrorView)', () => {
    const words = (err: unknown) => studioLiveFailure(err, limits)?.text ?? null;
    expect(words(openRouterError('auth', { status: 401, message: 'no' }))).toBe(copy.playground_live.errors.unauthorized);
    expect(words(openRouterError('credits', { status: 402, message: 'no' }))).toBe(copy.playground_live.errors.no_credits);
    expect(words(openRouterError('credits', { status: 402, message: 'no', details: { in_flight: true } }))).toBe(copy.playground_live.errors.credits_inflight);
    expect(words(openRouterError('retryable', { status: 429, message: 'slow down' }))).toBe(copy.playground_live.errors.rate_limited);
    expect(words(openRouterError('retryable', { status: 503, message: 'busy' }))).toBe(copy.playground_live.retry_busy);
    expect(words(openRouterError('bad_request', { status: 403, message: 'flagged', details: { moderation: true } }))).toBe(copy.playground_live.errors.moderation);
    expect(words(transportError('timeout', 0))).toBe(copy.playground_live.errors.timeout);
    // Every OpenRouter kind has a sentence (never the raw code or a blank).
    for (const kind of OPENROUTER_ERROR_KINDS.filter((k) => k !== 'aborted')) {
      expect(words(openRouterError(kind, { status: 400, message: 'x' })), kind).toMatch(/\w+ \w+ \w+/);
    }
  });

  it('adds why nothing was billed when the browser blocked the request, and says nothing about a cancel', () => {
    expect(studioLiveFailure(transportError('network', 0), limits)).toEqual({ text: copy.playground_live.errors.network, hint: copy.playground_live.browser_blocked });
    expect(studioLiveFailure(transportError('network', 502), limits)?.hint).toBeNull();
    expect(studioLiveFailure(transportError('aborted', 0), limits)).toBeNull();
  });

  it('leaves any other error to its own message', () => {
    expect(studioLiveFailure(new ApiError({ kind: 'http', status: 422, code: 'validation_failed', message: 'x' }), limits)).toBeNull();
    expect(studioLiveFailure(new ApiError({ kind: 'http', status: 400, code: 'constructor', message: 'x' }), limits)).toBeNull();
    expect(studioLiveFailure(new Error('boom'), limits)).toBeNull();
  });

  it('says out of credits or key refused for a gateway draft, never "engine unavailable"', () => {
    expect(studioDraftFailureText('no_credits')).toBe(copy.studio.draft_no_credits);
    expect(studioDraftFailureText('judge_unauthorized')).toBe(copy.studio.draft_key_refused);
    expect(studioDraftFailureText('engine_unavailable')).toBeNull();
  });
});

describe('studioRowReasonText: why a test row has no outcome, never the raw reason', () => {
  const browser = { maxTokens: 8000 };

  it('uses the status words the trace pages use', () => {
    expect(studioRowReasonText('timeout')).toBe('The check timed out');
    expect(studioRowReasonText('no_evidence', browser)).toBe('No documents in this request');
    expect(studioRowReasonText('evidence_too_short')).toBe('The documents are too short to check against');
  });

  it('words a failed browser request as the live Playground does, and one too large as the tab’s token limit', () => {
    expect(studioRowReasonText('error:retryable', browser)).toBe(copy.playground_live.retry_busy);
    expect(studioRowReasonText('error:bad_response', browser)).toBe(copy.playground_live.errors.bad_response);
    expect(studioRowReasonText('error:network', browser)).toBe(copy.playground_live.errors.network);
    expect(studioRowReasonText('error:too_large', browser)).toBe("This check is larger than this demo's limit of 8,000 tokens per call. Shorten the documents or the answer.");
    // Every OpenRouter kind reads as a sentence, never as error:<kind>.
    for (const kind of OPENROUTER_ERROR_KINDS) expect(studioRowReasonText(`error:${kind}`, browser), kind).not.toContain(':');
  });

  it('on a gateway, or for a kind the browser does not report, says the check failed and why in plain words', () => {
    expect(studioRowReasonText('error:auth')).toBe('The check failed (auth)');
    expect(studioRowReasonText('error:too_large')).toBe('The check failed (too large)');
    expect(studioRowReasonText('error:compile', browser)).toBe('The check failed (compile)');
    expect(studioRowReasonText('error:internal', browser)).toBe('The check failed (internal)');
  });

  it('humanizes any other reason, including one that names an Object member', () => {
    expect(studioRowReasonText('not_run')).toBe('Not run');
    expect(studioRowReasonText('missing_answer')).toBe('Missing answer');
    expect(studioRowReasonText('constructor')).toBe('Constructor');
  });
});
