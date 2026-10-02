/**
 * TrustAgent v2 REST and WebSocket v2 wire types.
 *
 * CONTRACT-OWNED FILE. It transcribes docs/v2/SPEC.md §2.6, which is normative, and it must match
 * contracts/schema/api.v1.schema.json: every schema definition carries the name, fields and enum
 * values of the type it describes here. Change it only through a contract PR that updates the
 * spec, this file, the schema and the fixtures together. Go structs use identical JSON tags.
 *
 * Conventions (SPEC §2.1):
 * - Proto-mirrored enums are UPPER_SNAKE (ClaimVerdict, AuditVerdict); all others are lower_snake.
 * - Times are RFC3339Nano UTC strings (ISODateTime); durations are integer `*_ms`; money `*_usd`.
 * - Risk values are floats in [0,1] where high = risky.
 * - Text offsets are UTF-16 code units, half-open [start, end); -1 means unaligned. JavaScript
 *   string indices are UTF-16 too, so `text.slice(start, end)` is correct for aligned offsets.
 * - Without the `content` scope the server returns the same objects with text fields set to null
 *   and `redacted: true`.
 *
 * Types generated from the other contract schemas (PolicyDoc, RouteSettings, RouteEvaluatorRef,
 * EvaluatorSpec, EvaluatorDraft, CalibrationFile, CalibrationEntry, BenchmarkRun) live in
 * src/lib/contracts/types.gen.ts (json-schema-to-typescript, drift-checked) and are re-exported
 * here, as §2.6 prints them.
 */

// primitives
/**
 * RFC3339Nano timestamp in UTC.
 * @format date-time
 */
export type ISODateTime = string;
/** Server-generated UUIDv7. Legacy rows keep trace_id = audit_id. */
export type TraceId = string;
/** Claim verdict (proto-mirrored, UPPER_SNAKE). NOT_CHECKED is excluded from all aggregates. */
export type ClaimVerdict =
  'SUPPORTED' | 'PARTIALLY_SUPPORTED' | 'CONTRADICTED' | 'UNSUPPORTED' | 'UNKNOWN' | 'NOT_CHECKED';
/** Response-level audit verdict (proto-mirrored, UPPER_SNAKE). */
export type AuditVerdict =
  'GROUNDED' | 'HALLUCINATED' | 'UNVERIFIABLE' | 'NO_CLAIMS' | 'NOT_ASSESSED';
export type AuditStatus = 'queued' | 'running' | 'completed' | 'degraded' | 'failed';
export type TraceAuditStatus =
  | 'none'
  | 'queued'
  | 'sampled_out'
  | 'dropped'
  | 'disabled'
  | 'running'
  | 'completed'
  | 'degraded'
  | 'failed'
  | 'not_assessed';
export type AuditSelection =
  'high' | 'sample' | 'error_sample' | 'uniform' | 'direct' | 'playground' | 'studio';
export type Grade = 'A' | 'B' | 'C' | 'D' | 'F';
export type PolicyMode = 'observe' | 'flag' | 'block' | 'fallback';
export type GateAction = 'observe' | 'pass' | 'flag' | 'block' | 'fallback';
export type WouldAction = 'pass' | 'flag' | 'block';
export type GateStatus = 'ok' | 'partial' | 'not_assessed' | 'skipped' | 'timeout' | 'error';
export type GateVerdict = 'supported' | 'unsupported' | 'contradicted' | 'not_assessed' | 'unknown';
export type EvaluatorLevel = 'ok' | 'warn' | 'critical';
export type EvaluatorResultStatus = 'assessed' | 'not_assessed' | 'error';
export type SkipReason =
  | 'unparseable_request'
  | 'upstream_error'
  | 'no_text'
  | 'too_long'
  | 'sampled_out'
  | 'overload'
  | 'budget'
  | 'decision_model_off'
  | 'breaker_open'
  | 'rate_limited'
  | 'client_disconnected'
  | 'undecodable'
  | 'unsupported_api'
  | 'multi_choice'
  | 'credentials_unavailable'
  | 'legacy';
export type Degraded =
  'uncalibrated' | 'budget' | 'breaker' | 'rate_limited' | 'timeout' | 'error' | 'overload' | 'off';
export type DecisionBackend =
  'openrouter-decisions' | 'openrouter-systemone' | 'typesafe' | 'compatible' | 'mock' | 'off';
export type EvidenceSource =
  'request_system' | 'request_context' | 'tool_results' | 'extension' | 'knowledge';
// jev* only from v2.1
export type DecidedBy = 'llm' | 'rule' | 'none' | 'jev' | 'jev>llm' | 'jev>llm_failed';
export type TraceSource = 'proxy' | 'direct' | 'playground' | 'test' | 'trafficgen';
export type ApiFormat = 'openai_chat' | 'openai_completions' | 'anthropic_messages' | 'direct';
export type Scope = 'ingest' | 'read' | 'content' | 'admin';
export type TimeWindow = '15m' | '1h' | '24h' | '7d' | '30d';
export type ValueLabel = 'measured' | 'estimate' | 'third_party' | 'vendor' | 'simulated';
export type CalibrationState = 'measured' | 'provisional' | 'proxy' | 'mock' | 'family' | 'none';
export type ComponentStateName = 'up' | 'degraded' | 'down' | 'disabled' | 'unknown';
/** Keyset page (`limit` 1-200, default 50, plus `cursor`); `next_cursor` is null at the end. */
export interface Page<T> {
  items: T[];
  next_cursor: string | null;
}
export interface FieldError {
  path: string;
  line?: number;
  col?: number;
  message: string;
  /** A machine code the web renders words for, e.g. reenter_secret (D37) or fallback_key_required (D41). */
  code?: string;
  /** e.g. {names: [credential]} for reenter_secret, {host} for fallback_key_required. */
  details?: Record<string, unknown>;
}
/** REST error body for every `/api/*` error (`/v1/*` errors use the upstream API's shape). */
export interface ApiErrorBody {
  error: {
    code: string;
    message: string;
    field_errors?: FieldError[];
    details?: Record<string, unknown>;
    retry_after_ms?: number | null;
  };
  trace_id?: string | null;
}
export interface ChatMessage {
  role: 'system' | 'developer' | 'user' | 'assistant' | 'tool';
  content: string;
  name?: string;
}

