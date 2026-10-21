/**
 * Wire objects (src/types/api.ts shapes) for the three S0 demo scenarios in
 * contracts/demo/scenarios.v1.json, at each stage of a trace's life: started (gate pending),
 * decided (the fast check answered) and audited (the deep audit completed).
 *
 * The request, answer, judge claims and every `expect` value come from the scenario pack, so the
 * fixtures follow the contract instead of restating it. Evaluator thresholds are the templates' own
 * (contracts/evaluators/*.v1.json). Until the P1b golden API fixtures land
 * (contracts/fixtures/api/*.json, SPEC §2.4), these are what the msw handlers serve.
 */
import scenarioPack from '@contracts/demo/scenarios.v1.json';
import contradictionSpec from '@contracts/evaluators/contradiction.v1.json';
import groundednessSpec from '@contracts/evaluators/groundedness.v1.json';
import type {
  AuditDetail,
  AuditVerdict,
  ChatMessage,
  ClaimVerdict,
  ClaimView,
  DetectorResult,
  EvaluatorResult,
  EvidenceSummary,
  GateAction,
  GateDecisionView,
  GateStatus,
  GateVerdict,
  TraceAuditStatus,
  TraceDetail,
  TraceSummary,
  WouldAction,
} from '@/types/api';
import { iso } from './fixtures';

export type ScenarioId = 'grounded-pass' | 'refund-contradiction-flag' | 'no-evidence-not-assessed';
export type Stage = 'started' | 'decided' | 'audited';

interface ScenarioJudgeClaim {
  claim: string;
  quote: string;
  verdict: ClaimVerdict;
  confidence: number;
  evidence: string[];
  reasoning: string;
}

interface Scenario {
  id: ScenarioId;
  title: string;
  route: string;
  request: { model: string; messages: ChatMessage[] };
  response: string;
  mock_answers: Record<string, { type: 'noul'; noul: number }>;
  judge: { claims: ScenarioJudgeClaim[] } | null;
  expect: {
    mode: 'flag';
    action: GateAction;
    would_action: WouldAction;
    status: GateStatus;
    verdict: GateVerdict;
    max_risk: number | null;
    reasons: string[];
    checked_against: string;
    deep_audit: GateDecisionView['deep_audit'];
    audit: TraceAuditStatus;
    audit_verdict: AuditVerdict;
    outcome: string;
  };
}

const scenarios = (scenarioPack as unknown as { scenarios: Scenario[] }).scenarios;

export function scenario(id: ScenarioId): Scenario {
  const s = scenarios.find((x) => x.id === id);
  if (!s) throw new Error(`contracts/demo/scenarios.v1.json has no scenario ${id}`);
  return s;
}

export const SCENARIO_IDS: readonly ScenarioId[] = ['grounded-pass', 'refund-contradiction-flag', 'no-evidence-not-assessed'];

/** Stable ids per scenario: UUIDv7-shaped trace ids, created one second apart. */
const INDEX: Record<ScenarioId, number> = {
  'grounded-pass': 1,
  'refund-contradiction-flag': 2,
  'no-evidence-not-assessed': 3,
};
export const traceIdOf = (id: ScenarioId) => `0192f3a0-0000-7000-8000-00000000000${INDEX[id]}`;
export const auditIdOf = (id: ScenarioId) => `0192f3a0-0000-7000-9000-00000000000${INDEX[id]}`;
const createdAt = (id: ScenarioId) => iso(INDEX[id] * 1000);

export const REFUND_DOC = 'refund-policy.md';
const EVIDENCE_TEXT = 'Annual plans can be refunded within 30 days of purchase. Monthly plans are non-refundable.';
const EVIDENCE_TOKENS = 24;

function evidenceSummary(s: Scenario): EvidenceSummary {
  const used = s.expect.checked_against === 'none' ? [] : [
    { source: 'request_system' as const, items: 1, labels: [REFUND_DOC], tokens: EVIDENCE_TOKENS },
  ];
  return {
    configured: ['request_system', 'request_context', 'tool_results', 'extension'],
    used,
    empty: used.length === 0 ? ['request_system', 'request_context', 'tool_results', 'extension'] : ['request_context', 'tool_results', 'extension'],
    knowledge: 'not_requested',
    truncated: false,
    tokens: used.length === 0 ? 0 : EVIDENCE_TOKENS,
  };
}

const userRequest = (s: Scenario) => s.request.messages.find((m) => m.role === 'user')?.content ?? null;
const systemInstructions = (s: Scenario) =>
  s.request.messages.find((m) => m.role === 'system')?.content.split('\n\n<document')[0] ?? null;

type EvaluatorSpec = typeof groundednessSpec | typeof contradictionSpec;
const SPECS: Record<string, EvaluatorSpec> = { 'groundedness@1': groundednessSpec, 'contradiction@1': contradictionSpec };

