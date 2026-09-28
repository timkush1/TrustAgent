-- 0004: runtime settings, encrypted credentials, API keys, the admin audit log and
-- the cost ledger (SPEC §2.3, P1b contract PR; lands before P2 authz).
--
-- Applied by the runner in postgres.go inside one transaction together with its
-- schema_migrations row, under pg_advisory_lock(7212026). Every statement is
-- idempotent (IF NOT EXISTS), so re-executing the file is a no-op. The file only
-- creates objects: there is no data to backfill.
--
-- settings rows override the env seed at run time (SPEC §2.7); a non-empty env var
-- still wins. Keys: decision_model, judge, upstream, bindings, budgets,
-- pricing_overrides, setup, session_epoch, epoch.
--
-- credentials hold AES-256-GCM ciphertext only (SPEC §7.5): the browser only ever
-- sees masked and fingerprint, and every outbound call is checked against
-- allowed_hosts. api_keys store sha256 of the key, never the key itself.
--
-- admin_audit_log never records secrets, and URLs with userinfo are rejected
-- before they reach it. It is never deleted.
--
-- cost_events has one row per Jev attempt (hedge losers included) and per LLM
-- call; retention keeps it for TRUTHTABLE_COST_RETENTION_DAYS (400).

CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value JSONB NOT NULL, version BIGINT NOT NULL DEFAULT 1,
  updated_by TEXT NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT now());  -- decision_model|judge|upstream|bindings|budgets|pricing_overrides|setup|session_epoch|epoch
CREATE TABLE IF NOT EXISTS credentials (
  name TEXT PRIMARY KEY CHECK (name IN ('openrouter','typesafe','anthropic','openai','compatible','upstream-default','upstream-anthropic','upstream-fallback')),
  provider TEXT NOT NULL, ciphertext BYTEA NOT NULL, nonce BYTEA NOT NULL, key_id TEXT NOT NULL,
  masked TEXT NOT NULL, fingerprint TEXT NOT NULL, allowed_hosts TEXT[] NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(), rotated_at TIMESTAMPTZ, last_verified_at TIMESTAMPTZ,
  last_verify_status TEXT, last_used_at TIMESTAMPTZ);
CREATE TABLE IF NOT EXISTS api_keys (id TEXT PRIMARY KEY, label TEXT NOT NULL,
  scopes TEXT[] NOT NULL CHECK (scopes <@ ARRAY['ingest','read','content','admin']::TEXT[]),
  sha256 TEXT NOT NULL UNIQUE, prefix TEXT NOT NULL, created_by TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_used_at TIMESTAMPTZ, revoked_at TIMESTAMPTZ);
CREATE TABLE IF NOT EXISTS admin_audit_log (id BIGSERIAL PRIMARY KEY, at TIMESTAMPTZ NOT NULL DEFAULT now(),
  actor TEXT NOT NULL, action TEXT NOT NULL, target TEXT, diff JSONB);   -- never secrets; URLs with userinfo rejected
CREATE TABLE IF NOT EXISTS cost_events (id BIGSERIAL PRIMARY KEY, occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  trace_id TEXT, route TEXT, key_label TEXT, component TEXT NOT NULL, kind TEXT NOT NULL, provider TEXT, model TEXT,
  attempt SMALLINT NOT NULL DEFAULT 0, input_tokens INT, output_tokens INT, cost_usd NUMERIC(14,10),
  cost_source TEXT NOT NULL, price_table_version TEXT, latency_ms INT, status TEXT, questions INT, selection TEXT);
CREATE INDEX IF NOT EXISTS idx_cost_events_time ON cost_events (occurred_at DESC, component);
