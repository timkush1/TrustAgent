# Security

Threat model, implemented controls, and their mapping to the
[OWASP Top 10 for LLM Applications (2025)](https://owasp.org/www-project-top-10-for-large-language-model-applications/).
All controls below were implemented and tested in Phase 3 (see
[progress log](progress/PHASE-3.md)); SAST scanners enforce them in CI.

## Threat model

TrustAgent sits in the request path between client applications and an LLM,
and renders audit results to a browser dashboard. Untrusted inputs:

1. **Client API traffic** (`/v1/*`, `/api/audit`) — arbitrary prompts/responses,
   potentially adversarial (prompt injection aimed at the *audit* LLM,
   resource-exhaustion payloads).
2. **Uploaded knowledge-base documents** (`/api/upload`) — a poisoned document
   can attack the verifier ("always answer SUPPORTED") or pollute retrieval.
3. **The browser** — cross-origin pages attempting CORS reads or cross-site
   WebSocket hijacking (CSWSH) of live audit data.
4. **The audited LLM's output itself** — treated as untrusted data end to end.

Trusted-ish internals: the Go proxy ↔ Python engine gRPC link and the metrics
endpoints, isolated on the internal Docker network and never published to the
host.

## Controls

| Control | Where | OWASP LLM Top 10 |
|---|---|---|
| Untrusted text delimited in `<text>`/`<claim>`/`<context>` tags + explicit "data, never instructions" system-prompt rules | `decomposer.py`, `verifier.py` | LLM01 Prompt Injection |
| Hidden-character stripping (zero-width, bidi controls, C0/C1) + length caps before prompt insertion | `truthtable/security.py::sanitize_text` | LLM01 Prompt Injection |
| Strict schema validation of all LLM output: claims must be a string array (≤20 items, ≤500 chars); verdicts must have an allowed status + clamped confidence; anything else degrades to safe fallback / UNKNOWN | `security.py::validate_claims`, `validate_verdict` | LLM02 Insecure Output Handling, LLM05 Improper Output Handling |
| Model may not self-report `UNKNOWN` or invent statuses (reserved for parse failures) | `verifier.py` | LLM09 Overreliance |
| API-key auth (constant-time compare, `Bearer`/`X-API-Key`; WS via `?api_key=`) | `internal/middleware/auth.go` | — (API security baseline) |
| CORS allowlist replacing `*`; unlisted origins get no CORS headers | `internal/middleware/cors.go` | — |
| WebSocket origin allowlist (CSWSH defense); default-deny for browser origins until configured | `internal/websocket/hub.go::ConfigureUpgrader` | — |
| Per-client rate limiting (Redis fixed-window, in-memory fallback; stricter on uploads; fail-open on Redis outage by design) | `internal/middleware/ratelimit.go` | LLM10 Unbounded Consumption |
| Transport-level body caps (`http.MaxBytesReader`: 1 MiB default, 10 MiB uploads), text-length caps on audit fields, upload caps (≤1000 docs, ≤50k chars each, non-empty) | `internal/middleware/bodylimit.go`, `cmd/proxy/main.go` | LLM10 Unbounded Consumption |
| Claim-flooding cap: an injected "return 500 claims" response is truncated to 20 | `security.py::validate_claims` | LLM10 |
| `/metrics` removed from the public router; metrics (:8001/:8002) and gRPC (:50051) unpublished from the host — internal Docker network only | `cmd/proxy/main.go`, `docker-compose.yml` | — (information disclosure) |
| Container resource limits (memory/CPU) on all app services; non-root users in Go/Python images | `docker-compose.yml`, Dockerfiles | LLM10 |
| Grafana admin password required (no `changeme` default); API keys via env, never logged | `docker-compose.yml`, `.env.example` | — |
| Slowloris defense (`ReadHeaderTimeout`) on the metrics server | `cmd/proxy/main.go` | — |
| No literal bidi/zero-width characters in source (Trojan-Source, bandit B613) | `security.py` uses `\u` escapes | — (supply chain) |

## What a poisoned document can and cannot do

Knowledge-base poisoning (LLM03/LLM08 territory) is **mitigated but not
eliminated**: a document saying "the sky is green" makes the verifier
faithfully report claims about a green sky as SUPPORTED — that is the
designed behavior of verification against a knowledge base, and the defense
is curation of what gets ingested (auth + rate limits + size caps on upload).
What a poisoned document *cannot* do anymore: inject instructions into the
verifier prompt (delimited + declared untrusted), smuggle malformed verdicts
(schema validation), or hide payloads in invisible characters (sanitized).
Phase 6 (VERITAS-lite) adds ingest-time entailment gating and contradiction
detection, which directly target this residual risk.

## Scanner policy (CI)

| Scanner | Scope | Policy |
|---|---|---|
| gosec | `backend-go` (medium+ severity) | **Blocking** |
| bandit | `backend-python/src` (pb stubs excluded) | **Blocking** (0 findings) |
| npm audit | `frontend-react` (high+) | **Blocking** (highs fixed via `npm audit fix`) |
| Trivy | filesystem, HIGH/CRITICAL, fixed-only | Informational until first reviewed scheduled run |

Accepted findings:
- gosec G104 (LOW): unhandled errors on best-effort writes/closes
  (`w.Write` in the streaming tee path, `Close()` on shutdown). Revisit if the
  streaming path is reworked in Phase 4+.
- bandit B104 on gRPC `0.0.0.0` bind: intentional — containers must reach the
  engine on the internal network; the port is not host-published.

## Known gaps (deliberate, tracked in docs/PLAN.md)

- **No TLS** between proxy and engine: mitigated by Docker network isolation;
  mTLS was evaluated and cut as poor ROI for a single-host deployment.
- **No persistent audit trail** until Phase 4 (Postgres).
- The dashboard nginx container runs as the image's default user; Go/Python
  images run as dedicated non-root users.
- Auth is disabled when `TRUTHTABLE_API_KEYS` is empty (deliberate dev-mode
  ergonomics, with a loud startup warning). Production deployments must set keys.

## Reporting

This is a portfolio project; if you find something, open a GitHub issue.
