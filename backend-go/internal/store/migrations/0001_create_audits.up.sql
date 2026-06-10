CREATE TABLE IF NOT EXISTS audits (
    audit_id               TEXT PRIMARY KEY,
    request_id             TEXT NOT NULL,
    user_query             TEXT NOT NULL,
    llm_response           TEXT NOT NULL,
    model                  TEXT NOT NULL DEFAULT '',
    faithfulness_score     DOUBLE PRECISION NOT NULL,
    grade                  TEXT NOT NULL,
    hallucination_detected BOOLEAN NOT NULL,
    reasoning_trace        TEXT NOT NULL DEFAULT '',
    processing_time_ms     BIGINT NOT NULL DEFAULT 0,
    step_timings           JSONB,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audits_created_at ON audits (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audits_grade ON audits (grade);
CREATE INDEX IF NOT EXISTS idx_audits_flagged ON audits (hallucination_detected);

CREATE TABLE IF NOT EXISTS audit_claims (
    id         BIGSERIAL PRIMARY KEY,
    audit_id   TEXT NOT NULL REFERENCES audits (audit_id) ON DELETE CASCADE,
    position   INT NOT NULL,
    claim      TEXT NOT NULL,
    status     TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL,
    evidence   JSONB
);

CREATE INDEX IF NOT EXISTS idx_audit_claims_audit_id ON audit_claims (audit_id);
