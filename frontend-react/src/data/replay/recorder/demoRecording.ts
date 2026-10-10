/**
 * Builds the committed demo recording (trustagent.recording/v1, SPEC §2.13) from the demo scenario
 * pack, the way `make demo` + `export-replay` would record it: trafficgen sends every scenario of
 * one set (default `s0`, the walking-skeleton trio: grounded, refund 60 vs 30 days, no documents)
 * once per round, the mock fast check answers with the scenario's scripted probabilities, and the
 * mock judge returns the scenario's scripted claims. The `s0` set is fixed; the larger `demo` set
 * grows with streaming, Anthropic and multi-route scenarios this build does not model.
 *
 * Nothing here is typed by hand. Each decision runs through the browser gate pipeline (runGate in
 * src/lib/gate, D135: evidence extraction, detectors, compile, decide over the TypeScript decision
 * core the golden vectors pin to the Go gateway) with the scenario's scripted answers, and must
 * reproduce the scenario's `expect` block, or the build fails. The
 * manifest mirrors backend-go/internal/recording (Build, events, the pending and decided summaries
 * of the stream), so P5 can swap this file set for `export-replay` output without touching the
 * replay.
 *
 * Every recording this module writes is kind `mock` with the fixed label "Simulated (mock checks +
 * mock judge): not model output", answer_source `scripted`, and costs with cost_source
 * `simulated`. It never produces a `recorded` manifest.
 *
 * Pure: no clock, no randomness, no I/O. The caller passes the parsed contract files and the start
 * time; ids and jitter are derived from sha256 of (scenario, round).
 */
import type { EvaluatorSpec } from '@/lib/contracts/types.gen';
import { defaultBudget } from '@/lib/decision/compile';
import { sha256Hex } from '@/lib/decision/jcs';
import type { Spec } from '@/lib/decision/spec';
import { resolveRoute as resolveGateRoute, RouteError, specMap } from '@/lib/gate/route';
import { NOOP_LEDGER, runGate } from '@/lib/gate/runner';
import type { PolicyInput, ResolvedRoute } from '@/lib/gate/types';
import type { JevCall } from '@/lib/openrouter/types';
import { DEFAULT_CPT, est } from '@/lib/decision/tokens';
import type {
  AuditDetail,
  AuditScores,
  AuditVerdict,
  ChatMessage,
  ClaimVerdict,
  ClaimView,
  CostRecord,
  EvidenceItem,
  EvidenceSpan,
  EvidenceSummary,
  GateDecisionView,
  PolicyMode,
  Recording,
  TraceDetail,
  TraceSummary,
} from '@/types/api';

// ------------------------------------------------------------------------------------ constants

export const RECORDING_SCHEMA = 'trustagent.recording/v1';
/** contracts/ui/copy.v1.json recording.mock: the only label a mock recording may carry. */
export const MOCK_LABEL = 'Simulated (mock checks + mock judge): not model output';
/** The mock fast check's snapshot (backend-go internal/jev DefaultMockModel). */
export const MOCK_SNAPSHOT = 'mock/jev-heuristic-v1';
/** The list price the mock backend prices its simulated cost at (jev.DefaultPricePerMTok). */
export const MOCK_PRICE_PER_MTOK = 0.042;
/** The Python mock judge names itself provider "mock", model "mock". */
export const MOCK_JUDGE = { provider: 'mock', model: 'mock' } as const;

// ------------------------------------------------------------------------------------ inputs

export interface ScenarioClaim {
  claim: string;
  quote: string;
  verdict: ClaimVerdict;
  confidence: number;
  evidence: string[];
  reasoning: string;
}

