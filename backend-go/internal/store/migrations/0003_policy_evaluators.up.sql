-- 0003: policy versions, evaluators, calibrations, labels and Studio results (SPEC §2.3, P1b contract PR).
--
-- Applied by the runner in postgres.go inside one transaction together with its
-- schema_migrations row, under pg_advisory_lock(7212026). Every statement is
-- idempotent (IF NOT EXISTS / CREATE OR REPLACE / DROP ... IF EXISTS then CREATE),
-- so re-executing the file is a no-op. The file only creates objects: there is no
-- data to backfill.
--
-- policy_versions holds whole policy documents; the newest row wins (SPEC §7.2).
-- sha256 is sha256(JCS(doc)); file_sha256 is the hash of the policies.yaml bytes a
-- file or adopt_file version came from.
--
-- evaluator_versions rows are immutable: the trigger rejects every UPDATE, so a
-- changed spec is always a new version. calibration_key is sha256(JCS({question,
-- signal})), which calibrations reference through target_key.
--
-- One calibration is active per (snapshot, target_kind, target_key); quality
-- `mock` is honoured only with the mock decision backend (SPEC §2.11).
--
-- labels.sample freezes the inputs a label was given for; retention nulls it at
-- sample_expires_at (TRUTHTABLE_LABEL_SAMPLE_RETENTION). studio_results are kept
-- for 30 days; policy_versions, evaluator_versions, calibrations and label rows
-- are never deleted.

CREATE TABLE IF NOT EXISTS policy_versions (
  id BIGSERIAL PRIMARY KEY, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  source TEXT NOT NULL CHECK (source IN ('bootstrap','file','dashboard','rollback','adopt_file')),
  author TEXT NOT NULL, message TEXT NOT NULL DEFAULT '', doc JSONB NOT NULL, yaml TEXT NOT NULL,
  sha256 TEXT NOT NULL,                      -- sha256(JCS(doc))
  file_sha256 TEXT, parent_id BIGINT REFERENCES policy_versions (id));
CREATE TABLE IF NOT EXISTS evaluators (
  id TEXT PRIMARY KEY CHECK (id ~ '^[a-z][a-z0-9_]{1,47}$'), name TEXT NOT NULL, category TEXT NOT NULL,
  origin TEXT NOT NULL CHECK (origin IN ('builtin','file','dashboard')), latest_version INT NOT NULL,
  archived BOOLEAN NOT NULL DEFAULT false, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS evaluator_versions (
  evaluator_id TEXT NOT NULL REFERENCES evaluators (id), version INT NOT NULL CHECK (version >= 1),
  spec JSONB NOT NULL, spec_sha256 TEXT NOT NULL, question_sha256 TEXT NOT NULL,
  calibration_key TEXT NOT NULL,             -- sha256(JCS({question, signal}))
  created_by TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), changelog TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (evaluator_id, version));
CREATE OR REPLACE FUNCTION trustagent_forbid_update() RETURNS trigger AS $$
BEGIN RAISE EXCEPTION 'evaluator_versions rows are immutable'; END; $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS evaluator_versions_immutable ON evaluator_versions;
CREATE TRIGGER evaluator_versions_immutable BEFORE UPDATE ON evaluator_versions FOR EACH ROW EXECUTE FUNCTION trustagent_forbid_update();
CREATE TABLE IF NOT EXISTS calibrations (
  id BIGSERIAL PRIMARY KEY, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), created_by TEXT NOT NULL,
  origin TEXT NOT NULL CHECK (origin IN ('file','import','fit')), backend TEXT NOT NULL, snapshot TEXT NOT NULL, family TEXT NOT NULL,
  target_kind TEXT NOT NULL CHECK (target_kind IN ('evaluator','cascade_option','llm_confidence','kb')),
  target_key TEXT NOT NULL,                  -- evaluator: "<id>@<v>#<calibration_key[:16]>#<params_sha[:16]|->"
  quality TEXT NOT NULL CHECK (quality IN ('measured','provisional','proxy','mock')),
  label_source JSONB NOT NULL, entry JSONB NOT NULL, file_sha256 TEXT, active BOOLEAN NOT NULL DEFAULT true);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_calibration ON calibrations (snapshot, target_kind, target_key) WHERE active;
CREATE TABLE IF NOT EXISTS labels (
  trace_id TEXT NOT NULL, target TEXT NOT NULL,       -- "<id>@<v>" | "claim:<index>" | "response"
  label TEXT NOT NULL,                                -- should_fire|should_not_fire | claim verdicts | hallucinated|grounded
  labeled_by TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), note TEXT,
  label_source TEXT NOT NULL DEFAULT 'human' CHECK (label_source IN ('human','public_dataset')),
  sample JSONB, sample_expires_at TIMESTAMPTZ,        -- frozen inputs; nulled after TRUTHTABLE_LABEL_SAMPLE_RETENTION
  risk_raw REAL, snapshot TEXT, PRIMARY KEY (trace_id, target, labeled_by));
CREATE TABLE IF NOT EXISTS studio_results (
  run_id TEXT NOT NULL, kind TEXT NOT NULL CHECK (kind IN ('test','dry_run')), sample_id TEXT NOT NULL,
  evaluator_ref TEXT NOT NULL, answer JSONB, risk_raw REAL, risk REAL, level TEXT, status TEXT, escalate TEXT,
  snapshot TEXT, latency_ms INT, cost_usd NUMERIC(14,10), created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (run_id, sample_id, evaluator_ref));
