<!--
  GENERATED FILE: do not edit by hand.
  Source: contracts/config/keys.v1.json (schema: contracts/schema/keys.v1.schema.json).
  Regenerate with `make config-docs` (python3 scripts/gen_config_docs.py);
  `make config-docs-check` and CI fail when this file and the registry disagree.
-->

# Configuration reference

TrustAgent is configured with environment variables. This page lists every one of them:
189 keys in 13 groups, generated from the key registry
[`contracts/config/keys.v1.json`](../contracts/config/keys.v1.json).

**You rarely need this page.** The demo (`make demo`) needs no settings at all, and a
normal install needs one key, `OPENROUTER_API_KEY` (see [JEV-ACCESS.md](JEV-ACCESS.md)).
The 15 settings most people ever change are in [`.env.example`](../.env.example);
every other key is marked *advanced* and has a safe default.

## How settings are read

- **Where to put them.** In `.env` at the repository root. `make up` and `make demo`
  create it from `.env.example` when it is missing and fill in the generated passwords.
- **Which value wins at start.** Built-in default, then `.env`, then the process
  environment. A variable that is already set in the environment is never overwritten
  by `.env`.
- **Settings you can change while running** (marked *Also editable while running*)
  also get a value in dashboard Settings. A non-empty
  environment variable always wins over it and shows there as read-only (*managed by
  environment*); to change such a value, edit `.env` and run `make up` again.
- **Durations** use Go syntax in both services: `800ms`, `30s`, `5m`, `2160h`.
- **Mistakes are reported, not guessed.** An invalid value produces a warning with a
  hint, and an unknown `TRUTHTABLE_*` name gets a *did you mean …?* suggestion. With
  `TRUTHTABLE_STRICT_CONFIG=true` (the demo, CI and e2e set it) any warning stops the
  service.
- **Aliases** are older or alternative names. When both are set, the canonical name wins.

## Reading the tables

| Column | Meaning |
|---|---|
| Key | The variable name, plus any aliases. |
| Default | The built-in value. *empty* means unset; *none* means there is no fixed value (it is generated or derived, as noted). `compose:` is the value the shipped `docker-compose.yml` or image sets. |
| Services | Who reads it: `proxy` = the Go gateway; `engine` = the Python audit engine; `compose` = docker compose and make; `dashboard` = the dashboard container (at start); `build` = the dashboard build (baked into the bundle); `tools` = setup and demo tools (trafficgen, doctor, configure). |
| Advanced | `no` = listed in `.env.example`. `yes` = add it to `.env` yourself when you need it. |
| Secret | `yes` = never logged, never sent to the browser, masked in the dashboard. |
| Restart | What a change needs. `yes` = a restart: edit `.env`, then run `make up` again. `rebuild` = a new build: the key is read only when the dashboard bundle is built (for example `npm run build` in `frontend-react/`). `no` = nothing when changed in dashboard Settings; a value in `.env` still takes effect at the next `make up`. |
| Description | What it does, then the value syntax. |

## Groups