/** One scenario of contracts/demo/scenarios.v1.json (the fields a recording needs). */
export interface Scenario {
  id: string;
  title: string;
  sets: string[];
  route: string;
  api_format: string;
  stream: boolean;
  answer_source: 'scripted' | 'generated';
  request: { model: string; messages: ChatMessage[] };
  response: string;
  mock_answers: Record<string, { type: 'noul' | 'choice' | 'score'; noul?: number; choice?: string; score?: number }>;
  judge: { claims: ScenarioClaim[] } | null;
  expect: {
    mode: PolicyMode;
    checked_against: string;
    action: string;
    would_action: string | null;
    status: string;
    verdict: string | null;
    max_risk: number | null;
    reasons: string[];
    deep_audit: string;
    audit: string;
    audit_verdict: AuditVerdict;
  };
}

export type { PolicyInput } from '@/lib/gate/types';

export interface BuildInputs {
  /** contracts/demo/scenarios.v1.json */
  scenarios: { scenarios: Scenario[] };
  /** config/policies.yaml (the policy `make demo` runs), parsed. */
  policy: PolicyInput;
  /** contracts/evaluators/*.json */
  templates: readonly EvaluatorSpec[];
}

export interface BuildOptions {
  /** The scenario set trafficgen sends (`--scenarios s0`). */
  set: string;
  /** Rounds of the scenario set (trafficgen --loop). */
  rounds: number;
  /** Pause between rounds (trafficgen --interval). */
  intervalMs: number;
  /** When the first request was sent (RFC 3339 UTC). */
  startAt: string;
  gitSha: string;
  version: string;
}

export const DEFAULT_BUILD_OPTIONS: Omit<BuildOptions, 'gitSha' | 'version'> = {
  set: 's0',
  rounds: 6,
  intervalMs: 45_000,
  startAt: '2026-09-28T09:00:00.000Z',
};

export interface BuiltRecording {
  manifest: Recording;
  /** traces/<trace_id>.json, oldest first. */
  details: TraceDetail[];
}

export class RecordingBuildError extends Error {
  constructor(message: string) {
    super(`demo recording: ${message}`);
    this.name = 'RecordingBuildError';
  }
}

// ------------------------------------------------------------------------------------ helpers


/** RFC 3339 UTC with milliseconds, as the gateway writes times. */
function iso(ms: number): string {
  return new Date(ms).toISOString();
}

/** Deterministic hex from a seed (sha256 of its canonical JSON). */
function seedHex(...parts: (string | number)[]): string {
  return sha256Hex(parts.join('\u0000'));
}

/** An integer in [lo, hi] from 4 hex digits of a seed. */
function pick(hex: string, at: number, lo: number, hi: number): number {
  const n = parseInt(hex.slice(at, at + 4), 16);
  return lo + (n % (hi - lo + 1));
}

/** A canonical lowercase UUIDv7 for epoch ms `ms`, with the random bits taken from `hex`. */
export function uuidv7(ms: number, hex: string): string {
  const t = ms.toString(16).padStart(12, '0').slice(-12);
  const variant = ((parseInt(hex[3], 16) & 0x3) | 0x8).toString(16);
  return `${t.slice(0, 8)}-${t.slice(8, 12)}-7${hex.slice(0, 3)}-${variant}${hex.slice(4, 7)}-${hex.slice(7, 19)}`;
}

/** A route's effective settings (lib/gate/route.ts), failing the build for a route the policy lacks. */
export function resolveRoute(policy: PolicyInput, name: string): ResolvedRoute {
  try {
    return resolveGateRoute(policy, name);
  } catch (err) {
    if (err instanceof RouteError) throw new RecordingBuildError(err.message);
    throw err;
  }
}

/** The mock fast check: each asked question gets the scenario's scripted answer. */
function scriptedCall(s: Scenario): JevCall {
  return {
    backend: 'mock',
    model: MOCK_SNAPSHOT,
    call: async (request) => {
      const answers: Record<string, Scenario['mock_answers'][string]> = {};
      for (const qid of Object.keys(request.body.questions)) {
        const a = s.mock_answers[qid];
        check(a !== undefined, `scenario ${s.id} has no mock answer for ${qid} (the mock backend would refuse it in strict mode)`);
        answers[qid] = a;
      }
      return { answers, usage: { input_tokens: request.est_tokens, cost: null }, snapshot: MOCK_SNAPSHOT, latency_ms: 0 };
    },
  };
}

