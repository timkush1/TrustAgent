/**
 * Wire objects (src/types/api.ts shapes) for the control-plane pages: Overview, Policies,
 * Evaluators, Playground and Knowledge. They follow the contract files wherever one exists (the
 * SPEC §2.7 example policy, the built-in evaluator templates, the demo scenarios), so a fixture
 * cannot drift from what the gateway ships.
 *
 * Until contracts/schema/api.v1.schema.json covers these types (its ROOTS are the S0 core subset),
 * the golden JSON fixtures of contracts/fixtures/api cannot be validated for them; these builders
 * are type-checked against api.ts instead, which is the schema's source.
 */
import exampleYaml from '@contracts/policies/example.policies.yaml?raw';
import { parse as parseYaml } from 'yaml';
import type {
  BlockReadiness,
  CalibrationProgress,
  ConflictPair,
  EffectiveRoute,
  EvaluatorDetail,
  EvaluatorMeta,
  EvaluatorSpec,
  EvaluatorVersion,
  EvidenceCoverage,
  EvidenceSuggestion,
  IngestResult,
  KBClaim,
  KBStats,
  LabelledRate,
  OverviewStats,
  PlaygroundResult,
  PolicyDoc,
  PolicyDryRunResult,
  PolicyState,
  PolicyVersion,
  PolicyVersionMeta,
  ReliabilityResponse,
  RoiSummary,
  ValidateResult,
} from '@/types/api';
import { iso, T0 } from '../fixtures';
import { s0Decision, traceIdOf } from '../scenarios';

const templates = import.meta.glob<EvaluatorSpec>('../../../../contracts/evaluators/*.v1.json', {
  eager: true,
  import: 'default',
});

/** The built-in evaluator templates (contracts/evaluators), by id. */
export const TEMPLATES: Record<string, EvaluatorSpec> = Object.fromEntries(
  Object.values(templates).map((spec) => [spec.id, spec]),
);

const HOUR = 3_600_000;
const QUARTER = HOUR / 4;

// ------------------------------------------------------------------------------------ overview

export function labelledRate(overrides: Partial<LabelledRate> = {}): LabelledRate {
  return {
    value: null,
    label: 'measured',
    basis: 'uniform-sample deep audits only',
    n: 12,
    ci95: null,
    min_n: 30,
    ...overrides,
  };
}

export function overviewStats(overrides: Partial<OverviewStats> = {}): OverviewStats {
  // 24 hours in 15-minute buckets.
  const series = Array.from({ length: 96 }, (_, i) => ({
    t: new Date(T0 - (95 - i) * QUARTER).toISOString(),
    passed: 4 + (i % 3),
    would_flag: i % 7 === 0 ? 1 : 0,
    flagged: i % 3 === 0 ? 1 : 0,
    blocked: i % 24 === 0 ? 1 : 0,
    replaced: 0,
    not_checked: i % 4 === 0 ? 1 : 0,
    error: i === 48 ? 1 : 0,
  }));
  return {
    window: '24h',
    bucket: '15m',
    from: new Date(T0 - 24 * HOUR).toISOString(),
    to: iso(),
    include_mock: false,
    totals: {
      requests: 612,
      observed: 0,
      passed: 521,
      flagged: 34,
      blocked: 4,
      fallback: 0,
      not_assessed: 72,
      gate_errors: 1,
      audited: 58,
      audits_failed: 1,
      decision_cost_usd: 0.0243,
      llm_cost_usd: 0.41,
    },
    hallucination_rate: labelledRate(),
    audited_hallucination_share: labelledRate({
      value: 0.31,
      n: 58,
      ci95: [0.2, 0.44],
      basis: 'among audited answers (selected by risk; not a traffic rate)',
    }),
    evidence_coverage: {
      with_evidence: 540,
      groundedness_assessed: 521,
      rate: 521 / 612,
      by_source: { request_system: 410, request_context: 88, tool_results: 64, extension: 30, knowledge: 0 },
    },
    outcomes_series: series,
    what_was_caught: [
      { reason: 'contradiction:critical', count: 21, latest_trace_id: traceIdOf('refund-contradiction-flag') },
      { reason: 'groundedness:warn', count: 12, latest_trace_id: traceIdOf('refund-contradiction-flag') },
      { reason: 'det:numbers_not_in_evidence', count: 5, latest_trace_id: traceIdOf('refund-contradiction-flag') },
    ],
    latency_ms: {
      overhead: { p50: 14, p95: 41 },
      gate: { p50: 212, p95: 480 },
      upstream: { p50: 910, p95: 2400 },
      audit: { p50: 8200, p95: 17400 },
    },
    by_route: [
      { route: 'support-bot', requests: 480, flagged: 30, blocked: 0, audited: 44, hallucination_rate: labelledRate() },
      { route: 'payments-faq', requests: 132, flagged: 4, blocked: 4, audited: 14, hallucination_rate: labelledRate({ n: 3 }) },
    ],
    by_model: [{ model: 'gpt-4o-mini', requests: 612, audited: 58, hallucination_rate: labelledRate() }],
    claim_verdicts_among_audited: {
      SUPPORTED: 140,
      PARTIALLY_SUPPORTED: 12,
      CONTRADICTED: 21,
      UNSUPPORTED: 17,
      UNKNOWN: 2,
      NOT_CHECKED: 9,
    },
    top_contradicted: [{ claim: 'A full refund is available within 60 days of purchase.', count: 7, latest_trace_id: traceIdOf('refund-contradiction-flag') }],
    stage_latency_ms: {
      detectors: { p50: 1, p95: 2 },
      evidence: { p50: 2, p95: 4 },
      knowledge: { p50: null, p95: null },
      decision: { p50: 205, p95: 470 },
      queue_wait: { p50: 30, p95: 400 },
      decompose: { p50: 2100, p95: 4800 },
      retrieve: { p50: null, p95: null },
      verify: { p50: 4300, p95: 9800 },
      score: { p50: 3, p95: 6 },
    },
    backend_mix: { 'openrouter-decisions': 612 },
    ...overrides,
  };
}