// evidence & decisions
export interface EvidenceItem {
  id: string;
  source: EvidenceSource;
  label: string;
  text: string;
  truncated: boolean;
  untrusted?: boolean;
}
export interface EvidenceSummary {
  configured: EvidenceSource[];
  used: { source: EvidenceSource; items: number; labels: string[]; tokens: number }[];
  empty: EvidenceSource[];
  knowledge: 'not_requested' | 'disabled' | 'used' | 'no_hits' | 'timeout' | 'error';
  truncated: boolean;
  tokens: number;
}
/** Detector hit in the answer text: UTF-16 code units, half-open; -1 means unaligned. */
export interface DetectorFinding {
  text: string;
  start: number;
  end: number;
}
export type DetectorId =
  | 'numbers_not_in_evidence'
  | 'dates_not_in_evidence'
  | 'urls_not_in_evidence'
  | 'quotes_not_in_evidence'
  | 'citation_out_of_range'
  | 'secret_pattern'
  | 'system_prompt_leak';
export interface DetectorResult {
  id: DetectorId;
  ran: boolean;
  fired: boolean;
  findings: DetectorFinding[];
}
export interface RawAnswer {
  type: 'noul' | 'choice' | 'score';
  noul?: number;
  choice?: string;
  score?: number;
  confidence?: number;
  probabilities?: Record<string, number>;
  legend?: Record<string, unknown>;
}
export interface EvaluatorResult {
  ref: string;
  stage: 'active' | 'shadow';
  internal: boolean;
  status: EvaluatorResultStatus;
  reason: string | null;
  type: 'noul' | 'choice' | 'score' | null;
  answer: RawAnswer | null;
  probabilities: Record<string, number> | null;
  risk_raw: number | null;
  risk: number | null;
  threshold_space: 'raw' | 'calibrated';
  calibrated: boolean;
  calibration_state: CalibrationState;
  calibration_warning: string | null;
  level: EvaluatorLevel | null;
  capped: 'evidence_truncated' | 'knowledge_only' | null;
  top: string | null;
  vendor_confidence: number | null;
  confidence_source: 'vendor' | 'fallback_top_prob' | 'choice_only' | null; // display only
  escalate: 'uncertain' | 'numeric_or_date_unverified' | 'error' | null;
  corroborated: boolean | null;
  block_eligible: boolean;
  thresholds: { warn_at: number; critical_at: number | null; escalate_from: number | null };
}
export interface DecisionModelUsage {
  backend: DecisionBackend;
  model_requested: string | null;
  snapshot: string | null;
  calls: number;
  attempts: number;
  hedged: boolean;
  latency_ms: number | null;
  connect_ms: number | null;
  input_tokens: number | null;
  cost_usd: number | null;
  cost_source: 'reported' | 'computed' | 'free_local' | 'simulated' | 'unknown';
}
export interface CompileInfo {
  cpt: number;
  requests: number;
  question_set_sha256: string;
  request_sha256: string[];
  packed_evidence_ids: string[];
  truncated_ids: string[];
  est_tokens: number[];
}
export interface GateDecisionView {
  // == contracts/schema/decision.v1.schema.json
  spec: 'decision/v1';
  trace_id: TraceId;
  attempt: 0 | 1;
  created_at: ISODateTime;
  route: string;
  policy_version: number | null;
  mode: PolicyMode;
  effective_mode: PolicyMode;
  fail: 'open' | 'closed';
  status: GateStatus;
  skip_reason: SkipReason | null;
  action: GateAction;
  would_action: WouldAction | null;
  enforced: boolean;
  verdict: GateVerdict | null;
  max_risk: number | null;
  calibration: 'all' | 'partial' | 'none';
  degraded: Degraded | null;
  disagreement: boolean;
  deep_audit: 'high' | 'sample' | 'sample_error' | 'none';
  reasons: string[];
  evaluators: Record<string, EvaluatorResult>;
  detectors: DetectorResult[];
  excluded: { ref: string; reason: string }[];
  decision_model: DecisionModelUsage | null;
  compile: CompileInfo | null;
  evidence_summary: EvidenceSummary | null;
  late: boolean;
  gate_ms: number | null;
}
export interface GateDecisionRow {
  trace_id: TraceId;
  created_at: ISODateTime;
  route: string;
  mode: PolicyMode;
  effective_mode: PolicyMode;
  action: GateAction;
  would_action: WouldAction | null;
  status: GateStatus;
  verdict: GateVerdict | null;
  max_risk: number | null;
  reasons: string[];
  late: boolean;
  decision_snapshot: string | null;
  decision_ms: number | null;
  cost_usd: number | null;
  audit_verdict: AuditVerdict | null;
  evaluator_levels: Record<string, EvaluatorLevel | null>;
}