function tokensOf(text: string): number {
  return est(text, DEFAULT_CPT);
}

// ------------------------------------------------------------------------------------ one trace

interface TraceTimes {
  /** Request received. */
  created: number;
  /** Gate decision recorded. */
  decided: number;
  /** Deep audit finished (null without an audit). */
  audited: number | null;
  upstreamMs: number;
  gateMs: number;
  overheadMs: number;
  decisionLatencyMs: number;
  auditMs: number;
}

function evidenceSummary(route: ResolvedRoute, items: readonly EvidenceItem[], packed: readonly string[], truncated: boolean): EvidenceSummary {
  const configured = route.evidence.sources;
  const used = configured
    .map((source) => {
      const mine = items.filter((it) => it.source === source && packed.includes(it.id));
      return { source, items: mine.length, labels: mine.map((it) => it.label), tokens: mine.reduce((n, it) => n + tokensOf(it.text), 0) };
    })
    .filter((u) => u.items > 0);
  return {
    configured: [...configured],
    used,
    empty: configured.filter((src) => !used.some((u) => u.source === src)),
    knowledge: 'not_requested',
    truncated,
    tokens: used.reduce((n, u) => n + u.tokens, 0),
  };
}

function check(cond: boolean, message: string): void {
  if (!cond) throw new RecordingBuildError(message);
}

function sameList(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((x, i) => x === b[i]);
}

/** Scoring v2 (SPEC §3.2 scoring/aggregate.py) over one-hot verdict distributions. */
function score(claims: readonly ClaimView[], unsupportedPolicy: 'hallucination' | 'unverifiable'): { scores: AuditScores; verdict: AuditVerdict } {
  const decided = claims.filter((c) => c.status !== 'UNKNOWN' && c.status !== 'NOT_CHECKED');
  const dist = decided.map((c) => ({
    s: c.status === 'SUPPORTED' ? 1 : c.status === 'PARTIALLY_SUPPORTED' ? 0.5 : 0,
    c: c.status === 'CONTRADICTED' ? 1 : 0,
    u: c.status === 'UNSUPPORTED' ? 1 : c.status === 'PARTIALLY_SUPPORTED' ? 0.5 : 0,
  }));
  const n = dist.length;
  const mean = (k: 's' | 'c' | 'u') => (n === 0 ? null : dist.reduce((acc, d) => acc + d[k], 0) / n);
  const F = mean('s');
  const K = mean('c');
  const V = mean('u');
  const sumS = dist.reduce((a, d) => a + d.s, 0);
  const sumC = dist.reduce((a, d) => a + d.c, 0);
  const accuracy = sumS + sumC > 0 ? sumS / (sumS + sumC) : null;
  const clamp = (x: number) => Math.min(1, Math.max(0, x));
  const trust = F === null || K === null ? null : unsupportedPolicy === 'hallucination' ? clamp(F - K) : accuracy;
  const maxC = n === 0 ? null : Math.max(...dist.map((d) => d.c));
  const maxU = n === 0 ? 0 : Math.max(...dist.map((d) => d.u));
  let verdict: AuditVerdict;
  if (claims.length === 0) verdict = 'NO_CLAIMS';
  else if (n === 0) verdict = 'UNVERIFIABLE';
  else if ((maxC ?? 0) >= 0.35 || (unsupportedPolicy === 'hallucination' && (maxU >= 0.6 || (V ?? 0) >= 0.3))) verdict = 'HALLUCINATED';
  else if ((V ?? 0) >= 0.3 || maxU >= 0.6) verdict = 'UNVERIFIABLE';
  else verdict = 'GROUNDED';
  return {
    verdict,
    scores: {
      trust_score: trust,
      faithfulness: F,
      contradiction_rate: K,
      unverified_rate: V,
      coverage: V === null ? null : 1 - V,
      accuracy_addressed: accuracy,
      max_contradiction: maxC,
      p_hallucinated: null,
      claims_total: claims.length,
      claims_decided: n,
      claims_unknown: claims.filter((c) => c.status === 'UNKNOWN').length,
    },
  };
}