/** A proxy with no traffic yet. */
export function emptyOverview(): OverviewStats {
  const base = overviewStats();
  return {
    ...base,
    totals: {
      requests: 0,
      observed: 0,
      passed: 0,
      flagged: 0,
      blocked: 0,
      fallback: 0,
      not_assessed: 0,
      gate_errors: 0,
      audited: 0,
      audits_failed: 0,
      decision_cost_usd: 0,
      llm_cost_usd: 0,
    },
    hallucination_rate: labelledRate({ n: 0 }),
    audited_hallucination_share: labelledRate({ n: 0 }),
    evidence_coverage: {
      with_evidence: 0,
      groundedness_assessed: 0,
      rate: null,
      by_source: { request_system: 0, request_context: 0, tool_results: 0, extension: 0, knowledge: 0 },
    },
    outcomes_series: [],
    what_was_caught: [],
    by_route: [],
    by_model: [],
    top_contradicted: [],
    backend_mix: {},
  };
}

export function roiSummary(overrides: Partial<RoiSummary> = {}): RoiSummary {
  return {
    window: '24h',
    gated_responses: 540,
    simulated: false,
    decision_cost: {
      value: 0.0243,
      label: 'measured',
      basis: 'reported by OpenRouter per call',
      usd: 0.0243,
      per_1k: 0.045,
    },
    llm_cost: {
      value: 0.41,
      label: 'measured',
      basis: 'deep-audit judge calls, reported cost',
      usd: 0.41,
      audits: 58,
      by_selection: { high: 44, sample: 10, uniform: 4 },
    },
    total_per_1k: { value: 0.8, label: 'measured', basis: 'fast check plus deep audit, per 1,000 gated answers' },
    counterfactuals: [
      {
        kind: 'llm_same_checks',
        method: 'token_model',
        usd: 1.22,
        low: 1.05,
        high: 1.4,
        per_1k: 2.26,
        n: 540,
        label: 'estimate',
        basis: 'the same questions asked of Claude Haiku 4.5 at list price',
      },
    ],
    savings_vs_same_checks: { usd_low: 0.6, usd_high: 0.97, label: 'estimate' },
    outcomes: { flagged: 34, blocked: 4, fallback: 0, not_assessed: 72, label: 'measured' },
    latency_ms: { gate_p50: 212, gate_p95: 480, deadline_miss_rate: 0.004, label: 'measured' },
    sampling: { uniform_rate: 0.01, uniform_today: 4, uniform_cap: 50, uniform_stopped_at: null, llm_cap_hit_at: null },
    third_party: [],
    ...overrides,
  };
}

export function evidenceCoverage(overrides: Partial<EvidenceCoverage> = {}): EvidenceCoverage {
  return {
    window: '24h',
    route: null,
    requests: 612,
    with_evidence: 540,
    groundedness_assessed: 521,
    by_source: { request_system: 410, request_context: 88, tool_results: 64, extension: 30, knowledge: 0 },
    top_reasons: [
      { reason: 'no_evidence', count: 72 },
      { reason: 'evidence_too_short', count: 12 },
      { reason: 'evidence_packed_away', count: 7 },
    ],
    ...overrides,
  };
}

