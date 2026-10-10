/**
 * The glass box on a replayed trace (D109): "Show exact Jev request" with no gateway to ask. The
 * decision-model requests are rebuilt in the browser, the way the gateway rebuilds them for
 * GET /api/traces/:id/decision-request (backend-go cmd/proxy/decision_request_api.go): TS
 * compile() over the trace's stored body (its evidence, user request, system instructions, rules
 * and the answer of that attempt), with the route from `virtual:trustagent/policy` (the policy the
 * recording was made with), the built-in evaluator templates, the recorded chars-per-token and the
 * recorded model.
 *
 * - `matches_recorded` compares the rebuilt request fingerprints with the ones recorded with the
 *   decision (CompileInfo.request_sha256).
 * - `mismatch_reason`: `policy_missing` (the route is not in the policy), `evaluator_missing` (an
 *   enabled evaluator has no built-in template), `body_truncated` (the stored text was cut), or
 *   null when the body itself differs (a recording's sanitizer scrubbed it).
 *
 * On the simulated tape a match proves nothing (the tape was built by the same compile()), and the
 * trace page says so (`recording.glassbox.rebuilt_simulated`). Loaded only when the panel opens.
 */
import { BUILTIN_TEMPLATES, compile, CompileError, decisionItems, defaultBudget, type Budget, type Compiled } from '@/lib/decision';
import { mergeSettings, resolveRoute, RouteError, specMap } from '@/lib/gate/route';
import { compileRouteOf } from '@/lib/gate/runner';
import type { PolicyInput, ResolvedRoute } from '@/lib/gate/types';
import type { DecisionRequestView, GateDecisionView, TraceDetail } from '@/types/api';
import { ApiError } from '../errors';

/** The built-in templates as compile() reads them (the static build has no evaluator library). */
let builtinSpecs: ReturnType<typeof specMap> | null = null;
function specs(): ReturnType<typeof specMap> {
  builtinSpecs ??= specMap(Object.values(BUILTIN_TEMPLATES));
  return builtinSpecs;
}

/** gate.RouteBudget: the default budget with the recorded chars-per-token and the route's limits. */
function routeBudget(policy: PolicyInput, route: ResolvedRoute, cpt: number): Budget {
  const base = defaultBudget();
  const raw = policy.routes?.find((r) => r.name === route.name);
  const decision = mergeSettings(policy.defaults?.decision, raw?.decision);
  const maxResponse = decision.max_response_tokens;
  const maxEvidence = route.evidence.max_tokens;
  return {
    ...base,
    cpt: cpt > 0 ? cpt : base.cpt,
    max_response_tokens: typeof maxResponse === 'number' ? maxResponse : base.max_response_tokens,
    evidence_max_tokens: typeof maxEvidence === 'number' ? maxEvidence : base.evidence_max_tokens,
  };
}

/** The model the requests were addressed to: the recorded one, else the route's pin, else the recording's. */
function modelOf(policy: PolicyInput, decision: GateDecisionView, fallback: string | null): string {
  const raw = policy.routes?.find((r) => r.name === decision.route);
  const pinned = mergeSettings(policy.defaults?.decision, raw?.decision).model;
  return decision.decision_model?.model_requested || (typeof pinned === 'string' && pinned) || fallback || 'unset';
}

const sameList = (a: readonly string[], b: readonly string[]) => a.length === b.length && a.every((x, i) => x === b[i]);

/**
 * Rebuilds the requests of one recorded decision (attempt 0, or 1 for the fallback answer).
 * Throws ApiError 404 when the trace has no decision for that attempt (yet), 409 body_expired when
 * the recording kept no body for it.
 */
export function rebuildDecisionRequest(
  detail: TraceDetail,
  attempt: number,
  policy: PolicyInput,
  recordingModel: string | null = null,
): DecisionRequestView {
  const decision = detail.decisions.find((d) => d.attempt === attempt);
  if (!decision) {
    throw new ApiError({ kind: 'http', status: 404, code: 'not_found', message: 'This trace has no recorded decision for that attempt.' });
  }
  const body = detail.body;
  if (!body.available) {
    throw new ApiError({
      kind: 'http',
      status: 409,
      code: 'body_expired',
      message: 'The recording kept no body for this trace, so its decision request cannot be rebuilt.',
    });
  }
  const recorded = decision.compile?.request_sha256 ?? [];
  const view: DecisionRequestView = {
    requests: [],
    request_sha256: [],
    matches_recorded: false,
    cpt: decision.compile && decision.compile.cpt > 0 ? decision.compile.cpt : defaultBudget().cpt,
    mismatch_reason: null,
  };
  const mismatch = (reason: NonNullable<DecisionRequestView['mismatch_reason']>): DecisionRequestView => ({ ...view, mismatch_reason: reason });

  let route: ResolvedRoute;
  try {
    route = resolveRoute(policy, decision.route);
  } catch (err) {
    if (err instanceof RouteError) return mismatch('policy_missing');
    throw err;
  }
  const known = specs();
  if (route.evaluators.some((e) => e.enabled !== false && !known.has(e.ref))) return mismatch('evaluator_missing');

  const text = attempt === 1 ? body.final_response_text : body.response_text;
  if (text) {
    let compiled: Compiled | null = null;
    try {
      compiled = compile(
        compileRouteOf(route, modelOf(policy, decision, recordingModel)),
        known,
        {
          user_request: body.user_request ?? '',
          assistant_response: text,
          system_instructions: body.system_instructions ?? '',
          evidence: decisionItems(body.evidence),
          rules: body.rules,
        },
        routeBudget(policy, route, view.cpt),
      );
    } catch (err) {
      // As the gateway: a route that cannot be compiled rebuilds no request (and so differs).
      if (!(err instanceof CompileError)) throw err;
    }
    if (compiled) {
      view.requests = compiled.requests.map((r) => ({ model: r.model, state: r.state, questions: r.questions }));
      view.request_sha256 = compiled.request_sha256;
    }
  }
  view.matches_recorded = sameList(view.request_sha256, recorded);
  if (!view.matches_recorded && body.truncated) return mismatch('body_truncated');
  return view;
}
