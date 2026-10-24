/* eslint-disable */
/**
 * GENERATED FILE: do not edit. Produced by json-schema-to-typescript from contracts/schema
 * (src/lib/contracts/generate.ts). Regenerate with `make gen-types`; types.gen.test.ts fails when
 * this file and the schemas drift apart. Schema titles are the type names.
 */
/**
 * A versioned evaluator (decision spec): one independent Jev question plus how its answer becomes a risk level (SPEC §2.8). Copied verbatim from the spec; titles and descriptions are the only additions. Hard rules that JSON Schema cannot express (risk_weights keys are option keys, risk_levels < criteria count, escalate_from < warn_at <= critical_at, backticked references resolve, no question ids in text) are enforced by the Go/TS validators and, for committed files, by validate.mjs.
 */
export type EvaluatorSpec = {
  spec_version: 'evaluator/v1';
  id: string;
  version: number;
  name: string;
  description?: string;
  category: 'grounding' | 'safety' | 'compliance' | 'quality' | 'security' | 'custom';
  builtin?: boolean;
  verdict_role?: 'unsupported' | 'contradicted';
  state_fields: StateFields;
  params?: Params;
  question:
    | {
        type: 'noul';
        instructions: EvaluatorText;
        criteria: {
          true: EvaluatorText;
          false: EvaluatorText;
        };
      }
    | {
        type: 'choice';
        instructions: EvaluatorText;
        criteria: {
          [k: string]: null | EvaluatorOption | EvaluatorText;
        };
      }
    | {
        type: 'score';
        instructions: EvaluatorText;
        /**
         * @minItems 2
         * @maxItems 10
         */
        criteria: EvaluatorText[];
      };
  evidence: Evidence;
  signal: Signal;
  outcomes: Outcomes;
  escalation?: Escalation;
  threshold_basis?: string;
  tags?: Tags;
  parent?: string | null;
  changelog?: string;
};
/**
 * @minItems 1
 */
export type StateFields = (
  'user_request' | 'assistant_response' | 'evidence' | 'system_instructions' | 'rules'
)[];
export type EvaluatorText =
  | string
  | EvaluatorText[]
  | {
      [k: string]: EvaluatorText;
    };
/**
 * @maxItems 10
 */
export type Tags = string[];
/**
 * An LLM-drafted evaluator from Studio (SPEC §2.8): evaluator.v1 without id, version, builtin, parent and changelog, plus rationale, split_suggestions and exactly 6 test samples. Field schemas are referenced from evaluator.v1 so the two cannot drift; the cross-field rules are repeated here.
 */
export type EvaluatorDraft = {
  spec_version: 'evaluator/v1';
  name: string;
  description?: string;
  category: 'grounding' | 'safety' | 'compliance' | 'quality' | 'security' | 'custom';
  verdict_role?: 'unsupported' | 'contradicted';
  state_fields: StateFields;
  params?: Params;
  question:
    | {
        type: 'noul';
        instructions: EvaluatorText;
        criteria: {
          true: EvaluatorText;
          false: EvaluatorText;
        };
      }
    | {
        type: 'choice';
        instructions: EvaluatorText;
        criteria: {
          [k: string]: null | EvaluatorOption | EvaluatorText;
        };
      }
    | {
        type: 'score';
        instructions: EvaluatorText;
        /**
         * @minItems 2
         * @maxItems 10
         */
        criteria: EvaluatorText[];
      };
  evidence: Evidence;
  signal: Signal;
  outcomes: Outcomes;
  escalation?: Escalation;
  threshold_basis?: string;
  tags?: Tags;
  /**
   * Why the drafter chose this question shape (shown in Studio).
   */
  rationale: string;
  /**
   * Suggestions to split an over-broad check into narrower evaluators.
   *
   * @maxItems 5
   */
  split_suggestions?: string[];
  /**
   * @minItems 6
   * @maxItems 6
   */
  test_samples: {
    user_request?: string;
    assistant_response: string;
    /**
     * @maxItems 8
     */
    evidence?: string[];
    should_fire: boolean;
  }[];
};
/**
 * "<id>@<version>". "@latest" is forbidden (SPEC §2.7).
 */
export type EvaluatorRef = string;
export type PolicyMode = 'observe' | 'flag' | 'block' | 'fallback';
export type FailMode = 'open' | 'closed';
export type EvidenceSource =
  'request_system' | 'request_context' | 'tool_results' | 'extension' | 'knowledge';