export function evidenceSuggestion(overrides: Partial<EvidenceSuggestion> = {}): EvidenceSuggestion {
  return {
    kind: 'tag_pattern',
    pattern: { pattern: '<kb_article[^>]*>(.*?)</kb_article>', flags: 's' },
    label: '<kb_article> blocks in the system prompt',
    would_gain: 340,
    sample: 500,
    example_trace_id: traceIdOf('no-evidence-not-assessed'),
    ...overrides,
  };
}

// ------------------------------------------------------------------------------------ policies

export const EXAMPLE_POLICY_YAML: string = exampleYaml;
export const EXAMPLE_POLICY_DOC = parseYaml(exampleYaml) as PolicyDoc;

function readiness(evaluator: string, missing: BlockReadiness['missing'] = []): BlockReadiness {
  return { evaluator, ready: missing.length === 0, missing };
}

export function effectiveRoutes(): EffectiveRoute[] {
  return [
    {
      name: 'support-bot',
      match: { headers: { 'X-TrustAgent-Route': 'support-bot' } },
      mode: 'flag',
      effective_mode: 'flag',
      fail: 'open',
      downgrades: [],
      block_readiness: [],
      evaluators: [
        { ref: 'groundedness@1', stage: 'active', resolved: true, calibration_state: 'none', threshold_space: 'raw', warn_at: 0.8, critical_at: 0.95 },
        { ref: 'contradiction@1', stage: 'active', resolved: true, calibration_state: 'none', threshold_space: 'raw', warn_at: 0.7, critical_at: 0.95 },
        { ref: 'commitments@1', stage: 'active', resolved: true, calibration_state: 'none', threshold_space: 'raw', warn_at: 0.6, critical_at: 0.9 },
        { ref: 'off_brand_tone@1', stage: 'shadow', resolved: true, calibration_state: 'none', threshold_space: 'raw', warn_at: 0.6, critical_at: null },
      ],
      settings: { mode: 'flag', streaming: 'passthrough', require_calibration: true },
    },
    {
      name: 'payments-faq',
      match: { headers: { 'X-TrustAgent-Route': 'payments-faq' }, paths: ['/v1/chat/completions'] },
      mode: 'fallback',
      effective_mode: 'flag',
      fail: 'closed',
      downgrades: [
        { code: 'uncalibrated', message: 'groundedness@1 is not calibrated for typesafe/jev-1.13-20260917', fix: { kind: 'link', target: '/app/evaluators/groundedness' } },
      ],
      block_readiness: [readiness('groundedness@1', ['calibration']), readiness('contradiction@1', ['calibration'])],
      evaluators: [
        { ref: 'groundedness@1', stage: 'active', resolved: true, calibration_state: 'provisional', threshold_space: 'raw', warn_at: 0.8, critical_at: 0.95 },
        { ref: 'contradiction@1', stage: 'active', resolved: true, calibration_state: 'provisional', threshold_space: 'raw', warn_at: 0.7, critical_at: 0.95 },
      ],
      settings: { mode: 'fallback', fail: 'closed', streaming: 'buffer', require_calibration: true },
    },
  ];
}

export function policyState(overrides: Partial<PolicyState> = {}): PolicyState {
  return {
    id: 3,
    created_at: iso(-2 * HOUR),
    source: 'file',
    author: 'file:config/policies.yaml',
    message: 'Loaded from policies.yaml',
    sha256: 'a'.repeat(64),
    yaml: EXAMPLE_POLICY_YAML,
    doc: structuredClone(EXAMPLE_POLICY_DOC),
    etag: '"v3"',
    locked: false,
    persisted: true,
    editable: true,
    editable_reason: null,
    effective_routes: effectiveRoutes(),
    file: { path: '/etc/trustagent/policies.yaml', sha256: 'a'.repeat(64), status: 'ok', errors: [] },
    warnings: [],
    ...overrides,
  };
}

export function policyVersionMeta(id: number, overrides: Partial<PolicyVersionMeta> = {}): PolicyVersionMeta {
  return {
    id,
    created_at: iso(-(10 - id) * HOUR),
    source: id === 1 ? 'bootstrap' : id === 2 ? 'dashboard' : 'file',
    author: id === 2 ? 'setup-admin' : 'file:config/policies.yaml',
    message: id === 1 ? 'Built-in default' : id === 2 ? 'Flag support-bot' : 'Loaded from policies.yaml',
    sha256: String(id).repeat(64).slice(0, 64),
    ...overrides,
  };
}