// traces & audits
export interface TraceSummary {
  trace_id: TraceId;
  client_request_id: string | null;
  created_at: ISODateTime;
  updated_at: ISODateTime;
  source: TraceSource;
  api_format: ApiFormat;
  route: string;
  policy_version: number | null;
  mode: PolicyMode;
  effective_mode: PolicyMode | null;
  model: string | null;
  stream: boolean;
  upstream_status: number | null;
  answer_source: 'scripted' | 'generated' | 'live' | null;
  action: GateAction | null;
  would_action: WouldAction | null;
  gate_status: GateStatus | 'pending';
  skip_reason: SkipReason | null;
  verdict: GateVerdict | null;
  max_risk: number | null;
  reasons: string[];
  checked_against: EvidenceSummary | null;
  timings: {
    upstream_ms: number | null;
    ttft_ms: number | null;
    gate_ms: number | null;
    overhead_ms: number | null;
    total_ms: number | null;
  };
  decision_model: {
    backend: DecisionBackend;
    snapshot: string | null;
    cost_usd: number | null;
  } | null;
  audit: {
    audit_id: string | null;
    status: TraceAuditStatus;
    lane: 'high' | 'low' | null;
    selection: AuditSelection | null;
    verdict: AuditVerdict | null;
    trust_score: number | null;
    grade: Grade | null;
    verdict_counts: Record<ClaimVerdict, number> | null;
  };
  question_preview: string | null;
  response_preview: string | null;
  redacted: boolean;
  flags: string[];
}
export interface LabelView {
  target: string;
  label: string;
  labeled_by: string;
  created_at: ISODateTime;
  note: string | null;
  /** The caller gave this label: labels are per labeller, and DELETE /api/labels removes only the caller's own. */
  mine: boolean;
}
export interface TraceDetail extends TraceSummary {
  body: {
    available: boolean;
    unavailable_reason: 'expired' | 'not_stored' | 'scope' | null;
    messages: ChatMessage[];
    user_request: string | null;
    system_instructions: string | null;
    response_text: string | null;
    final_response_text: string | null;
    evidence: EvidenceItem[];
    rules: Record<string, string>;
    truncated: boolean;
  };
  decisions: GateDecisionView[];
  audit_detail: AuditDetail | null;
  labels: LabelView[];
}
export interface JudgeInfo {
  configured_provider: string;
  effective_provider: string;
  model: string;
  fallback_active: boolean;
  fallback_reason: string | null;
  structured_outputs: boolean;
  healthy: boolean;
  credential_source: 'env' | 'dashboard' | 'none';
}
export interface AuditScores {
  trust_score: number | null;
  faithfulness: number | null;
  contradiction_rate: number | null;
  unverified_rate: number | null;
  coverage: number | null;
  accuracy_addressed: number | null;
  max_contradiction: number | null;
  p_hallucinated: number | null;
  claims_total: number;
  claims_decided: number;
  claims_unknown: number;
}
export interface CostRecord {
  component: string;
  kind: 'decision' | 'llm';
  provider: string;
  model: string;
  input_tokens: number;
  output_tokens: number;
  cost_usd: number;
  cost_source: 'reported' | 'computed' | 'free_local' | 'simulated' | 'unknown';
  latency_ms: number;
  status: string;
  questions: number;
}
/** Where a claim's support sits in evidence document `doc_index`; UTF-16 code units, half-open. */
export interface EvidenceSpan {
  doc_index: number;
  doc_id: string;
  start: number;
  end: number;
  text: string;
  kind: 'quote_verified' | 'quote_unverified' | 'retrieved';
}
export interface ClaimView {
  index: number;
  claim: string | null;
  status: ClaimVerdict;
  confidence: number;
  evidence: string[];
  reasoning: string | null;
  decided_by: DecidedBy;
  verdict_probabilities: Partial<
    Record<'supported' | 'partially_supported' | 'contradicted' | 'unsupported', number>
  >;
  probability_source: 'llm_confidence' | 'llm_calibrated' | 'rule' | 'jev' | 'jev_calibrated';
  /** Claim location in the answer text: UTF-16 code units, half-open; null when unaligned. */
  span: { start: number; end: number; method: 'exact' | 'normalized' | 'fuzzy' } | null;
  quote: string | null;
  evidence_spans: EvidenceSpan[];
}
export interface AuditSummary {
  audit_id: string;
  trace_id: TraceId;
  created_at: ISODateTime;
  route: string | null;
  source: TraceSource;
  model: string | null;
  status: AuditStatus;
  verdict: AuditVerdict | null;
  not_assessed_reason: string | null;
  trust_score: number | null;
  grade: Grade | null;
  hallucination_detected: boolean;
  verdict_counts: Record<ClaimVerdict, number>;
  processing_ms: number;
  judge: JudgeInfo | null;
  gate_action: GateAction | null;
  selection: AuditSelection | null;
  inclusion_prob: number | null;
  query_preview: string | null;
  redacted: boolean;
}
export interface AuditDetail extends AuditSummary {
  user_query: string | null;
  llm_response: string | null;
  claims: ClaimView[];
  scores: AuditScores | null;
  degraded_reasons: string[];
  error: { kind: string; message: string } | null;
  reasoning_trace: string;
  step_timings: Record<string, number>;
  gate: {
    action: GateAction;
    would_action: WouldAction | null;
    verdict: GateVerdict | null;
    max_risk: number | null;
    delivered: 'original' | 'fallback' | 'blocked';
  } | null;
  evidence_summary: EvidenceSummary | null;
  context_used: EvidenceItem[];
  unsupported_policy: 'hallucination' | 'unverifiable' | null;
  scoring_version: string;
  costs: CostRecord[];
  recorded_at?: ISODateTime;
}
export interface DirectAuditRequest {
  query: string;
  response: string;
  model?: string;
  route?: string;
  context?: { id?: string; text: string; source?: string }[];
}

// status & session
/**
 * GET /health (SPEC §2.4 endpoint 1): public liveness. `audit_engine` is the cached result of the
 * real engine ping, not merely "a client exists". SPEC §2.6 leaves this shape unnamed; the name is
 * added here so the schema can describe it.
 */
export interface HealthStatus {
  status: 'ok';
  version: string;
  demo: boolean;
  audit_engine: boolean;
}
export interface ComponentState {
  state: ComponentStateName;
  detail?: string;
  checked_at?: ISODateTime;
  latency_ms?: number;
}
export interface DecisionModelStatus {
  provider: 'jev';
  backend: DecisionBackend;
  backend_reason: string;
  endpoint_host: string | null;
  model_requested: string | null;
  snapshot: string | null;
  credential: { source: 'env' | 'dashboard' | 'none'; name: string | null; masked: string | null };
  state:
    | 'off'
    | 'mock'
    | 'checking'
    | 'ready'
    | 'degraded'
    | 'breaker_open'
    | 'unauthorized'
    | 'no_credits'
    | 'budget_exhausted'
    | 'misconfigured'
    | 'unreachable';
  breaker: 'closed' | 'open' | 'half_open';
  last_error: { kind: string; message: string; hint: string; at: ISODateTime } | null;
  calibration_state: CalibrationState;
  calibrated_targets: number;
  p50_ms: number | null;
  p95_ms: number | null;
  calls_24h: number;
  cost_24h_usd: number;
  rpm_limit: number;
  rpm_used: number;
}
export interface SystemStatus {
  version: string;
  build_sha: string;
  demo: boolean;
  readonly: boolean;
  test_mode: boolean;
  auth: {
    ingest: 'keys' | 'open';
    read: 'keys' | 'public';
    setup_required: boolean;
    caller_scopes: Scope[];
  };
  internal_rpc: 'token' | 'loopback';
  limits: { max_text_chars: number; max_upload_bytes: number; max_body_bytes: number };
  persistence: ComponentState & { configured: boolean; migration: string | null };
  engine: ComponentState & {
    version?: string;
    runtime_config_version?: number;
    /** D34: the Go settings epoch of the last push the engine applied. */
    runtime_config_epoch?: string;
    max_audits?: number;
  };
  judge: {
    state: ComponentStateName;
    provider: string | null;
    model: string | null;
    credential_source: 'env' | 'dashboard' | 'none';
    fallback_active: boolean;
    fallback_reason: string | null;
    warnings: string[];
  };
  verifier: { configured: string; effective: 'llm' | 'off'; reason: string };
  decision_model: DecisionModelStatus;
  knowledge: ComponentState & { enabled: boolean; kb_claims?: number; embedding_model?: string };
  policy: {
    version: number | null;
    source: 'file' | 'dashboard' | 'rollback' | 'bootstrap' | 'adopt_file' | 'builtin';
    locked: boolean;
    persisted: boolean;
    editable: boolean;
    file_status: 'ok' | 'missing' | 'invalid' | 'diverged';
    file_error: string | null;
  };
  budgets: {
    decision_usd_day: number;
    decision_spent_today_usd: number;
    llm_usd_day: number;
    llm_spent_today_usd: number;
    decision_exhausted: boolean;
    decision_reserve_only: boolean;
    llm_exhausted: boolean;
  };
  credentials: { store: 'ready' | 'unavailable' | 'no_database'; undecryptable: string[] };
  redis: ComponentState & { configured: boolean };
  warnings: string[];
  /** The warnings that carry details (master_key_changed, fail_closed_unprotected, ...); codes also appear in warnings. */
  warning_details?: StatusWarning[];
}
/** A status or settings warning with details (D32, D77): the web renders words by code. */
export interface StatusWarning {
  code: string;
  message: string;
  details?: WarningDetails;
}
export interface WarningDetails {
  /** fail_closed_unprotected, confirmation_required: the enforcing routes affected (D77). */
  routes?: string[];
  /** master_key_changed, reenter_secret: the credential names (D32, D37). */
  names?: string[];
}
export type SystemStatusLite = Pick<SystemStatus, 'version' | 'demo' | 'readonly'> & {
  engine: ComponentStateName;
  decision_model: DecisionModelStatus['state'];
  judge: ComponentStateName;
};
export interface ReadyStatus {
  status: 'ready' | 'degraded' | 'not_ready';
  components: Record<
    'proxy' | 'policy' | 'database' | 'engine' | 'judge' | 'decision_model' | 'knowledge' | 'redis',
    string
  >;
}
export interface SetupStatus {
  setup_required: boolean;
  ingest_auth: 'keys' | 'open';
  demo: boolean;
  readonly: boolean;
  persistence: boolean;
  public_url: string;
  recovery_command: string;
}
export interface SessionInfo {
  actor: string;
  scopes: Scope[];
  expires_at: ISODateTime;
  sliding: boolean;
  created_admin_key?: string;
  /** The api_keys id of the key this caller signed in with; absent for an env key. */
  key_id?: string;
}
export interface ApiKeyView {
  id: string;
  label: string;
  scopes: Scope[];
  prefix: string;
  created_at: ISODateTime;
  last_used_at: ISODateTime | null;
  revoked_at: ISODateTime | null;
}
export interface AdminAuditEntry {
  id: number;
  at: ISODateTime;
  actor: string;
  action: string;
  target: string | null;
  diff: unknown;
}