function evaluatorResult(s: Scenario, ref: string): EvaluatorResult {
  const spec = SPECS[ref];
  const qid = `${spec.id}__v${spec.version}`;
  const answer = s.mock_answers[qid];
  const thresholds = {
    warn_at: spec.outcomes.warn_at,
    critical_at: spec.outcomes.critical_at,
    escalate_from: spec.escalation.escalate_from,
  };
  const base = {
    ref,
    stage: 'active' as const,
    internal: false,
    type: 'noul' as const,
    probabilities: null,
    threshold_space: 'raw' as const,
    calibrated: false,
    calibration_state: 'none' as const,
    calibration_warning: null,
    capped: null,
    top: null,
    vendor_confidence: null,
    confidence_source: null,
    block_eligible: spec.outcomes.block_eligible,
    thresholds,
  };
  if (!answer) {
    return {
      ...base,
      type: null,
      status: 'not_assessed',
      reason: 'no_evidence',
      answer: null,
      risk_raw: null,
      risk: null,
      level: null,
      escalate: null,
      corroborated: null,
    };
  }
  const risk = answer.noul;
  const level = thresholds.critical_at !== null && risk >= thresholds.critical_at ? 'critical' : risk >= thresholds.warn_at ? 'warn' : 'ok';
  return {
    ...base,
    status: 'assessed',
    reason: null,
    answer,
    risk_raw: risk,
    risk,
    level,
    escalate: level === 'critical' ? 'numeric_or_date_unverified' : null,
    corroborated: level === 'critical' ? true : null,
  };
}

function detectors(s: Scenario): DetectorResult[] {
  if (s.expect.checked_against === 'none') return [];
  const fired = s.expect.reasons.includes('det:numbers_not_in_evidence');
  const at = s.response.indexOf('60');
  return [
    {
      id: 'numbers_not_in_evidence',
      ran: true,
      fired,
      findings: fired ? [{ text: '60', start: at, end: at + 2 }] : [],
    },
  ];
}

export function s0Decision(id: ScenarioId): GateDecisionView {
  const s = scenario(id);
  const e = s.expect;
  const evaluators = Object.fromEntries(['groundedness@1', 'contradiction@1'].map((ref) => [ref, evaluatorResult(s, ref)]));
  const noEvidence = e.checked_against === 'none';
  return {
    spec: 'decision/v1',
    trace_id: traceIdOf(id),
    attempt: 0,
    created_at: iso(INDEX[id] * 1000 + 212),
    route: s.route,
    policy_version: 1,
    mode: e.mode,
    effective_mode: e.mode,
    fail: 'open',
    status: e.status,
    skip_reason: null,
    action: e.action,
    would_action: e.would_action,
    enforced: true,
    verdict: e.verdict,
    max_risk: e.max_risk,
    calibration: 'none',
    degraded: null,
    disagreement: false,
    deep_audit: e.deep_audit,
    reasons: e.reasons,
    evaluators,
    detectors: detectors(s),
    excluded: noEvidence
      ? [
          { ref: 'groundedness@1', reason: 'no_evidence' },
          { ref: 'contradiction@1', reason: 'no_evidence' },
        ]
      : [],
    decision_model: noEvidence
      ? null
      : {
          backend: 'mock',
          model_requested: null,
          snapshot: 'mock/jev',
          calls: 1,
          attempts: 1,
          hedged: false,
          latency_ms: 4,
          connect_ms: null,
          input_tokens: 612,
          cost_usd: 0,
          cost_source: 'simulated',
        },
    compile: null,
    evidence_summary: evidenceSummary(s),
    late: false,
    gate_ms: noEvidence ? 0 : 5,
  };
}

function claims(s: Scenario): ClaimView[] {
  return (s.judge?.claims ?? []).map((c, index) => {
    const at = s.response.indexOf(c.quote);
    return {
      index,
      claim: c.claim,
      status: c.verdict,
      confidence: c.confidence,
      evidence: c.evidence,
      reasoning: c.reasoning,
      decided_by: 'llm',
      verdict_probabilities: {},
      probability_source: 'llm_confidence',
      span: at === -1 ? null : { start: at, end: at + c.quote.length, method: 'exact' },
      quote: c.quote,
      evidence_spans: [],
    };
  });
}

const counts = (list: ClaimView[]): Record<ClaimVerdict, number> => {
  const out: Record<ClaimVerdict, number> = {
    SUPPORTED: 0,
    PARTIALLY_SUPPORTED: 0,
    CONTRADICTED: 0,
    UNSUPPORTED: 0,
    UNKNOWN: 0,
    NOT_CHECKED: 0,
  };
  for (const c of list) out[c.status] += 1;
  return out;
};