export function policyVersion(id: number, yaml = EXAMPLE_POLICY_YAML): PolicyVersion {
  return { ...policyVersionMeta(id), yaml, doc: parseYaml(yaml) as PolicyDoc };
}

export function validateResult(overrides: Partial<ValidateResult> = {}): ValidateResult {
  return {
    ok: true,
    errors: [],
    warnings: [],
    block_readiness: {},
    diff: '--- current\n+++ proposed\n@@ -32,3 +32,3 @@\n   - name: support-bot\n     match: {headers: {X-TrustAgent-Route: support-bot}}\n-    mode: flag\n+    mode: observe\n',
    ...overrides,
  };
}

export function dryRunResult(overrides: Partial<PolicyDryRunResult> = {}): PolicyDryRunResult {
  const zero = { observe: 0, pass: 0, flag: 0, block: 0, fallback: 0 };
  return {
    window: '7d',
    evaluated: 1840,
    replay_only: true,
    by_action_current: { ...zero, pass: 1700, flag: 128, block: 12 },
    by_action_proposed: { ...zero, observe: 1840 },
    changed: 140,
    newly_blocked: 0,
    newly_flagged: 0,
    no_longer_flagged: 128,
    by_route: [
      { route: 'support-bot', current: { ...zero, pass: 1300, flag: 110 }, proposed: { ...zero, observe: 1410 } },
      { route: 'payments-faq', current: { ...zero, pass: 400, flag: 18, block: 12 }, proposed: { ...zero, observe: 430 } },
    ],
    deep_audit_agreement: { would_block_hallucinated: 9, would_block_grounded: 1, would_block_unaudited: 2 },
    examples: [{ trace_id: traceIdOf('refund-contradiction-flag'), current: 'flag', proposed: 'observe' }],
    skipped: [{ reason: 'body_expired', count: 20 }],
    caveats: ['Replays the stored fast-check answers; checks that did not run on an answer are not re-asked.'],
    ...overrides,
  };
}

// ---------------------------------------------------------------------------------- evaluators

const ATTACHED: Record<string, EvaluatorMeta['attached_routes']> = {
  groundedness: [
    { route: 'support-bot', stage: 'active' },
    { route: 'payments-faq', stage: 'active' },
  ],
  contradiction: [
    { route: 'support-bot', stage: 'active' },
    { route: 'payments-faq', stage: 'active' },
  ],
  commitments: [{ route: 'support-bot', stage: 'active' }],
  off_brand_tone: [{ route: 'support-bot', stage: 'shadow' }],
};

export function evaluatorMeta(id: string, overrides: Partial<EvaluatorMeta> = {}): EvaluatorMeta {
  const spec = TEMPLATES[id];
  if (!spec) throw new Error(`contracts/evaluators has no ${id}`);
  return {
    id,
    name: spec.name,
    category: spec.category,
    origin: 'builtin',
    latest_version: spec.version,
    archived: false,
    attached_routes: ATTACHED[id] ?? [],
    updated_at: iso(-24 * HOUR),
    ...overrides,
  };
}

export function evaluatorMetas(): EvaluatorMeta[] {
  return Object.keys(TEMPLATES)
    .sort()
    .map((id) => evaluatorMeta(id));
}

export function calibrationProgress(ref: string, overrides: Partial<CalibrationProgress> = {}): CalibrationProgress {
  return {
    evaluator_ref: ref,
    snapshot: 'typesafe/jev-1.13-20260917',
    human_labels: { n: 43, pos: 31, neg: 12 },
    needed: { n: 200, pos: 30, neg: 30 },
    proxy_labels: 58,
    state: 'provisional',
    can_fit_measured: false,
    can_fit_provisional: true,
    ...overrides,
  };
}

export function evaluatorVersion(id: string, version = 1, overrides: Partial<EvaluatorVersion> = {}): EvaluatorVersion {
  const spec = TEMPLATES[id];
  if (!spec) throw new Error(`contracts/evaluators has no ${id}`);
  return {
    evaluator_id: id,
    version,
    spec: { ...spec, version },
    spec_sha256: `${version}`.repeat(64).slice(0, 64),
    question_sha256: 'b'.repeat(64),
    calibration_key: `${id}@${version}:${'c'.repeat(16)}`,
    created_by: 'builtin',
    created_at: iso(-72 * HOUR + version * HOUR),
    changelog: version === 1 ? 'Initial version' : `Version ${version}`,
    ...overrides,
  };
}