export type EvaluatorStage = 'active' | 'shadow';
export type ThresholdSpace = 'raw' | 'calibrated';
/**
 * Replaces (does not merge) the defaults list. Default: question set gateway-default. Unique ids; param names unique across the route.
 *
 * @maxItems 16
 */
export type Evaluators = RouteEvaluatorRef[];
export type DetectorId =
  | 'numbers_not_in_evidence'
  | 'dates_not_in_evidence'
  | 'urls_not_in_evidence'
  | 'quotes_not_in_evidence'
  | 'citation_out_of_range'
  | 'secret_pattern'
  | 'system_prompt_leak';
export type RouteName = string;
/**
 * RFC 3339 (nano precision allowed) in UTC with a trailing Z (SPEC §2.1).
 */
export type ISODateTime = string;
export type DecisionBackend =
  'openrouter-decisions' | 'openrouter-systemone' | 'typesafe' | 'compatible' | 'mock' | 'off';
export type EvaluatorId = string;
export type Sha256Hex = string;
export type StateField = 'user_request' | 'assistant_response' | 'evidence' | 'system_instructions' | 'rules';
export type Probability = number;
/**
 * Exactly 101 non-decreasing values in [0,1]; t[i] = calibrated(i/100). Monotonicity is checked by validate.mjs.
 *
 * @minItems 101
 * @maxItems 101
 */
export type CalibrationTable = Probability[];
export type CalibrationQuality = 'measured' | 'provisional' | 'proxy' | 'mock';
export type GitSha = string;
/**
 * US dollars as a JSON float64 (fields named *_usd, SPEC §2.1).
 */
export type Usd = number;
/**
 * The vendor hosts a measured run or a recorded tape may name (D203). Any other host (mock-upstream, a proxy, a gateway) makes the run simulated, with the reason non_vendor_host.
 */
export type VendorHost = 'openrouter.ai' | 'api.typesafe.ai' | 'api.anthropic.com';
export type QuestionType = 'noul' | 'choice' | 'score';
/**
 * Why compile() excluded an evaluator (SPEC §2.9 compile steps 1, 2, 7).
 */
export type ExclusionReason = 'too_long' | 'no_evidence' | 'evidence_too_short' | 'evidence_packed_away';
export type CalibrationState = 'measured' | 'provisional' | 'proxy' | 'mock' | 'family' | 'none';
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
export type Uuid = string;
export type GateStatus = 'ok' | 'partial' | 'not_assessed' | 'skipped' | 'timeout' | 'error';
export type GateAction = 'observe' | 'pass' | 'flag' | 'block' | 'fallback';
export type WouldAction = 'pass' | 'flag' | 'block';
export type GateVerdict = 'supported' | 'unsupported' | 'contradicted' | 'not_assessed' | 'unknown';
export type Degraded =
  'uncalibrated' | 'budget' | 'breaker' | 'rate_limited' | 'timeout' | 'error' | 'overload' | 'off';
export type DeepAuditClass = 'high' | 'sample' | 'sample_error' | 'none';
export type EvaluatorResultStatus = 'assessed' | 'not_assessed' | 'error';
export type EvaluatorLevel = 'ok' | 'warn' | 'critical';
/**
 * Duration in integer milliseconds (fields named *_ms).
 */
export type Ms = number;
export type CostSource = 'reported' | 'computed' | 'free_local' | 'simulated' | 'unknown';
export type AnswerSource = 'scripted' | 'generated' | 'live';
export type ApiFormat = 'openai_chat' | 'openai_completions' | 'anthropic_messages' | 'direct';
export type AuditVerdict = 'GROUNDED' | 'HALLUCINATED' | 'UNVERIFIABLE' | 'NO_CLAIMS' | 'NOT_ASSESSED';
export type ClaimVerdict =
  'SUPPORTED' | 'PARTIALLY_SUPPORTED' | 'CONTRADICTED' | 'UNSUPPORTED' | 'UNKNOWN' | 'NOT_CHECKED';
export type ISODate = string;
/**
 * A bare, lowercase host name: no scheme, port, path or user (D203). Recordings and benchmark runs keep hosts bare because the recording sanitizer scrubs hosts inside URLs.
 */
