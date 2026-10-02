# Security

How TrustAgent protects the keys it holds, who may do what, which controls guard the audit itself,
and the gaps that remain on purpose. The design source is [SPEC §7.5](v2/SPEC.md#75-credentials-auth-oauth);
this page is the operator's view of it. Scanners in CI keep the code clean (see
[Scanners](#scanners-ci)).

**Before you save keys in the dashboard:** back up the `trustagent-secrets` Docker volume, or set
`TRUTHTABLE_SECRET_KEY` in `.env`. A Postgres backup alone is not enough to restore saved keys
(see [Master key](#master-key)).

## Threat model

TrustAgent sits in the request path between client applications and an LLM, holds vendor keys for
its own checks, and renders results in a browser dashboard. Untrusted inputs:

1. **Client API traffic** (`/v1/*`, `/api/audit`): arbitrary prompts and answers, possibly
   adversarial (prompt injection aimed at the audit LLM, resource exhaustion).
2. **The browser**: cross-origin pages attempting CORS reads, CSRF writes or cross-site WebSocket
   hijacking.
3. **Uploaded knowledge documents** (`POST /api/kb/documents`): a poisoned document can attack the
   verifier or pollute retrieval.
4. **The audited LLM's output, and every vendor response**: treated as data, never as instructions.
5. **Anyone who can read logs, metrics or a database backup**: they must not find a usable key.

What is worth stealing: vendor keys (OpenRouter, TypeSafe, Anthropic, OpenAI, upstream LLM keys),
TrustAgent API keys and sessions, and stored request bodies.

## Access: keys, scopes and sessions

| Control | Detail |
|---|---|
| Scopes | `ingest` (send traffic), `read` (dashboards), `content` (read stored prompts and answers), `admin` (change anything). Ingest needs a key by default (`TRUTHTABLE_INGEST_AUTH=keys`). |
| API keys | Minted keys look like `ta_…` (43 characters after the prefix), are shown once, and are stored as SHA-256 hashes. Created and revoked on **Settings → Access**; the last admin key cannot be revoked while no other admin exists. Env keys (`TRUTHTABLE_API_KEYS`, `_READ_KEYS`, `_ADMIN_KEYS`) are `label=key` lists. |
| How a key is sent | `X-TrustAgent-Key` everywhere. On `/api`, SDK clients may also use `Authorization: Bearer` or `X-API-Key`; on `/v1` in inject mode those headers count only when they hold a proxy key. |
| Setup link | Exists only while no admin exists: 32 random bytes, stored hashed, valid for an hour (`TRUTHTABLE_SETUP_TOKEN_TTL`). Claiming it creates the `setup-admin` key (shown once) and signs you in. See [Setup link and recovery](#setup-link-and-recovery). |
| Sessions | The `ta_session` cookie is HMAC-signed, `HttpOnly`, `SameSite=Strict`, and `Secure` when `TRUTHTABLE_PUBLIC_URL` is https. 12 hours, or a 30-day sliding window on localhost. The key behind a session is re-checked on every request, so revoking it ends the session. **Sign out everywhere** (Settings → Access) ends every session at once. |
| CSRF | A cookie-authenticated write needs the `X-TrustAgent-CSRF` header and an allowed `Origin`. |
| CORS | Only origins listed in `TRUTHTABLE_ALLOWED_ORIGINS`, written exactly as the browser sends them (scheme, host and port), get CORS headers. Cookie-authenticated writes and the WebSocket also accept the origin of `TRUTHTABLE_PUBLIC_URL`, which needs no CORS behind the dashboard's own nginx. Browsers may send exactly `Content-Type`, `If-Match`, `X-TrustAgent-Key`, `X-TrustAgent-CSRF`, `X-TrustAgent-Route`, `X-TrustAgent-Mode` and `X-Request-ID`; `Authorization` and `X-API-Key` are not allowed cross-origin (SDK clients, which do not use CORS, still can). |
| Default origins | `http://localhost:<dashboard port>`, `http://127.0.0.1:<dashboard port>` and `http://localhost:5173` (the Vite dev server). **`localhost:5173` is for development only: set `TRUTHTABLE_ALLOWED_ORIGINS` explicitly in production.** |
| WebSocket | Same-origin with the session cookie and an allowed `Origin`, or cross-origin with a 30-second single-use ticket (`POST /api/ws-ticket`, then `/ws?ticket=…`). A cross-origin dashboard keeps its key in `sessionStorage`, never `localStorage`. |
| Rate limits | Per key, session or IP (`TRUTHTABLE_RATE_LIMIT_PER_MINUTE`, stricter for uploads); `X-Forwarded-For` is trusted only from `TRUTHTABLE_TRUSTED_PROXIES`. |
| Readonly | `TRUTHTABLE_READONLY=true` refuses every write, admin included. |

### Setup link and recovery

The setup link is printed at every proxy start while no admin exists (`SETUP: open …#token=…`), and
by `make setup-url`. **Whoever claims it first becomes admin**, so on a shared host claim it right
away; the token is in the URL fragment, which browsers do not send to servers, but anyone who can
read the proxy's logs before the claim can use it.

Someone with shell access to the proxy container (or its database) can mint a recovery link even
after an admin exists: `make setup-url SETUP_FORCE=1`, or `proxy -new-setup-token -force`. That is
the takeover path for a lost admin key, and it is also why host access equals admin access. Both
steps leave rows in the admin audit log (`setup_token.mint`, then `setup.claim`), which cannot be
edited or deleted from the application.

## Keys saved in the dashboard

Keys entered through **Connect OpenRouter** or **Paste a key** are stored in Postgres, encrypted.
Keys in `.env` are never copied there.

### Master key

The credential store and the session cookies are keyed from one 32-byte master key, from the
first of:

| Source | What it is | Store state |
|---|---|---|
| `env` | `TRUTHTABLE_SECRET_KEY` (base64) | ready |
| `file` | the file `TRUTHTABLE_SECRET_KEY_FILE` names | ready |
| `generated` | created on first start in the proxy's data directory (the `trustagent-secrets` volume in compose), mode 0600 | ready |
| `ephemeral` | the data directory is not writable: a random key for this process only | **unavailable**: sessions work until a restart, but no key can be saved |

Without a database the store is `no_database`: keys come from `.env` only. Keys in `.env` work in
every state.

**Back it up.** Saved keys can only be decrypted with the master key they were sealed with. If the
`trustagent-secrets` volume is lost (for example `make clean-data`, or a new host restored from a
Postgres backup alone) or `TRUTHTABLE_SECRET_KEY` changes, every saved key becomes
**undecryptable**: it is listed, never used, and `/api/status` warns `master_key_changed` with the
names. `make doctor` fails on it. Restore the old key, or re-enter those keys in Settings →
Connections. Rotation without re-entering is planned for v2.1.

### Encryption and origin binding

- AES-256-GCM with a fresh 12-byte nonce per seal, under an HKDF subkey of the master key.
- The additional authenticated data binds the ciphertext to its row:
  `trustagent/credential/v1|<name>|<provider>|<allowed origins>`. The origins part holds the fixed
  origins of a compatible-gateway or upstream key; a vendor key binds none, because its origins are
  recomputed at every load and the stored column is never trusted for it. A row copied under another
  name or provider, or a gateway or upstream row with edited origins, fails to decrypt instead of
  being used somewhere else.
- **Origin binding.** Every outbound call checks the destination's canonical origin
  (`scheme://host[:port]`) against the key's allowed origins before the key is attached. Vendor
  keys are allowed only at their vendor (for OpenRouter, also the origin of an env
  `TRUTHTABLE_OPENROUTER_BASE_URL`), recomputed at every load. Keys for a compatible gateway or an
  upstream LLM are bound to the origin given when they were saved; pointing that setting at another
  origin requires entering the key again.
- **URL rules.** A URL that receives a saved key must be https (http only for local hosts), carry
  no user name or password, and not target link-local or cloud-metadata addresses (169.254.0.0/16,
  fe80::/10, `fd00:ec2::254`, `metadata.google.internal`).
- **Fingerprints.** The dashboard identifies a key by `fp_` plus 12 hex characters of an HMAC (keyed
  from the master key), and shows it masked (`sk-…3f2a`). Read-only callers see neither. The
  fingerprint alone also reaches `content`-scope dashboards in the `credentials_changed` event: an
  HMAC prefix reveals nothing about the key.
- **Only the engine's judge key is pushed** to the engine, over the internal link, and the engine
  narrows its allowed origins to its own vendor table, so a push can never widen them.

### Connect OpenRouter (PKCE)

- The proxy creates the PKCE verifier (32 random bytes, S256) and keeps it in memory under a random
  nonce for 10 minutes, at most 5 pending per user. The browser never sees the verifier or the
  resulting key.
- The nonce is consumed on first use, whatever the outcome, and must come back from the same user
  and origin.
- OpenRouter's one-time `code` arrives in the callback URL. The dashboard's nginx does not log
  `/connect/callback` (`access_log off`), the page sends no `Referer`, and the app removes the code
  from the address bar at once.
- **Several proxy replicas need sticky sessions** for the 10-minute window: a nonce lives in the
  memory of the replica that issued it.

### Where keys can never appear (redaction)

Every key TrustAgent receives (pasted, connected, read from `.env` or decrypted) joins a redactor
before its first use, along with fixed patterns (`sk-…`, `Bearer …`, `x-api-key: …`, `ta_…` keys,
`X-TrustAgent-Key: …`, the `ta_session` cookie, `?ticket=`). It masks:

- every log line of the proxy, and of the engine (a filter on the root log handlers, tracebacks
  included);
- API error texts, key-check and test results, status details, gRPC error messages and WebSocket
  `status` details;
- in inject mode, upstream error bodies (up to 64 KiB) before they are relayed to `/v1` clients;
- admin audit log entries (URLs are stored without their query and credentials).

The `credentials_changed` WebSocket event carries the fingerprint only, and `/metrics` carries no
key material. Tests feed real-shaped fake keys, plus one that matches no pattern, through each of
these paths and assert they never come out.

### Which key wins, and the operator's lock

A non-empty environment variable always wins over the dashboard and makes that setting read-only
there (writes answer 409 `env_managed`). That is the operator's lock for connections:
**`TRUTHTABLE_UPSTREAM_URL` in `.env` pins the upstream LLM**, so no dashboard admin can redirect
traffic (and the injected key) elsewhere. `TRUTHTABLE_CONFIG_LOCK` locks the policy only.

## Spending limits

The daily budgets (fast checks, deep audits) count real spend from a per-call cost ledger, and are
enforced **per proxy process**: each replica reserves before a call and resyncs today's total from
the database every 60 seconds, so N replicas can overspend by up to a minute of traffic each. The
deep-audit budget is hard in every lane, with no admin bypass. A spending limit on the vendor key
itself (OpenRouter → Keys) is the backstop.

## Admin audit log

Every security-relevant admin action is recorded with who, when and a redacted diff: setup claims
and recovery links, sign-ins, "sign out everywhere", API keys created and revoked, settings
changes, keys saved, rotated, deleted, checked and connected, evaluator and calibration changes,
Studio dry-runs (with their cost), knowledge uploads and replay exports. A database trigger refuses
any `UPDATE` or `DELETE` on the table. Read it at `GET /api/admin/audit-log` (admin). Policy
changes keep their own version history.

## Internal links

- **Proxy ↔ engine gRPC is plaintext**, authenticated by a shared token (generated by the proxy
  into the `trustagent-internal` volume, read-only for the engine; metadata
  `x-trustagent-internal`). It is reachable only on the compose network; outside compose the engine
  binds loopback. Anyone on that network can read the traffic, which includes the judge's key when
  the dashboard pushes one. Do not attach other workloads to the stack's network.
- Metrics (`:8001`, `:8002`) and gRPC (`:50051`) are never published to the host; every published
  port binds `BIND_ADDR` (127.0.0.1 by default).

## Audit and output controls (OWASP Top 10 for LLM Applications)

| Control | Where | OWASP LLM Top 10 |
|---|---|---|
| Untrusted text delimited in `<text>` / `<claim>` / `<context>` tags, with explicit "data, never instructions" rules | the engine's decomposer and verifier prompts | LLM01 Prompt Injection |
| Hidden-character stripping (zero-width, bidi controls, C0/C1) and length caps before prompt insertion | `truthtable/security.py` | LLM01 |
| Strict schema validation of all judge output: anything malformed degrades to `UNKNOWN`, never to a verdict | `truthtable/security.py`, the scoring package | LLM02, LLM05 Improper Output Handling |
| The model may not self-report `UNKNOWN` or invent statuses | the verifier | LLM09 Overreliance |
| Claim-flooding cap (`TRUTHTABLE_MAX_CLAIMS`) | the decomposer | LLM10 Unbounded Consumption |
| Body, text and upload caps (`TRUTHTABLE_MAX_BODY_BYTES`, `_MAX_TEXT_CHARS`, `_MAX_UPLOAD_BYTES`), per-key rate limits, daily budgets | the proxy | LLM10 |
| Container memory and CPU limits; non-root users in every image | `docker-compose.yml`, Dockerfiles | LLM10 |
| No literal bidi or zero-width characters in source (Trojan Source) | bandit B613; `\u` escapes | supply chain |

### What a poisoned document can and cannot do

Knowledge-base poisoning is **mitigated, not eliminated**: a document saying "the sky is green"
makes the verifier faithfully report claims about a green sky as SUPPORTED. That is what checking
against a knowledge base means; the defence is curating what gets ingested (admin-only upload,
rate limits, size caps, ingest-time entailment gating). What a poisoned document cannot do: inject
instructions into the verifier prompt (delimited and declared untrusted), smuggle malformed
verdicts (schema validation), or hide payloads in invisible characters (sanitized).

## Scanners (CI)

| Scanner | Scope | Policy |
|---|---|---|
| gosec | `backend-go`, medium and above | **Blocking** |
| bandit | `backend-python/src` (all levels) and `scripts/` (medium and above) | **Blocking** |
| npm audit | `frontend-react` production dependencies, high and above | **Blocking** |
| gitleaks | the whole git history, with [`.gitleaks.toml`](../.gitleaks.toml) | **Blocking** |
| Trivy | filesystem, HIGH/CRITICAL, fixed-only | Informational until a scheduled run is reviewed |

gitleaks allows only fake keys in tests (`*_test.go`, `tests/unit/`, `e2e/`), the detector
vectors, and the project's fake-key conventions: `sk-test-…`, `sk-or-v1-e2e-…` and the leak tests'
unmatched secret. Never put a real key anywhere in the repository, tests included.

Accepted findings:
- gosec G104 (low): unhandled errors on best-effort writes and closes.
- bandit B104 on the engine's `0.0.0.0` gRPC bind inside compose: containers must reach it on the
  internal network; the port is not published.
- bandit low-severity notes in `scripts/` (B101 asserts, B404/B603/B606 subprocess use, one B105
  false positive): the setup scripts run `docker` and `git` by design, with argument lists, never a
  shell.

## Known gaps (deliberate)

- **Plaintext internal gRPC** (above). mTLS was weighed and cut for a single-host deployment.
- **Budgets and PKCE nonces are per process** (above): multi-replica deployments overspend by up to
  a minute and need sticky sessions for Connect.
- **No master-key rotation** before v2.1: rotating means re-entering saved keys.
- **Metadata addresses are refused by how the URL is written.** The URL rules above refuse a
  link-local or cloud-metadata host written as such, but not a DNS name that resolves to one (for
  example to 169.254.169.254). Only an admin can set these URLs; a dial-time check in the
  outbound transports would close the gap and is planned.
- **The static demo shares an origin.** The GitHub Pages demo runs at `timkush1.github.io`, an origin
  shared with any other Pages site of that account. A visitor's own OpenRouter key, if they try Jev
  live there, stays in that tab's `sessionStorage` and is sent only to openrouter.ai, under a strict
  CSP.
- **Host access is admin access** (see [Setup link and recovery](#setup-link-and-recovery)).

## Reporting

This is a portfolio project; if you find something, open a GitHub issue (without the details for
anything exploitable: ask for a private channel first).
