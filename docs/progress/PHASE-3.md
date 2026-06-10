# Phase 3 — Security Hardening

**Status**: ✅ Complete (2026-06-10)
**Goal**: fix every finding from the security audit, map controls to the OWASP
LLM Top 10, and enforce the posture with blocking CI scanners.
Full control inventory: [docs/SECURITY.md](../SECURITY.md).

## What was done

### Go proxy (`backend-go`)
- New `internal/middleware/` package:
  - **auth.go** — API-key auth (`Bearer`/`X-API-Key`), constant-time compare
    via `crypto/subtle`; empty key set = dev mode with a loud startup warning.
  - **cors.go** — origin allowlist replacing `Access-Control-Allow-Origin: *`;
    unlisted origins receive no CORS headers.
  - **ratelimit.go** — fixed-window limiter: Redis-backed when `REDIS_URL` is
    set (the Redis container finally has a job), in-memory fallback otherwise;
    keyed by API key, falling back to client IP; fails open on Redis outage.
  - **bodylimit.go** — transport-level body caps via `http.MaxBytesReader`.
- `cmd/proxy/main.go`: all `/v1/*` and `/api/*` routes now run
  auth → rate limit → body limit; `/api/upload` gets a stricter rate limit and
  a 10 MiB cap; `/health` stays open. Public `/metrics` route **removed**
  (Prometheus scrapes the internal :8002 server). WebSocket handshake requires
  `?api_key=` when auth is enabled. Input validation: query/response ≤ 20k
  chars, model name ≤ 200, uploads ≤ 1000 docs × 50k chars, non-empty content.
- `internal/websocket/hub.go`: `CheckOrigin` no longer returns `true`
  unconditionally — origin allowlist via `ConfigureUpgrader`; empty Origin
  (non-browser clients) allowed, covered by API-key auth instead.
- gosec G112 fix: `ReadHeaderTimeout` on the metrics server.

### Python engine (`backend-python`)
- New `truthtable/security.py`:
  - `sanitize_text` — strips zero-width/bidi/control characters (escape
    sequences only in source — literal ones are a Trojan-Source vector, which
    bandit B613 caught in the first draft of this very file), caps length.
  - `parse_json_strict` / `validate_claims` / `validate_verdict` — strict
    schema validation of all LLM output.
- `decomposer.py`: system prompt declares `<text>` content untrusted data;
  input sanitized; output must be a string array (≤20 claims, ≤500 chars
  each); schema violations fall back to whole-response-as-one-claim.
- `verifier.py`: `<claim>`/`<context>` declared untrusted; context sanitized;
  verdicts validated (status whitelist — the model may not self-report
  UNKNOWN — confidence clamped to [0,1], evidence capped); anything malformed
  degrades to UNKNOWN/0.0.

### Deployment (`docker-compose.yml`, `.env.example`)
- gRPC :50051 and metrics :8001/:8002 no longer published to the host
  (internal network only; verified Prometheus scrapes via service names).
- Memory/CPU limits on audit-engine, proxy, dashboard.
- Grafana password is now required (`:?` expansion error instead of
  `changeme` default).
- **Bug fix**: proxy env names in compose didn't match `config.go`
  (`UPSTREAM_LLM_URL` vs `TRUTHTABLE_UPSTREAM_URL` etc.) — in Docker the proxy
  was silently defaulting to `https://api.openai.com` as upstream. Now mapped
  correctly, plus new `TRUTHTABLE_API_KEYS` / `TRUTHTABLE_ALLOWED_ORIGINS`
  passthrough documented in `.env.example`.

### CI scanners flipped to blocking
- gosec at medium+ severity (1 finding found and fixed: G112 Slowloris).
- bandit (1 HIGH found and fixed: B613 Trojan-Source — in the new security
  module itself; 2 MEDIUM B104 triaged as intentional, one suppressed with
  justification). Now 0 findings.
- npm audit at high+ (3 high vulns fixed via `npm audit fix`, incl. rollup
  path traversal; 2 moderates remain below threshold).
- Trivy stays informational until a full scheduled run is reviewed.

## Tests added (+31; totals: 53 Python, Go middleware suite)
- Go `internal/middleware/middleware_test.go` (14): auth 401/200 paths, bearer
  + header keys, constant-time helper, CORS allow/deny/preflight, WS origin
  rules, memory-limiter window behavior, 429 enforcement, per-key buckets,
  body-limit 413.
- Python `tests/unit/test_prompt_injection.py` (17): hidden-char stripping,
  truncation, fence-tolerant strict parsing, object smuggling, claim flooding,
  injected-instruction fallbacks, verdict status whitelist, confidence
  clamping, untrusted-data prompt assertions.

## Verification evidence
- `pytest tests/unit` → **53 passed**; ruff + black clean; bandit **0 findings**;
  golden eval baseline **unchanged** (hardening is behavior-preserving on
  clean inputs).
- `go build`/`go vet` clean; all Go package tests `ok`; gosec medium+ exit 0.
- `npm audit --audit-level=high` passes; vitest 15/15 after dep fixes.
- `docker compose config` validates.