export type Hostname = string;
/**
 * "<id>__v<version>" (SPEC §2.9 compile step 5).
 */
export type QuestionId = string;
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
export type TraceSource = 'proxy' | 'direct' | 'playground' | 'test' | 'trafficgen';
export type ValueLabel = 'measured' | 'estimate' | 'third_party' | 'vendor' | 'simulated';
export type WsEventType =
  | 'hello'
  | 'hb'
  | 'trace_started'
  | 'gate_decision'
  | 'audit_queued'
  | 'audit_started'
  | 'audit_completed'
  | 'audit_failed'
  | 'audit_dropped'
  | 'gap'
  | 'policy_updated'
  | 'policy_error'
  | 'evaluator_updated'
  | 'calibration_updated'
  | 'decision_model_changed'
  | 'status'
  | 'credentials_changed'
  | 'settings_updated';

export interface Params {
  [k: string]: {
    default: string;
    label?: string;
    help?: string;
  };
}
export interface EvaluatorOption {
  what: string;
  not_for?: string;
  /**
   * @maxItems 2
   */
  examples?: string[];
}
export interface Evidence {
  requires: 'required' | 'optional' | 'none';
  min_chars?: number;
}
export interface Signal {
  invert?: boolean;
  risk_weights?: {
    [k: string]: number;
  };
  /**
   * @minItems 1
   */
  risk_levels?: number[];
}
export interface Outcomes {
  warn_at: number;
  critical_at?: number | null;
  min_confidence?: number | null;
  block_eligible?: boolean;
  requires_corroboration?: string[];
  implies?: string[];
}
export interface Escalation {
  escalate_from?: number | null;
  numeric_or_date?: boolean;
  on_error?: 'escalate' | 'ignore';
}
/**
 * contracts/question-sets/<id>.v<version>.json: a named list of evaluator refs (SPEC §2.8).
 */
export interface QuestionSet {
  id: string;
  version: number;
  description?: string;
  /**
   * @minItems 1
   * @maxItems 16
   */
  evaluators: EvaluatorRef[];
}
/**
 * config/policies.yaml, schema policy/v1 (SPEC §2.7). Parsed with yaml.v3 KnownFields(true). routes[] merge over defaults: nested objects merge key by key, arrays replace. The first matching route wins; every matcher given in a route must match; with no match, defaults apply as route "default". Cross-field rules that need the merged route (hedge_after_ms < deadline_ms, unique evaluator ids, param names unique across the route and declared by the evaluator, escalate_from < warn_at <= critical_at, evidence_sources a subset of evidence.sources, knowledge only with the add-on) are enforced by the Go loader.
 */
export interface PolicyDoc {
  version: 1;
  defaults?: RouteSettings;
  overrides?: PolicyOverrides;
  /**
   * @maxItems 64
   */
  routes?: PolicyRoute[];
}
/**
 * The fields shared by defaults and routes (SPEC §2.7 RouteSettings). Every field is optional in a route and merged over defaults.
 */
