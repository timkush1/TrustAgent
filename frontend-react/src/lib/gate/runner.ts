/**
 * The one browser gate pipeline (D135): evidence → detectors → compile → the decision model →
 * decide, as the Go gateway runs a check. The demo recorder runs every scenario through it with
 * scripted answers (so the simulated tape is this code's output), the static live Playground with
 * OpenRouter from the browser, and the static Studio's test bench with a draft spec.
 *
 * Parity with backend-go internal/studio Runner.Evaluate (gstudio):
 * - compile within min(24000, maxTokens) (a question that cannot fit is the token cap, refused
 *   before any call), with the route's threshold overrides for decide();
 * - every request of the check is reserved before the first call, all or none (D101); a refusal
 *   throws GateRefusedError and nothing is sent;
 * - the requests run concurrently; each is settled once with its HTTP status and any usage, so a
 *   failed request that did not bill releases its slot;
 * - a failed request becomes its request status (`timeout`, or `error:<kind>`) and decide() applies
 *   the route's fail policy, exactly like the gateway; only a cancelled check (the caller's signal,
 *   or Forget key) rejects, with the `aborted` error.
 *
 * Step 0b moved this out of the recorder without changing what it computes; `make recording-check`
 * keeps the tape byte-identical. Owned by WEB-LIVE.
 */
import { ApiError } from '@/data/errors';
import { compile, CompileError, decideEvaluators, defaultBudget, type CompileRoute, type ThresholdOverrides } from '@/lib/decision/compile';
import { decide } from '@/lib/decision/decide';
import { detect, detectorStates } from '@/lib/decision/detectors';
import { decisionItems, extractEvidence } from '@/lib/decision/evidence';
import type { RawAnswerInput } from '@/lib/decision/normalize';
import { errorDetails, errorKind, transportError } from '@/lib/openrouter/errors';
import type { JevCall, JevCallResult, LedgerRefusal, UsageLedger } from '@/lib/openrouter/types';
import type { BrowserGateResult, ResolvedRoute, RunGateInput } from './types';

/** A ledger that never refuses and counts nothing (the recorder, tests). */
export const NOOP_LEDGER: UsageLedger = {
  reserve: () => ({ ok: true }),
  settle: () => {},
};

/** The caps refused the check before any call (D101). */
export class GateRefusedError extends Error {
  readonly reason: LedgerRefusal;
  constructor(reason: LedgerRefusal) {
    super(`the check was refused before any call: ${reason}`);
    this.name = 'GateRefusedError';
    this.reason = reason;
  }
}

/** decide()'s threshold overrides: each route evaluator's `thresholds` (Go Runner.Thresholds). */
export function routeThresholds(route: ResolvedRoute): Record<string, ThresholdOverrides> {
  const out: Record<string, ThresholdOverrides> = {};
  for (const e of route.evaluators) {
    const th = (e as { thresholds?: unknown }).thresholds;
    if (typeof th === 'object' && th !== null && !Array.isArray(th)) out[e.ref] = th as ThresholdOverrides;
  }
  return out;
}

/** The request status decide() reads for a failed request (gstudio: `timeout` or `error:<kind>`). */
export function failedStatus(err: unknown): string {
  const kind = errorKind(err);
  return kind === 'timeout' ? 'timeout' : `error:${kind}`;
}

/**
 * Wraps a JevCall so the caller can see why each request failed (runGate keeps only the request
 * status, as the gateway does; the live panel words the failure itself).
 */
export function recordFailures(call: JevCall): { call: JevCall; failures: Map<number, unknown> } {
  const failures = new Map<number, unknown>();
  return {
    failures,
    call: {
      backend: call.backend,
      model: call.model,
      call: async (request, signal) => {
        try {
          return await call.call(request, signal);
        } catch (err) {
          failures.set(request.index, err);
          throw err;
        }
      },
    },
  };
}

/** compile()'s route for a resolved route and the transport's model. */
export function compileRouteOf(route: ResolvedRoute, model: string): CompileRoute {
  return {
    evaluators: route.evaluators.map((e) => ({ ref: e.ref, stage: e.stage, enabled: e.enabled, params: e.params })),
    on_no_evidence: route.onNoEvidence,
    evidence_sources: route.evidence.sources,
    client_rules: route.clientRules,
    model,
  };
}