/** The completed deep audit of a scenario that has a judge fixture. */
export function s0Audit(id: ScenarioId): AuditDetail {
  const s = scenario(id);
  if (!s.judge) throw new Error(`scenario ${id} has no deep audit (judge: null)`);
  const e = s.expect;
  const list = claims(s);
  return {
    audit_id: auditIdOf(id),
    trace_id: traceIdOf(id),
    created_at: iso(INDEX[id] * 1000 + 3000),
    route: s.route,
    source: 'trafficgen',
    model: s.request.model,
    status: 'completed',
    verdict: e.audit_verdict,
    not_assessed_reason: null,
    trust_score: e.audit_verdict === 'GROUNDED' ? 0.97 : 0.04,
    grade: e.audit_verdict === 'GROUNDED' ? 'A' : 'F',
    hallucination_detected: e.audit_verdict === 'HALLUCINATED',
    verdict_counts: counts(list),
    processing_ms: 2400,
    judge: null,
    gate_action: e.action,
    selection: e.deep_audit === 'high' ? 'high' : 'sample',
    inclusion_prob: null,
    query_preview: userRequest(s),
    redacted: false,
    user_query: userRequest(s),
    llm_response: s.response,
    claims: list,
    scores: null,
    degraded_reasons: [],
    error: null,
    reasoning_trace: '',
    step_timings: {},
    gate: { action: e.action, would_action: e.would_action, verdict: e.verdict, max_risk: e.max_risk, delivered: 'original' },
    evidence_summary: evidenceSummary(s),
    context_used: [],
    unsupported_policy: null,
    scoring_version: '2',
    costs: [],
  };
}

/** A scenario's trace summary at `stage`. The no-evidence scenario has no audit to wait for. */
export function s0Trace(id: ScenarioId, stage: Stage = 'audited'): TraceSummary {
  const s = scenario(id);
  const e = s.expect;
  const started: TraceSummary = {
    trace_id: traceIdOf(id),
    client_request_id: null,
    created_at: createdAt(id),
    updated_at: createdAt(id),
    source: 'trafficgen',
    api_format: 'openai_chat',
    route: s.route,
    policy_version: 1,
    mode: e.mode,
    effective_mode: null,
    model: s.request.model,
    stream: false,
    upstream_status: null,
    answer_source: 'scripted',
    action: null,
    would_action: null,
    gate_status: 'pending',
    skip_reason: null,
    verdict: null,
    max_risk: null,
    reasons: [],
    checked_against: null,
    timings: { upstream_ms: null, ttft_ms: null, gate_ms: null, overhead_ms: null, total_ms: null },
    decision_model: null,
    audit: {
      audit_id: null,
      status: 'none',
      lane: null,
      selection: null,
      verdict: null,
      trust_score: null,
      grade: null,
      verdict_counts: null,
    },
    question_preview: userRequest(s),
    response_preview: s.response,
    redacted: false,
    flags: [],
  };
  if (stage === 'started') return started;

  const d = s0Decision(id);
  const hasAudit = s.judge !== null;
  const decided: TraceSummary = {
    ...started,
    updated_at: d.created_at,
    effective_mode: d.effective_mode,
    upstream_status: 200,
    action: d.action,
    would_action: d.would_action,
    gate_status: d.status,
    verdict: d.verdict,
    max_risk: d.max_risk,
    reasons: d.reasons,
    checked_against: d.evidence_summary,
    timings: { upstream_ms: 180, ttft_ms: null, gate_ms: d.gate_ms, overhead_ms: 3, total_ms: 188 },
    decision_model: d.decision_model ? { backend: 'mock', snapshot: 'mock/jev', cost_usd: 0 } : null,
    audit: hasAudit
      ? {
          ...started.audit,
          audit_id: auditIdOf(id),
          status: 'queued',
          lane: e.deep_audit === 'high' ? 'high' : 'low',
          selection: e.deep_audit === 'high' ? 'high' : 'sample',
        }
      : { ...started.audit, status: 'not_assessed', verdict: 'NOT_ASSESSED' },
  };
  if (stage === 'decided' || !hasAudit) return decided;

  const a = s0Audit(id);
  return {
    ...decided,
    updated_at: a.created_at,
    audit: {
      ...decided.audit,
      status: 'completed',
      verdict: a.verdict,
      trust_score: a.trust_score,
      grade: a.grade,
      verdict_counts: a.verdict_counts,
    },
  };
}

/** GET /api/traces/:id for a scenario at `stage`, with the stored body (support-bot stores all). */
export function s0Detail(id: ScenarioId, stage: Stage = 'audited'): TraceDetail {
  const s = scenario(id);
  const summary = s0Trace(id, stage);
  const noEvidence = s.expect.checked_against === 'none';
  return {
    ...summary,
    body: {
      available: true,
      unavailable_reason: null,
      messages: s.request.messages,
      user_request: userRequest(s),
      system_instructions: systemInstructions(s),
      response_text: s.response,
      final_response_text: s.response,
      evidence: noEvidence
        ? []
        : [{ id: REFUND_DOC, source: 'request_system', label: REFUND_DOC, text: EVIDENCE_TEXT, truncated: false }],
      rules: {},
      truncated: false,
    },
    decisions: stage === 'started' ? [] : [s0Decision(id)],
    audit_detail: stage === 'audited' && s.judge ? s0Audit(id) : null,
    labels: [],
  };
}

/** The three scenario traces, newest first (the order GET /api/traces returns). */
export function s0Traces(stage: Stage = 'audited'): TraceSummary[] {
  return [...SCENARIO_IDS].reverse().map((id) => s0Trace(id, stage));
}