// overview / roi / evidence
export interface LabelledValue {
  value: number | null;
  label: ValueLabel;
  basis: string;
  source_url?: string;
  as_of?: string;
}
export interface LabelledRate extends LabelledValue {
  n: number;
  ci95: [number, number] | null;
  min_n: number;
}
export interface OverviewStats {
  window: TimeWindow;
  bucket: '1m' | '15m' | '6h' | '1d';
  from: ISODateTime;
  to: ISODateTime;
  include_mock: boolean;
  totals: {
    requests: number;
    observed: number;
    passed: number;
    flagged: number;
    blocked: number;
    fallback: number;
    not_assessed: number;
    gate_errors: number;
    audited: number;
    audits_failed: number;
    decision_cost_usd: number;
    llm_cost_usd: number;
  };
  hallucination_rate: LabelledRate; // uniform-sample audits only; value null when n < min_n (30)
  // basis "among audited answers (selected by risk; not a traffic rate)"
  audited_hallucination_share: LabelledRate;
  evidence_coverage: {
    with_evidence: number;
    groundedness_assessed: number;
    rate: number | null;
    by_source: Record<EvidenceSource, number>;
  };
  outcomes_series: {
    t: ISODateTime;
    passed: number;
    would_flag: number;
    /** Observe-only answers the gate would have blocked (counted apart from would_flag). */
    would_block: number;
    flagged: number;
    blocked: number;
    replaced: number;
    not_checked: number;
    error: number;
  }[];
  what_was_caught: { reason: string; count: number; latest_trace_id: TraceId }[];
  latency_ms: Record<
    'overhead' | 'gate' | 'upstream' | 'audit',
    { p50: number | null; p95: number | null }
  >;
  by_route: {
    route: string;
    requests: number;
    flagged: number;
    blocked: number;
    audited: number;
    hallucination_rate: LabelledRate;
  }[];
  by_model: {
    model: string;
    requests: number;
    audited: number;
    hallucination_rate: LabelledRate;
  }[];
  claim_verdicts_among_audited: Record<ClaimVerdict, number>;
  top_contradicted: { claim: string | null; count: number; latest_trace_id: TraceId }[];
  stage_latency_ms: Record<
    | 'detectors'
    | 'evidence'
    | 'knowledge'
    | 'decision'
    | 'queue_wait'
    | 'decompose'
    | 'retrieve'
    | 'verify'
    | 'score',
    { p50: number | null; p95: number | null }
  >;
  backend_mix: Partial<Record<DecisionBackend, number>>;
}
export interface Counterfactual {
  kind: 'llm_same_checks' | 'llm_full_claim_audit';
  method: 'token_model' | 'uniform_sample' | 'fitted_from_audits' | 'static_assumptions';
  usd: number | null;
  low: number | null;
  high: number | null;
  per_1k: number | null;
  n: number;
  label: 'estimate' | 'simulated';
  basis: string;
}
export interface RoiSummary {
  window: TimeWindow;
  gated_responses: number;
  simulated: boolean;
  decision_cost: LabelledValue & { usd: number; per_1k: number | null };
  llm_cost: LabelledValue & {
    usd: number;
    audits: number;
    by_selection: Partial<Record<AuditSelection, number>>;
  };
  total_per_1k: LabelledValue;
  counterfactuals: Counterfactual[];
  savings_vs_same_checks: {
    usd_low: number | null;
    usd_high: number | null;
    label: 'estimate' | 'simulated';
  };
  outcomes: {
    flagged: number;
    blocked: number;
    fallback: number;
    not_assessed: number;
    label: 'measured' | 'simulated';
  };
  latency_ms: {
    gate_p50: number | null;
    gate_p95: number | null;
    deadline_miss_rate: number | null;
    label: 'measured' | 'simulated';
  };
  sampling: {
    uniform_rate: number;
    uniform_today: number;
    uniform_cap: number;
    uniform_stopped_at: ISODateTime | null;
    llm_cap_hit_at: ISODateTime | null;
  };
  third_party: string[];
}
export interface RoiTimeseries {
  window: TimeWindow;
  bucket: '1h' | '1d';
  points: {
    t: ISODateTime;
    decision_usd: number;
    llm_usd: number;
    same_checks_usd: number | null;
    gated: number;
  }[];
}
export interface PriceEntry {
  match: string;
  provider: string;
  input_per_mtok: number;
  output_per_mtok: number;
  source?: string;
  label?: 'vendor_price' | 'override';
  as_of?: string;
  note?: string;
}
export interface PricingTable {
  version: string;
  currency: 'USD';
  entries: PriceEntry[];
}
export interface EvidenceCoverage {
  window: TimeWindow;
  route: string | null;
  requests: number;
  with_evidence: number;
  groundedness_assessed: number;
  by_source: Record<EvidenceSource, number>;
  top_reasons: {
    reason: 'no_evidence' | 'evidence_too_short' | 'evidence_packed_away' | 'too_long';
    count: number;
  }[];
}
export interface EvidenceSuggestion {
  kind: 'tag_pattern' | 'line_header' | 'extension';
  pattern: { pattern: string; flags: string } | null;
  label: string;
  would_gain: number;
  sample: number;
  example_trace_id: TraceId | null;
}
/** GET /api/evidence/suggestions (#51, D64). */
export interface EvidenceSuggestionList {
  items: EvidenceSuggestion[];
}
/** One decision-model request body as compiled (session_id is never returned). */
export interface JevRequest {
  model: string;
  state: Record<string, unknown>;
  questions: Record<string, unknown>;
}
/** GET /api/traces/:id/decision-request (#12, D63): the glass box. */
export interface DecisionRequestView {
  requests: JevRequest[];
  request_sha256: string[];
  matches_recorded: boolean;
  cpt: number;
  mismatch_reason: 'body_truncated' | 'policy_missing' | 'evaluator_missing' | null;
}