export interface RouteSettings {
  mode?: PolicyMode;
  fail?: FailMode;
  /**
   * Synchronous gate budget, excluding the upstream call.
   */
  deadline_ms?: number;
  /**
   * 0 (no hedge) or [100, deadline_ms).
   */
  hedge_after_ms?: number;
  /**
   * Observe routes only: share of requests sent to the fast check. Detectors always run.
   */
  sample_rate?: number;
  /**
   * Caps block/fallback at flag unless every block-set evaluator is calibrated.
   */
  require_calibration?: boolean;
  /**
   * SPEC §2.9 no-evidence rule.
   */
  on_no_evidence?: 'ignore' | 'flag' | 'block';
  /**
   * Block/fallback with passthrough become effective flag (stream_not_buffered).
   */
  streaming?: 'passthrough' | 'buffer';
  /**
   * Write ": trustagent {...}" before the terminal SSE event.
   */
  stream_annotations?: 'comment' | 'none';
  /**
   * Buffer mode SSE keepalive interval; 0 = none.
   */
  buffer_keepalive_s?: number;
  /**
   * action_only sends only Trace-Id, Request-ID and Action.
   */
  headers?: 'full' | 'action_only';
  evidence?: EvidenceSettings;
  evaluators?: Evaluators;
  detectors?: DetectorSettings;
  decision?: DecisionSettings;
  deep_audit?: DeepAuditSettings;
  block?: BlockSettings;
  fallback?: FallbackSettings;
  store_bodies?: 'all' | 'sampled' | 'none';
  /**
   * With store_bodies: sampled, the share of passed traces whose bodies are kept.
   */
  body_sample_rate_pass?: number;
}
export interface EvidenceSettings {
  /**
   * Priority order. knowledge needs the knowledge add-on.
   */
  sources?: EvidenceSource[];
  system_mode?: 'off' | 'documents_only' | 'whole';
  /**
   * Treat "Context:" style line headers as block starts (SPEC §2.10).
   */
  line_headers?: boolean;
  /**
   * @maxItems 16
   */
  tag_patterns?: {
    /**
     * RE2 syntax, no inline flags.
     */
    pattern: string;
    /**
     * Subset of "ims".
     */
    flags?: string;
  }[];
  max_tokens?: number;
  /**
   * Tool-name globs excluded from evidence and marked untrusted.
   *
   * @maxItems 32
   */
  untrusted_tools?: string[];
  knowledge?: {
    /**
     * fallback: only when the request sources are empty.
     */
    when?: 'fallback' | 'always';
    top_k?: number;
    deadline_ms?: number;
    min_score?: number;
  };
}
export interface RouteEvaluatorRef {
  ref: EvaluatorRef;
  stage?: EvaluatorStage;
  params?: {
    [k: string]: string;
  };
  thresholds?: {
    space?: ThresholdSpace;
    warn_at?: number;
    critical_at?: number | null;
    escalate_from?: number | null;
  };
  enabled?: boolean;
  /**
   * Narrows (never widens) the route evidence.sources.
   */
  evidence_sources?: EvidenceSource[];
}
export interface DetectorSettings {
  /**
   * Numbers and dates never flag alone.
   */
  flag_on?: DetectorId[];
  block_on?: DetectorId[];
  /**
   * Integers above min_integer are always checked; unit-adjacent integers are always checked.
   */
  min_integer?: number;
}
export interface DecisionSettings {
  /**
   * Per-route model pin. Default: TRUTHTABLE_JEV_MODEL.
   */
  model?: string;
  max_response_tokens?: number;
}
export interface DeepAuditSettings {
  enabled?: boolean;
  /**
   * Share of passes sent to the deep audit (lane "sample").
   */
  pass_sample_rate?: number;
  /**
   * Pre-gate Bernoulli over all gated traces; the unbiased hallucination rate (capped by TRUTHTABLE_UNIFORM_AUDITS_PER_DAY).
   */
  uniform_sample_rate?: number;
  /**
   * Share of skipped/timeout/error gates sent to the deep audit (lane "sample_error").
   */
  error_sample_rate?: number;
  /**
   * auto = llm in 2.0.
   */
  verifier?: 'auto' | 'llm';
}
export interface BlockSettings {
  /**
   * message: HTTP 200 with finish_reason content_filter; error: HTTP block.status.
   */
  style?: 'message' | 'error';
  message?: string;
  /**
   * Error style only; never 429 or 5xx.
   */
  status?: 400 | 403 | 409 | 422 | 451;
  anthropic_stop_reason?: 'refusal' | 'end_turn';
}
export interface FallbackSettings {
  /**
   * Never a URL (prevents SSRF and key exfiltration).
   */
  upstream?: 'same' | 'fallback';
  /**
   * Empty = the original model.
   */
  model?: string;
  system_prompt?: string;
  on_fail?: 'block' | 'flag' | 'original';
}
export interface PolicyOverrides {
  /**
   * Whether the X-TrustAgent-Mode request header may change the mode. stricter_only allows observe -> flag -> block/fallback only.
   */
  client_mode?: 'none' | 'stricter_only' | 'any';
  /**
   * Honour the request body trustagent.rules object. Off by default: end users must not rewrite rules.
   */
  client_rules?: boolean;
}
/**
 * A route: name, match and any RouteSettings field (merged over defaults).
 */