export function evaluatorDetail(id: string, overrides: Partial<EvaluatorDetail> = {}): EvaluatorDetail {
  const latest = evaluatorVersion(id);
  const { spec: _spec, ...meta } = latest;
  return {
    ...evaluatorMeta(id),
    versions: [meta],
    latest,
    calibration_progress: calibrationProgress(`${id}@${latest.version}`),
    ...overrides,
  };
}

export function reliability(overrides: Partial<ReliabilityResponse> = {}): ReliabilityResponse {
  const bins = Array.from({ length: 10 }, (_, i) => {
    const lo = i / 10;
    const n = [2, 1, 3, 4, 2, 5, 6, 8, 7, 5][i];
    const meanPred = lo + 0.05;
    return { lo, hi: lo + 0.1, n, mean_pred: meanPred, frac_pos: Math.min(1, Math.max(0, meanPred - 0.08 + (i % 3) * 0.04)) };
  });
  return {
    evaluator: 'groundedness@1',
    snapshot: 'typesafe/jev-1.13-20260917',
    window: '7d',
    n_by_source: { human: 31, uniform_audit: 12 },
    bins,
    ece: 0.061,
    n: 43,
    ...overrides,
  };
}

// ---------------------------------------------------------------------------------- playground

export function playgroundResult(overrides: Partial<PlaygroundResult> = {}): PlaygroundResult {
  const decision = s0Decision('refund-contradiction-flag');
  return {
    trace_id: decision.trace_id,
    upstream: { status: 200, ms: 842 },
    response_text: 'You can get a full refund within 60 days of purchase.',
    delivered_text: 'You can get a full refund within 60 days of purchase.',
    headers: {
      'X-TrustAgent-Trace-Id': decision.trace_id,
      'X-TrustAgent-Route': 'support-bot@v3',
      'X-TrustAgent-Mode': 'flag',
      'X-TrustAgent-Action': 'flag',
      'X-TrustAgent-Would-Action': 'block',
      'X-TrustAgent-Status': 'ok',
      'X-TrustAgent-Verdict': 'contradicted',
      'X-TrustAgent-Risk': '0.98',
      'X-TrustAgent-Reasons': 'contradiction:critical,groundedness:critical,det:numbers_not_in_evidence',
      'X-TrustAgent-Checked-Against': 'request_system:1',
      'X-TrustAgent-Audit': 'queued',
    },
    decision,
    audit_status: 'queued',
    ...overrides,
  };
}

// ----------------------------------------------------------------------------------- knowledge

export function kbStats(overrides: Partial<KBStats> = {}): KBStats {
  return { total_claims: 42, accepted: 39, quarantined: 3, conflict_pairs: 1, ...overrides };
}

export function kbClaim(i: number, overrides: Partial<KBClaim> = {}): KBClaim {
  return {
    claim_id: `claim-${i}`,
    claim: `Annual plans can be refunded within ${30 + i} days of purchase.`,
    source_doc_id: 'refund-policy.md',
    source_excerpt: 'Annual plans can be refunded within 30 days of purchase.',
    status: 'accepted',
    entailment_score: 0.93,
    conflicts_with: [],
    ingested_at: iso(-HOUR),
    quarantine_reason: null,
    decided_by: 'llm',
    ...overrides,
  };
}

export function conflictPair(): ConflictPair {
  return {
    claim_a: kbClaim(0),
    claim_b: kbClaim(1, { status: 'quarantined', claim: 'Annual plans can be refunded within 60 days.', quarantine_reason: 'Contradicts claim-0' }),
  };
}

export function ingestResult(overrides: Partial<IngestResult> = {}): IngestResult {
  return {
    documents_ingested: 2,
    claims_accepted: 5,
    claims_quarantined: 1,
    conflicts_detected: 1,
    warnings: [],
    cost_usd: 0.004,
    claim_results: [
      {
        claim_id: 'claim-10',
        claim: 'Monthly plans are non-refundable.',
        source_doc_id: 'refund-policy.md',
        status: 'accepted',
        entailment_score: 0.97,
        conflicts_with: [],
        decided_by: 'llm',
        quarantine_reason: null,
      },
      {
        claim_id: 'claim-11',
        claim: 'Refunds take 60 days.',
        source_doc_id: 'faq.md',
        status: 'quarantined',
        entailment_score: 0.41,
        conflicts_with: ['claim-0'],
        decided_by: 'llm',
        quarantine_reason: 'Conflicts with an accepted claim',
      },
    ],
    ...overrides,
  };
}