// policies
export type { PolicyDoc, RouteSettings, RouteEvaluatorRef } from '@/lib/contracts/types.gen';
export interface PolicyVersionMeta {
  id: number;
  created_at: ISODateTime;
  source: 'bootstrap' | 'file' | 'dashboard' | 'rollback' | 'adopt_file';
  author: string;
  message: string;
  sha256: string;
}
export interface PolicyVersion extends PolicyVersionMeta {
  yaml: string;
  doc: import('@/lib/contracts/types.gen').PolicyDoc;
}
export interface Downgrade {
  code:
    | 'stream_not_buffered'
    | 'uncalibrated'
    | 'snapshot_changed'
    | 'budget'
    | 'breaker_open'
    | 'decision_model_off'
    | 'trusts_user_context';
  message: string;
  fix: { kind: 'link' | 'action'; target: string } | null;
}
export interface BlockReadiness {
  evaluator: string;
  ready: boolean;
  missing: ('evidence' | 'corroborator' | 'calibration' | 'buffering' | 'mode')[];
}
export interface EffectiveRoute {
  name: string;
  match: Record<string, unknown>;
  mode: PolicyMode;
  effective_mode: PolicyMode;
  fail: 'open' | 'closed';
  downgrades: Downgrade[];
  block_readiness: BlockReadiness[];
  evaluators: {
    ref: string;
    stage: 'active' | 'shadow';
    resolved: boolean;
    calibration_state: CalibrationState;
    threshold_space: 'raw' | 'calibrated';
    warn_at: number;
    critical_at: number | null;
  }[];
  settings: import('@/lib/contracts/types.gen').RouteSettings;
}
export interface PolicyState extends PolicyVersion {
  etag: string;
  locked: boolean;
  persisted: boolean;
  editable: boolean;
  editable_reason: string | null;
  effective_routes: EffectiveRoute[];
  file: {
    path: string;
    sha256: string | null;
    status: 'ok' | 'missing' | 'invalid' | 'diverged';
    errors: FieldError[];
  };
  warnings: string[];
}
export interface ValidateResult {
  ok: boolean;
  errors: FieldError[];
  warnings: FieldError[];
  block_readiness: Record<string, BlockReadiness[]>;
  diff: string;
}
export interface PolicyDryRunResult {
  window: TimeWindow;
  evaluated: number;
  replay_only: true;
  by_action_current: Record<GateAction, number>;
  by_action_proposed: Record<GateAction, number>;
  changed: number;
  newly_blocked: number;
  newly_flagged: number;
  no_longer_flagged: number;
  by_route: {
    route: string;
    current: Record<GateAction, number>;
    proposed: Record<GateAction, number>;
  }[];
  deep_audit_agreement: {
    would_block_hallucinated: number;
    would_block_grounded: number;
    would_block_unaudited: number;
  };
  examples: { trace_id: TraceId; current: GateAction; proposed: GateAction }[];
  skipped: { reason: 'evaluator_not_in_history' | 'no_answers' | 'body_expired'; count: number }[];
  caveats: string[];
}