export interface PolicyRoute {
  /**
   * "default" is reserved for the defaults route.
   */
  name: RouteName;
  description?: string;
  match: RouteMatch;
  mode?: PolicyMode;
  fail?: FailMode;
  /**
   * Synchronous gate budget, excluding the upstream call.
   */
  deadline_ms?: number;
  /**
   * 0 (no hedge) or [100, deadline_ms).
   */
  hedge_after_ms?: number;
  /**
   * Observe routes only: share of requests sent to the fast check. Detectors always run.
   */
  sample_rate?: number;
  /**
   * Caps block/fallback at flag unless every block-set evaluator is calibrated.
   */
  require_calibration?: boolean;
  /**
   * SPEC §2.9 no-evidence rule.
   */
  on_no_evidence?: 'ignore' | 'flag' | 'block';
  /**
   * Block/fallback with passthrough become effective flag (stream_not_buffered).
   */
  streaming?: 'passthrough' | 'buffer';
  /**
   * Write ": trustagent {...}" before the terminal SSE event.
   */
  stream_annotations?: 'comment' | 'none';
  /**
   * Buffer mode SSE keepalive interval; 0 = none.
   */
  buffer_keepalive_s?: number;
  /**
   * action_only sends only Trace-Id, Request-ID and Action.
   */
  headers?: 'full' | 'action_only';
  evidence?: EvidenceSettings;
  evaluators?: Evaluators;
  detectors?: DetectorSettings;
  decision?: DecisionSettings;
  deep_audit?: DeepAuditSettings;
  block?: BlockSettings;
  fallback?: FallbackSettings;
  store_bodies?: 'all' | 'sampled' | 'none';
  /**
   * With store_bodies: sampled, the share of passed traces whose bodies are kept.
   */
  body_sample_rate_pass?: number;
}
/**
 * Every matcher given must match.
 */
export interface RouteMatch {
  /**
   * Header name (case-insensitive) -> exact value, e.g. {X-TrustAgent-Route: support-bot}.
   */
  headers?: {
    [k: string]: string;
  };
  /**
   * Request path globs.
   *
   * @maxItems 32
   */
  paths?: string[];
  /**
   * Model name globs.
   *
   * @maxItems 32
   */
  models?: string[];
  /**
   * API key labels.
   *
   * @maxItems 32
   */
  key_labels?: string[];
}
/**
 * trustagent.calibration/v1, the "thresholds JSON" (SPEC §2.11). quality is re-derived by the server on import: measured = human/public-dataset labels only with n >= 200, n_pos >= 30, n_neg >= 30; provisional = below that or a family fallback; proxy = any uniform-audit or judge labels; mock = snapshot mock/* (only honoured with the mock backend in demo mode).
 */
export interface CalibrationFile {
  schema: 'trustagent.calibration/v1';
  created_at: ISODateTime;
  /**
   * e.g. "cmd/calibrate@<git_sha>" or "api/calibration/fit".
   */
  generated_by: string;
  decision_model: {
    backend: DecisionBackend;
    /**
     * e.g. typesafe/jev-1.13-20260917 or mock/jev-heuristic-v1.
     */
    snapshot: string;
    /**
     * Snapshot minus the -YYYYMMDD suffix.
     */
    family: string;
  };
  entries: CalibrationEntry[];
  warnings: string[];
}
export interface CalibrationEntry {
  target: {
    /**
     * Other target kinds are reserved for v2.1.
     */
    kind: 'evaluator';
    evaluator_id: EvaluatorId;
    evaluator_version: number;
    calibration_key: Sha256Hex;
    /**
     * sha256(JCS(resolved route params)) for evaluators with params, else null.
     */
    params_sha256: Sha256Hex | null;
  };
  fit_context: {
    state_fields: StateField[];
    partition: 'single';
    question_set_sha256: Sha256Hex;
  };
  /**
   * PAV isotonic when n >= 500, otherwise Platt; "auto" at fit time records the method it picked.
   */
  method: 'isotonic' | 'platt';
  table: CalibrationTable;
  platt: {
    a: number;
    b: number;
  } | null;
  quality: CalibrationQuality;
  n: number;
  n_pos: number;
  n_neg: number;
  label_source: {
    human: number;
    public_dataset: number;
    uniform_audit: number;
    judge?: number;
  };
  datasets: string[];
  metrics: {
    ece_before: number | null;
    ece_after: number | null;
    brier_before: number | null;
    brier_after: number | null;
    auroc: Probability | null;
  };
  reliability: {
    lo: Probability;
    hi: Probability;
    n: number;
    mean_pred: Probability;
    frac_pos: Probability;
  }[];
  recommended: {
    space: 'calibrated';
    warn_at: {
      value: Probability;
      precision: Probability;
      recall: Probability;
    } | null;
    critical_at: {
      value: Probability;
      precision_lb95: Probability;
      predicted_pos: number;
    } | null;
    critical_null_reason: string | null;
  };
}
/**
 * docs/benchmarks/results/<run_id>.json (SPEC §2.13), indexed by docs/benchmarks/index.json. Only measured, non-mock runs are ever written (make bench-publish refuses mock).
 */
