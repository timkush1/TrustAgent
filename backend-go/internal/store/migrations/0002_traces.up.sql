-- 0002: gateway traces, gate decisions and the v2 audit columns (SPEC §2.3, P1-core).
--
-- Applied by the runner in postgres.go inside one transaction together with its
-- schema_migrations row, under pg_advisory_lock(7212026). Every statement is
-- idempotent (IF NOT EXISTS / guarded UPDATE / ON CONFLICT), so re-executing the
-- file is a no-op.
--
-- Dependent tables (trace_bodies, gate_decisions) deliberately carry no foreign
-- keys: the ordered trace writer UPSERTs rows independently and retention deletes
-- them by age.
--
-- Legacy (0001) rows are upgraded in place: audits get trace_id = audit_id, claim
-- statuses are upper-cased (UNSPECIFIED -> UNKNOWN), duplicate claim rows created
-- by B4 are removed, and one skipped/legacy trace is backfilled per audit.
--
-- There is no down migration. Downgrading to v1 after 0002 is impossible because
-- v1 cannot scan the NULL scores, grades and texts that 0002 allows.

CREATE TABLE IF NOT EXISTS traces (
  trace_id TEXT PRIMARY KEY, client_request_id TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  source TEXT NOT NULL, api_format TEXT NOT NULL, path TEXT NOT NULL, route TEXT NOT NULL,
  policy_version BIGINT, mode TEXT NOT NULL, effective_mode TEXT, key_label TEXT, model TEXT,
  stream BOOLEAN NOT NULL DEFAULT false, scenario TEXT, answer_source TEXT,            -- scripted|generated|live
  upstream_status INT, upstream_ms INT, ttft_ms INT, total_ms INT, overhead_ms INT, gate_ms INT,
  prompt_tokens INT, completion_tokens INT,
  action TEXT,                                   -- NULL while pending; observe|pass|flag|block|fallback
  would_action TEXT, gate_status TEXT NOT NULL DEFAULT 'pending',   -- pending|ok|partial|not_assessed|skipped|timeout|error
  skip_reason TEXT, verdict TEXT, max_risk REAL, reasons TEXT[] NOT NULL DEFAULT '{}',
  evidence_summary JSONB, decision_backend TEXT, decision_snapshot TEXT, decision_cost_usd NUMERIC(14,10),
  audit_id TEXT, audit_status TEXT NOT NULL DEFAULT 'none',  -- none|queued|sampled_out|dropped|disabled|running|completed|degraded|failed|not_assessed
  audit_lane TEXT, audit_selection TEXT,          -- high|sample|error_sample|uniform|direct|playground|studio
  question_preview TEXT, response_preview TEXT,   -- ≤200 / ≤300 chars
  flags TEXT[] NOT NULL DEFAULT '{}');            -- client_disconnected|late_decision|fallback_used|test_mode|mock_decision|mock_judge|uniform_sample|body_expired|text_redacted