// evaluators, studio, calibration
export type {
  EvaluatorSpec,
  EvaluatorDraft,
  CalibrationFile,
  CalibrationEntry,
  BenchmarkRun,
} from '@/lib/contracts/types.gen';
export interface CalibrationProgress {
  evaluator_ref: string;
  snapshot: string | null;
  human_labels: { n: number; pos: number; neg: number };
  needed: { n: 200; pos: 30; neg: 30 };
  proxy_labels: number;
  state: CalibrationState;
  can_fit_measured: boolean;
  can_fit_provisional: boolean;
}
export interface EvaluatorMeta {
  id: string;
  name: string;
  category: string;
  origin: 'builtin' | 'file' | 'dashboard';
  latest_version: number;
  archived: boolean;
  attached_routes: { route: string; stage: 'active' | 'shadow' }[];
  updated_at: ISODateTime;
}
export interface EvaluatorVersion {
  evaluator_id: string;
  version: number;
  spec: import('@/lib/contracts/types.gen').EvaluatorSpec;
  spec_sha256: string;
  question_sha256: string;
  calibration_key: string;
  created_by: string;
  created_at: ISODateTime;
  changelog: string;
}
export interface EvaluatorDetail extends EvaluatorMeta {
  versions: Omit<EvaluatorVersion, 'spec'>[];
  latest: EvaluatorVersion;
  calibration_progress: CalibrationProgress;
}
export interface Lint {
  code: string;
  severity: 'error' | 'warning';
  path: string;
  message: string;
}
/** POST /api/evaluators/validate (#30). */
export interface EvaluatorValidateRequest {
  spec: import('@/lib/contracts/types.gen').EvaluatorSpec;
}
/** Schema errors and lints (Go internal/decision/lint.go is the reference, D54); hashes are null while errors remain. */
export interface EvaluatorValidateResult {
  errors: FieldError[];
  lints: Lint[];
  question_sha256: string | null;
  calibration_key: string | null;
  question_tokens: number | null;
}
/** POST /api/evaluators (#31). */
export interface EvaluatorCreateRequest {
  spec: import('@/lib/contracts/types.gen').EvaluatorSpec;
  changelog: string;
}
/** POST /api/evaluators/:id/versions (#32): base_version is the latest version the editor started from. */
export interface EvaluatorVersionCreateRequest {
  spec: import('@/lib/contracts/types.gen').EvaluatorSpec;
  changelog: string;
  base_version: number;
}
/** POST /api/evaluators/:id/archive (#33). */
export interface EvaluatorArchiveRequest {
  archived: boolean;
}
export interface StudioDraftResponse {
  draft: import('@/lib/contracts/types.gen').EvaluatorDraft | null;
  errors: FieldError[];
  lints: Lint[];
  model: string;
  cost_usd: number;
  /** D82: simulated for a mock judge, so the draft cost is labelled Simulated. */
  cost_source: CostRecord['cost_source'];
  attempts: number;
}
/** POST /api/studio/draft (#34). */
export interface StudioDraftRequest {
  /** At most 2000 characters. */
  description: string;
  route?: string;
}
/** POST /api/studio/samples (#36). */
export interface StudioSamplesRequest {
  route?: string;
  window: TimeWindow;
  filter: 'flagged' | 'random' | 'disagreement';
  /** At most 50. */
  limit?: number;
}
export interface StudioSamplesResponse {
  samples: StudioSample[];
}
export interface StudioSample {
  id: string;
  trace_id?: TraceId;
  user_request?: string;
  assistant_response: string;
  evidence?: { id?: string; text: string; source?: EvidenceSource }[];
  system_instructions?: string;
  rules?: Record<string, string>;
  expected?: 'fire' | 'no_fire';
}
export interface StudioTestRequest {
  spec?: import('@/lib/contracts/types.gen').EvaluatorSpec;
  ref?: string;
  samples: StudioSample[];
  thresholds?: { warn_at?: number; critical_at?: number | null };
}
export interface StudioTestResponse {
  run_id: string;
  results: {
    sample_id: string;
    status: EvaluatorResultStatus;
    reason: string | null;
    answer: RawAnswer | null;
    probabilities: Record<string, number> | null;
    risk_raw: number | null;
    risk: number | null;
    level: EvaluatorLevel | null;
    escalate: string | null;
    fires: boolean | null;
    latency_ms: number;
    cost_usd: number;
    expected: 'fire' | 'no_fire' | null;
    agrees: boolean | null;
  }[];
  summary: {
    n: number;
    assessed: number;
    agreement: number | null;
    cost_usd: number;
    snapshot: string | null;
    backend: DecisionBackend;
  };
}
export interface QuickCalibrationRequest {
  labeled: { risk_raw: number; label: boolean }[];
  objective?: 'max_f1' | 'min_threshold_at_precision';
  target_precision?: number;
  current?: number;
}
export interface QuickCalibration {
  n: number;
  n_pos: number;
  n_neg: number;
  enough: boolean;
  message: string;
  threshold: number | null;
  precision: { point: number; ci95: [number, number] } | null;
  recall: { point: number; ci95: [number, number] } | null;
  loo_range: [number, number] | null;
  unstable: boolean;
  mapping: {
    method: 'platt' | 'isotonic' | null;
    table: number[] | null;
    platt: { a: number; b: number } | null;
  };
  grid: { threshold: number; tp: number; fp: number; tn: number; fn: number }[];
}
export interface StudioDryRunRequest {
  spec?: import('@/lib/contracts/types.gen').EvaluatorSpec;
  ref?: string;
  thresholds?: { warn_at?: number; critical_at?: number | null };
  route: string;
  window: '24h' | '7d';
  max_samples: number;
  confirm_cost?: boolean;
}
export interface DryRunResult {
  run_id: string;
  evaluator_ref: string;
  route: string;
  window: TimeWindow;
  sampled: number;
  population: number;
  assessed: number;
  not_assessed: number;
  errors: number;
  fired: number;
  fire_rate: number;
  fire_rate_ci95: [number, number];
  extrapolated_fires: number;
  overlap: { evaluator_ref: string; both_fired: number }[];
  agreement_with_audit: { n: number; agree: number } | null;
  examples: { trace_id: TraceId; risk: number; level: EvaluatorLevel }[];
  cost_usd: number;
  snapshot: string | null;
}
export type StudioDryRunEvent =
  | { event: 'estimate'; data: { est_cost_usd: number; sampled: number; population: number } }
  | { event: 'progress'; data: { evaluated: number; fired: number; cost_usd: number } }
  | { event: 'done'; data: DryRunResult }
  | { event: 'error'; data: ApiErrorBody };
export interface LabelQueueItem {
  trace_id: TraceId;
  created_at: ISODateTime;
  route: string;
  evaluator_ref: string;
  risk_raw: number | null;
  level: EvaluatorLevel | null;
  user_request: string | null;
  response_text: string | null;
  evidence_labels: string[];
}
export interface CalibrationRecordMeta {
  id: number;
  created_at: ISODateTime;
  origin: 'file' | 'import' | 'fit';
  backend: string;
  snapshot: string;
  family: string;
  target_kind: string;
  target_key: string;
  quality: 'measured' | 'provisional' | 'proxy' | 'mock';
  active: boolean;
}
export interface CalibrationRecord extends CalibrationRecordMeta {
  entry: import('@/lib/contracts/types.gen').CalibrationEntry;
}
export interface ReliabilityResponse {
  evaluator: string;
  snapshot: string | null;
  window: TimeWindow;
  n_by_source: Record<string, number>;
  bins: { lo: number; hi: number; n: number; mean_pred: number; frac_pos: number }[];
  ece: number | null;
  n: number;
}

