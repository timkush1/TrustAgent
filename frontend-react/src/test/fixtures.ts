/**
 * Builders for wire objects (src/types/api.ts) used across tests. Values follow the S0 demo
 * scenarios in contracts/demo/scenarios.v1.json.
 */
import type { AuditDetail, GateDecisionView, TraceSummary, WsEvent, WsHello } from '@/types/api';

export const T0 = Date.parse('2026-09-27T12:00:00.000Z');
export const iso = (offsetMs = 0) => new Date(T0 + offsetMs).toISOString();

export function trace(overrides: Partial<TraceSummary> = {}): TraceSummary {
  return {
    trace_id: '0192f3a0-0000-7000-8000-000000000001',
    client_request_id: null,
    created_at: iso(),
    updated_at: iso(),
    source: 'trafficgen',
    api_format: 'openai_chat',
    route: 'support-bot',
    policy_version: 1,
    mode: 'flag',
    effective_mode: null,
    model: 'gpt-4o-mini',
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
    question_preview: null,
    response_preview: null,
    redacted: false,
    flags: [],
    ...overrides,
  };
}

/** The refund-contradiction-flag scenario after its decision. */
export function flaggedTrace(overrides: Partial<TraceSummary> = {}): TraceSummary {
  return trace({
    updated_at: iso(300),
    effective_mode: 'flag',
    action: 'flag',
    would_action: 'block',
    gate_status: 'ok',
    verdict: 'contradicted',
    max_risk: 0.98,
    reasons: ['contradiction:critical', 'groundedness:critical', 'det:numbers_not_in_evidence'],
    checked_against: {
      configured: ['request_system'],
      used: [{ source: 'request_system', items: 1, labels: ['refund-policy.md'], tokens: 24 }],
      empty: [],
      knowledge: 'not_requested',
      truncated: false,
      tokens: 24,
    },
    audit: {
      audit_id: 'a-1',
      status: 'queued',
      lane: 'high',
      selection: 'high',
      verdict: null,
      trust_score: null,
      grade: null,
      verdict_counts: null,
    },
    ...overrides,
  });
}

export function decision(overrides: Partial<GateDecisionView> = {}): GateDecisionView {
  return {
    spec: 'decision/v1',
    trace_id: '0192f3a0-0000-7000-8000-000000000001',
    attempt: 0,
    created_at: iso(300),
    route: 'support-bot',
    policy_version: 1,
    mode: 'flag',
    effective_mode: 'flag',
    fail: 'open',
    status: 'ok',
    skip_reason: null,
    action: 'flag',
    would_action: 'block',
    enforced: true,
    verdict: 'contradicted',
    max_risk: 0.98,
    calibration: 'none',
    degraded: null,
    disagreement: false,
    deep_audit: 'high',
    reasons: ['contradiction:critical'],
    evaluators: {},
    detectors: [],
    excluded: [],
    decision_model: null,
    compile: null,
    evidence_summary: null,
    late: false,
    gate_ms: 212,
    ...overrides,
  };
}

export function audit(overrides: Partial<AuditDetail> = {}): AuditDetail {
  return {
    audit_id: 'a-1',
    trace_id: '0192f3a0-0000-7000-8000-000000000001',
    created_at: iso(400),
    route: 'support-bot',
    source: 'trafficgen',
    model: 'gpt-4o-mini',
    status: 'completed',
    verdict: 'HALLUCINATED',
    not_assessed_reason: null,
    trust_score: 0.1,
    grade: 'F',
    hallucination_detected: true,
    verdict_counts: {
      SUPPORTED: 0,
      PARTIALLY_SUPPORTED: 0,
      CONTRADICTED: 1,
      UNSUPPORTED: 0,
      UNKNOWN: 0,
      NOT_CHECKED: 0,
    },
    processing_ms: 1200,
    judge: null,
    gate_action: 'flag',
    selection: 'high',
    inclusion_prob: null,
    query_preview: null,
    redacted: false,
    user_query: null,
    llm_response: null,
    claims: [],
    scores: null,
    degraded_reasons: [],
    error: null,
    reasoning_trace: '',
    step_timings: {},
    gate: null,
    evidence_summary: null,
    context_used: [],
    unsupported_policy: null,
    scoring_version: '2',
    costs: [],
    ...overrides,
  };
}

export function hello(overrides: Partial<WsHello> = {}): WsHello {
  return {
    protocol: 2,
    server_version: '2.0.0-dev',
    epoch: 'epoch-a',
    last_seq: 10,
    replayed_from: null,
    demo: true,
    readonly: false,
    status: { version: '2.0.0-dev', demo: true, readonly: false, engine: 'up', decision_model: 'mock', judge: 'up' },
    ...overrides,
  };
}

type Envelope<E> = E extends { type: infer T; data: infer D } ? { type: T; data: D } : never;

/** A WS v2 frame object (seq/epoch/ts filled in). */
export function frame(
  e: Envelope<Exclude<WsEvent, { type: 'hb' }>>,
  seq: number,
  epoch = 'epoch-a',
  extra: { ts?: string; trace_id?: string } = {},
): WsEvent {
  return { v: 2, seq, epoch, ts: extra.ts ?? iso(seq * 100), ...(extra.trace_id ? { trace_id: extra.trace_id } : {}), ...e } as WsEvent;
}

export const hb = (): WsEvent => ({ v: 2, type: 'hb', ts: iso() });