CREATE INDEX IF NOT EXISTS idx_traces_created ON traces (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_traces_updated ON traces (updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_traces_route   ON traces (route, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_traces_action  ON traces (action, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_traces_audit   ON traces (audit_id);
CREATE INDEX IF NOT EXISTS idx_traces_client  ON traces (client_request_id);

CREATE TABLE IF NOT EXISTS trace_bodies (      -- no FK; retention by expires_at / trace age
  trace_id TEXT PRIMARY KEY, messages JSONB NOT NULL, user_request TEXT, system_instructions TEXT,
  response_text TEXT, final_response_text TEXT, evidence JSONB NOT NULL DEFAULT '[]', rules JSONB NOT NULL DEFAULT '{}',
  bytes INT NOT NULL, truncated BOOLEAN NOT NULL DEFAULT false,      -- TRUTHTABLE_BODY_MAX_BYTES cap
  inclusion_prob REAL NOT NULL DEFAULT 1,                             -- store_bodies=sampled: 1 for non-pass, body_sample_rate_pass for pass
  expires_at TIMESTAMPTZ NOT NULL);
CREATE INDEX IF NOT EXISTS idx_trace_bodies_expires ON trace_bodies (expires_at);

CREATE TABLE IF NOT EXISTS gate_decisions (    -- no FK
  id BIGSERIAL PRIMARY KEY, trace_id TEXT NOT NULL, attempt SMALLINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  policy_version BIGINT, route TEXT NOT NULL, mode TEXT NOT NULL, effective_mode TEXT NOT NULL,
  status TEXT NOT NULL, action TEXT NOT NULL, would_action TEXT, verdict TEXT, max_risk REAL,
  calibration TEXT NOT NULL DEFAULT 'none', degraded TEXT, reasons TEXT[] NOT NULL DEFAULT '{}',
  decision JSONB NOT NULL,                     -- decision/v1 incl. raw answers
  detectors JSONB NOT NULL DEFAULT '[]', excluded JSONB NOT NULL DEFAULT '[]', evidence_summary JSONB,
  question_set_sha256 TEXT, request_sha256 TEXT[], cpt REAL,
  packed_evidence_ids TEXT[] NOT NULL DEFAULT '{}', truncated_ids TEXT[] NOT NULL DEFAULT '{}',
  decision_backend TEXT, decision_model_requested TEXT, decision_snapshot TEXT,
  decision_ms INT, decision_calls SMALLINT, decision_attempts SMALLINT, decision_hedged BOOLEAN, connect_ms INT,
  input_tokens INT, cost_usd NUMERIC(14,10), error_kind TEXT, error_message TEXT,
  late BOOLEAN NOT NULL DEFAULT false, gate_ms INT, UNIQUE (trace_id, attempt));
CREATE INDEX IF NOT EXISTS idx_gd_created  ON gate_decisions (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_gd_updated  ON gate_decisions (updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_gd_snapshot ON gate_decisions (decision_snapshot, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_gd_route    ON gate_decisions (route, created_at DESC);

ALTER TABLE audits
  ADD COLUMN IF NOT EXISTS trace_id TEXT, ADD COLUMN IF NOT EXISTS client_request_id TEXT,
  ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'proxy', ADD COLUMN IF NOT EXISTS route TEXT,
  ADD COLUMN IF NOT EXISTS api_format TEXT, ADD COLUMN IF NOT EXISTS priority TEXT,
  ADD COLUMN IF NOT EXISTS selection TEXT, ADD COLUMN IF NOT EXISTS inclusion_prob REAL,
  ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'completed', ADD COLUMN IF NOT EXISTS verdict TEXT,
  ADD COLUMN IF NOT EXISTS not_assessed_reason TEXT, ADD COLUMN IF NOT EXISTS error TEXT, ADD COLUMN IF NOT EXISTS error_kind TEXT,
  ADD COLUMN IF NOT EXISTS scores JSONB, ADD COLUMN IF NOT EXISTS degraded_reasons TEXT[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS judge JSONB, ADD COLUMN IF NOT EXISTS decision_model JSONB, ADD COLUMN IF NOT EXISTS cascade JSONB,
  ADD COLUMN IF NOT EXISTS gate JSONB, ADD COLUMN IF NOT EXISTS evidence_summary JSONB, ADD COLUMN IF NOT EXISTS context_used JSONB,
  ADD COLUMN IF NOT EXISTS unsupported_policy TEXT, ADD COLUMN IF NOT EXISTS scoring_version TEXT NOT NULL DEFAULT 'v1',
  ADD COLUMN IF NOT EXISTS cost_usd NUMERIC(14,10), ADD COLUMN IF NOT EXISTS queue_wait_ms INT,
  ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ, ADD COLUMN IF NOT EXISTS text_redacted BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS text_expires_at TIMESTAMPTZ;           -- user_query/llm_response/context nulled after BODY_RETENTION
ALTER TABLE audits ALTER COLUMN faithfulness_score DROP NOT NULL;
ALTER TABLE audits ALTER COLUMN grade DROP NOT NULL;
ALTER TABLE audits ALTER COLUMN user_query DROP NOT NULL;
ALTER TABLE audits ALTER COLUMN llm_response DROP NOT NULL;
UPDATE audits SET trace_id = audit_id WHERE trace_id IS NULL;     -- audit_id is unique; request_id was not (B4)
CREATE INDEX IF NOT EXISTS idx_audits_trace ON audits (trace_id);
CREATE INDEX IF NOT EXISTS idx_audits_verdict ON audits (verdict, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audits_selection ON audits (selection, created_at DESC);

ALTER TABLE audit_claims
  ADD COLUMN IF NOT EXISTS reasoning TEXT, ADD COLUMN IF NOT EXISTS decided_by TEXT, ADD COLUMN IF NOT EXISTS verdict_probabilities JSONB,
  ADD COLUMN IF NOT EXISTS probability_source TEXT, ADD COLUMN IF NOT EXISTS jev JSONB, ADD COLUMN IF NOT EXISTS llm JSONB,
  ADD COLUMN IF NOT EXISTS escalation_reason TEXT, ADD COLUMN IF NOT EXISTS shadow BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS span_start INT, ADD COLUMN IF NOT EXISTS span_end INT, ADD COLUMN IF NOT EXISTS span_method TEXT,
  ADD COLUMN IF NOT EXISTS quote TEXT, ADD COLUMN IF NOT EXISTS evidence_spans JSONB;
ALTER TABLE audit_claims ALTER COLUMN claim DROP NOT NULL;         -- store_bodies=none keeps verdicts, not text
UPDATE audit_claims SET status = upper(status) WHERE status <> upper(status);
UPDATE audit_claims SET status = 'UNKNOWN' WHERE status = 'UNSPECIFIED';
DELETE FROM audit_claims c USING audit_claims d                  -- repair B4 duplicate claim rows
 WHERE c.audit_id = d.audit_id AND c.position = d.position AND c.id > d.id;
INSERT INTO traces (trace_id, created_at, updated_at, source, api_format, path, route, mode, action, gate_status,
                    skip_reason, audit_id, audit_status, model, question_preview, response_preview)
SELECT a.audit_id, a.created_at, a.created_at, 'proxy', 'openai_chat', '/v1/chat/completions', 'default', 'observe',
       NULL, 'skipped', 'legacy', a.audit_id, 'completed', NULLIF(a.model, ''), left(a.user_query, 200), left(a.llm_response, 300)
  FROM audits a ON CONFLICT (trace_id) DO NOTHING;