export interface BenchmarkRun {
  run_id: string;
  created_at: ISODateTime;
  git_sha: GitSha;
  tier: 'lite' | 'full';
  measured: true;
  mock: false;
  dataset: {
    name: string;
    subset: string | null;
    split: string;
    n: number;
    revision: string;
    license_note: string;
  };
  /**
   * @minItems 1
   */
  systems: {
    id: 'gate' | 'llm_same_checks' | 'llm_full_audit' | 'cascade_response';
    decision_model: {
      backend: DecisionBackend;
      snapshot: string | null;
    } | null;
    judge: {
      provider: string;
      model: string;
    } | null;
    metrics: {
      precision: number | null;
      recall: number | null;
      f1: number | null;
      accuracy: number | null;
      balanced_accuracy: number | null;
      auroc: number | null;
      ece: number | null;
      brier: number | null;
      /**
       * 95% confidence intervals [lo, hi] per metric.
       */
      ci95?: {
        /**
         * @minItems 2
         * @maxItems 2
         */
        precision?: number[];
        /**
         * @minItems 2
         * @maxItems 2
         */
        recall?: number[];
        /**
         * @minItems 2
         * @maxItems 2
         */
        f1?: number[];
        /**
         * @minItems 2
         * @maxItems 2
         */
        accuracy?: number[];
        /**
         * @minItems 2
         * @maxItems 2
         */
        balanced_accuracy?: number[];
        /**
         * @minItems 2
         * @maxItems 2
         */
        auroc?: number[];
        /**
         * @minItems 2
         * @maxItems 2
         */
        ece?: number[];
        /**
         * @minItems 2
         * @maxItems 2
         */
        brier?: number[];
      };
    };
    latency_ms: {
      p50: number | null;
      p95: number | null;
    };
    cost: {
      usd_total: Usd;
      usd_per_1k: Usd;
      /**
       * simulated is impossible here (mock is false).
       */
      cost_source: 'reported' | 'computed' | 'free_local' | 'unknown';
      /**
       * The mean input tokens of the examples that made a call, as the vendor counted them; null when fewer than 95% of them have a count (D229). A cost per answer grows with the documents' length, so it is published beside the cost.
       */
      input_tokens_per_answer?: number | null;
    };
    reliability: {
      lo: Probability;
      hi: Probability;
      n: number;
      mean_pred: Probability;
      frac_pos: Probability;
    }[];
  }[];
  errors: {
    failed: number;
    excluded_over_budget: number;
  };
  environment: {
    region: string;
    concurrency: number;
  };
  /**
   * The hosts the run actually called (D203), resolved from TRUTHTABLE_OPENROUTER_BASE_URL or TRUTHTABLE_JEV_BASE_URL and the judge provider. A published run names vendor hosts only; null when the run has no such system.
   */
  vendor?: {
    decision_host: VendorHost | null;
    judge_host: VendorHost | null;
  };
  /**
   * What the run was made from (D204, D205): the dataset commit, the policy gate-eval ran with (builtin: TRUTHTABLE_POLICY_FILE removed), the gate-eval binary, and the machine's conditions.
   */
  inputs?: {
    dataset_revision: string;
    policy_source: 'builtin' | 'file';
    gate_eval_sha256: Sha256Hex | null;
    host_env: {
      mem_available_mb: number;
      /**
       * @minItems 3
       * @maxItems 3
       */
      loadavg: number[];
      platform: string;
      region: string;
    };
  };
  /**
   * Examples attempted, scored, failed and excluded over every system (D204). Publish refuses failure_rate > 0.05 or scored < 0.95 * n; by_system has the same counts per system.
   */
  quality?: {
    n: number;
    scored: number;
    failed: number;
    excluded: number;
    failure_rate: number;
    by_system?: {
      [k: string]: {
        n: number;
        scored: number;
        failed: number;
        excluded: number;
      };
    };
  };
  /**
   * Taken at run start (git status --porcelain --untracked-files=no); publish refuses a run started on a dirty tree (D204).
   */
  dirty?: false;
  /**
   * How the gate latency was taken (D205): warm (gate-eval --warm made the free key call on the same client first) or cold, and how many gate calls ran at once.
   */
  latency?: {
    connection: 'warm' | 'cold';
    gate_concurrency: number;
  };
}
/**
 * The input of the pure decide() core (SPEC §2.9). Used by decide vectors and the policy dry-run replay. calibration entries are looked up by the caller for the current snapshot (SPEC §2.11).
 */
