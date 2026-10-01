-- 0005: P4 additions to the cost ledger, traces, calibrations and the admin audit
-- log (P4-PLAN D44, D45, D55; SPEC §2.3 erratum).
--
-- Applied by the runner in postgres.go inside one transaction together with its
-- schema_migrations row, under pg_advisory_lock(7212026). Every statement is
-- idempotent (IF NOT EXISTS, CREATE OR REPLACE, DROP ... IF EXISTS before CREATE),
-- so re-executing the file is a no-op. The file only adds objects: new columns are
-- nullable or carry a constant default, so there is no data to backfill.
--
-- cost_events: LLM rows link to their audit by audit_id and are stamped when the
-- audit completes; hedge marks a hedge attempt, late a row booked after its trace
-- was already answered, estimated a cost the ledger estimated rather than read
-- from a provider. Both new indexes are partial, so rows without a link cost
-- nothing.
--
-- traces.response_chars is the rune count of the gated answer text (NULL for
-- traces written before P4).
--
-- calibrations_file_entry keeps one mirrored row per (file, target): a mirrored
-- file row is never updated, only inserted once (D55).
--
-- admin_audit_log is append-only: any UPDATE or DELETE raises (D44).

ALTER TABLE cost_events ADD COLUMN IF NOT EXISTS audit_id TEXT;
ALTER TABLE cost_events ADD COLUMN IF NOT EXISTS hedge BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE cost_events ADD COLUMN IF NOT EXISTS late BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE cost_events ADD COLUMN IF NOT EXISTS estimated BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX IF NOT EXISTS idx_cost_events_trace ON cost_events (trace_id) WHERE trace_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_cost_events_audit ON cost_events (audit_id) WHERE audit_id IS NOT NULL;

ALTER TABLE traces ADD COLUMN IF NOT EXISTS response_chars INT;

CREATE UNIQUE INDEX IF NOT EXISTS calibrations_file_entry ON calibrations (file_sha256, target_key) WHERE origin = 'file';

CREATE OR REPLACE FUNCTION trustagent_forbid_audit_change() RETURNS trigger AS $$
BEGIN RAISE EXCEPTION 'admin_audit_log rows are append-only'; END; $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS admin_audit_log_append_only ON admin_audit_log;
CREATE TRIGGER admin_audit_log_append_only BEFORE UPDATE OR DELETE ON admin_audit_log
  FOR EACH ROW EXECUTE FUNCTION trustagent_forbid_audit_change();
