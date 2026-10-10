/**
 * The one browser gate pipeline (D135): evidence → detectors → compile → the decision model →
 * decide, as the Go gateway runs a check. The demo recorder runs every scenario through it with
 * scripted answers (so the simulated tape is this code's output), the static live Playground with
 * OpenRouter from the browser, and the static Studio's test bench with a draft spec.
 *
 * Step 0b moved this out of the recorder without changing what it computes (`make recording-check`
 * stays byte-identical). WEB-LIVE completes it in LV-3: gstudio Runner.Evaluate parity, the error
 * paths and playgroundResult.ts. Owned by WEB-LIVE.
 */
import { compile, decideEvaluators, defaultBudget, type CompileRoute } from '@/lib/decision/compile';
import { decide } from '@/lib/decision/decide';
import { detect, detectorStates } from '@/lib/decision/detectors';
import { decisionItems, extractEvidence } from '@/lib/decision/evidence';
import type { RawAnswerInput } from '@/lib/decision/normalize';
import type { JevCallResult, LedgerRefusal, UsageLedger } from '@/lib/openrouter/types';
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
  const base = defaultBudget();
  const compiled = compile(
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
  const started = clock();
  if (compiled.requests.length > 0) {
    const reserved = input.ledger.reserve(compiled.est_tokens);
    if (!reserved.ok) throw new GateRefusedError(reserved.reason);
  }
  const calls: (JevCallResult | null)[] = await Promise.all(
    compiled.requests.map(async (body, index) => {
      try {
        const result = await call.call({ index, body, est_tokens: compiled.est_tokens[index] }, input.signal);
        input.ledger.settle(200, result.usage);
        return result;
      } catch (err) {
        input.ledger.settle(0);
        throw err;
      }
    }),
  );
  const finished = clock();

  // In question_map order, as the gateway merges the answers of its requests.
  const answers: Record<string, RawAnswerInput> = {};
  for (const qid of Object.keys(compiled.question_map)) {
    const answer = calls[compiled.qid_request[qid]]?.answers[qid];
    if (answer !== undefined) answers[qid] = answer;
  }
  const request_status = Object.fromEntries(compiled.requests.map((_, k) => [String(k), calls[k] ? 'ok' : 'bad_response']));

  const decision = decide({
    policy: {
      configured_mode: route.mode,
      fail: route.fail,
      require_calibration: route.requireCalibration,
      on_no_evidence: route.onNoEvidence,
      detectors: { flag_on: route.detectors.flag_on, block_on: route.detectors.block_on },
    },
    evaluators: decideEvaluators(compiled, specs),
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