export interface DecideInput {
  policy: {
    configured_mode: PolicyMode;
    fail: FailMode;
    require_calibration: boolean;
    on_no_evidence: 'ignore' | 'flag' | 'block';
    detectors: {
      flag_on: DetectorId[];
      block_on: DetectorId[];
    };
  };
  /**
   * Route order.
   */
  evaluators: {
    ref: EvaluatorRef;
    spec: EvaluatorSpec;
    stage: EvaluatorStage;
    internal: boolean;
    /**
     * Route overrides; an absent key falls back to the spec value (has() semantics: an explicit null overrides).
     */
    thresholds: {
      space: ThresholdSpace;
      warn_at?: Probability;
      critical_at?: Probability | null;
      escalate_from?: Probability | null;
    };
  }[];
  answers: {
    [k: string]: RawAnswer;
  } | null;
  question_map: {
    [k: string]: EvaluatorRef;
  };
  excluded: {
    ref: EvaluatorRef;
    reason: ExclusionReason;
  }[];
  calibration: {
    [k: string]: {
      table?: CalibrationTable;
      quality?: CalibrationQuality;
      /**
       * common.v1 CalibrationState plus other_snapshot: the target is calibrated only on other snapshots, so the entry carries no table (D56). decision.v1 calibration_state never takes other_snapshot.
       */
      state: CalibrationState | 'other_snapshot';
      /**
       * The state fields the calibration was fitted with (D56). When they differ from the evaluator's own, decide() sets calibration_warning and still applies the calibration.
       */
      fit_state_fields?: string[];
    };
  };
  meta: {
    backend: DecisionBackend;
    stream: 'none' | 'passthrough' | 'buffer';
    evidence_present: boolean;
    evidence_truncated: boolean;
    knowledge_only: boolean;
    skip_reason: SkipReason | null;
    /**
     * Per compiled request k: ok | timeout | error:<kind>.
     */
    request_status: {
      [k: string]: string;
    };
    qid_request: {
      [k: string]: number;
    };
    question_errors: {
      [k: string]: string;
    };
  };
  detectors: {
    [k: string]: {
      ran: boolean;
      fired: boolean;
    };
  };
}
/**
 * A vendor answer as received (SPEC §2.6 RawAnswer). Deliberately lenient: normalize() owns range and consistency checks, so out-of-range or foreign values must stay representable.
 */
export interface RawAnswer {
  type: QuestionType;
  noul?: number;
  choice?: string;
  score?: number;
  confidence?: number;
  probabilities?: {
    [k: string]: number;
  };
  legend?: {};
}
/**
 * decision/v1: one gate decision as stored and served (SPEC §2.6 GateDecisionView, produced by decide() in SPEC §2.9). Go structs and TS types use identical JSON keys.
 */