- [Core](#core): 19 keys
- [Auth](#auth): 13 keys
- [Upstream](#upstream): 13 keys
- [Limits](#limits): 6 keys
- [Persistence](#persistence): 7 keys
- [Engine link, worker, sampling](#engine-link-worker-sampling): 9 keys
- [WebSocket](#websocket): 5 keys
- [Policy and gate](#policy-and-gate): 11 keys
- [Decision model](#decision-model): 38 keys
- [Budgets, Studio](#budgets-studio): 6 keys
- [Engine: judge](#engine-judge): 11 keys
- [Engine: pipeline and server](#engine-pipeline-and-server): 26 keys
- [Compose, host, dashboard, build](#compose-host-dashboard-build): 25 keys

## Core

Ports, timeouts, logging, identity of the instance.

| Key | Default | Services | Advanced | Secret | Restart | Description |
|---|---|---|---|---|---|---|
| `TRUTHTABLE_PORT` | `8080` | proxy | yes | no | yes | HTTP and WebSocket listen port of the proxy.<br>Integer, from 1 to 65535. |
| `TRUTHTABLE_METRICS_PORT` | `8002` | proxy | yes | no | yes | Prometheus metrics port of the proxy.<br>Integer, from 1 to 65535. |
| `TRUTHTABLE_READ_TIMEOUT` | `30s` | proxy | yes | no | yes | HTTP server read timeout.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_READ_HEADER_TIMEOUT` | `10s` | proxy | yes | no | yes | HTTP server read-header timeout.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_WRITE_TIMEOUT` | `0` | proxy | yes | no | yes | HTTP server write timeout. 0 means per-route deadlines apply instead (M12): /api, /v1 (upstream timeout + 30 s) and KB upload (ingest timeout + 30 s).<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_SHUTDOWN_TIMEOUT` | `30s` | proxy | yes | no | yes | Graceful shutdown limit for the HTTP server.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_SHUTDOWN_DRAIN_TIMEOUT` | `20s` | proxy | yes | no | yes | How long shutdown waits for queued deep audits to drain (M6).<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_LOG_LEVEL` | `info` | proxy, engine | yes | no | yes | Log level.<br>One of `debug`, `info`, `warn`, `error`. |
| `TRUTHTABLE_ENV_FILE` | `auto` | proxy, engine | yes | no | yes | Dotenv file to load. auto: Go walks up to the directory containing VERSION and uses its .env; Python loads repo_root/.env then ./.env. An empty value disables dotenv loading. The loader never overwrites a variable that is already set.<br>File path. |
| `TRUTHTABLE_STRICT_CONFIG` | `false` | proxy, engine | yes | no | yes | Exit with code 2 on any configuration warning (invalid value or unknown `TRUTHTABLE_*` key). CI and e2e set it.<br>`true` or `false`. |
| `TRUTHTABLE_DATA_DIR` | `./.trustagent`<br>compose: `/var/lib/trustagent` | proxy | yes | no | yes | Proxy data directory; holds secret.key.<br>File path. |
| `TRUTHTABLE_PUBLIC_URL` | `http://localhost:3000` | proxy | no | no | yes | Public dashboard URL: base of the OpenRouter PKCE callback, the cookie Secure default and the CSRF origin.<br>URL. |
| `TRUTHTABLE_TRUSTED_PROXIES` | *empty*<br>compose sets the internal network CIDR | proxy | yes | no | yes | CIDRs whose X-Forwarded-For is trusted for client IPs (M8, C-M21). Empty trusts none.<br>Comma-separated list. |
| `TRUTHTABLE_ALLOWED_ORIGINS` | `http://localhost:5173,http://localhost:3000` | proxy | yes | no | yes | Allowed browser origins for CORS and WebSocket.<br>Comma-separated list. |
| `TRUTHTABLE_DEMO_MODE` | `false` | proxy, engine | yes | no | yes | Demo mode: forces the mock decision model and labels every surface as simulated.<br>`true` or `false`. |
| `TRUTHTABLE_READONLY` | `false` | proxy, engine | yes | no | yes | Public read-only instance: every write endpoint is refused.<br>`true` or `false`. |
| `TRUTHTABLE_PUBLIC_READ` | `false` | proxy, engine | yes | no | yes | Allow reads without a key (auth.read = public).<br>`true` or `false`. |
| `TRUTHTABLE_TEST_MODE` | `false` | proxy, engine | yes | no | yes | Honour test_response and X-TrustAgent-Scenario. Without it, test_response returns 400 test_mode_disabled (M7).<br>`true` or `false`. |
| `BIND_ADDR` | `127.0.0.1` | compose | no | no | yes | Host address every published compose port binds to.<br>Text. |

## Auth

API keys, dashboard sessions, the setup token and internal RPC credentials.

| Key | Default | Services | Advanced | Secret | Restart | Description |
|---|---|---|---|---|---|---|
| `TRUTHTABLE_INGEST_AUTH` | `keys` | proxy | yes | no | yes | Who may send /v1 traffic: keys (an ingest key is required) or open. open is refused while an upstream credential is injected.<br>One of `keys`, `open`. |
| `TRUTHTABLE_API_KEYS` | *empty* | proxy | no | yes | yes | Env API keys as label=key,...; scopes ingest + read + content.<br>Comma-separated `label=key` pairs. |
| `TRUTHTABLE_READ_KEYS` | *empty* | proxy | yes | yes | yes | Env API keys with the read scope only.<br>Comma-separated `label=key` pairs. |
| `TRUTHTABLE_ADMIN_KEYS`<br>alias `TRUTHTABLE_ADMIN_KEY` | *empty* | proxy | no | yes | yes | Env API keys with every scope. Admin keys never change the ingest/read auth mode.<br>Comma-separated `label=key` pairs. |
| `TRUTHTABLE_SECRET_KEY` | *empty* | proxy | yes | yes | yes | Master key (base64, 32 bytes) for credential encryption and sessions. Otherwise read from `TRUTHTABLE_SECRET_KEY_FILE`, otherwise generated there.<br>Text. |
| `TRUTHTABLE_SECRET_KEY_FILE` | `${TRUTHTABLE_DATA_DIR}/secret.key` | proxy | yes | no | yes | Where the master key is read from or generated (mode 0600, directory 0700).<br>File path. |
| `TRUTHTABLE_SESSION_TTL` | `12h` | proxy | yes | no | yes | Dashboard session lifetime. Sessions slide for 30 days when `TRUTHTABLE_PUBLIC_URL` is localhost.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_SETUP_TOKEN_TTL` | `1h` | proxy | yes | no | yes | Lifetime of the one-time setup token.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_SETUP_TOKEN` | *empty* | proxy | yes | yes | yes | Seeds the one-time setup token instead of generating one.<br>Text. |
| `TRUTHTABLE_COOKIE_SECURE` | `auto` | proxy | yes | no | yes | Secure flag on session cookies. auto = true when `TRUTHTABLE_PUBLIC_URL` is https.<br>One of `auto`, `true`, `false`. |
| `TRUTHTABLE_INTERNAL_TOKEN` | *empty* | proxy, engine | yes | yes | yes | Shared token for proxy -> engine RPCs (metadata x-trustagent-internal). The proxy generates it when unset; the engine reads it.<br>Text. |
| `TRUTHTABLE_INTERNAL_TOKEN_FILE` | `/var/lib/trustagent-internal/internal.token` | proxy, engine | yes | no | yes | File holding the internal token (proxy writes, engine reads).<br>File path. |
| `TRUTHTABLE_KEY_DECISION_USD_DAY` | `0` | proxy | yes | no | yes | Optional per-API-key daily decision spend cap in USD. 0 = off.<br>Number, at least 0. |

## Upstream

Where the proxy forwards LLM requests.

| Key | Default | Services | Advanced | Secret | Restart | Description |
|---|---|---|---|---|---|---|
| `TRUTHTABLE_UPSTREAM_URL`<br>alias `UPSTREAM_LLM_URL` | *empty*<br>empty resolves to https://api.openai.com | proxy | no | no | no | Default upstream for OpenAI-format requests. Empty = https://api.openai.com.<br>URL. Shorthands: `ollama` = `${OLLAMA_BASE_URL}`, `openrouter` = `https://openrouter.ai/api`. Also editable while running in dashboard Settings. |
| `TRUTHTABLE_UPSTREAM_API_KEY` | *empty* | proxy | no | yes | yes | Upstream credential. Setting it enables inject mode: client credentials are stripped and this one is sent on `TRUTHTABLE_UPSTREAM_AUTH_HEADER`.<br>Text. |
| `TRUTHTABLE_UPSTREAM_AUTH_HEADER` | `authorization` | proxy | yes | no | no | Header that carries the injected upstream credential.<br>One of `authorization`, `api-key`, `x-api-key`. Also editable while running in dashboard Settings. |
| `TRUTHTABLE_ANTHROPIC_UPSTREAM_URL` | `https://api.anthropic.com` | proxy | yes | no | no | Upstream for Anthropic Messages requests.<br>URL. Also editable while running in dashboard Settings. |
| `TRUTHTABLE_ANTHROPIC_UPSTREAM_API_KEY` | *empty* | proxy | yes | yes | yes | Injected credential for the Anthropic upstream. The dashboard alternative is the encrypted upstream-anthropic credential in Settings; a non-empty value here wins.<br>Text. |
| `TRUTHTABLE_FALLBACK_UPSTREAM_URL` | *empty* | proxy | yes | no | no | Upstream used by fallback.upstream: fallback. Empty = no fallback upstream.<br>URL. Also editable while running in dashboard Settings. |
| `TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY` | *empty* | proxy | yes | yes | yes | Credential for the fallback upstream. The dashboard alternative is the encrypted upstream-fallback credential in Settings; a non-empty value here wins.<br>Text. |
| `TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT` | `openai` | proxy | yes | no | no | API format of the fallback upstream; a mismatch with the request degrades to block (fallback_format_mismatch).<br>One of `openai`, `anthropic`. Also editable while running in dashboard Settings. |
| `TRUTHTABLE_UPSTREAM_HEADER_TIMEOUT` | `60s` | proxy | yes | no | yes | Time to the upstream response headers.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_UPSTREAM_TIMEOUT` | `5m` | proxy | yes | no | yes | Total upstream request timeout.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_STREAM_IDLE_TIMEOUT` | `120s` | proxy | yes | no | yes | Maximum gap between upstream stream events.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_MAX_UPSTREAM_RESPONSE_BYTES` | `8388608` | proxy | yes | no | yes | Largest upstream body the proxy gates (8 MiB). Larger bodies pass through with Status skipped:upstream_error.<br>Byte count, at least 1. |
| `OLLAMA_BASE_URL` | `http://localhost:11434`<br>compose: `http://ollama:11434` | proxy, engine | yes | no | yes | Ollama endpoint: the ollama upstream shorthand and the local judge.<br>URL. |

## Limits

Rate limits and size caps.

| Key | Default | Services | Advanced | Secret | Restart | Description |
|---|---|---|---|---|---|---|
| `TRUTHTABLE_RATE_LIMIT_PER_MINUTE` | `120` | proxy | yes | no | yes | Requests per minute per key (else per client IP).<br>Integer, at least 0. |
| `TRUTHTABLE_UPLOAD_LIMIT_PER_MINUTE` | `10` | proxy | yes | no | yes | Knowledge uploads per minute per key.<br>Integer, at least 0. |
| `TRUTHTABLE_MAX_BODY_BYTES` | `1048576` | proxy | yes | no | yes | Largest accepted request body (1 MiB); larger returns 413.<br>Byte count, at least 1. |
| `TRUTHTABLE_MAX_UPLOAD_BYTES` | `10485760` | proxy | yes | no | yes | Largest accepted knowledge upload (10 MiB).<br>Byte count, at least 1. |
| `TRUTHTABLE_MAX_TEXT_CHARS` | `20000` | proxy | yes | no | yes | Largest direct-audit text, counted in runes.<br>Integer, at least 1. |
| `REDIS_URL`<br>alias `TRUTHTABLE_REDIS_URL` | *empty* | proxy | yes | yes | yes | Redis for shared rate limiting (redis profile). Empty = in-memory limiter.<br>URL. |

## Persistence

Database and retention.

| Key | Default | Services | Advanced | Secret | Restart | Description |
|---|---|---|---|---|---|---|
| `TRUTHTABLE_DATABASE_URL` | *empty*<br>compose sets it to the postgres service | proxy | yes | yes | yes | PostgreSQL URL (>= 14). Empty = in-memory only (dashboard edits apply until restart).<br>URL. |
| `TRUTHTABLE_RETENTION_DAYS` | `30` | proxy | yes | no | yes | Days traces, decisions and audits are kept.<br>Integer, at least 1. |
| `TRUTHTABLE_COST_RETENTION_DAYS` | `400` | proxy | yes | no | yes | Days cost events are kept.<br>Integer, at least 1. |
| `TRUTHTABLE_BODY_RETENTION` | `168h` | proxy | yes | no | yes | How long stored request and response bodies are kept.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_BODY_MAX_BYTES` | `262144` | proxy | yes | no | yes | Largest body stored per trace.<br>Byte count, at least 0. |
| `TRUTHTABLE_LABEL_SAMPLE_RETENTION` | `2160h` | proxy | yes | no | yes | How long bodies of labelled samples are kept for calibration (90 days).<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_TRACE_FLUSH` | `async` | proxy | yes | no | yes | Trace writer mode. sync writes before responding (tests).<br>One of `async`, `sync`. |

## Engine link, worker, sampling

How the proxy reaches the audit engine and queues deep audits.

| Key | Default | Services | Advanced | Secret | Restart | Description |
|---|---|---|---|---|---|---|
| `TRUTHTABLE_GRPC_ADDRESS` | `localhost:50051`<br>compose: `engine:50051` | proxy | yes | no | yes | Audit engine gRPC address (host:port).<br>Text. |
| `TRUTHTABLE_GRPC_TIMEOUT` | `120s` | proxy | yes | no | yes | Deadline of one Evaluate call.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_INGEST_TIMEOUT` | `6m` | proxy | yes | no | yes | Deadline of a knowledge ingest call.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_WORKER_COUNT` | `10` | proxy | yes | no | yes | Deep-audit workers. Warns when above the engine max_audits.<br>Integer, at least 1. |
| `TRUTHTABLE_QUEUE_SIZE` | `1000` | proxy | yes | no | yes | Low-lane deep-audit queue capacity.<br>Integer, at least 1. |
| `TRUTHTABLE_AUDIT_QUEUE_HIGH` | `200` | proxy | yes | no | yes | High-lane deep-audit queue capacity.<br>Integer, at least 1. |
| `TRUTHTABLE_AUDIT_HIGH_PER_MIN` | `30` | proxy | yes | no | yes | Token bucket for the high lane; overflow goes to the low lane.<br>Integer, at least 0. |
| `TRUTHTABLE_UNIFORM_AUDITS_PER_DAY` | `50` | proxy | yes | no | yes | Daily cap on uniform-sample deep audits; sampling stops for the day once reached.<br>Integer, at least 0. |
| `TRUTHTABLE_ENGINE_HEALTH_INTERVAL` | `10s` | proxy | yes | no | yes | Engine health ping interval (M4).<br>Duration (Go syntax, e.g. `30s`, `5m`). |

## WebSocket

Live dashboard stream limits.

| Key | Default | Services | Advanced | Secret | Restart | Description |
|---|---|---|---|---|---|---|
| `TRUTHTABLE_WS_MAX_CLIENTS` | `200` | proxy | yes | no | yes | Maximum concurrent WebSocket clients.<br>Integer, at least 1. |
| `TRUTHTABLE_WS_MAX_PER_IP` | `10` | proxy | yes | no | yes | Maximum WebSocket clients per IP.<br>Integer, at least 1. |
| `TRUTHTABLE_WS_REPLAY_BUFFER` | `2000` | proxy | yes | no | yes | Events kept in the replay ring for resync.<br>Integer, at least 0. |
| `TRUTHTABLE_WS_REPLAY_MAX_BYTES` | `67108864` | proxy | yes | no | yes | Byte cap of the replay ring (64 MiB).<br>Byte count, at least 0. |
| `TRUTHTABLE_WS_HEARTBEAT` | `20s` | proxy | yes | no | yes | WebSocket heartbeat interval.<br>Duration (Go syntax, e.g. `30s`, `5m`). |

## Policy and gate

Policy file, evaluators, calibration and gate defaults.

| Key | Default | Services | Advanced | Secret | Restart | Description |
|---|---|---|---|---|---|---|
| `TRUTHTABLE_POLICY_FILE` | `config/policies.yaml`<br>compose: `/etc/trustagent/policies.yaml` | proxy | yes | no | yes | Policy file (schema policy/v1).<br>File path. |
| `TRUTHTABLE_POLICY_RELOAD_INTERVAL` | `5s` | proxy | yes | no | yes | Policy file poll interval. 0 disables polling.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_CONFIG_LOCK` | `false` | proxy | yes | no | yes | Make the policy file the only authority: dashboard policy edits are refused (423).<br>`true` or `false`. |
| `TRUTHTABLE_EVALUATORS_DIR` | `config/evaluators` | proxy | yes | no | yes | Directory of custom evaluator files (evaluator/v1).<br>File path. |
| `TRUTHTABLE_CALIBRATION_DIR` | `config/calibration` | proxy | yes | no | yes | Directory of calibration files (trustagent.calibration/v1).<br>File path. |
| `TRUTHTABLE_CALIBRATION_FAMILY_FALLBACK` | `false` | proxy | yes | no | yes | Use a calibration of the same snapshot family when the exact snapshot has none (forced to provisional).<br>`true` or `false`. |
| `TRUTHTABLE_GATE_DEFAULT_MODE` | `observe` | proxy | no | no | yes | Seeds defaults.mode when the policy file omits it.<br>One of `observe`, `flag`, `block`, `fallback`. Seeds a value that is then edited in the dashboard or policy file. |
| `TRUTHTABLE_GATE_FAIL_MODE` | `open` | proxy | yes | no | yes | Seeds defaults.fail when the policy file omits it.<br>One of `open`, `closed`. Seeds a value that is then edited in the dashboard or policy file. |
| `TRUTHTABLE_GATE_MAX_CONCURRENCY` | `256` | proxy | yes | no | yes | Concurrent asynchronous gates; overflow is skipped:overload (detectors still run).<br>Integer, at least 1. |
| `TRUTHTABLE_KNOWLEDGE_ENABLED`<br>aliases `KNOWLEDGE_ENABLED`, `RAG_ENABLED` | `false` | proxy, engine | yes | no | yes | Enable the knowledge add-on (evidence source knowledge).<br>`true` or `false`. |
| `TRUTHTABLE_KNOWLEDGE_DEADLINE` | `150ms` | proxy, engine | yes | no | yes | Default for evidence.knowledge.deadline_ms.<br>Duration (Go syntax, e.g. `30s`, `5m`). |

## Decision model

The fast check (Jev). Go only in 2.0.

| Key | Default | Services | Advanced | Secret | Restart | Description |
|---|---|---|---|---|---|---|
| `TRUTHTABLE_DECISION_PROVIDER` | `jev` | proxy | yes | no | yes | Decision model family. Reserved for another backend family.<br>One of `jev`. |
| `TRUTHTABLE_JEV_BACKEND` | `auto` | proxy | no | no | no | Fast-check backend. auto: demo -> mock; base URL + `TRUTHTABLE_JEV_API_KEY` -> compatible; OpenRouter key -> openrouter-decisions; TypeSafe key -> typesafe; otherwise off.<br>One of `auto`, `openrouter-decisions`, `openrouter-systemone`, `typesafe`, `compatible`, `mock`, `off`. Also editable while running in dashboard Settings. |
| `OPENROUTER_API_KEY` | *empty* | proxy, engine | no | yes | yes | One OpenRouter key powers the fast check and the judge.<br>Text. |
| `TYPESAFE_API_KEY` | *empty* | proxy | yes | yes | yes | TypeSafe direct API key (backend typesafe).<br>Text. |
| `TRUTHTABLE_JEV_API_KEY` | *empty* | proxy | yes | yes | yes | Credential for an explicit backend or compatible only; never applied to the auto-selected vendor backends.<br>Text. |
| `TRUTHTABLE_JEV_API_KEY_FILE` | *empty* | proxy | yes | no | yes | File holding `TRUTHTABLE_JEV_API_KEY`.<br>File path. |
| `TRUTHTABLE_JEV_MODEL` | *empty*<br>openrouter-\*: typesafe/jev-1.13; typesafe: jev-1.13.0; compatible: required; mock: mock/jev-heuristic-v1 | proxy | no | no | no | Decision model id. \~typesafe/jev-latest and jev-latest are allowed with a warning (thresholds are tuned per version).<br>Text. Also editable while running in dashboard Settings. |
| `TRUTHTABLE_JEV_BASE_URL` | *empty*<br>per backend | proxy | yes | no | no | Decision endpoint base URL; https only (http for localhost). Used for compatible, or for vendor backends when set explicitly.<br>URL. Also editable while running in dashboard Settings. |
| `TRUTHTABLE_JEV_PATH` | *empty*<br>per backend | proxy | yes | no | no | Decision endpoint path appended to the base URL.<br>Text. Also editable while running in dashboard Settings. |
| `TRUTHTABLE_OPENROUTER_BASE_URL` | `https://openrouter.ai` | proxy, engine | yes | no | yes | OpenRouter base for PKCE, key checks, backends and the judge; tests point it at mock-upstream.<br>URL. |
| `TRUTHTABLE_JEV_ATTEMPT_TIMEOUT` | `3s` | proxy | yes | no | yes | Per-attempt timeout. Each attempt runs detached with this timeout; a synchronous gate stops waiting at the route deadline, and an attempt still running then may finish as a late decision (never enforced).<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_JEV_ASYNC_DEADLINE` | `10s` | proxy | yes | no | yes | Deadline for observe gates and late decisions.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_JEV_MAX_RETRIES` | `1` | proxy | yes | no | yes | Retries of a synchronous decision call.<br>Integer, at least 0. |
| `TRUTHTABLE_JEV_MAX_RETRIES_ASYNC` | `2` | proxy | yes | no | yes | Retries of an asynchronous decision call.<br>Integer, at least 0. |
| `TRUTHTABLE_JEV_RETRY_AFTER_CAP` | `10s` | proxy | yes | no | yes | Longest Retry-After honoured.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_GATE_JEV_RPM` | `1000` | proxy | yes | no | yes | Decision calls per minute (below the vendor 1,200 RPM).<br>Integer, at least 1. |
| `TRUTHTABLE_GATE_JEV_SYNC_RESERVE` | `0.3` | proxy | yes | no | yes | Share of the rate limit reserved for synchronous gates.<br>Number, from 0 to 1. |
| `TRUTHTABLE_GATE_JEV_MAX_INFLIGHT` | `64` | proxy | yes | no | yes | Concurrent decision calls.<br>Integer, at least 1. |
| `TRUTHTABLE_JEV_TOKEN_BUDGET` | `24000` | proxy | yes | no | yes | Token budget of one decision request (SPEC §2.9 budget.tokens).<br>Integer, at least 1000. |
| `TRUTHTABLE_JEV_CHARS_PER_TOKEN` | `4.0` | proxy | yes | no | yes | Initial characters-per-token estimate (cpt); EWMA-corrected at runtime.<br>Number, from 2.5 to 6. |
| `TRUTHTABLE_JEV_MAX_QUESTIONS_PER_CALL` | `16` | proxy | yes | no | yes | Questions per decision request before splitting.<br>Integer, at least 1. |
| `TRUTHTABLE_JEV_MAX_RESPONSE_TOKENS` | `6000` | proxy | yes | no | yes | Seeds decision.max_response_tokens; longer responses are not assessed (too_long).<br>Integer, from 500 to 12000. Seeds a value that is then edited in the dashboard or policy file. |
| `TRUTHTABLE_JEV_BREAKER_FAILURES` | `5` | proxy | yes | no | yes | Consecutive vendor faults that open the circuit breaker.<br>Integer, at least 1. |
| `TRUTHTABLE_JEV_BREAKER_COOLDOWN` | `30s` | proxy | yes | no | yes | How long the breaker stays open.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_JEV_AUTH_COOLDOWN` | `5m` | proxy | yes | no | yes | Pause after an authentication failure.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_JEV_PRICE_PER_MTOK` | *empty*<br>empty uses the price table (0.042) | proxy | yes | no | yes | Override of the decision input price in USD per million tokens.<br>Number, at least 0. |
| `TRUTHTABLE_JEV_APP_URL` | `https://github.com/timkush1/TrustAgent` | proxy, engine | yes | no | yes | OpenRouter attribution header HTTP-Referer.<br>URL. |
| `TRUTHTABLE_JEV_APP_TITLE` | `TrustAgent` | proxy, engine | yes | no | yes | OpenRouter attribution header X-Title.<br>Text. |
| `TRUTHTABLE_JEV_ZDR` | `false` | proxy | yes | no | yes | Request zero data retention routing on OpenRouter.<br>`true` or `false`. |
| `TRUTHTABLE_JEV_SEND_SESSION_ID` | `true` | proxy | yes | no | yes | Send the trace id as session_id.<br>`true` or `false`. |
| `TRUTHTABLE_JEV_STARTUP_PROBE` | `true` | proxy | yes | no | yes | Send one tiny probe question at startup to verify the backend.<br>`true` or `false`. |
| `TRUTHTABLE_JEV_KEEPALIVE` | `60s` | proxy | yes | no | yes | Keep-warm interval while a synchronous route exists.<br>Duration (Go syntax, e.g. `30s`, `5m`). |
| `TRUTHTABLE_JEV_LOG_PAYLOADS` | `false` | proxy | yes | no | yes | Log decision request and response bodies (keys are never logged).<br>`true` or `false`. |
| `TRUTHTABLE_JEV_MOCK_MODE` | `scenario` | proxy | yes | no | yes | Mock backend mode: scenario answers from contracts demo scenarios (mock_answers); hash gives deterministic hash-based answers; heuristic gives lexical heuristics.<br>One of `scenario`, `hash`, `heuristic`. |
| `TRUTHTABLE_JEV_MOCK_STRICT` | `false` | proxy | yes | no | yes | Fail instead of falling back when a scenario has no mock answer.<br>`true` or `false`. |
| `TRUTHTABLE_JEV_MOCK_FIXTURES` | *empty*<br>empty uses the embedded contracts/demo/scenarios.v1.json | proxy | yes | no | yes | Scenario file for the mock backend.<br>File path. |
| `TRUTHTABLE_JEV_MOCK_SEED` | `42` | proxy | yes | no | yes | Seed of the mock backend.<br>Integer. |
| `TRUTHTABLE_JEV_MOCK_LATENCY` | `40ms` | proxy | yes | no | yes | Simulated mock latency. Faults are e2e-only, through mock-upstream.<br>Duration (Go syntax, e.g. `30s`, `5m`). |

## Budgets, Studio

Daily spend caps, prices and Studio limits.

| Key | Default | Services | Advanced | Secret | Restart | Description |
|---|---|---|---|---|---|---|
| `TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY` | `2.00` | proxy | no | no | no | Daily fast-check spend cap in USD; seeds the runtime budget. 0 = unlimited. Past the cap, observe/flag routes skip (degraded: budget), fail-open routes pass and fail-closed block routes block.<br>Number, at least 0. Also editable while running in dashboard Settings. |
| `TRUTHTABLE_SPEND_CAP_LLM_USD_DAY` | `5.00` | proxy | no | no | no | Daily deep-audit (LLM judge) spend cap in USD; seeds the runtime budget. 0 = unlimited. The cap is hard in every lane.<br>Number, at least 0. Also editable while running in dashboard Settings. |
| `TRUTHTABLE_SPEND_ENFORCING_RESERVE` | `0.25` | proxy | yes | no | yes | Share of the decision cap reserved for enforcing routes: observe and flag routes stop at 75% of the cap.<br>Number, from 0 to 1. |
| `TRUTHTABLE_PRICE_TABLE_FILE` | *empty*<br>empty uses the embedded contracts/pricing/prices.v1.json | proxy, engine | yes | no | yes | Price table override (schema pricing.v1).<br>File path. |
| `TRUTHTABLE_STUDIO_CONFIRM_ABOVE_USD` | `0.10` | proxy | yes | no | yes | Studio asks for confirmation above this estimated cost.<br>Number, at least 0. |
| `TRUTHTABLE_DRYRUN_MAX_SAMPLES` | `200` | proxy | yes | no | yes | Largest Studio dry-run sample.<br>Integer, at least 1. |

## Engine: judge

The LLM judge used by the deep audit.

| Key | Default | Services | Advanced | Secret | Restart | Description |
|---|---|---|---|---|---|---|
| `LLM_PROVIDER` | `auto` | engine | no | no | no | Deep-audit judge. auto: explicit > Anthropic key > OpenRouter key > OpenAI key > Ollama (if reachable) > none; mock in demo mode.<br>One of `auto`, `anthropic`, `openrouter`, `openai`, `ollama`, `mock`, `none`. Also editable while running in dashboard Settings. |
| `LLM_MODEL` | *empty*<br>per provider: anthropic claude-haiku-4-5, openrouter `OPENROUTER_JUDGE_MODEL`, ollama `OLLAMA_FALLBACK_MODEL` | engine | yes | no | no | Judge model.<br>Text. Also editable while running in dashboard Settings. |
| `LLM_FALLBACK` | `none` | engine | yes | no | no | Explicit opt-in fallback judge; reported, never hidden.<br>One of `none`, `ollama`. Also editable while running in dashboard Settings. |
| `ANTHROPIC_API_KEY` | *empty* | engine | no | yes | yes | Anthropic key for the judge.<br>Text. |
| `OPENAI_API_KEY` | *empty* | engine | yes | yes | yes | OpenAI key for the judge.<br>Text. |
| `OPENROUTER_JUDGE_MODEL` | `anthropic/claude-haiku-4.5` | engine | yes | no | yes | Judge model when the judge runs through OpenRouter.<br>Text. |
| `OLLAMA_FALLBACK_MODEL` | `llama3.2:1b` | engine | yes | no | yes | Ollama judge model.<br>Text. |
| `MOCK_FIXTURES_PATH` | *empty*<br>empty uses the embedded contracts demo scenarios | engine | yes | no | yes | Mock judge fixtures (`LLM_PROVIDER`=mock).<br>File path. |
| `TRUTHTABLE_JUDGE_MAX_INFLIGHT` | `16` | engine | yes | no | yes | Process-wide concurrent judge calls.<br>Integer, at least 1. |
| `VERIFIER_CONCURRENCY` | `5` | engine | yes | no | yes | Concurrent claim verifications per audit.<br>Integer, at least 1. |
| `TRUTHTABLE_ANTHROPIC_STRUCTURED_OUTPUTS` | `auto` | engine | yes | no | yes | Use Anthropic structured outputs. auto disables them once when the API rejects the parameter.<br>One of `auto`, `on`, `off`. |

## Engine: pipeline and server

Deep-audit pipeline tuning and the engine server.

| Key | Default | Services | Advanced | Secret | Restart | Description |
|---|---|---|---|---|---|---|
| `TRUTHTABLE_VERIFIER`<br>alias `VERIFIER_MODE` | `auto` | engine | yes | no | yes | Claim verifier. auto = llm in 2.0.<br>One of `auto`, `llm`. |
| `TRUTHTABLE_VERIFY_CONTEXT_SCOPE` | `per_claim` | engine | yes | no | yes | Evidence given to each claim verification.<br>One of `per_claim`, `all`. |
| `TRUTHTABLE_AUDIT_EXTRACT_CLAIMS_WITHOUT_EVIDENCE` | `false` | engine | yes | no | yes | Decompose answers even without evidence; the claims come back `NOT_CHECKED`.<br>`true` or `false`. |
| `TRUTHTABLE_SCORING_PARTIAL_WEIGHT` | `0.5` | engine | yes | no | yes | Weight of `PARTIALLY_SUPPORTED` claims in scoring.<br>Number, from 0 to 1. |
| `TRUTHTABLE_SCORING_CONTRADICTION_FLAG` | `0.35` | engine | yes | no | yes | Contradiction threshold for HALLUCINATED.<br>Number, from 0 to 1. |
| `TRUTHTABLE_SCORING_UNSUPPORTED_FLAG` | `0.60` | engine | yes | no | yes | Unsupported threshold for HALLUCINATED.<br>Number, from 0 to 1. |
| `TRUTHTABLE_SCORING_UNSUPPORTED_FRACTION_FLAG` | `0.30` | engine | yes | no | yes | Unsupported-claim fraction for HALLUCINATED.<br>Number, from 0 to 1. |
| `TRUTHTABLE_SCORING_MIN_COVERAGE` | `0.5` | engine | yes | no | yes | Minimum decided-claim coverage for a verdict.<br>Number, from 0 to 1. |
| `TRUTHTABLE_SCORING_MAX_UNKNOWN_FRACTION` | `0.5` | engine | yes | no | yes | UNKNOWN claim fraction at or above which the audit FAILS (judge_unavailable).<br>Number, from 0 to 1. |
| `TRUTHTABLE_SCORING_GRADE_CUTOFFS` | `0.9,0.7,0.5,0.3` | engine | yes | no | yes | trust_score cutoffs for grades A/B/C/D.<br>Comma-separated list. |
| `TRUTHTABLE_UNSUPPORTED_POLICY` | `auto` | engine | yes | no | yes | Whether UNSUPPORTED claims count as hallucination or as unverifiable.<br>One of `auto`, `hallucination`, `unverifiable`. |
| `TRUTHTABLE_MAX_CLAIMS` | `25` | engine | yes | no | yes | Most claims decomposed per answer.<br>Integer, at least 1. |
| `TRUTHTABLE_RETRIEVER_TOP_K` | `3` | engine | yes | no | yes | Knowledge hits per claim (knowledge add-on only).<br>Integer, at least 1. |
| `TRUTHTABLE_RETRIEVER_SCORE_THRESHOLD` | `0.3` | engine | yes | no | yes | Minimum knowledge hit score (knowledge add-on only).<br>Number, from 0 to 1. |
| `QDRANT_URL` | *empty* | engine | yes | no | yes | Vector store URL (knowledge add-on only).<br>URL. |
| `TRUTHTABLE_EMBEDDING_MODEL` | `all-MiniLM-L6-v2` | engine | yes | no | yes | Embedding model (knowledge add-on only).<br>Text. |
| `GRPC_HOST` | `127.0.0.1`<br>compose: `0.0.0.0` | engine | yes | no | yes | Engine gRPC bind host. The engine refuses a non-loopback address without an internal token.<br>Text. |
| `GRPC_PORT` | `50051` | engine | yes | no | yes | Engine gRPC port.<br>Integer, from 1 to 65535. |
| `METRICS_PORT` | `8001` | engine | yes | no | yes | Engine Prometheus metrics port.<br>Integer, from 1 to 65535. |
| `TRUTHTABLE_ENGINE_MAX_RPCS` | `64` | engine | yes | no | yes | Concurrent engine RPCs (C-H13).<br>Integer, at least 1. |
| `TRUTHTABLE_ENGINE_MAX_AUDITS` | `16` | engine | yes | no | yes | Concurrent audits (C-H13); reported to the proxy as max_audits.<br>Integer, at least 1. |
| `LOG_LEVEL` | `INFO` | engine | yes | no | yes | Engine log level.<br>One of `DEBUG`, `INFO`, `WARNING`, `ERROR`. |
| `LANGSMITH_API_KEY` | *empty* | engine | yes | yes | yes | LangSmith tracing key.<br>Text. |
| `LANGSMITH_PROJECT` | `trustagent` | engine | yes | no | yes | LangSmith project.<br>Text. |
| `LANGSMITH_TRACING` | `false` | engine | yes | no | yes | Enable LangSmith tracing (needs `LANGSMITH_API_KEY`).<br>`true` or `false`. |
| `TRUTHTABLE_CONTRACTS_DIR` | `auto` | engine | yes | no | yes | Where the engine reads contracts/. auto walks up to contracts/, then /app/contracts (the image copies it there).<br>File path. |

## Compose, host, dashboard, build

Compose, host ports, the dashboard container and frontend build.

| Key | Default | Services | Advanced | Secret | Restart | Description |
|---|---|---|---|---|---|---|
| `COMPOSE_PROFILES` | *empty* | compose | no | no | yes | Optional compose add-ons.<br>Comma-separated list of `knowledge`, `local-llm`, `observability`, `redis`, `demo`, `load`. |
| `COMPOSE_PROJECT_NAME` | `truthtable` | compose | yes | no | yes | Compose project name.<br>Text. |
| `POSTGRES_PASSWORD` | *none*<br>generated by configure --ensure when .env is new; never changed once the volume exists | compose | yes | yes | yes | Postgres password.<br>Text. |
| `GRAFANA_ADMIN_PASSWORD` | *none*<br>generated by configure --ensure | compose | yes | yes | yes | Grafana admin password (observability profile).<br>Text. |
| `PROXY_HOST_PORT` | `8080` | compose | yes | no | yes | Host port of the proxy.<br>Integer, from 1 to 65535. |
| `DASHBOARD_HOST_PORT` | `3000` | compose | yes | no | yes | Host port of the dashboard.<br>Integer, from 1 to 65535. |
| `POSTGRES_HOST_PORT` | `5432` | compose | yes | no | yes | Host port of Postgres.<br>Integer, from 1 to 65535. |
| `MOCK_UPSTREAM_HOST_PORT` | `18090` | compose | yes | no | yes | Host port of the mock upstream (the onboarding and e2e stacks only; it fakes OpenRouter's sign-in and key endpoints).<br>Integer, from 1 to 65535. |
| `TRUSTAGENT_VERSION` | *none*<br>the contents of VERSION | compose | yes | no | yes | Image tag of the app services.<br>Text. |
| `ENGINE_EXTRAS` | *empty* | compose | yes | no | yes | Engine image build extras; knowledge installs the knowledge-base dependencies.<br>Text. |
| `TRAFFICGEN_API_KEY` | *none*<br>generated by configure --ensure | compose, tools | yes | yes | yes | Ingest key used by the demo traffic generator.<br>Text. |
| `MOCK_UPSTREAM_ADDR` | `:8090`<br>compose: `:8090` | tools | yes | no | yes | Listen address of mock-upstream, the fake of every vendor API the e2e and load stacks use (compose profiles e2e and load). The -addr flag overrides it.<br>Text. |
| `DASHBOARD_API_BASE` | *empty* | dashboard | yes | no | yes | API base written to /config.json. Empty = same origin.<br>URL. |
| `DASHBOARD_WS_URL` | *empty*<br>derived from the page origin | dashboard | yes | no | yes | WebSocket URL written to /config.json.<br>URL. |
| `DASHBOARD_PROXY_UPSTREAM` | `http://proxy:8080` | dashboard | yes | no | yes | nginx proxy_pass target for /api, /v1, /ws and /health.<br>URL. |
| `DASHBOARD_CONNECT_SRC` | *empty* | dashboard | yes | no | yes | Extra CSP connect-src entries.<br>Comma-separated list. |
| `VITE_APP_MODE` | `full` | build | yes | no | rebuild | Frontend build: the full dashboard or the static GitHub Pages site.<br>One of `full`, `static`. |
| `VITE_BASE_PATH` | `/` | build | yes | no | rebuild | Base path of the build (Pages: /TrustAgent/).<br>Text. |
| `VITE_DEMO_DATASET` | `auto` | build | yes | no | rebuild | Replay dataset of the static build.<br>One of `recorded`, `mock`, `auto`. |
| `VITE_REPLAY_SPEED` | `20` | build | yes | no | rebuild | Replay speed multiplier of the static build.<br>Number, at least 0. |
| `VITE_PLAYGROUND_MAX_CALLS` | `100` | build | yes | no | rebuild | Static in-browser playground call cap (Studio drafts included).<br>Integer, at least 0. |
| `VITE_PLAYGROUND_MAX_TOKENS` | `8000` | build | yes | no | rebuild | Static in-browser playground token cap per call.<br>Integer, at least 0. |
| `VITE_JEV_MODEL` | `typesafe/jev-1.13` | build | yes | no | rebuild | Decision model used by the static playground.<br>Text. |
| `VITE_OPENROUTER_JUDGE_MODEL` | `anthropic/claude-haiku-4.5` | build | yes | no | rebuild | Judge and drafter model used by the static site.<br>Text. |
| `VITE_REPO_URL` | `https://github.com/timkush1/TrustAgent` | build | yes | no | rebuild | Repository link.<br>URL. |
