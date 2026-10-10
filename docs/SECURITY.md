# Security

How TrustAgent protects the keys it holds, who may do what, which controls guard the audit itself,
the public static demo, the supply chain, and the gaps that remain on purpose. The design source is [SPEC §7.5](v2/SPEC.md#75-credentials-auth-oauth);
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
  fe80::/10, `fd00:ec2::254`, `metadata.google.internal`). The same addresses are refused again at
  dial time, after DNS, by every outbound client that calls an admin-chosen address (the upstream
  proxy, the decision model and key checks, the OpenRouter key exchange): a host name that
  resolves to 169.254.169.254 is refused before a byte is sent.
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

## The static demo

The public demo (<https://timkush1.github.io/TrustAgent/>) is static files on GitHub Pages: no
TrustAgent server, no cookies, nothing a visitor types is stored by us. Its one sensitive thing is a
visitor's own OpenRouter key, when they choose **Try Jev live** or the Studio's **Draft with AI**.

| Concern | What the site does |
|---|---|
| **Where the key lives** | Only in that tab's `sessionStorage`, with the time it was stored. It is forgotten after **2 hours**, or at once with **Forget key** (which also aborts any request in flight; the call counter stays). Live mode writes nothing to `localStorage`, cookies or IndexedDB. A tab restored from the back-forward cache re-reads storage and applies the 2-hour limit, so it never shows a forgotten key. When the browser blocks storage, the key lives in memory until the page is left, and Connect is disabled |
| **Connect OpenRouter (PKCE) from the browser** | The verifier is kept in `sessionStorage` for **10 minutes**; the callback page removes the `code` from the address bar before anything else, exchanges it once (never twice, even under React's double effects), never shows OpenRouter's error text, and refuses a code this tab did not start (login CSRF). **Paste a key** is always offered |
| **Where the key goes** | Only to `https://openrouter.ai/api/...`, with exactly five request headers (`authorization`, `content-type`, `accept`, `http-referer`, `x-openrouter-title`), with `credentials: 'omit'`, and the site's address in `HTTP-Referer`
(OpenRouter's app attribution). No other origin ever sees it; links that leave the site open with `noopener noreferrer`, so a new tab gets no copy of `sessionStorage` |
| **The shared origin** | Every Pages site of the account shares the origin `https://timkush1.github.io`, and with it `sessionStorage` per tab. A script on another of those sites, opened **in the same tab**, could read the key. That is why the key is per tab, short-lived and forgettable, and why the owner checks that no other site on the origin loads third-party scripts before launch (a custom domain or an organisation site removes the sharing) |
| **Content Security Policy** | A `<meta>` CSP: `default-src 'self'; connect-src 'self' https://openrouter.ai; script-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'none'`, and `referrer` `no-referrer`. Pages cannot send response headers, so there is **no** `frame-ancestors`, HSTS or `X-Frame-Options` beyond what GitHub sets |
| **Framing** | Because a meta CSP cannot forbid framing, the consent dialog, the key field and the callback check `window.top !== window.self`; framed, they show only "Open in its own tab", with no key input and no exchange |
| **Courtesy caps** | At most **100** calls to OpenRouter per tab (checks and AI drafts) and **8,000** estimated tokens per call, reserved before each request. Only calls that bill count; a 402 (no credits), network errors and refusals before sending do not. After **10 refused requests in a row** the panel pauses ("OpenRouter keeps refusing these requests") with Retry and Forget key. These caps keep a mistake small; they are **not a security boundary**, because anyone can call openrouter.ai with their own key. The real limit is the credit limit you give the key on openrouter.ai |
| **What leaves the browser** | What you type and the example documents go to OpenRouter and the model's provider (TypeSafe for checks, Anthropic for drafts). The consent box says so before the first call |
| **The published files** | The Pages build runs gitleaks over the built site and fails on any key-like string; no `VITE_*` build variable may be named like a secret |

**Cross-origin access (CORS).** The browser may read OpenRouter's answers only because OpenRouter
allows it: on 2026-10-09 the preflights from the Pages origin to the decisions, chat, key-exchange
and key endpoints answered with `access-control-allow-origin: *` for those five headers, and error
bodies were readable ([SPEC Appendix D](v2/SPEC.md#appendix-d-assumptions-to-verify-gate)). If
OpenRouter changes that, live mode fails closed with a network error; the replay is unaffected.

**Connect on the Pages address is not verified yet.** Whether OpenRouter accepts the
`/TrustAgent/connect/callback/` address is checked by the owner when Pages is enabled (SPEC
Appendix D). Until then Paste stays available next to Connect, and if OpenRouter refuses the
address, Paste becomes the primary action.

## Supply chain

| Control | What it gives |
|---|---|
| Base images pinned by multi-arch **index digest** (`@sha256:...`) with a version comment | A rebuild cannot pick up a different base silently; Dependabot proposes digest refreshes as reviewable PRs |
| Workflow actions pinned to a full **commit SHA** (with a `# vX.Y.Z` comment), `persist-credentials: false` on every checkout, top-level `permissions: {}` with each job granted only what it needs, no `pull_request_target`, no `${{ github.event.* }}` inside `run:` | A moved tag or a compromised action release cannot change what CI runs; a job holds no write token it does not need |
| `actionlint` and `zizmor` over every workflow (`make lint-workflows`) | Workflow mistakes and the known GitHub Actions attack patterns fail the lint |
| Release images with BuildKit **provenance** (`mode=max`) and an **SBOM** | What each image was built from, and what is inside it |
| A GitHub **artifact attestation** for each pushed image digest | Anyone can check an image came from this repository's workflow: `gh attestation verify oci://ghcr.io/timkush1/truthtable-proxy:<version> -R timkush1/TrustAgent` |
| Pages deploys only from `main` or a `v*` tag, when the owner sets `PAGES_ENABLED`, through the `github-pages` environment; images are pushed only from `main` or a `v*` tag of this repository | A pull request builds the site and the images and publishes nothing |

There is no cosign signature: the GitHub attestation covers provenance for now (see the gaps
below).

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
- **Several gateway replicas share less than one.** Budgets, rate limits without Redis, and PKCE
  nonces are per process (above): multi-replica deployments overspend by up to a minute and need
  sticky sessions for Connect. **The per-name limit on credential checks is also per replica**, so
  N replicas allow N times the checks. A shared nonce store, shared caps and a keyed verify limiter
  are planned for a later release.
- **No master-key rotation** before v2.1: rotating means re-entering saved keys.
- **The static demo shares an origin** with every other Pages site of the account, and its CSP is a
  `<meta>` tag with no `frame-ancestors` (Pages sends no custom headers). Framing is refused in
  the page instead, and the key is per tab, short-lived and forgettable
  ([The static demo](#the-static-demo)).
- **The static demo's caps are a courtesy**, not a security boundary: the key's credit limit on
  openrouter.ai is the real one.
- **Connect on the Pages address is unverified** until the owner's check when Pages is enabled;
  Paste is always offered.
- **No cosign signatures** on release images yet: provenance, SBOMs and GitHub artifact
  attestations only.
- **Host access is admin access** (see [Setup link and recovery](#setup-link-and-recovery)).

## Reporting

This is a portfolio project; if you find something, open a GitHub issue (without the details for
anything exploitable: ask for a private channel first).