export interface GateDecisionView {
  spec: 'decision/v1';
  trace_id: Uuid;
  /**
   * 1 = the re-gate of a fallback answer.
   */
  attempt: 0 | 1;
  created_at: ISODateTime;
  route: RouteName;
  policy_version: number | null;
  mode: PolicyMode;
  effective_mode: PolicyMode;
  fail: FailMode;
  status: GateStatus;
  skip_reason: SkipReason | null;
  action: GateAction;
  would_action: WouldAction | null;
  enforced: boolean;
  verdict: GateVerdict | null;
  max_risk: Probability | null;
  calibration: 'all' | 'partial' | 'none';
  degraded: Degraded | null;
  disagreement: boolean;
  deep_audit: DeepAuditClass;
  reasons: string[];
  /**
   * Keyed by evaluator ref, in route order; includes shadow and internal evaluators.
   */
  evaluators: {
    [k: string]: EvaluatorResult;
  };
  detectors: DetectorResult[];
  excluded: {
    ref: EvaluatorRef;
    reason: ExclusionReason;
  }[];
  decision_model: DecisionModelUsage | null;
  compile: CompileInfo | null;
  evidence_summary: EvidenceSummary | null;
  /**
   * Arrived after the deadline; recorded, never enforced.
   */
  late: boolean;
  gate_ms: Ms | null;
}
export interface EvaluatorResult {
  ref: EvaluatorRef;
  stage: EvaluatorStage;
  /**
   * true for the implicit factual_claims@1 (SPEC §2.9 compile step 3).
   */
  internal: boolean;
  status: EvaluatorResultStatus;
  reason: string | null;
  type: QuestionType | null;
  answer: RawAnswer | null;
  probabilities: {
    [k: string]: Probability;
  } | null;
  risk_raw: Probability | null;
  risk: Probability | null;
  threshold_space: ThresholdSpace;
  calibrated: boolean;
  calibration_state: CalibrationState;
  calibration_warning: string | null;
  level: EvaluatorLevel | null;
  capped: ('evidence_truncated' | 'knowledge_only') | null;
  top: string | null;
  vendor_confidence: number | null;
  confidence_source: ('vendor' | 'fallback_top_prob' | 'choice_only') | null;
  escalate: ('uncertain' | 'numeric_or_date_unverified' | 'error') | null;
  corroborated: boolean | null;
  block_eligible: boolean;
  thresholds: {
    warn_at: Probability;
    critical_at: Probability | null;
    escalate_from: Probability | null;
  };
}
export interface DetectorResult {
  id: DetectorId;
  ran: boolean;
  fired: boolean;
  findings: {
    text: string;
    /**
     * UTF-16 code units, half-open; -1 = unaligned.
     */
    start: number;
    end: number;
  }[];
}
export interface DecisionModelUsage {
  backend: DecisionBackend;
  model_requested: string | null;
  snapshot: string | null;
  calls: number;
  attempts: number;
  hedged: boolean;
  latency_ms: Ms | null;
  connect_ms: Ms | null;
  input_tokens: number | null;
  cost_usd: Usd | null;
  cost_source: CostSource;
}
export interface CompileInfo {
  /**
   * Characters per token used by est() (SPEC §2.9 Tokens).
   */
  cpt: number;
  requests: number;
  question_set_sha256: Sha256Hex;
  request_sha256: Sha256Hex[];
  packed_evidence_ids: string[];
  truncated_ids: string[];
  est_tokens: number[];
}
/**
 * What a gate decision was checked against (SPEC §2.6 EvidenceSummary; JSON twin of proto EvidenceSummary). tokens are estimated fast-check tokens (SPEC §2.9 est()).
 */
export interface EvidenceSummary {
  /**
   * The route evidence.sources, in priority order.
   */
  configured: EvidenceSource[];
  used: {
    source: EvidenceSource;
    items: number;
    labels: string[];
    tokens: number;
  }[];
  /**
   * Configured sources that yielded nothing.
   */
  empty: EvidenceSource[];
  knowledge: 'not_requested' | 'disabled' | 'used' | 'no_hits' | 'timeout' | 'error';
  truncated: boolean;
  tokens: number;
}
/**
 * contracts/vectors/<kind>/<name>.json (SPEC §7.6). expected may be a subset of the output; numbers are compared as parsed values within tolerance, never as bytes; strings and arrays compare exactly. An unknown kind fails every runner. Lifecycle: hand-authored vectors start as draft; Go cmd/vectorgen produces mechanical expected values, which are reviewed and then frozen. A frozen vector never changes; a behaviour change adds a new vector.
 */
export interface GoldenVector {
  vector: '1';
  kind:
    | 'canonical'
    | 'tokens'
    | 'question_hash'
    | 'normalize'
    | 'compile'
    | 'decide'
    | 'calibration_apply'
    | 'calibrate_fit'
    | 'detectors'
    | 'evidence'
    | 'lint'
    | 'cost'
    | 'pkce';
  /**
   * Equals the file name without .json.
   */
  name: string;
  status: 'draft' | 'frozen';
  description?: string;
  input: unknown;
  expected: unknown;
  tolerance?: number;
  /**
   * e.g. "cmd/vectorgen@<git_sha>" for mechanical expected values.
   */
  generated_by?: string;
}