// Labels and calibration (D55, D59-D62)
/** <id>@<v> takes should_fire|should_not_fire; claim:<i> a claim verdict; response hallucinated|grounded (D59). */
export type LabelValue = 'should_fire' | 'should_not_fire' | ClaimVerdict | 'hallucinated' | 'grounded';
/** PUT /api/labels (#40). */
export interface LabelPut {
  trace_id: TraceId;
  target: string;
  label: LabelValue;
  note?: string;
}
/** DELETE /api/labels (#41). */
export interface LabelDelete {
  trace_id: TraceId;
  target: string;
}
/** GET /api/labels/queue (#39, D60). */
export interface LabelQueue {
  items: LabelQueueItem[];
}
/** GET /api/calibration (#42). */
export interface CalibrationList {
  items: CalibrationRecordMeta[];
}
/** POST /api/calibration (#44). */
export interface CalibrationImportRequest {
  file: import('@/lib/contracts/types.gen').CalibrationFile;
  activate: boolean;
}
export interface CalibrationImportResult {
  imported: CalibrationRecordMeta[];
  rejected: {
    target_key: string | null;
    reason: 'unknown_version' | 'calibration_key_mismatch' | 'mock_snapshot' | 'invalid';
    message: string;
  }[];
}
/** POST /api/calibration/fit (#45). */
export interface CalibrationFitRequest {
  target: string;
  route?: string;
  snapshot?: string;
  sources: ('labels' | 'uniform_audit')[];
  method?: 'auto' | 'isotonic' | 'platt';
  /** The adopt flow fits with activate:false, previews, then activates (D62). */
  activate: boolean;
}
export interface CalibrationFitMetrics {
  ece_before: number | null;
  ece_after: number | null;
  brier_before: number | null;
  brier_after: number | null;
  auroc: number | null;
}
/** Recommended thresholds in calibrated space (calibration.v1 recommended). */
export interface CalibrationRecommended {
  space: 'calibrated';
  warn_at: { value: number; precision: number; recall: number } | null;
  critical_at: { value: number; precision_lb95: number; predicted_pos: number } | null;
  critical_null_reason: string | null;
}
export interface CalibrationFitResponse {
  record: CalibrationRecord;
  reliability: ReliabilityResponse;
  metrics: CalibrationFitMetrics;
  recommended: CalibrationRecommended;
}

// settings / credentials
export interface Bindings {
  decision_model: string | null;
  judge: string | null;
  upstream_default: string | null;
}
export interface UpstreamSetting {
  url: string | null;
  credential: string | null;
  auth_header: 'authorization' | 'api-key' | 'x-api-key';
}
export interface RuntimeSettings {
  decision_model: {
    backend: DecisionBackend | 'auto';
    model: string | null;
    base_url: string | null;
    path: string | null;
  };
  judge: {
    provider: 'auto' | 'anthropic' | 'openrouter' | 'openai' | 'ollama' | 'mock' | 'none';
    model: string | null;
  };
  upstream: {
    default: UpstreamSetting;
    anthropic: UpstreamSetting;
    fallback: UpstreamSetting & { format: 'openai' | 'anthropic' };
  };
  bindings: Bindings;
  budgets: { decision_usd_day: number; llm_usd_day: number };
  pricing_overrides: PriceEntry[];
}
export interface SettingKeyView {
  key: string;
  env: string | null;
  value: string;
  default: string;
  source: 'default' | 'dotenv' | 'env' | 'dashboard' | 'file' | 'seed';
  secret: boolean;
  advanced: boolean;
  restart_required: boolean;
  editable: boolean;
  services: string[];
}
export interface SettingsView {
  version: number;
  etag: string;
  groups: { name: string; keys: SettingKeyView[] }[];
  runtime: RuntimeSettings;
  env_managed: string[];
  warnings: { key: string; message: string; hint: string; code?: string; details?: WarningDetails }[];
  dotenv_path: string | null;
}
export interface CredentialView {
  name: string;
  provider: 'openrouter' | 'typesafe' | 'anthropic' | 'openai' | 'generic';
  source: 'env' | 'dashboard';
  masked: string;
  fingerprint: string;
  allowed_hosts: string[];
  created_at: ISODateTime | null;
  rotated_at: ISODateTime | null;
  last_verified_at: ISODateTime | null;
  last_verify_status: 'ok' | 'unauthorized' | 'no_credits' | 'unreachable' | 'unknown' | null;
  last_used_at: ISODateTime | null;
  used_by: (
    'decision_model' | 'judge' | 'upstream_default' | 'upstream_anthropic' | 'upstream_fallback'
  )[];
  undecryptable: boolean;
}
export interface DecisionModelTestResult {
  ok: boolean;
  backend: DecisionBackend;
  endpoint_host: string | null;
  model_requested: string | null;
  snapshot: string | null;
  latency_ms: number | null;
  input_tokens: number | null;
  cost_usd: number | null;
  cost_source: string | null;
  error: { kind: string; status: number | null; message: string; hint: string } | null;
}

// Settings writes, credentials, Connect OpenRouter, keys (D29-D41, D75-D81)
/** What a credential write or a verify may bind (D75): never the upstreams, which bind through PATCH. */
export type BindingTarget = 'decision_model' | 'judge';
/** PATCH /api/settings body (#53): any subset of RuntimeSettings, nested objects partial too. */
export interface RuntimeSettingsPatch {
  decision_model?: Partial<RuntimeSettings['decision_model']>;
  judge?: Partial<RuntimeSettings['judge']>;
  upstream?: {
    default?: Partial<UpstreamSetting>;
    anthropic?: Partial<UpstreamSetting>;
    fallback?: Partial<UpstreamSetting & { format: 'openai' | 'anthropic' }>;
  };
  bindings?: Partial<Bindings>;
  budgets?: Partial<RuntimeSettings['budgets']>;
  pricing_overrides?: PriceEntry[];
}
export type CredentialProvider = CredentialView['provider'];
export type CredentialVerifyStatus = 'ok' | 'unauthorized' | 'no_credits' | 'unreachable' | 'unknown';
/** PUT /api/credentials/:name (#55). base_url only for compatible and upstream-* (D29). */
export interface CredentialPutRequest {
  provider: CredentialProvider;
  secret: string;
  base_url?: string;
  /** Default true; bind with verify:false is a 422 (D75). */
  verify?: boolean;
  bind?: BindingTarget[];
}
/** POST /api/credentials/:name/verify (#57). */
export interface CredentialVerifyRequest {
  bind?: BindingTarget[];
}
/** The result of checking a key with its provider (D40). The secret never appears here. */
export interface CredentialVerification {
  ok: boolean;
  status: CredentialVerifyStatus;
  /** A code the web renders words for (e.g. no_credits), or null. */
  hint: string | null;
  latency_ms: number | null;
  cost_usd: number | null;
  cost_source: CostRecord['cost_source'] | null;
  /** OpenRouter GET /api/v1/key: the key's label and spending limit (null = no limit). */
  key_label?: string | null;
  limit_usd?: number | null;
  limit_remaining_usd?: number | null;
}
export type BindingSkipReason = 'env_managed' | 'backend_pinned' | 'verification_failed';
/** #55, #57 and #59 (D75): one shared write; bindings apply only when verification.ok. */
export interface CredentialWriteResult {
  credential: CredentialView;
  /** false when a failed verification left the currently bound key in place. */
  stored: boolean;
  bindings: { applied: BindingTarget[]; skipped: { target: BindingTarget; reason: BindingSkipReason }[] };
  /** null when the write asked for verify:false. */
  verification: CredentialVerification | null;
}
export interface CredentialList {
  items: CredentialView[];
}
/** POST /api/connect/openrouter/start (#58, D38). */
export interface ConnectStartRequest {
  /** A dashboard path under /app/ to return to. */
  return_to?: string;
}
export interface ConnectStartResponse {
  auth_url: string;
  nonce: string;
  expires_in: number;
}
/** POST /api/connect/openrouter/complete (#59, D38). */
export interface ConnectCompleteRequest {
  code: string;
  nonce: string;
  bind?: BindingTarget[];
}
export type ConnectCompleteResponse = CredentialWriteResult & { return_to: string };
/** POST /api/ws-ticket (#7, D71): a one-use ticket for ?ticket= on the WebSocket URL. */
export interface WsTicket {
  ticket: string;
  expires_in: number;
}
/** POST /api/keys (#68). */
export interface ApiKeyCreateRequest {
  label: string;
  scopes: Scope[];
}
/** The created key: secret is shown once and never again. */
export type CreatedApiKey = ApiKeyView & { secret: string };
export interface ApiKeyList {
  items: ApiKeyView[];
}