function verdictCounts(claims: readonly ClaimView[]): Record<ClaimVerdict, number> {
  const out: Record<ClaimVerdict, number> = {
    SUPPORTED: 0,
    PARTIALLY_SUPPORTED: 0,
    CONTRADICTED: 0,
    UNSUPPORTED: 0,
    UNKNOWN: 0,
    NOT_CHECKED: 0,
  };
  for (const c of claims) out[c.status] += 1;
  return out;
}

interface BuiltTrace {
  detail: TraceDetail;
  times: TraceTimes;
}

async function buildTrace(s: Scenario, route: ResolvedRoute, specs: ReadonlyMap<string, Spec>, round: number, created: number): Promise<BuiltTrace> {
  const seed = seedHex('trace', s.id, round);
  const auditSeed = seedHex('audit', s.id, round);
  const traceId = uuidv7(created, seed);

  // Tier 0 and the fast check through the browser gate pipeline (D135), with the scripted mock
  // answers and no caps.
  const gate = await runGate({
    route,
    specs,
    request: s.request,
    response: s.response,
    apiFormat: 'openai_chat',
    call: scriptedCall(s),
    ledger: NOOP_LEDGER,
    maxTokens: defaultBudget().tokens,
    clock: () => created,
  });
  const ex = gate.extraction;
  const detectors = gate.detectors;
  const compiled = gate.compiled;
  const core = gate.decision;

  // The recording must show what the scenario pack promises.
  const e = s.expect;
  check(core.action === e.action, `${s.id}: action ${core.action}, expected ${e.action}`);
  check(core.would_action === e.would_action, `${s.id}: would_action ${String(core.would_action)}, expected ${String(e.would_action)}`);
  check(core.status === e.status, `${s.id}: status ${core.status}, expected ${e.status}`);
  check(core.verdict === e.verdict, `${s.id}: verdict ${String(core.verdict)}, expected ${String(e.verdict)}`);
  check(core.max_risk === e.max_risk, `${s.id}: max_risk ${String(core.max_risk)}, expected ${String(e.max_risk)}`);
  check(sameList(core.reasons, e.reasons), `${s.id}: reasons ${core.reasons.join(',')}, expected ${e.reasons.join(',')}`);
  check(core.deep_audit === e.deep_audit, `${s.id}: deep_audit ${core.deep_audit}, expected ${e.deep_audit}`);

  // Simulated timings of the demo stack (mock upstream answer, mock fast check, mock judge).
  const called = compiled.requests.length > 0;
  const upstreamMs = pick(seed, 20, 140, 260);
  const decisionLatencyMs = called ? pick(seed, 24, 3, 9) : 0;
  const gateMs = decisionLatencyMs + pick(seed, 28, 1, 3);
  const overheadMs = pick(seed, 32, 2, 5);
  const decided = created + upstreamMs + gateMs + overheadMs;
  const inputTokens = called ? compiled.est_tokens.reduce((a, b) => a + b, 0) : 0;
  const decisionCost = called ? (inputTokens * MOCK_PRICE_PER_MTOK) / 1e6 : null;

  const packed = compiled.packed_ids;
  const summaryEvidence = evidenceSummary(route, ex.items, packed, compiled.truncated);
  const checkedAgainst = summaryEvidence.used.map((u) => `${u.source}:${u.items}`).join(',') || 'none';
  check(checkedAgainst === s.expect.checked_against, `${s.id}: checked against ${checkedAgainst}, expected ${s.expect.checked_against}`);
  const decision: GateDecisionView = {
    spec: 'decision/v1',
    trace_id: traceId,
    attempt: 0,
    created_at: iso(decided),
    route: route.name,
    policy_version: 1,
    mode: route.mode,
    fail: route.fail,
    ...core,
    detectors,
    excluded: compiled.excluded,
    decision_model: called
      ? {
          backend: 'mock',
          model_requested: MOCK_SNAPSHOT,
          snapshot: MOCK_SNAPSHOT,
          calls: compiled.requests.length,
          attempts: compiled.requests.length,
          hedged: false,
          latency_ms: decisionLatencyMs,
          connect_ms: null,
          input_tokens: inputTokens,
          cost_usd: decisionCost,
          cost_source: 'simulated',
        }
      : null,
    compile: {
      cpt: compiled.cpt,
      requests: compiled.requests.length,
      question_set_sha256: compiled.question_set_sha256,
      request_sha256: compiled.request_sha256,
      packed_evidence_ids: compiled.packed_ids,
      truncated_ids: compiled.truncated_ids,
      est_tokens: compiled.est_tokens,
    },
    evidence_summary: summaryEvidence,
    late: false,
    gate_ms: gateMs,
  } as GateDecisionView;

  // Deep audit selection (support-bot audits every pass; grounding triggers take the high lane).
  const auditLane = core.deep_audit === 'high' ? 'high' : core.deep_audit === 'sample' && route.deepAudit.pass_sample_rate >= 1 ? 'low' : null;
  const audited = auditLane !== null;
  check(audited === (s.judge !== null), `${s.id}: an audit is ${audited ? 'planned' : 'not planned'}, but the scenario ${s.judge ? 'has' : 'has no'} judge fixture`);
  const auditId = audited ? uuidv7(decided, auditSeed) : null;
  const auditMs = audited ? pick(auditSeed, 20, 900, 1600) : 0;
  const auditDone = audited ? decided + auditMs : null;

  const userRequest = ex.user_request;
  const body: TraceDetail['body'] = {
    available: true,
    unavailable_reason: null,
    messages: s.request.messages,
    user_request: userRequest,
    system_instructions: ex.system_instructions,
    response_text: s.response,
    final_response_text: s.response,
    evidence: ex.items,
    rules: {},
    truncated: false,
  };

  let auditDetail: AuditDetail | null = null;
  if (audited && s.judge && auditId && auditDone !== null) {
    const docs = ex.items;
    const claims: ClaimView[] = s.judge.claims.map((c, index) => {
      const at = s.response.indexOf(c.quote);
      const spans: EvidenceSpan[] = [];
      for (const quote of c.evidence) {
        docs.forEach((doc, docIndex) => {
          const i = doc.text.indexOf(quote);
          if (i >= 0) spans.push({ doc_index: docIndex, doc_id: doc.id, start: i, end: i + quote.length, text: quote, kind: 'quote_verified' });
        });
      }
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
        evidence_spans: spans,
      };
    });
    const unsupportedPolicy = docs.length > 0 ? 'hallucination' : 'unverifiable';
    const scored = score(claims, unsupportedPolicy);
    check(scored.verdict === e.audit_verdict, `${s.id}: audit verdict ${scored.verdict}, expected ${e.audit_verdict}`);
    const decompose = pick(auditSeed, 24, 300, 500);
    const verify = Math.max(auditMs - decompose - 20, 100);
    const costs: CostRecord[] = [
      { component: 'decompose', kind: 'llm', provider: MOCK_JUDGE.provider, model: MOCK_JUDGE.model, input_tokens: 0, output_tokens: 0, cost_usd: 0, cost_source: 'simulated', latency_ms: decompose, status: 'ok', questions: 0 },
      { component: 'verify', kind: 'llm', provider: MOCK_JUDGE.provider, model: MOCK_JUDGE.model, input_tokens: 0, output_tokens: 0, cost_usd: 0, cost_source: 'simulated', latency_ms: verify, status: 'ok', questions: 0 },
    ];
    auditDetail = {
      audit_id: auditId,
      trace_id: traceId,
      created_at: iso(decided),
      route: route.name,
      source: 'trafficgen',
      model: s.request.model,
      status: 'completed',
      verdict: scored.verdict,
      not_assessed_reason: null,
      trust_score: scored.scores.trust_score,
      grade: null,
      hallucination_detected: scored.verdict === 'HALLUCINATED',
      verdict_counts: verdictCounts(claims),
      processing_ms: auditMs,
      judge: {
        configured_provider: MOCK_JUDGE.provider,
        effective_provider: MOCK_JUDGE.provider,
        model: MOCK_JUDGE.model,
        fallback_active: false,
        fallback_reason: null,
        structured_outputs: true,
        healthy: true,
        credential_source: 'none',
      },
      gate_action: core.action,
      selection: auditLane === 'high' ? 'high' : 'sample',
      inclusion_prob: 1,
      query_preview: userRequest,
      redacted: false,
      user_query: userRequest,
      llm_response: s.response,
      claims,
      scores: scored.scores,
      degraded_reasons: [],
      error: null,
      reasoning_trace: '',
      step_timings: { decompose, verify, score: 20 },
      gate: { action: core.action, would_action: core.would_action, verdict: core.verdict, max_risk: core.max_risk, delivered: 'original' },
      evidence_summary: summaryEvidence,
      context_used: docs,
      unsupported_policy: unsupportedPolicy,
      scoring_version: 'v2',
      costs,
    };
  }

  const summary: TraceSummary = {
    trace_id: traceId,
    client_request_id: null,
    created_at: iso(created),
    updated_at: iso(auditDone ?? decided),
    source: 'trafficgen',
    api_format: 'openai_chat',
    route: route.name,
    policy_version: 1,
    mode: route.mode,
    effective_mode: core.effective_mode,
    model: s.request.model,
    stream: false,
    upstream_status: 200,
    answer_source: s.answer_source,
    action: core.action,
    would_action: core.would_action,
    gate_status: core.status,
    skip_reason: core.skip_reason,
    verdict: core.verdict,
    max_risk: core.max_risk,
    reasons: core.reasons,
    checked_against: summaryEvidence,
    timings: {
      upstream_ms: upstreamMs,
      ttft_ms: null,
      gate_ms: gateMs,
      overhead_ms: overheadMs,
      total_ms: upstreamMs + gateMs + overheadMs,
    },
    decision_model: called ? { backend: 'mock', snapshot: MOCK_SNAPSHOT, cost_usd: decisionCost } : null,
    audit: auditDetail
      ? {
          audit_id: auditDetail.audit_id,
          status: 'completed',
          lane: auditLane,
          selection: auditDetail.selection,
          verdict: auditDetail.verdict,
          trust_score: auditDetail.trust_score,
          grade: auditDetail.grade,
          verdict_counts: auditDetail.verdict_counts,
        }
      : { audit_id: null, status: 'not_assessed', lane: null, selection: null, verdict: 'NOT_ASSESSED', trust_score: null, grade: null, verdict_counts: null },
    question_preview: userRequest,
    response_preview: s.response,
    redacted: false,
    flags: [],
  };
  check(summary.audit.status === (e.audit === 'queued' ? 'completed' : e.audit), `${s.id}: audit state ${summary.audit.status}, expected ${e.audit}`);

  return {
    detail: { ...summary, body, decisions: [decision], audit_detail: auditDetail, labels: [] },
    times: { created, decided, audited: auditDone, upstreamMs, gateMs, overheadMs, decisionLatencyMs, auditMs },
  };
}

