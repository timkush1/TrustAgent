/**
 * A browser check as the Playground shows it (D135): `BrowserGateResult` → the `PlaygroundResult`
 * ResultTimeline renders (`decision` is a full decision/v1 view), and the compiled requests → the
 * `DecisionRequestView` "Show exact Jev request" renders (compile.ts `Compiled`).
 *
 * Nothing here is invented: the decision, detectors, exclusions, compile info and evidence summary
 * are runGate's own output, the model usage is what OpenRouter reported (its cost labelled
 * `reported`; without one the cost is `unknown` unless the caller priced it, then `computed`), the
 * request bodies are the ones sent. There is no upstream call in the browser (the answer is given)
 * and no deep audit (it does not run in the browser). Owned by WEB-LIVE (LV-3).
 */
import type { Compiled } from '@/lib/decision/compile';
import { DEFAULT_CPT, est } from '@/lib/decision/tokens';
import type { JevCall } from '@/lib/openrouter/types';
import type { DecisionModelUsage, DecisionRequestView, EvidenceSummary, GateDecisionView, PlaygroundResult } from '@/types/api';
import type { BrowserGateResult, ResolvedRoute } from './types';

export interface PlaygroundResultOptions {
  /** A local id for this result (never stored or sent); a random UUID by default. */
  traceId?: string;
  /** Epoch ms of the check; Date.now by default. */
  createdAt?: number;
  /** The policy's version (virtual:trustagent/policy). */
  policyVersion?: number | null;
  /** The route's block message, shown as the delivered text when the action is block or fallback. */
  blockMessage?: string | null;
  /** The check's cost priced here (lib/decision/cost.ts) when OpenRouter reported none; labelled computed. */
  computedCostUsd?: number | null;
}

/** Which configured evidence sources the compiled requests used (the gateway's evidence_summary). */
export function evidenceSummaryOf(gate: Pick<BrowserGateResult, 'route' | 'extraction' | 'compiled'>): EvidenceSummary {
  const configured = gate.route.evidence.sources;
  const packed = new Set(gate.compiled.packed_ids);
  const used = configured
    .map((source) => {
      const mine = gate.extraction.items.filter((it) => it.source === source && packed.has(it.id));
      return { source, items: mine.length, labels: mine.map((it) => it.label), tokens: mine.reduce((n, it) => n + est(it.text, DEFAULT_CPT), 0) };
    })
    .filter((u) => u.items > 0);
  return {
    configured: [...configured],
    used,
    empty: configured.filter((src) => !used.some((u) => u.source === src)),
    knowledge: 'not_requested',
    truncated: gate.compiled.truncated,
    tokens: used.reduce((n, u) => n + u.tokens, 0),
  };
}

/** The decision-model usage of a browser check, null when nothing was asked. */
export function decisionModelUsage(gate: BrowserGateResult, call: Pick<JevCall, 'backend' | 'model'>, computedCostUsd: number | null = null): DecisionModelUsage | null {
  const n = gate.compiled.requests.length;
  if (n === 0) return null;
  const answered = gate.calls.filter((c) => c !== null);
  const reported = gate.usage.cost;
  const cost = reported ?? computedCostUsd;
  return {
    backend: call.backend,
    model_requested: call.model,
    snapshot: gate.snapshot,
    calls: n,
    // No retries and no hedging in the browser (D100): one attempt per request.
    attempts: n,
    hedged: false,
    latency_ms: answered.length > 0 ? gate.latency_ms : null,
    connect_ms: null,
    input_tokens: answered.length > 0 ? gate.usage.input_tokens : null,
    cost_usd: cost,
    cost_source: reported !== null ? 'reported' : computedCostUsd !== null ? 'computed' : 'unknown',
  };
}

/** The full decision/v1 view of a browser check. */
export function gateDecisionView(gate: BrowserGateResult, call: Pick<JevCall, 'backend' | 'model'>, opts: PlaygroundResultOptions = {}): GateDecisionView {
  const c = gate.compiled;
  const route: ResolvedRoute = gate.route;
  return {
    spec: 'decision/v1',
    trace_id: opts.traceId ?? crypto.randomUUID(),
    attempt: 0,
    created_at: new Date(opts.createdAt ?? Date.now()).toISOString(),
    route: route.name,
    policy_version: opts.policyVersion ?? null,
    mode: route.mode,
    fail: route.fail,
    ...gate.decision,
    detectors: gate.detectors,
    excluded: c.excluded,
    decision_model: decisionModelUsage(gate, call, opts.computedCostUsd ?? null),
    compile: {
      cpt: c.cpt,
      requests: c.requests.length,
      question_set_sha256: c.question_set_sha256,
      request_sha256: c.request_sha256,
      packed_evidence_ids: c.packed_ids,
      truncated_ids: c.truncated_ids,
      est_tokens: c.est_tokens,
    },
    evidence_summary: evidenceSummaryOf(gate),
    late: false,
    gate_ms: gate.latency_ms,
  };
}

/** The Playground's result for a browser check. */
export function playgroundResult(gate: BrowserGateResult, call: Pick<JevCall, 'backend' | 'model'>, response: string, opts: PlaygroundResultOptions = {}): PlaygroundResult {
  const decision = gateDecisionView(gate, call, opts);
  const replaced = decision.action === 'block' || decision.action === 'fallback';
  return {
    trace_id: decision.trace_id,
    upstream: null,
    response_text: response,
    delivered_text: replaced ? (opts.blockMessage ?? '') : response,
    headers: {},
    decision,
    audit_status: 'none',
  };
}

/** "Show exact Jev request": the bodies a browser check sent, exactly as compiled. */
export function decisionRequestView(compiled: Compiled): DecisionRequestView {
  return {
    requests: compiled.requests.map((r) => ({ model: r.model, state: r.state, questions: r.questions })),
    request_sha256: compiled.request_sha256,
    // The bodies shown are the bodies sent: nothing was rebuilt.
    matches_recorded: true,
    cpt: compiled.cpt,
    mismatch_reason: null,
  };
}