export async function runGate(input: RunGateInput): Promise<BrowserGateResult> {
  const { route, specs, call } = input;
  const clock = input.clock ?? Date.now;

  // Tier 0: extraction and the detectors.
  const extraction = extractEvidence(input.apiFormat, input.request, route.evidence);
  const evidence = decisionItems(extraction.items);
  const detectors = detect({
    response: input.response,
    evidence,
    userRequest: extraction.user_request,
    systemInstructions: extraction.system_instructions,
    rules: null,
    preDedupItems: extraction.pre_dedup_items,
    maxMarker: extraction.max_marker,
    minInteger: route.detectors.min_integer,
  });

  // The fast check: compile within min(24000, maxTokens), reserve every slot, then ask.
  if (input.signal?.aborted) throw transportError('aborted', 0, input.signal.reason);
  const base = defaultBudget();
  let compiled: ReturnType<typeof compile>;
  try {
    compiled = compile(
      compileRouteOf(route, call.model),
      specs,
      {
        user_request: extraction.user_request,
        assistant_response: input.response,
        system_instructions: extraction.system_instructions,
        evidence,
        rules: {},
      },
      { ...base, tokens: Math.min(base.tokens, input.maxTokens) },
    );
  } catch (err) {
    // A question that cannot fit next to the state within the per-call cap.
    if (err instanceof CompileError && err.message.includes('does not fit')) throw new GateRefusedError('token_cap');
    throw err;
  }
  const started = clock();
  if (compiled.requests.length > 0) {
    const reserved = input.ledger.reserve(compiled.est_tokens);
    if (!reserved.ok) throw new GateRefusedError(reserved.reason);
  }
  const failures: unknown[] = compiled.requests.map(() => undefined);
  const calls: (JevCallResult | null)[] = await Promise.all(
    compiled.requests.map(async (body, index) => {
      try {
        const result = await call.call({ index, body, est_tokens: compiled.est_tokens[index] }, input.signal);
        input.ledger.settle(200, result.usage);
        return result;
      } catch (err) {
        input.ledger.settle(err instanceof ApiError ? err.status : 0, errorDetails(err).usage);
        failures[index] = err;
        return null;
      }
    }),
  );
  const finished = clock();

  // A cancelled check has no decision: the caller's signal, or Forget key aborting every request.
  const failed = failures.filter((f) => f !== undefined);
  if (input.signal?.aborted || (failed.length > 0 && failed.every((f) => errorKind(f) === 'aborted'))) {
    const abort = failed.find((f) => errorKind(f) === 'aborted');
    throw abort instanceof ApiError ? abort : transportError('aborted', 0, input.signal?.reason);
  }

  // In question_map order, as the gateway merges the answers of its requests.
  const answers: Record<string, RawAnswerInput> = {};
  for (const qid of Object.keys(compiled.question_map)) {
    const answer = calls[compiled.qid_request[qid]]?.answers[qid];
    if (answer !== undefined) answers[qid] = answer;
  }
  const request_status = Object.fromEntries(compiled.requests.map((_, k) => [String(k), calls[k] ? 'ok' : failedStatus(failures[k])]));

  const decision = decide({
    policy: {
      configured_mode: route.mode,
      fail: route.fail,
      require_calibration: route.requireCalibration,
      on_no_evidence: route.onNoEvidence,
      detectors: { flag_on: route.detectors.flag_on, block_on: route.detectors.block_on },
    },
    evaluators: decideEvaluators(compiled, specs, routeThresholds(route)),
    answers: compiled.requests.length === 0 ? null : (answers as never),
    question_map: compiled.question_map,
    excluded: compiled.excluded,
    calibration: input.calibration ?? {},
    meta: {
      backend: call.backend,
      stream: 'none',
      evidence_present: evidence.length > 0,
      evidence_truncated: compiled.truncated,
      knowledge_only: false,
      skip_reason: null,
      request_status,
      qid_request: compiled.qid_request,
      question_errors: {},
    },
    detectors: detectorStates(detectors),
  });

  const answered = calls.filter((c): c is JevCallResult => c !== null);
  const costs = answered.map((c) => c.usage.cost);
  return {
    route,
    extraction,
    evidence,
    detectors,
    compiled,
    answers,
    request_status,
    calls,
    decision,
    usage: {
      input_tokens: answered.reduce((n, c) => n + c.usage.input_tokens, 0),
      cost: costs.length === 0 || costs.some((c) => c === null) ? null : costs.reduce<number>((sum, c) => sum + (c ?? 0), 0),
    },
    snapshot: answered[0]?.snapshot ?? null,
    latency_ms: compiled.requests.length === 0 ? 0 : Math.max(0, finished - started),
  };
}