// ------------------------------------------------------------------------------------ events

type RecordingEvent = Recording['events'][number];

/** The summary as trace_started showed it (recording.go pendingSummary). */
export function pendingSummary(s: TraceSummary): TraceSummary {
  return {
    ...s,
    updated_at: s.created_at,
    effective_mode: null,
    upstream_status: null,
    action: null,
    would_action: null,
    skip_reason: null,
    verdict: null,
    max_risk: null,
    gate_status: 'pending',
    reasons: [],
    checked_against: null,
    timings: { upstream_ms: null, ttft_ms: null, gate_ms: null, overhead_ms: null, total_ms: null },
    decision_model: null,
    audit: { audit_id: null, status: 'none', lane: null, selection: null, verdict: null, trust_score: null, grade: null, verdict_counts: null },
    response_preview: null,
  };
}

/** The summary as gate_decision showed it: decided, a planned audit still queued (recording.go decidedSummary). */
export function decidedSummary(s: TraceSummary): TraceSummary {
  if (['running', 'completed', 'degraded', 'failed'].includes(s.audit.status)) {
    return {
      ...s,
      audit: { audit_id: s.audit.audit_id, status: 'queued', lane: s.audit.lane, selection: s.audit.selection, verdict: null, trust_score: null, grade: null, verdict_counts: null },
    };
  }
  return s;
}