// playground / kb / recording
export interface PlaygroundRequest {
  mode: 'chat' | 'audit';
  route?: string;
  model?: string;
  messages: ChatMessage[];
  evidence?: { id?: string; text: string; source?: string }[];
  response?: string;
  stream?: boolean;
  mode_override?: PolicyMode;
  scenario?: string;
}
export interface PlaygroundResult {
  trace_id: TraceId;
  upstream: { status: number; ms: number } | null;
  response_text: string;
  delivered_text: string;
  headers: Record<string, string>;
  decision: GateDecisionView;
  audit_status: TraceAuditStatus;
}
export interface KBClaim {
  claim_id: string;
  claim: string;
  source_doc_id: string;
  source_excerpt: string;
  status: 'accepted' | 'quarantined';
  entailment_score: number;
  conflicts_with: string[];
  ingested_at: ISODateTime;
  quarantine_reason: string | null;
  decided_by: string | null;
}
export interface KBStats {
  total_claims: number;
  accepted: number;
  quarantined: number;
  conflict_pairs: number;
}
export interface ConflictPair {
  claim_a: KBClaim;
  claim_b: KBClaim;
}
export interface IngestResult {
  documents_ingested: number;
  claims_accepted: number;
  claims_quarantined: number;
  conflicts_detected: number;
  warnings: string[];
  cost_usd: number;
  claim_results: {
    claim_id: string;
    claim: string;
    source_doc_id: string;
    status: 'accepted' | 'quarantined';
    entailment_score: number;
    conflicts_with: string[];
    decided_by: string | null;
    quarantine_reason: string | null;
  }[];
}
export interface Recording {
  // contracts/schema/recording.v1.schema.json
  schema: 'trustagent.recording/v1';
  kind: 'recorded' | 'mock';
  label: string;
  recorded_at: ISODateTime;
  git_sha: string;
  trustagent_version: string;
  decision_model: { backend: DecisionBackend; snapshot: string | null };
  judge: { provider: string; model: string; fallback_active: boolean };
  calibration_state: CalibrationState;
  answer_sources: { scripted: number; generated: number };
  totals: { traces: number; decision_usd: number; llm_usd: number };
  /** recording.v1 `roi`: the headline only (Go recording.ROI); the full RoiSummary is recomputed from the traces. */
  roi: Pick<RoiSummary, 'window' | 'gated_responses' | 'simulated'>;
  // details in traces/<id>.json as TraceDetail incl. scripted/generated bodies
  traces: TraceSummary[];
  events: { t_ms: number; type: WsEvent['type']; trace_id?: TraceId; data: unknown }[];
}

// WebSocket v2
// Envelope shared by every event except `hb` (see WsEvent).
interface Env<T extends string, D> {
  v: 2;
  seq: number;
  epoch: string;
  type: T;
  ts: ISODateTime;
  trace_id?: TraceId;
  data: D;
}
export interface WsHello {
  protocol: 2;
  server_version: string;
  epoch: string;
  last_seq: number;
  replayed_from: number | null;
  demo: boolean;
  readonly: boolean;
  status: SystemStatusLite;
}
/**
 * One WS v2 text frame (one JSON object per frame; negotiate with `GET /ws?protocol=2`).
 * `seq` is assigned by the hub and is only comparable within one `epoch`.
 * `hb` frames carry no `seq` and no `data`.
 */
export type WsEvent =
  | Env<'hello', WsHello>
  | { v: 2; type: 'hb'; ts: ISODateTime }
  | Env<'trace_started', { trace: TraceSummary }>
  | Env<'gate_decision', { trace: TraceSummary; decision: GateDecisionView }>
  | Env<'audit_queued', { audit_id: string; lane: 'high' | 'low'; queue_depth: number }>
  | Env<'audit_started', { audit_id: string; queue_wait_ms: number }>
  | Env<'audit_completed', { audit: AuditDetail }>
  | Env<
      'audit_failed',
      {
        audit_id: string;
        kind:
          'engine_unavailable' | 'timeout' | 'judge_unavailable' | 'decomposer_failed' | 'internal';
        message: string;
        retryable: boolean;
      }
    >
  | Env<
      'audit_dropped',
      { audit_id: string | null; reason: 'queue_full' | 'stopped' | 'no_engine' | 'llm_budget' }
    >
  | Env<
      'gap',
      {
        from_seq: number;
        to_seq: number;
        reason: 'slow_consumer' | 'buffer_evicted' | 'epoch_changed' | 'hub_overflow';
        resync: { traces: string; decisions: string; audits: string };
      }
    >
  | Env<'policy_updated', { version: number; source: string; author: string; message: string }>
  | Env<'policy_error', { source: 'file' | 'database'; errors: FieldError[] }>
  | Env<'evaluator_updated', { id: string; version: number; origin: string }>
  | Env<'calibration_updated', { snapshot: string; imported: number }>
  | Env<
      'decision_model_changed',
      {
        backend: DecisionBackend;
        previous_snapshot: string | null;
        snapshot: string;
        calibration: CalibrationState;
      }
    >
  | Env<
      'status',
      {
        component:
          | 'engine'
          | 'judge'
          | 'decision_model'
          | 'database'
          | 'knowledge'
          | 'policy'
          | 'budget'
          | 'redis';
        state: string;
        detail?: string;
      }
    >
  | Env<
      'credentials_changed',
      { name: string; action: 'put' | 'delete' | 'rotated'; fingerprint: string | null }
    >
  | Env<'settings_updated', { version: number; keys: string[] }>;