/** recording.go events(): the WS v2 stream of every trace, t_ms after the first request. */
function events(built: readonly BuiltTrace[]): RecordingEvent[] {
  const start = built[0].times.created;
  const ms = (t: number) => Math.max(t - start, 0);
  const out: RecordingEvent[] = [];
  for (const { detail, times } of built) {
    const summary: TraceSummary = { ...detail };
    delete (summary as Partial<TraceDetail>).body;
    delete (summary as Partial<TraceDetail>).decisions;
    delete (summary as Partial<TraceDetail>).audit_detail;
    delete (summary as Partial<TraceDetail>).labels;
    const at = ms(times.created);
    out.push({ t_ms: at, type: 'trace_started', trace_id: summary.trace_id, data: { trace: pendingSummary(summary) } });
    const decidedAt = summary.timings.total_ms !== null ? at + summary.timings.total_ms : at;
    out.push({ t_ms: decidedAt, type: 'gate_decision', trace_id: summary.trace_id, data: { trace: decidedSummary(summary), decision: detail.decisions[0] } });
    if (summary.audit.audit_id && summary.audit.lane) {
      out.push({ t_ms: decidedAt, type: 'audit_queued', trace_id: summary.trace_id, data: { audit_id: summary.audit.audit_id, lane: summary.audit.lane, queue_depth: 0 } });
    }
    const a = detail.audit_detail;
    if (a) {
      const done = Math.max(ms(Date.parse(a.created_at)) + a.processing_ms, decidedAt);
      out.push({ t_ms: done, type: 'audit_completed', trace_id: summary.trace_id, data: { audit: a } });
    }
  }
  // A stable sort by time keeps each trace's own order.
  return out.map((ev, i) => ({ ev, i })).sort((x, y) => x.ev.t_ms - y.ev.t_ms || x.i - y.i).map((x) => x.ev);
}

/** The CalibrationState values from weakest to strongest (recording.go calibrationStates). */
const CALIBRATION_STATES: Recording['calibration_state'][] = ['none', 'mock', 'proxy', 'family', 'provisional', 'measured'];

/** The weakest calibration of the active, assessed, non-internal evaluators ("none" when there are none). */
function calibrationState(details: readonly TraceDetail[]): Recording['calibration_state'] {
  let weakest = CALIBRATION_STATES.length - 1;
  let found = false;
  for (const d of details) {
    for (const decision of d.decisions) {
      for (const ev of Object.values(decision.evaluators)) {
        if (ev.stage !== 'active' || ev.internal || ev.status !== 'assessed') continue;
        found = true;
        weakest = Math.min(weakest, Math.max(CALIBRATION_STATES.indexOf(ev.calibration_state), 0));
      }
    }
  }
  return found ? CALIBRATION_STATES[weakest] : 'none';
}

function roiWindow(spanMs: number): Recording['roi']['window'] {
  const windows: [Recording['roi']['window'], number][] = [
    ['15m', 15 * 60_000],
    ['1h', 3_600_000],
    ['24h', 86_400_000],
    ['7d', 7 * 86_400_000],
  ];
  return windows.find(([, d]) => spanMs <= d)?.[0] ?? '30d';
}

// ------------------------------------------------------------------------------------ build

/**
 * The demo recording: `rounds` rounds of the scenario set, `intervalMs` apart, each scenario sent
 * after the previous one answered. Throws RecordingBuildError when a decision or audit differs
 * from its scenario's `expect`.
 */
export async function buildDemoRecording(inputs: BuildInputs, opts: BuildOptions): Promise<BuiltRecording> {
  const scenarios = inputs.scenarios.scenarios.filter((s) => s.sets.includes(opts.set));
  if (scenarios.length === 0) throw new RecordingBuildError(`no scenario is in set ${JSON.stringify(opts.set)}`);
  if (!Number.isInteger(opts.rounds) || opts.rounds < 1) throw new RecordingBuildError('rounds must be a positive integer');
  const start = Date.parse(opts.startAt);
  if (Number.isNaN(start)) throw new RecordingBuildError(`startAt ${JSON.stringify(opts.startAt)} is not a time`);
  const specs = specMap(inputs.templates);

  const built: BuiltTrace[] = [];
  for (let round = 0; round < opts.rounds; round += 1) {
    let t = start + round * opts.intervalMs;
    for (const s of scenarios) {
      if (s.api_format !== 'openai_chat' || s.stream) throw new RecordingBuildError(`${s.id}: only non-streamed openai_chat scenarios are recorded`);
      const route = resolveRoute(inputs.policy, s.route);
      const trace = await buildTrace(s, route, specs, round, t);
      built.push(trace);
      // trafficgen sends the next scenario once this one answered.
      t = trace.times.decided + pick(seedHex('gap', s.id, round), 0, 40, 120);
    }
  }

  const details = built.map((b) => b.detail);
  const summaries = details.map((d) => {
    const s: TraceSummary & Partial<Pick<TraceDetail, 'body' | 'decisions' | 'audit_detail' | 'labels'>> = { ...d };
    delete s.body;
    delete s.decisions;
    delete s.audit_detail;
    delete s.labels;
    return s as TraceSummary;
  });
  const decisionUsd = summaries.reduce((acc, s) => acc + (s.decision_model?.cost_usd ?? 0), 0);
  const last = built[built.length - 1].times.created;
  const manifest: Recording = {
    schema: RECORDING_SCHEMA,
    kind: 'mock',
    label: MOCK_LABEL,
    recorded_at: iso(built[0].times.created),
    git_sha: opts.gitSha,
    trustagent_version: opts.version,
    decision_model: { backend: 'mock', snapshot: MOCK_SNAPSHOT },
    judge: { provider: MOCK_JUDGE.provider, model: MOCK_JUDGE.model, fallback_active: false },
    calibration_state: calibrationState(details),
    answer_sources: {
      scripted: summaries.filter((s) => s.answer_source === 'scripted').length,
      generated: summaries.filter((s) => s.answer_source === 'generated').length,
    },
    totals: { traces: summaries.length, decision_usd: Number(decisionUsd.toFixed(9)), llm_usd: 0 },
    // export-replay writes only the schema minimum; the replay derives the rest from the traces.
    roi: {
      window: roiWindow(last - built[0].times.created),
      gated_responses: summaries.filter((s) => s.gate_status !== 'pending' && s.gate_status !== 'skipped').length,
      simulated: true,
    } as Recording['roi'],
    traces: summaries,
    events: events(built),
  };
  return { manifest, details };
}
