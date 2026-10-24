# P4: implementation plan to the P4 gate (security, credentials, Studio, ROI)

This is the final plan. It is the draft with both critiques applied; §22 lists what was rejected and why. It was checked read-only against these heads:

| Branch | Head | Notes |
|---|---|---|
| `feat/v2-jev-gateway` | `e07c455` | Merge of P3 (int4 @ `d2ff767`), M-Demo gate passed. **P4 cuts from here.** |
| `feat/v2-int4` | `d2ff767` | The gap analyses read `6c6a591`. Since then: `e5b5654` (stack-spec locators), `ee1f405` (Playground `EXAMPLES` moved from `playgroundModel.ts:137` to `:142`), `d2ff767` (docs). |

- File:line references point at `d2ff767`. If a line has moved, find it by symbol.
- Go paths are under `backend-go/` and web paths under `frontend-react/`, unless a path says otherwise.
- P3-PLAN D1–D28 stay binding.
  - D29–D74 keep their draft numbers; the critiques amended some of them.
  - D75–D90 are new.

---

## 0. How the wave runs

**Step 0.0 (owner, optional, does not block).** The localhost PKCE check (§21 #1, D88). Its result only decides which wizard button is primary.

**Step 0a (lead, ≈1.5 d).** Done in this order:
- the branch and the test database;
- the contract PR;
- the Go seams commit.

The Go and Python streams start when 0a is done.

**Step 0b (lead, ≈1 d, while the Go streams run).** The web seams, fixture seeds and msw seeds. The web streams start when 0b is done.

**Twelve streams.**
- Each works on branch `feat/v2-w5-<id>`, cut from `feat/v2-int5` after Step 0a (Go and PY) or 0b (web and DX).
- Each has its own worktree at `~/projects/TrustAgent-w5-<id>`.

| id | Stream | Size | Starts |
|---|---|---|---|
| `gsec` | GSEC: credential store, runtime config, settings, status, keys, redaction | L ≈8.5 d | after 0a |
| `gconn` | GCONN: credentials API, verify, PKCE/connect, hot apply (decision-model swap, upstream keys, engine push), #60, #70, CORS | L ≈7.5 d | after 0a |
| `gcost` | GCOST: calculator, cost vectors, ledger, caps, ROI, pricing | L ≈8 d | after 0a |
| `geval` | GEVAL: Go lint and validate, evaluator writes, labels, calibration store and API, calibration signals | L ≈8 d | after 0a |
| `gstudio` | GSTUDIO: Studio runner and APIs, decision-request, evidence suggestions | L ≈7 d | queue |
| `ggw` | GGW: P3-deferred gateway items (knowledge, Playground streaming, trace writer, #10, `/v1/models`), upstream-body redaction | M ≈5.5 d | queue (cut-line stream) |
| `py` | PY: `ApplyRuntimeConfig`, `DraftEvaluator`, `CostRecord`s, log redaction, contract vectors | M ≈5.5 d | after 0a (slot permitting) |
| `won` | WEB-ON: security plane, cross-origin auth, setup wizard, `/connect/callback`, Settings except Budgets, onboarding spec | L ≈8.5 d | after 0b |
| `wev` | WEB-EV: evaluators plane, label queue, "Was this right?", calibrate and adopt, glass box, trace actions, benchmarks, suggestions | M ≈5.5 d | queue |
| `wst` | WEB-ST: Studio (spec plumbing, editor, test bench, save and watch, dry-run), studio spec | M ≈5 d | queue |
| `wsv` | WEB-SV: Savings, Budgets and prices, banners, simulated badge, `reason`, Playground streaming UI, savings spec | M ≈5 d | queue |
| `dx` | DX: onboarding stack, make, CI, doctor, Grafana, nginx, docs, `configure --with-secrets` | M ≈5.5 d | queue |

**Concurrency (8 GB laptop; WSL showed about 3.2 GB available with swap already in use).**
- **Active streams:** at most **5** stream sessions at once. The lead may allow a 6th only while `free -m` shows at least 2.5 GB available and swap use is not growing.
- **Parking:** a stream that is waiting on a checkpoint commits its work, ends its session, and is restarted by the lead.
- **Start queue:** when a slot frees, the next stream in this order starts:
  1. GSEC, GCONN, GEVAL, GCOST, then WEB-ON (after 0b);
  2. PY;
  3. GSTUDIO (needs CP-LINT and CP-COST);
  4. WEB-ST;
  5. WEB-EV (needs CP-EVAL for live wiring only);
  6. WEB-SV;
  7. DX;
  8. GGW.

**Checkpoints.**
- The lead merges a producer's branch into `feat/v2-int5` once that checkpoint's gate passes.
- Before announcing it, the lead runs the full Step-0 gate (§2.6) on `feat/v2-int5`.
- Consumers then run `git merge feat/v2-int5`. A conflict means an ownership violation, and the work goes back to the owner.

| Checkpoint | Producer, after | Consumers |
|---|---|---|
| **CP-LINT** | GEVAL after EV-1 | GSTUDIO, WEB-ST |
| **CP-COSTV** | GCOST after COST-2 (calculator and frozen cost vectors) | PY (PY-6) |
| **CP-SEC1** | GSEC after SEC-3 | GCONN (CN-7), GCOST (COST-10, COST-11) |
| **CP-COST** | GCOST after COST-5 (ledger wired into the hooks) | GSTUDIO, GCONN (probe cost) |
| **CP-SEC2** | GSEC after SEC-5, **and** GCONN after CN-7 | WEB-ON, DX |
| **CP-SEC3** | GCONN after CN-8 (connect) | WEB-ON (ON-9), GSEC (SEC-8) |
| **CP-EVAL** | GEVAL after EV-5 | WEB-EV |

GSTUDIO has no checkpoint: the studio spec first runs live at I-5.

**Integration (lead):** I-1 to I-8 (§17).

### Rules for every stream

1. **Ownership.** Edit only files you own (§15). New files go only in directories you own, or at paths this plan names.
2. **Frozen after Step 0.** No stream edits these:
   - **Go:**
     - `cmd/proxy/{server,gateway,api,p3_api,notify_hub,harness_test,wiring_test,api_test,e2e_test}.go`;
     - `internal/store/{store,postgres,open,notify}.go` and `internal/store/migrations/`;
     - `internal/metrics/gateway.go`;
     - `internal/decision/vectors/kinds.go`.
   - **Contracts:**
     - `proto/`;
     - `contracts/{schema,config,ui}/**`;
     - `contracts/{superseded.v1.json,validate.mjs,vectors/README.md}`;
     - `contracts/fixtures/grpc/**`.
   - **Web:**
     - `src/types/api.ts`;
     - `src/data/{controlPlane,queries,liveStream,normalize}.ts` and `src/data/live/liveControlPlane.ts`;
     - `src/domain/{presentation,controlPresentation}.ts`;
     - `src/test/{fakeDataSource*,control/handlers.ts,control/fixtures.ts,control/renderPage*}`;
     - `src/routeTree.gen.ts`, `src/routes/app/settings/$section.tsx`, `src/components/settings/sectionComponents.ts`;
     - `src/components/ui/**`: new primitives come only through the lead.
   - **Dependencies:** `frontend-react/package.json` and the lockfile.

   If you need a change in one of these, stop and report it. The lead decides.
3. **Seams.**
   - The signatures Step 0 creates are fixed. Their bodies belong to the file's owner, and hook structs are append-only.
   - The `store.Store` interface is frozen. New storage is new `*PostgresStore` methods in owner files. Handlers depend on narrow interfaces declared in their own files.
4. **Fixtures and vectors.**
   - `contracts/fixtures/api/` is append-only, with files named `<Type>.<case>.json`.
   - Fixtures carry masked values only, in the `config.MaskSecret` form (`sk-…3f2a`).
   - A vector moves `draft → frozen` only when the Go reference passes it.
5. **Copy (D86).**
   - `contracts/ui/**` is frozen. Step 0a seeds every P4 key.
   - Any extra key a stream needs goes into its checkpoint report, and the lead adds it at the next checkpoint merge.
6. **Machine limits.**
   - **The lock.** One heavy command at a time across the whole machine: `flock /tmp/trustagent-heavy.lock <cmd>`.
   - **What counts as heavy:** every `go test`, `go vet ./...`, `npx tsc -b`, `npm run lint`, `npx vitest`, `npm run build`, `npm ci`, `make proto-*`, `make vectors-ts`, `make check`, `poetry run mypy`, and a full `pytest`.
   - **Scope:** keep runs to your own packages: `go test -race -p 2`, `vitest --maxWorkers=2`.
   - **Docker:** none, except `docker compose … config -q` and the lead's shared test Postgres (`truthtable-w5db`, host port 15442).
   - **Proto stubs:** Go streams run `make proto-go` once; PY runs `make proto-python` once; web streams need neither.
7. **Ports and projects.**
   - Never touch the owner's stacks: project `truthtable` or `truthtable-demo`, or ports 3000, 8080 and 5432.
   - The proofs use these projects:

     | Project | Proxy | Dashboard | Postgres | Mock-upstream |
     |---|---|---|---|---|
     | `truthtable-p4proof` | 18080 | 13000 | 15432 | – |
     | `truthtable-p4onb` | 28080 | 23000 | 25432 | 28090 |
     | `truthtable-p4e2e` | 28080 | 23000 | 25432 | – |

     `truthtable-p4e2e` reuses the ports after the onboarding stack is down.
8. **Secrets.**
   - Use fake keys only: `sk-test-…`, `sk-or-v1-e2e-…`.
   - Every leak test also uses one secret that matches no redaction pattern, for example `cmp0e2eZq7Lw4Rt9Yx2Vb6Nm`. The test first asserts that the pattern set alone leaves it unmasked.
   - Never print a secret, even on failure. Assert its absence instead.
9. **Commits.** Conventional commits with the attribution line. No push and no `VERSION` bump.

---

## 1. Lead decisions (binding; D29 onward)

### Security, credentials and settings

| # | Question | Decision | Owner |
|---|---|---|---|
| D29 | Credential names, providers and env vars | The map is below the table.<br>• PUT requires `provider` to match the name.<br>• `base_url` is accepted only for `compatible` and `upstream-*`, and it fixes their allowed origins (D78). For the four vendors it is a 422.<br>• **Env wins:** a non-empty env var shows the row with `source:'env'` (D80 covers engine-only vars), and PUT, DELETE and verify-with-bind return 409 `env_managed`. | GSEC, GCONN |
| D30 | Fingerprint | `fp_` plus the first 12 hex characters of HMAC-SHA256(k_fp, secret), where k_fp = `MasterKey.Derive("trustagent/credentials/v1/fingerprint")`.<br>Go-side env credentials use the same function. The engine never computes a fingerprint.<br>§7.5 erratum. | GSEC |
| D31 | Version, epoch and ETag | **Where they live:** the `settings` row `epoch` holds `{"epoch":"<uuidv7>"}`, inserted once if absent. Its `version` column is the global runtime-config version.<br>**What bumps it:** only writes to `decision_model`, `judge`, `upstream`, `bindings`, `budgets`, `pricing_overrides`, or any credential. `setup` and `session_epoch` do not. The bump happens in the same transaction as the write and its `admin_audit_log` row.<br>**ETag:** `SettingsView.version` = that version, ETag `"s<version>"`. If-Match missing gives 428; stale gives 412 `stale_version`.<br>**Without a database:** a random epoch per process, version 0, and every write answers 503 `persistence_disabled`. | GSEC |
| D32 | Store states | `ready` needs a database and a non-ephemeral master key (`env`, `file` or `generated`).<br>`MasterEphemeralOnly` gives `unavailable`: never seal with an ephemeral key, though sessions still use it. No database gives `no_database`.<br>**Undecryptable rows:** a row is `undecryptable` when its `key_id` differs from the master's, or the GCM open fails. GCM fails for a swapped name, provider or origins, or a tampered `allowed_hosts` (D78). Such a row is listed, never used, and raises the status warning `master_key_changed` with `details.names`.<br>Writes while `unavailable` answer 503 `credentials_unavailable`. Env credentials work in every state. | GSEC |
| D33 | Env precedence and `env_managed` | **Precedence:** built-in < env seed < `settings` row. A non-empty env var always wins (an empty one counts as unset).<br>**Shape:** `SettingsView.env_managed` lists dotted RuntimeSettings paths. A 409 carries `details.fields` (paths) and `details.keys` (registry names).<br>**Path → key map:**<br>• `decision_model.{backend,model,base_url,path}` ← `TRUTHTABLE_JEV_{BACKEND,MODEL,BASE_URL,PATH}`<br>• `judge.{provider,model}` ← `LLM_PROVIDER`, `LLM_MODEL` (via `EngineInfo.env_managed`, D34)<br>• `upstream.default.{url,auth_header}` ← `TRUTHTABLE_UPSTREAM_URL` (alias `UPSTREAM_LLM_URL`), `_UPSTREAM_AUTH_HEADER`<br>• `upstream.anthropic.url` ← `TRUTHTABLE_ANTHROPIC_UPSTREAM_URL`<br>• `upstream.fallback.{url,format}` ← `TRUTHTABLE_FALLBACK_UPSTREAM_{URL,FORMAT}`<br>• `budgets.*` ← `TRUTHTABLE_SPEND_CAP_{DECISION,LLM}_USD_DAY`<br>• `bindings.decision_model` when an env decision credential applies (`jev.go:466-490`), **or** when `decision_model.backend` is env-pinned to `mock`, `off`, or a backend that does not use the bound credential (skip reason `backend_pinned`)<br>• `bindings.judge` when `judge.provider` is env-managed **or** the engine reports `credential_source=env`<br>• `bindings.upstream_default` and `upstream.*.credential` when the matching `*_UPSTREAM_API_KEY` is set<br>• `pricing_overrides` is never env-managed. | GSEC |
| D34 | Engine push | **Proto (lead, additive):**<br>• `Credential.allowed_hosts = 4` (canonical origins, D78);<br>• `EngineInfo.env_managed = 15` (names of engine registry keys set non-empty; names only);<br>• `EngineInfo.runtime_config_epoch = 16`.<br>**What Go pushes:** `{version, epoch, credentials, judge, price_table_json, decision}`.<br>• `credentials` holds only the judge's bound credential.<br>• `price_table_json` = `effectivePriceJSON(snapshot.pricing_overrides)` (D53).<br>• `decision` is unset.<br>**Judge resolution:** the provider is `judge.provider` unless it is `auto`; otherwise the provider of `bindings.judge`; otherwise **no judge** is pushed. The model is `judge.model` or "". `JudgeConfig.base_url` is never set. `mock` and `none` carry no credential.<br>**A push without `judge` reverts** the engine to env resolution: the dashboard provider closes after in-flight calls drain, and `credential_source` becomes `env` or `none`.<br>**Engine side:**<br>• no LLM call on apply;<br>• `judge_status ∈ {configured, unchanged, env_managed, no_judge, invalid}`; `decision_status:"unset"`;<br>• allowed origins are the pushed list **intersected with** the engine's own vendor table plus its env `OPENROUTER_BASE_URL` origin, so a push can never widen them.<br>**An older engine:** UNIMPLEMENTED means it predates P4. Log one warning, then retry at the poll interval.<br>**`DraftEvaluator` errors:** FAILED_PRECONDITION `"no_judge"` → 503 `no_judge`; UNAVAILABLE or deadline → 503 `engine_unavailable`.<br>**Shared fixture:** `contracts/fixtures/grpc/RuntimeConfig.openrouter-judge.json` pins one push. Go asserts it builds exactly this; Python applies it. | lead, GCONN, PY |
| D35 | Registry | `TRUTHTABLE_ANTHROPIC_UPSTREAM_API_KEY` and `_FALLBACK_UPSTREAM_API_KEY` get `runtime:null`.<br>New compose key `MOCK_UPSTREAM_HOST_PORT` (service C, advanced, default 18090).<br>No new `TRUTHTABLE_*` key: the reload poll reuses `TRUTHTABLE_POLICY_RELOAD_INTERVAL`; the PKCE TTL (10 min), per-actor cap (5) and swap grace (30 s) are constants.<br>New `validate.mjs` rule: no `secret:true` key may have `runtime ≠ null`. | lead |
| D36 | DELETE of a bound credential | One transaction:<br>• unbind (`bindings.*` and `upstream.*.credential` set to null);<br>• write the `credential.delete` and `settings.update` audit rows;<br>• bump the version.<br>Then 204, push, and send `credentials_changed{delete}` and `settings_updated`.<br>The D77 confirmation applies first. | GCONN |
| D37 | Re-entering a secret after a URL change | A `decision_model.base_url` or `upstream.*.url` whose **origin** is outside the bound credential's allowed origins → 422 `validation_failed`. The field error has code `reenter_secret` and `details.names:[<credential>]`; the web renders the words.<br>The origins of `compatible` and `upstream-*` change only through PUT with the secret. | GSEC |
| D38 | PKCE (self-hosted) | **#58 start:**<br>• admin and writable; 409 `env_managed` when `OPENROUTER_API_KEY` is set; 503 when the store is not `ready`;<br>• verifier: 32 random bytes in b64url, S256;<br>• nonce: 16 random bytes in b64url, mapped in memory to `{verifier, actor, callback_origin, return_to, expires +10 min}`;<br>• at most 5 pending per actor (the oldest is evicted).<br>• **`callback_origin`** = the request `Origin` when `authz.AllowsOrigin` (`csrf.go:59`) accepts it, else `TRUTHTABLE_PUBLIC_URL`. An `Origin` that is present but not allowed → 409 `conflict` with `details.public_url`.<br>• **`return_to`:** after `path.Clean` it must match `^/app/` and contain no `//`, `\`, `%2f`, `%5c`, `..` or control characters, else 422.<br>• `auth_url` = `<origin of TRUTHTABLE_OPENROUTER_BASE_URL>/auth?callback_url=<callback_origin>/connect/callback&code_challenge=…&code_challenge_method=S256`.<br>• The browser stores `{nonce}` in `sessionStorage["ta.connect.pending"]`.<br>**#59 complete:**<br>• the nonce is consumed on the first call, whatever the outcome;<br>• unknown, expired, another actor's, or a different `Origin` → 400 `invalid_nonce`;<br>• `bind` ⊆ {`decision_model`, `judge`}, default both; anything else → 422;<br>• `POST <base>/api/v1/auth/keys` with the origin asserted, then the shared credential write (D75) with `verify:true`;<br>• the response is `ConnectCompleteResponse` = `CredentialWriteResult & {return_to}`; the web navigates only to the server-echoed `return_to`, through the router.<br>**Replicas:** several replicas need sticky sessions for the 10-minute window (SECURITY.md). | GCONN, WEB-ON |
| D39 | "Try it without a key (simulated checks, $0)" | **PATCH:** only the paths of `{decision_model:{backend:"mock"}, judge:{provider:"mock"}}` that are **not** in `SettingsView.env_managed`.<br>**Type:** `api.ts` adds `'mock'` to `RuntimeSettings.judge.provider`; the engine builds its mock judge.<br>**Labels:** Simulated in status, traces and Savings, plus the top-bar badge (D89).<br>**Never blocks:** simulated checks never block, because mock calibrations load only in demo or test mode (`allowMockCalibration`, `decision_wiring.go:57`). Uncalibrated block-set evaluators are therefore capped by `calibration_missing` (D56). The copy says so. | lead, GSEC, PY, WEB-ON |
| D40 | Verify per provider | Every check asserts the origin through the candidate's KeyFunc.<br>**openrouter:** `GET /api/v1/key` (`jev/keepwarm.go:31`), whose `label`, `limit` and `usage` go into `CredentialVerification`, then `jev.Probe` (`probe.go:48`) on a throwaway client bound to the candidate.<br>**typesafe, compatible:** `jev.Probe`.<br>**anthropic, openai:** `GET https://api.{anthropic,openai}.com/v1/models`.<br>**upstream-\*:** no call; status `unknown`.<br>**Status mapping:** 401/403 → `unauthorized`, 402 → `no_credits`, network/5xx/timeout → `unreachable` (`classify.go:31,281`).<br>**Error text:** cut to 300 characters, then redacted.<br>**Cost:** booked as component `probe`, with `Enforcing:false` (D82).<br>**Rate limit:** #57 `limitBy(1, 10 s)` per credential name. | GCONN |
| D41 | Upstream credentials | **Binding:** `upstream.<name>.credential` binds an `upstream-*` row. Every call asserts the URL origin.<br>**D41b:** `bindings.upstream_default` and `upstream.default.credential` are one field. A PATCH of either sets both; a PATCH where they disagree → 422.<br>**URL rules:** every upstream URL with a bound credential must pass `credentials.CheckURL` (D78).<br>**Rebuild:** the Set is rebuilt atomically on any `upstream.*` change, and `auth.SetInjecting` (`authz.go:332`) follows.<br>**Runtime checks:**<br>• D15 stays a warning.<br>• **The D23 condition becomes a 422:** a fallback on a different host with no upstream-fallback key → 422 on `upstream.fallback.url`, code `fallback_key_required`, `details.host`.<br>• Binding `upstream_default` with no ingest key (neither env `HasIngestKeys`, `authz.go:354`, nor an active `api_keys` row with `ingest`) → 409 `ingest_key_required`. | GSEC, GCONN |
| D42 | Redaction | **Go:**<br>• every secret **received** (#55 body, the PKCE-exchanged key) or **decrypted** joins the process Redactor (`mask.go:117`) before the first outbound call;<br>• `abortAPI`, verify, test and status error text, gRPC `status.Message()` and WS `status.detail` all pass through the Redactor;<br>• `credentials_changed` carries the fingerprint only;<br>• in inject mode, non-2xx upstream bodies up to 64 KiB are redacted before they are relayed to `/v1` clients (GGW).<br>**Python:** a filter on **every root handler** (not on loggers):<br>• redacts `record.getMessage()` and sets `args=()`;<br>• a `Formatter.formatException` override redacts tracebacks;<br>• patterns are Go's (`mask.go:89-96`) plus env and pushed secrets. | GSEC, GCONN, GGW, PY |
| D43 | CORS | Allowed headers become exactly the SPEC list; `Authorization` and `X-API-Key` are dropped (`middleware/cors.go:22-25`). The server still accepts both for SDK clients (`authz/resolve.go:96-101`). CHANGELOG entry. | GCONN, DX |
| D44 | Admin audit log | **Actions:**<br>• `setup.claim`, `setup_token.mint` (CLI and `make setup-url SETUP_FORCE=1`), `session.sign_in` (interactive `POST /api/session` only), `session.revoke_all`;<br>• `api_key.create|revoke`;<br>• `settings.update`;<br>• `credential.put|rotate|delete|verify`, `credential.connect_openrouter`;<br>• `evaluator.create|version|archive`;<br>• `calibration.import|fit|activate|deactivate`;<br>• `studio.dry_run` (with `cost_usd`), `kb.upload`, `export.replay`.<br>Policy writes keep their own version history and are not logged here.<br>**Append-only:** 0005 adds a `BEFORE UPDATE OR DELETE` trigger that raises an error.<br>**Scrubbing:** `InsertAdminAudit` scrubs instead of refusing: `url.Redacted()` on URL-shaped values, then `Redactor.Redact` over the marshalled diff.<br>Writers call the exported `store.InsertAdminAudit` (Step 0). | GSEC, GCONN, GCOST, GEVAL, GSTUDIO |

The D29 map of credential names, providers and env vars:

| `credentials.name` | `CredentialView.provider` | Env var (side) |
|---|---|---|
| `openrouter` | openrouter | `OPENROUTER_API_KEY` (Go) |
| `typesafe` | typesafe | `TYPESAFE_API_KEY` (Go) |
| `anthropic` | anthropic | `ANTHROPIC_API_KEY` (engine; D80) |
| `openai` | openai | `OPENAI_API_KEY` (engine; D80) |
| `compatible` | generic | `TRUTHTABLE_JEV_API_KEY` (`_FILE`) (Go) |
| `upstream-default` | generic | `TRUTHTABLE_UPSTREAM_API_KEY` (Go) |
| `upstream-anthropic` | generic | `TRUTHTABLE_ANTHROPIC_UPSTREAM_API_KEY` (Go) |
| `upstream-fallback` | generic | `TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY` (Go) |

| # | Question | Decision | Owner |
|---|---|---|---|
| D75 | Credential writes and bindings (#55, #57, #59) | **One shared function:** `credentials_service.go` `writeCredential(ctx, CredentialWrite) (CredentialWriteResult, error)`.<br>**Requests:**<br>• #55 body gains `bind?: BindingTarget[]` (⊆ {`decision_model`, `judge`});<br>• #57 body gains `{bind?}`.<br>**Responses:** #55 and #57 return `CredentialWriteResult {credential, stored, bindings:{applied, skipped:[{target, reason:'env_managed'\|'backend_pinned'\|'verification_failed'}]}, verification}`; #59 returns it with `return_to`.<br>**Rules:**<br>• `verify` defaults to true, and `bind` with `verify:false` → 422;<br>• bindings apply only when `verification.ok`, in the same transaction as the credential;<br>• a failed verification on a name that is **currently bound** stores nothing (`stored:false`, `credential` = the old view);<br>• a failed verification on an unbound name stores it unbound (402 → hint `no_credits`);<br>• env-managed targets go to `skipped`.<br>**Effects:** WS `credentials_changed`, plus `settings_updated` when a binding applied; audit row; push when bound.<br>Upstream bindings go only through PATCH (D41).<br>§2.4 errata #55, #57, #59. | lead, GCONN, WEB-ON |
| D76 | Leaving simulated mode | When a bind applies (D75, or a PATCH of `bindings.*`), a persisted `decision_model.backend:"mock"` becomes `"auto"` and `judge.provider:"mock"` becomes `"auto"`, in the same transaction and the same audit diff. The UI says "Simulated checks are now off". | GSEC (`runtimecfg.ApplyBindings`), GCONN, WEB-ON |
| D77 | Changes that would block every answer on fail-closed routes | **Enforcing route:** a current-policy route with `fail: closed` and mode `block` or `fallback`.<br>**#56:** deleting a credential bound to `decision_model` while any enforcing route exists → 409 `confirmation_required` with `details.routes`, unless `?confirm=true`.<br>**#53:** the same rule for `decision_model.backend:"off"`, `bindings.decision_model:null`, and `budgets.decision_usd_day` below today's real decision spend.<br>**Warnings:** `SettingsView.warnings` and `SystemStatus.warnings` gain `fail_closed_unprotected` (`details.routes`) whenever such a route has no usable decision model.<br>**Web:** the dialogs name the routes. | GSEC, GCONN, WEB-ON, WEB-SV |
| D78 | Origin binding | **Matching:** host binding compares **canonical origins** `scheme://host[:port]` (default ports elided, host lowercased), never bare hosts.<br>**KeyFunc:** its string parameter (`jev.go:297`) becomes the origin; the Step 0 seam passes `ep.origin` in place of `ep.host` (`jev/endpoint.go:88`, `http.go:210`). `upstream.Config.Key(ctx, origin)` does the same.<br>**AAD** = `trustagent/credential/v1|<name>|<provider>|<sorted origins joined by ",">`:<br>• **`compatible` and `upstream-*`** use the origins fixed at creation;<br>• **vendor rows** use an empty list. Their allowed origins are recomputed at load (vendor origin ∪ env `TRUTHTABLE_OPENROUTER_BASE_URL` origin for openrouter), and the stored column is never trusted for them.<br>**`CheckURL`:**<br>• https only, with http allowed only for local hosts;<br>• no userinfo;<br>• refuses link-local and metadata targets: 169.254.0.0/16, fe80::/10, `fd00:ec2::254`, `metadata.google.internal`.<br>The column name `allowed_hosts` stays; its values are origins.<br>§7.5 erratum. | lead (seam), GSEC, GCONN, PY |
| D79 | What non-admin callers see | For read-scope and public-read callers:<br>• `/api/settings` secret keys show `value:"set"` or `""`;<br>• no fingerprints;<br>• `dotenv_path:null`;<br>• no `masked` on `/api/status.decision_model.credential`.<br>For every caller, URL-kind values pass through `url.Redacted()` with the query stripped.<br>`CredentialView.masked` is always `config.MaskSecret` (`sk-…3f2a`). | GSEC |
| D80 | Engine-only env credentials | `anthropic` and `openai` set in the engine's env appear as `source:'env'` with `masked:""` and `fingerprint:""`, from `EngineInfo.env_managed`. The web shows "Set in the engine's .env", with Test disabled and the reason "check with `make doctor`".<br>#57 on Go-side env rows verifies and keeps `last_verify_status` in memory, with no `MarkVerified`. | GSEC, GCONN, WEB-ON |
| D81 | Last admin key | #69 → 409 `conflict` (`details.reason:"last_admin_key"`) when revoking it would leave no admin. Revoking is allowed when `HasEnvAdmin()` (`authz.go:359`) is true or another active admin `api_keys` row exists. The Access page warns before revoking the key the current session uses. | GSEC, WEB-ON |

### Costs

| # | Question | Decision | Owner |
|---|---|---|---|
| D45 | Migration `0005_p4.up.sql` | Idempotent, in the 0004 header style:<br>• `cost_events` gains `audit_id TEXT`, plus `hedge`, `late` and `estimated BOOLEAN NOT NULL DEFAULT false`;<br>• `idx_cost_events_trace (trace_id) WHERE trace_id IS NOT NULL`;<br>• `idx_cost_events_audit (audit_id) WHERE audit_id IS NOT NULL`;<br>• `traces.response_chars INT`;<br>• `calibrations_file_entry` UNIQUE `(file_sha256, target_key) WHERE origin='file'` (D55);<br>• `admin_audit_log` append-only trigger (D44).<br>LLM rows link by `audit_id` and are stamped at completion.<br>§2.3 erratum. | lead, GCOST |
| D46 | Simulated spend | `cost_source='simulated'` never counts toward caps or seeding. `SystemStatus.budgets.*_spent_today_usd` is real spend. ROI and Savings include simulated rows per `include_mock`, labelled Simulated. | GCOST, WEB-SV |
| D47 | Judge price for "same checks" | The effective judge (EngineInfo provider and model), priced through the PriceBook. For mock, `free_local` or unknown judges, the Haiku 4.5 list price; the basis text says which. | GCOST |
| D48 | LLM cap | **Rule:** hard in every lane and for every LLM caller, with no admin bypass.<br>**Audits:** `Pool.Submit` reserves an EWMA of settled audit cost (seed ≈ $0.0355 from `assumptions.v1.json`; $0 for a mock or Ollama judge) and settles on the `CostRecord`s. An RPC error with no result settles the estimate on DeadlineExceeded and 0 on Unavailable.<br>**Studio draft:** reserves `(prompt chars/cpt)·p_in + 1600·p_out`.<br>**KB ingest:** refused while the cap is exhausted, then booked afterwards (it may overshoot by one ingest).<br>**Past the cap:**<br>• audits from #15 and #61 → `audit_dropped{reason:"llm_budget"}` and trace flag `llm_budget`;<br>• #15, #34 and KB upload → 503 `budget_exhausted`.<br>**Near the cap:** `AuditOptions.llm_budget_exhausted` is set when the headroom after the reservation is below one estimate.<br>Errata #34, #65. | GCOST, GSTUDIO |
| D49 | WS encoding of the LLM cap | `status{component:"budget"}` carries the worse state of the two caps (`ok < reserve_only < exhausted`). `detail` names the cap: `decision`, `llm` or `decision,llm`. The web refetches `/api/status` on any budget event. §2.5 erratum. | GCOST, WEB-SV |
| D50 | Which spend counts where | ROI `decision_cost` counts only `component='gate'`. Caps and the Budgets bars count every component. | GCOST |
| D51 | Dropped cost rows | `trustagent_gateway_cost_write_dropped_total`, declared in Step 0. Appendix B erratum. | lead, GCOST |
| D52 | Calculator | The pure calculator is `internal/decision/cost.go`, contract-owned (§3.1); `internal/costs` uses it. `PriceEntry` lives there (D90). | GCOST |
| D53 | Pricing precedence and sections | **Precedence:** overrides (label `override`, first match wins) > `TRUTHTABLE_PRICE_TABLE_FILE` (new `config.PriceTableFile`) > contracts; provider match first.<br>**`/api/pricing`:** serves the effective table. The engine gets `effectivePriceJSON(overrides)`, a pure function of the snapshot.<br>**`ValidateOverrides`** refuses $0 for non-local providers and warns when a price is more than 100× the contracts price.<br>**Settings:** keeps 6 sections, and the overrides are a "Prices" panel inside Budgets.<br>**Access:** API keys plus "Sign out everywhere"; there is no sessions list.<br>§3.3 and §7.4 errata. | GCOST, GSEC, WEB-SV, WEB-ON |
| D82 | Decision spend from Studio, dry-run, Playground and probes | **Calls:** these are `CallOptions.Enforcing:false` (`jev.go:146-148`), so they can never spend the enforcing reserve.<br>**Refusal:** #35 and #38 answer 503 `budget_exhausted` when their estimate exceeds the headroom to the observe line (`costHooks.decisionHeadroom(false)`).<br>**409 body:** #38's 409 `confirmation_required` carries `details{estimate_usd, samples, population, decision_budget_left_usd}`. The web's first call never sends `confirm_cost`.<br>**Draft cost label:** `StudioDraftResponse` gains `cost_source`, so a mock draft is labelled Simulated. | GCOST, GSTUDIO, GGW, GCONN, WEB-ST |
| D85 | Cost vectors | At least 8 `contracts/vectors/cost/*.json`, each input carrying `"fn": "call_cost" \| "per_1k" \| "price_lookup"`.<br>**Runners:**<br>• Go runs every `fn`;<br>• Python runs `call_cost` only, because it receives the already-effective table;<br>• TS stays skipped until the P5 landing calculator.<br>`expected` comes from `cmd/vectorgen`.<br>Python's runner is `tests/unit/test_contract_vectors.py` (SPEC §3.2 name).<br>§7.6 erratum. | GCOST, PY, lead |

### Studio, labels and calibration

| # | Question | Decision | Owner |
|---|---|---|---|
| D54 | Validate and lint | **Go is the reference.**<br>• `internal/decision/lint.go` ports `lint.ts` 1:1, with the same codes.<br>• It adds JSON-schema checks of `evaluator.v1` and `evaluator-draft.v1` from `contracts.FS`, with santhosh-tekuri/jsonschema v6 (already in `go.mod`).<br>**Vectors:** `kinds.go` maps `"lint": lintKind`, a variable declared in GEVAL's `vectors/lint.go`. The 19 lint vectors freeze when Go passes.<br>**Registry:** loading runs the hard rules; a version that breaks one is excluded, with a status warning.<br>**Python:** ports only the seven hard-rule codes. | GEVAL, PY |
| D55 | Calibration storage | **File mirror:** file entries are mirrored into `calibrations` with `origin=file` and `created_by=file`. They are unique by `(file_sha256, target_key)` (0005 index), and a mirrored file row is never updated.<br>**A new file row:**<br>• is inserted **inactive** when an import or fit row is active for that `(snapshot, target_kind, target_key)`;<br>• otherwise, in the same transaction, it deactivates the older file row and is inserted active.<br>**Activation:** activating an import or fit row deactivates the active row for that key in the same transaction. Deactivating it re-activates the newest file row.<br>**Lookup:** with a database, active DB rows (NOTIFY through the hub, D84, plus the 30 s poll). Without one, files (D14), and #42-46b answer 503 `persistence_disabled`.<br>**Import** re-derives quality (`DeriveQuality`) and refuses:<br>• unknown versions;<br>• a `calibration_key` mismatch;<br>• `mock/*` snapshots outside demo or test mode. | GEVAL |
| D56 | `calibration_missing` and `calibration_warning` (D28) | **Schema (Step 0):**<br>• `decide-input.v1` gets its own `CalibrationEntryState = CalibrationState \| "other_snapshot"`, leaving `common.CalibrationState` unchanged;<br>• `if state == "other_snapshot" then table absent, else table required`;<br>• optional `fit_state_fields: string[]`;<br>• the `decision.v1` reasons pattern gains `calibration_missing`.<br>**Lookup:** for a target calibrated only on other snapshots, `{state:"other_snapshot"}`.<br>**Reasons:**<br>• an uncalibrated block-set evaluator gives `uncalibrated_snapshot` when every entry is `other_snapshot`, otherwise `calibration_missing`;<br>• differing state fields give `calibration_warning = "calibrated with different state fields"`, and the calibration still applies.<br>**Test (GEVAL, Go and TS):** `decision.v1.calibration_state` never takes the value `other_snapshot`. | lead, GEVAL |
| D57 | Evaluator writes | **#31:** 409 when the id exists anywhere.<br>**#32:** 403 for built-ins; 409 `file_managed` for `origin=file`; 412 when `base_version` is stale.<br>`CONFIG_LOCK` does not block evaluator writes.<br>The registry reloads synchronously before the 201. | GEVAL |
| D58 | Unsaved Studio spec | Compiled as `<id>@<latest+1>`. `studio_results.evaluator_ref = "<id>@draft:<spec_sha256[:12]>"`. Studio never writes `evaluator_versions`. | GSTUDIO |
| D59 | Labels | **Targets and values:** `<id>@<v>` takes `should_fire\|should_not_fire`; `claim:<i>` takes claim verdicts; `response` takes `hallucinated\|grounded` and is reserved.<br>**"Was this right?":** shown when at least one attempt-0 evaluator (shadow included) is at warn or above. It writes one `<ref>` label per such evaluator: Correct → `should_fire`, False alarm → `should_not_fire`.<br>**Queue keys:** C = `should_fire`, F = `should_not_fire`, S = skip.<br>**`labeled_by`:** the session sub or the key label. | GEVAL, WEB-EV |
| D60 | Queue order (#39) | Attempt-0 decisions in the window (default 7 d, limit ≤ 50) with a numeric `risk_raw` and a live body, not yet labelled by the caller. Ordered by `\|risk − warn_at\|` in the route's threshold space, then newest first. §2.4 #39 erratum. | GEVAL |
| D61 | WS for labels and archive | No label event: the web invalidates locally. An archive change sends `evaluator_updated`. | GEVAL, WEB-EV |
| D62 | Adopt flow | **API:**<br>• #45 is used with its existing `activate` flag set to false;<br>• new **#46b** `POST /api/calibration/:id/activate` (admin);<br>• #26 accepts optional `calibration_ids`, which override the active rows in the replay. The web type is `PolicyDryRunRequest.calibration_ids?: number[]` in `controlPlane.ts`, set in Step 0b.<br>**Adopt**, in order:<br>1. fit with `activate:false`;<br>2. Preview impact with `calibration_ids`;<br>3. confirm;<br>4. #46b activate;<br>5. `PUT /api/policies` with `space: calibrated` and the recommended thresholds.<br>§2.4 errata. | lead, GEVAL, WEB-EV |
| D63 | #12 response | **Types:** `DecisionRequestView {requests: JevRequest[], request_sha256: string[], matches_recorded, cpt, mismatch_reason: 'body_truncated'\|'policy_missing'\|'evaluator_missing'\|null}`, with `JevRequest {model, state, questions}`. `session_id` is never returned.<br>**Behaviour:** content scope; 409 `body_expired` without a body; `attempt=1` compiles `final_response_text`; compile shares the gate's code (`gate.CompileRouteOf` and the other Step 0 exports). | lead, GSTUDIO |
| D64 | Evidence suggestions (#51) | Scan at most the 500 most recent bodies with `no_evidence` in the window. Candidates are RE2 patterns of at most 256 characters with flags ⊆ `ims`. `would_gain` counts the bodies that gain evidence. At most 10 suggestions; admin; free. | GSTUDIO |

### P3-deferred items, web, e2e, process and DX

| # | Question | Decision | Owner |
|---|---|---|---|
| D65 | Knowledge on the hot path (G21) | `RetrieveContext` starts together with the upstream call and is joined before the gate within `TRUTHTABLE_KNOWLEDGE_DEADLINE`. A miss means no knowledge and no failure. `KnowledgeOnly` is set.<br>P4 has in-process tests only. `handbook` stays in `policies.pending.yaml`; the Docker `knowledge-only-capped` run moves to P5 (P3-PLAN §13 erratum). | GGW |
| D66 | Async trace writer and LRU (D17) | `internal/store/tracewriter`:<br>• a 10k ordered channel;<br>• flush every 250 ms or 200 rows;<br>• drops whole trace groups; retries row by row;<br>• a 2,000-trace LRU for #11 and #74;<br>• the D83 barrier.<br>Defaults: `async`; e2e stays `sync`. If proof A shows a flake caused by the writer, the lead sets `TRUTHTABLE_TRACE_FLUSH: sync` in `docker-compose.demo.yml` and records it. | GGW, lead |
| D67 | `/v1/models` family (D26) | Anthropic upstream when the request carries `anthropic-version`, or an `x-api-key` that is not a proxy key; otherwise OpenAI. Implemented in `internal/proxy/handler.go:159-160`. | GGW |
| D68 | #10 `reason` | `reason=<code>` → `reasons @> ARRAY[$1]`; `reason=<prefix>:` → prefix match over `unnest(reasons)`. Both are bounded by `since`. #10 erratum. | GGW, WEB-SV |
| D69 | Playground streaming (D12) | Chat mode with `stream:true` streams from the upstream and relays `token` events; `decision` may come before `done`. Audit mode keeps synthesized tokens. With a dashboard upstream credential, chat mode no longer needs the env key; this is tested with a fake Set through `Options.Upstreams`. Playground decision calls are `Enforcing:false` (D82). | GGW, WEB-SV |
| D70 | Proof topology and the onboarding stack | **Which stack runs what:**<br>• studio, savings and the P3 grep run on a demo-shaped stack;<br>• onboarding runs on a fresh stack from `docker-compose.yml + docker-compose.e2e.yml + docker-compose.onboarding.yml`.<br>**The onboarding overlay:**<br>• sets to empty: `TRUTHTABLE_JEV_{BACKEND,API_KEY,BASE_URL,PATH,MODEL}`, `TRUTHTABLE_ADMIN_KEYS` and `TRUTHTABLE_ADMIN_KEY`;<br>• sets `TRUTHTABLE_PUBLIC_READ:"false"` and `TRUTHTABLE_LOG_LEVEL: debug`;<br>• keeps `TRUTHTABLE_OPENROUTER_BASE_URL=http://mock-upstream:8090` and `LLM_PROVIDER=mock`, so the judge binding is skipped as env-managed;<br>• publishes mock-upstream on `${BIND_ADDR:-127.0.0.1}:${MOCK_UPSTREAM_HOST_PORT:-18090}:8090`.<br>**Teardown:** `onboarding-down` runs `down -v`.<br>**Playwright:**<br>• routes `http://mock-upstream:8090/**` through `route.fetch({url: 127.0.0.1:$MOCK_UPSTREAM_HOST_PORT…, maxRedirects: 0})` + `route.fulfill({response})`;<br>• fallback: read `Location` and call `page.goto`;<br>• the 402 case appends `mock_key=nocredits` to `/auth` (mock-upstream hook).<br>**Runs:** serially, with `retries: 0` and `CI=1`, under `env -u TRUSTAGENT_ADMIN_KEY`.<br>§4.2 erratum: the gate grep runs as two invocations. | DX, WEB-ON, GCONN |
| D71 | Cross-origin dashboard | A key sign-in form. The key lives in `sessionStorage["ta.dashboard.key"]` (never localStorage) and is sent as `X-TrustAgent-Key`. Before every WS (re)connect: `POST /api/ws-ticket`, then `?ticket=`. | WEB-ON |
| D72 | Static build | Static Studio stays "Coming soon" until P5. `/connect/callback` is full-build only. Benchmarks renders committed `docs/benchmarks/runs/*.json`, otherwise an empty state. | WEB-ST, WEB-EV, WEB-ON |
| D73 | Savings bands (no `api.ts` change) | **Same-checks band:** `same_checks_usd × [low/usd, high/usd]` of the summary's `llm_same_checks`.<br>**Full-audit band:** the `per_1k` range × `gated/1000`.<br>**Colour:** neutral (DESIGN.md). | WEB-SV |
| D74 | `configure --with-secrets` | `make configure WITH_SECRETS=1` runs `configure.py --with-secrets`: getpass prompts, `.env` written 0600, and no value is ever echoed or logged. §6 #4 and §3.4 errata. | DX |
| D83 | Async writer vs. later readers and writers | **Inside `tracewriter`:** `SaveAudit*` and `SetTraceAudit` first flush that trace's pending rows (a per-trace barrier), then call the inner store synchronously. This keeps "audits persisted before `audit_completed`" (§2.3).<br>**Hook:** Step 0 adds `s.traceBarrier(ctx, traceID) error` (a no-op until GGW sets it).<br>**Callers:** label PUT, #12 and #36 call it before reading through `s.pg`. | GGW, GEVAL, GSTUDIO |
| D84 | Config NOTIFY fan-out | **One listener:** the policy engine's existing listener (`policy/engine.go:623-640`). There is no second `Listen`, because pooled connections are scarce.<br>**Hub:** Step 0 adds `cmd/proxy/notify_hub.go` (`Subscribe(prefix, fn) (cancel)`, `Dispatch(payload)`), created before `newPolicyServices`. The engine's `OnNotify` calls `Dispatch`.<br>**Subscribers:** `evaluator` (`policy_wiring.go:150`), `settings:` and `credentials:` (GSEC), `calibrations:` (GEVAL). | lead, GSEC, GEVAL |
| D86 | Copy and words | **Ownership:** `contracts/ui/**` is frozen after Step 0a, and Step 0a seeds every P4 key. Words modules are prefixed (`sec*`, `cost*`, `eval*`, `studio*`), so `presentation.ts`'s `export *` cannot collide.<br>**Go messages:** short text plus `code` and `details.names`; the web renders the words by code.<br>**Plain language:**<br>• "Try it without a key (simulated checks, $0)";<br>• "budget" everywhere (`banners.llm_exhausted`: "Deep audits paused: today's deep-audit budget is used up");<br>• the credit prerequisite is $5, matching §6 (`setup.step_connect.no_credits` changes from "$1+"). | lead, all web |
| D87 | Onboarding UX | **Step 2:**<br>• a prerequisite line: "You need an OpenRouter account with about $5 of credit (≈1,600 answers at default settings, estimate)", linking to openrouter.ai/settings/credits;<br>• a no-credits state with "I added credits — check again" (#57 with `bind`);<br>• a no-limit callout when `verification.limit_usd` is null.<br>**Step 3** is disabled with a reason until a decision model is bound or simulated mode is on.<br>**Ingest key:** minted in step 3 or in Connect your app, only when no active ingest key exists.<br>**Click budget:** at most 8 user actions from the setup URL to "Ready".<br>**Time:** the five-minute target is measured in P5 (with GHCR images). | WEB-ON |
| D88 | Connect primary path | Connect OpenRouter is the primary button; Paste is always visible. If the owner's localhost check (§21 #1) fails, the lead flips the `CONNECT_PRIMARY` constant in `StepConnect.tsx` to paste at I-2 and records the result in `JEV-ACCESS.md` and SPEC Appendix D. | WEB-ON, lead |
| D89 | Simulated badge | When `decision_model.backend=mock` and the stack is not in demo mode, the top bar shows a persistent "Simulated checks" badge next to the demo and readonly badges. | WEB-SV |
| D90 | Import graph and placement | **`PriceEntry`:** declared (with the `api.ts` JSON tags) in Step 0's stub `internal/decision/cost.go`. `runtimecfg` and `costs` both import `decision`, and **`costs` never imports `runtimecfg`**: `initCosts` adapts `Manager.Subscribe` into a `func(budgets, overrides)` callback.<br>**File-level ownership inside shared packages:**<br>• `internal/credentials/{verify,pkce}.go` and `internal/runtimecfg/push.go` belong to GCONN;<br>• the rest of those packages belongs to GSEC.<br>**Gate check:** `go list -deps ./internal/costs \| grep -c internal/runtimecfg` prints 0. | lead |

---

## 2. Step 0 (lead)

### 2.0 Owner (optional, does not block)

Ask the owner for the 5-minute localhost PKCE check in §21 #1. Use the answer at I-2 (D88).

### 2.1 Step 0a: branch and test database

1. Create the integration branch and its worktree:
   ```bash
   git worktree add ~/projects/TrustAgent-int5 -b feat/v2-int5 feat/v2-jev-gateway   # e07c455
   ```
2. In the int5 worktree, run `python3 scripts/configure.py --ensure`.
   - This creates a fresh `.env` with a random `POSTGRES_PASSWORD`.
   - The int5 worktree never gets a copy of the owner's `.env`, which holds real keys.
3. Start the shared test Postgres:
   ```bash
   COMPOSE_PROJECT_NAME=truthtable-w5db POSTGRES_HOST_PORT=15442 docker compose up -d postgres
   ```
4. Give every stream the literal URL:
   ```
   TEST_DATABASE_URL=postgres://truthtable:<pw from scripts/envfile.py get POSTGRES_PASSWORD>@127.0.0.1:15442/truthtable?sslmode=disable
   ```

> **Substitution recorded at Step 0a (2026-10-01).** Items 3 and 4 were not run. To save memory, the wave uses a
> local PostgreSQL 16 (the pip-packaged `pgserver`, no Docker) instead of the `truthtable-w5db` container. Every
> stream and the lead set `export TEST_DATABASE_URL=$(<session scratchpad>/pg-url.sh)`; the script starts the server
> if needed and prints a pgx URL for the database `truthtable_test` over a Unix socket. `internal/storetest` still
> gives each test its own schema. Read every later mention of `truthtable-w5db` or port 15442 (§0 rule 6, §17 I-4
> and I-8, §18) as this server; there is no container to stop at I-8.

### 2.2 Step 0a: contract PR

**a. Proto** (D34), then `flock … make proto`:
- `Credential.allowed_hosts = 4` (`repeated string`);
- `EngineInfo.env_managed = 15`;
- `EngineInfo.runtime_config_epoch = 16`.

**b. Migration** `0005_p4.up.sql` (D45), with `internal/store/migrate_0005_test.go`. The test pins:
- the new columns;
- both indexes and the file-entry unique index;
- the audit-log trigger, by asserting that UPDATE and DELETE on `admin_audit_log` both fail.

**c. Registry** (D35), then `make config-docs`.

**d. Schemas** (D56), then `make gen-types`.

**e. `contracts/validate.mjs`:**
- accept `fixtures/grpc/*.json`;
- add the secret-runtime rule (D35).

**f. `api.ts`**, then `make gen-api-schema`:
- **Settings and credentials:**
  - `BindingTarget`, `RuntimeSettingsPatch` (deep partial);
  - `CredentialPutRequest {provider, secret, base_url?, verify?, bind?}`, `CredentialVerifyRequest {bind?}`;
  - `CredentialVerification {ok, status, hint, latency_ms, cost_usd, cost_source, key_label?, limit_usd?, limit_remaining_usd?}`;
  - `CredentialWriteResult` (D75), `CredentialList {items}`;
  - `ConnectStartRequest/Response`, `ConnectCompleteRequest`, `ConnectCompleteResponse = CredentialWriteResult & {return_to}`;
  - `WsTicket`;
  - `ApiKeyCreateRequest`, `CreatedApiKey = ApiKeyView & {secret}`, `ApiKeyList {items}`;
  - `'mock'` in `RuntimeSettings.judge.provider`.
- **Evaluators and Studio:**
  - `EvaluatorValidateRequest {spec}`, `EvaluatorValidateResult`;
  - `EvaluatorCreateRequest`, `EvaluatorVersionCreateRequest`, `EvaluatorArchiveRequest`;
  - `StudioDraftRequest`, `StudioDraftResponse.cost_source` (D82);
  - `StudioSamplesRequest`, `StudioSamplesResponse {samples}`.
- **Labels and calibration:**
  - `LabelValue`, `LabelPut`, `LabelDelete`, `LabelQueue {items}`;
  - `CalibrationList {items}`, `CalibrationImportRequest {file, activate}`, `CalibrationImportResult`;
  - `CalibrationFitRequest`, `CalibrationFitResponse`.
- **Evidence and decision request:** `EvidenceSuggestionList {items}`; `JevRequest`, `DecisionRequestView` (D63).

**g. Copy seeds** (`contracts/ui`, D86):
- **Banners and outcomes:**
  - `banners.llm_exhausted`;
  - `outcomes.v1.json` wording for `calibration_missing` and `calibration_warning`.
- **Setup:**
  - `setup.step_connect.{prerequisite, no_credits ($5), check_again, no_limit, simulated, simulated_off, callback_origin_mismatch, callback_expired}`;
    *Erratum (791c3bd):* `callback_origin_mismatch` is not seeded. The 409 that refuses a
    dashboard address carries the proxy's own message (which also covers a gateway with no public
    URL), and a callback from another address is answered as `invalid_nonce`, which reads
    `callback_expired`;
  - `setup.step_try.disabled_reason`.
- **Settings:**
  - `settings.credential_remove_impact`, `settings.env_managed`;
  - `settings.store_unavailable` (fix: set `TRUTHTABLE_SECRET_KEY`), `settings.master_key_changed`;
  - `settings.fail_closed_confirm`, `settings.last_admin_key`.
- **Studio:** `studio.dry_run_confirm`, `studio.dry_run_cancelled`.
- **Labels and benchmarks:** `labels.*` (queue keys and `body_expired`); `evaluators.benchmarks_empty` (exists; check it).
- **Badge:** `badges.simulated_checks`.

**h. `contracts/vectors/README.md`**, final rows:
- decide count;
- lint row "Go `internal/decision/lint.go` (P4)";
- cost row "Go all `fn`; Python `call_cost`; TS P5 (D85)".

**i. Vector runners:**
- `kinds.go` maps `"lint": lintKind` and `"cost": costKind`. Both variables are declared in the new `vectors/lint.go` (GEVAL) and `vectors/cost.go` (GCOST); each starts as a stub that keeps today's Skip text.
- TS `kinds.ts:283-286` texts:
  - `lint`: "Go: internal/decision/lint.go (P4)";
  - `cost`: "TS runner with the landing calculator (P5, D85)";
  - `pkce`: "src/lib/openrouter/pkce.ts (P5)".

**j. Push fixture.** `contracts/fixtures/grpc/RuntimeConfig.openrouter-judge.json` (D34): masked-shape fake key `sk-or-v1-fixture-…`, judge openrouter `anthropic/claude-haiku-4.5`, origins `["https://openrouter.ai"]`.

### 2.3 Step 0a: Go seams commit (no behaviour change except `response_chars`)

**a. `cmd/proxy/server.go`** (frozen afterwards)

1. **Fields.** `serverDeps.redactor`. `server` gains:
   - `redactor`, `engine *grpc.AuditClient`, `notify *notifyHub`;
   - `rt *runtimeParts` (`runtime_wiring.go`, GSEC);
   - `apply *applyParts` (`apply_wiring.go`, GCONN);
   - `costs costHooks` (`costs_wiring.go`, GCOST);
   - `eval *evalParts` (`eval_wiring.go`, GEVAL);
   - `traceBarrier func(ctx, traceID string) error` (GGW sets it in `trace_writer.go`).
2. **`costHooks`.** Every field is a nil-safe no-op until GCOST fills it:
   - `decision jev.CostSink`, `budget jev.Budget`, `prices jev.PriceFunc`;
   - `knownSnapshot string`, `uniformSeed int`;
   - `reserveLLM func(ctx, usd float64, component string) (settle func(actual float64), err error)`;
   - `recordLLM func(ctx, []*pb.CostRecord, CostLink)`;
   - `effectivePriceJSON func(overrides []decision.PriceEntry) []byte`;
   - `decisionHeadroom func(enforcing bool) float64`.

   `CostLink {TraceID, AuditID, Route, KeyLabel, Selection, Attempt}` is declared in `costs_wiring.go`.
3. **Types moved here.** The `apiGuards` type moves here from `policies_api.go:36` and gains `content gin.HandlerFunc` (`s.auth.require(authz.ScopeContent)`).
4. **Init order.**
   - Existing: the pool (`:118`), then `initCalibration` (`:129`).
   - Then `s.notify = newNotifyHub()`, `s.initRuntime(d)` (GSEC) and `s.initCosts(d)` (GCOST).
   - Then `newPolicyServices(…, notify)`.
   - Then `s.initEval(d)` (GEVAL), the gateway, and `s.initApply(d)` (GCONN).
   - The stubs build `runtimecfg.NewStatic(cfg)`, zero hooks and nil readers.
   - `newGateway` receives `gatewayHooks{…, runtime: s.rt.manager, costs: s.costs, retriever: s.knowledgeRetriever(d), redact: s.redact}`. The `knowledgeRetriever` stub is in the new `knowledge_wiring.go` (GGW).
5. **NOTIFY (D84).** `policyServices.notified` becomes a hub subscription for `evaluator`, and the engine's `OnNotify` calls `s.notify.Dispatch`.
6. **Routes.**
   - After `mountP3API`:
     - `s.mountSecurityAPI(api, guards)` (`runtime_wiring.go`: #52 and #53);
     - `s.mountConnectAPI(api, guards)` (`apply_wiring.go`: #54-60 and #70);
     - `s.mountCostAPI(api, guards)` (`costs_wiring.go`: #48);
     - `s.mountEvalAPI(api, guards)` (`eval_wiring.go`: #30-33 and #39-46b);
     - `s.mountStudioAPI(api, guards)` (new `studio_api.go`: #12, #34-38, #51).

     All of them register nothing yet.
   - The upload route becomes `s.handleFileUpload(cfg.MaxUploadBytes)`, a method in `kb_api.go` (GCOST).
7. **Loops.**
   - `start()` calls `startRuntimeLoops`, `startApplyLoops` and `startCostLoops`.
   - `shutdown()` calls `stopCosts(ctx)`.
8. **Redaction.** `func (s *server) redact(string) string` is the identity while `redactor` is nil.

**b. `cmd/proxy/notify_hub.go`** (new; frozen afterwards) per D84, with a unit test.

**c. `cmd/proxy/main.go`** (GSEC afterwards)
- Pass `redactor` into `serverDeps`.
- `d.store, flushTraces := wrapTraceWriter(cfg, d.store, log)`, a stub in the new `trace_writer.go` (GGW). Call the flush on shutdown, after the HTTP drain.

**d. `cmd/proxy/gateway.go`** (frozen afterwards)
- Move the `jev.New` block (`:115-133`) verbatim into `newDecisionClient(cfg, hooks, log)` in the new `decision_client.go` (GCONN). It passes `Costs`, `Budget`, `Prices` and `KnownSnapshot` from the hooks; nil keeps today's behaviour.
- `newUpstreams(cfg, auth, hooks.runtime, log)` returns `*upstreamSource{Current() *upstream.Set}` (GCONN). It is static for now.
- After `gate.New`: `g.SeedUniform(hooks.costs.uniformSeed)`.
- `proxy.Options{Upstreams: ups.Current, Knowledge: hooks.retriever, Redact: hooks.redact, …}`.

**e. `cmd/proxy/api.go`** (frozen afterwards)
- **Moved here:** `codeNoJudge` (from `audits_api.go:28`) and `codeConflict`, `codeFileManaged`, `codeStaleVersion`, `codePreconditionRequired` (from `policies_api.go:25-29`), each deleted at its old site.
- **New codes:** `env_managed`, `ingest_key_required`, `confirmation_required`, `body_expired`, `invalid_nonce`, `budget_exhausted`, `credentials_unavailable`, `decision_model_unavailable`, `engine_unavailable`, `persistence_disabled`. Add each only if it is not already declared.
- `abortAPI` passes `message` through the package variable `apiRedact`, which is the identity until GSEC sets it.

**f. `cmd/proxy/p3_api.go`** (frozen afterwards): `play := s.playgroundHandler()`, defined in `playground_api.go` (GGW).

**g. Hooks in owner files**
- `traces_api.go`: a `labels` hook replaces `Labels: []store.LabelView{}` at `:160`. Its default is `s.traceLabels`, an `eval_wiring.go` stub that returns an empty slice.
- `status_api.go`: `statusReporter.credentials func() statusCredentials`, `.spend func() *spendView` and `.push func() pushState`, used by `system()` when they are set (`:291`, `:305-312`, `:511`).
- `credentials_service.go` (GCONN): `CredentialWrite`, `CredentialWriteResult` and `(s *server) writeCredential(...)`, which returns `errNotImplemented`.
- `internal/upstream/risk.go` (GCONN): `RuntimeRisks(settings runtimecfg.RuntimeSettings, creds runtimecfg.CredentialSet) (fieldErrs []FieldError, warnings []Warning)`, a stub that returns nil. `CredentialSet` is a Step-0 type in `runtimecfg`, so `upstream` imports `runtimecfg` and `runtimecfg` never imports `upstream`.

**h. `cmd/proxy/harness_test.go`** (frozen)
- `envOpts` gains:
  - `pg *store.PostgresStore` (from `storetest`);
  - `mutate func(*config.Config)`;
  - `engine *enginetest.Engine`;
  - `logs *bytes.Buffer` (slog through the redactor);
  - `master *authz.MasterKey`.
- `testEnv` gains `wsFrames(t, n)`.

**i. `internal/grpc`** (GCONN afterwards)
- `client.go` gains `ApplyRuntimeConfig`, `DraftEvaluator` (60 s) and `RetrieveContext` (the knowledge deadline), each a thin wrapper with the internal-token metadata.
- `enginetest.Engine` gains `ApplyRuntimeConfigFunc`, `DraftEvaluatorFunc`, `RetrieveContextFunc`, `IngestDocumentsFunc` and a `Pushes()` recorder.

**j. `internal/proxy/handler.go`** (GGW afterwards)
- `Options.Upstreams func() *upstream.Set`, read per request.
- `Options.Knowledge Retriever`, an interface not used yet.
- `Options.Redact func(string) string`, not used yet.

**k. `internal/gate`** (GGW afterwards)
- Export `CompileRouteOf(r, p, model)`, `CompileInputsOf(x, rules)` and `RouteBudget(base, r, cpt)`; the methods delegate (`gate.go:393-438`).
- `(*Gate).SeedUniform(n)` sets the day's counter (`audit.go:78-101`).

**l. `internal/jev`** (GCOST afterwards, except `swap*`)
- `Config.Prices PriceFunc`; nil uses `priceFor`.
- `EstimateUSD(info, inputTokens) (float64, bool)` wraps `priceFor` (`cost.go:87`).
- **D78 seam:**
  - `endpoint` gains `origin`;
  - `keySource` passes `ep.origin` to `KeyFunc`;
  - the `KeyFunc` comment (`jev.go:294-296`) says "origin".
  - The jev tests are updated in the same commit.

**m. New stubs**
- `internal/decision/cost.go`: `PriceEntry` with the api.ts JSON tags (GCOST afterwards; contract-owned).
- `internal/runtimecfg/runtimecfg.go` (GSEC afterwards):
  - `RuntimeSettings` (JSON tags = `api.ts`);
  - `Snapshot{Version, Epoch, Settings, EnvManaged, Credentials CredentialSet, Undecryptable, Warnings}`;
  - `Manager` with `NewStatic(cfg)`, `Current()`, `Subscribe(fn) (cancel)`, `KeyFunc(name) jev.KeyFunc` and `SetEngineEnvManaged([]string)`;
  - `ApplyBindings(...)`.
- `internal/costs/overrides.go`: `ValidateOverrides([]decision.PriceEntry) []FieldError`, returning nil.

**n. Store, config and metrics**
- `internal/store`:
  - export `InsertAdminAudit(ctx, q, AdminAuditEntry)`, wrapping `authz_keys.go:70`;
  - the trace decision upsert gains `response_chars`, which `sink.go` fills with the rune count of the gated text. This is the one behaviour change in the commit.
- `internal/config/config.go`: `StudioConfirmAboveUSD`, `DryRunMaxSamples` and `PriceTableFile`, bound to their registry keys.
- `internal/metrics/gateway.go`: `cost_write_dropped_total` and `CostWriteDropped()`.

### 2.4 Step 0b: web seams, fixtures, msw (lead, ≈1 d)

1. **Planes.**
   - `src/data/controlPlane.ts`: `ControlPlane extends SecurityPlane, CostPlane, EvaluatorPlane, StudioPlane`, and `PolicyDryRunRequest.calibration_ids?: number[]`.
   - `src/data/control/{securityPlane,costPlane,evaluatorPlane,studioPlane}.ts` declare every method signature for §2.4 rows 6-7, 12, 30-46b, 48, 51-60 and 67-70, including `bind` on #55 and #57.
   - Each file exports `live<X>Plane(http)` and `unavailable<X>Plane()`. Both reject with 404 `not_implemented`.
   - `liveControlPlane.ts` and `unavailableControlPlane` spread them in.
   - Shape tables live in the plane files; `normalize.ts` stays frozen.
2. **Settings plane, done here.** `getSettings` and `patchSettings` (If-Match) get live bodies and msw handlers now, because both WEB-ON and WEB-SV need them.
3. **Queries.**
   - `src/data/queries.ts` re-exports `qkcSecurity`, `qkcCosts`, `qkcEvaluators` and `qkcStudio` from `src/data/queries/*.ts`.
   - `applyControlSideEffects` and `liveStream.ts` call `{security,cost,evaluator,studio}SideEffects(event, qc)`, which are no-ops for now.
4. **Words.** `src/domain/presentation.ts` re-exports `src/domain/words/{security,costs,evaluators,studio}.ts`, which start empty and are prefixed (D86).
5. **Errors.** `src/data/errors.ts` describes every P4 code from §2.3e (WEB-ON owns the file afterwards).
6. **msw.** `src/test/control/handlers.ts` spreads `{security,cost,evaluator,studio}Handlers` from new files (empty arrays), seeded from the fixtures in item 12.
7. **Routes.** Thin routes that render the placeholder until the owner's component lands:
   - `src/routes/connect/callback.tsx`;
   - `src/routes/app/evaluators/labels.tsx` and `benchmarks.tsx`;
   - `studio.tsx` moves to `studio/index.tsx`, and `studio/$id.$version.tsx` is added.

   Then `flock … npm run build` regenerates `routeTree.gen.ts` once, and it is frozen.
8. **Settings sections.** `$section.tsx` renders `SECTION_COMPONENTS[id]` from `components/settings/sectionComponents.ts`, which maps to six `{Connections,Models,Budgets,Access,Addons,AllConfig}Section.tsx` files. Each renders today's placeholder **without reading `planned`**.
9. **Savings tab.** `OverviewPage.tsx` renders `<SavingsPanel/>` from `components/savings/SavingsPanel.tsx` (strip plus placeholder).
10. **AppShell test.** Rewrite `components/layout/AppShell.test.tsx:31-38` to assert the Settings heading and "no dead buttons", with no phase text.
11. **Dependency.** `flock … npm i @codemirror/lang-json`.
12. **Fixture seeds** (`contracts/fixtures/api/`, masked only), then run `make contracts-check` right away:
    - **Settings and access:**
      - `SettingsView.admin`, `.read-scope`, `.env-managed`;
      - `CredentialList.default` (env, engine-env, dashboard and undecryptable rows), `CredentialView.openrouter`;
      - `CredentialWriteResult.bound`, `.judge-env-skipped`, `.not-stored-bound`;
      - `ConnectStartResponse.default`, `ConnectCompleteResponse.ok`, `.no-credits`;
      - `DecisionModelTestResult.ok`, `.no-credits`;
      - `Page-AdminAuditEntry.default`, `ApiKeyList.default`, `CreatedApiKey.default`, `WsTicket.default`;
      - `WsEvent.credentials_changed`, `WsEvent.settings_updated`.
    - **Costs:** `RoiTimeseries.7d`, `PricingTable.default`.
    - **Evaluators and Studio:**
      - `EvaluatorValidateResult.clean`, `.errors`;
      - `StudioDraftResponse.ok`, `StudioTestResponse.six`, `StudioSamplesResponse.flagged`;
      - `DryRunResult.done`, `QuickCalibration.unstable`.
    - **Labels and calibration:**
      - `LabelQueue.default`;
      - `CalibrationList.default`, `CalibrationRecord.provisional`;
      - `CalibrationImportResult.mixed`, `CalibrationFitResponse.provisional`.
    - **Decision request and evidence:** `DecisionRequestView.match`, `.mismatch`; `EvidenceSuggestionList.default`.
    - **Errors:**
      - `ApiErrorBody.env-managed`, `.budget-exhausted`;
      - `.confirmation-required-routes`, `.confirmation-required-dry-run`;
      - `.invalid-nonce`, `.ingest-key-required`, `.last-admin-key`.

### 2.5 Step 0 import-graph check

```bash
test "$(go -C backend-go list -deps ./internal/costs | grep -c internal/runtimecfg)" = 0
```

### 2.6 Step-0 gate (also run on `feat/v2-int5` after every checkpoint merge)

From the repo root, one command after another:

```bash
L="flock /tmp/trustagent-heavy.lock"
test -z "$(gofmt -l backend-go)" && $L go -C backend-go vet ./...
$L env -u TEST_DATABASE_URL go -C backend-go test -race -p 2 ./...
go -C backend-go test ./internal/decision/... -run TestVectors
$L go -C backend-go test -p 1 ./internal/store/... -run 'Migrat'                       # TEST_DATABASE_URL set
$L go -C backend-go test ./cmd/proxy -run 'TestDemoScenariosEndToEnd|TestE2EScenariosAgainstMockUpstream' -count=1
test "$(go -C backend-go list -deps ./internal/costs | grep -c internal/runtimecfg)" = 0
(cd backend-python && $L poetry run pytest tests/unit -q)
$L make check && $L make vectors-ts && $L make test-e2e-static
```

---

## 3. GSEC: credential store, runtime config, settings, status, keys, redaction

- **Branch:** `feat/v2-w5-gsec`.
- **Size:** L, about 8.5 days. It is on the security chain.
- **Read first:**
  - §7.5 (≈2704-2753) and §6 (2516-2600);
  - §2.4 common rules and rows 3-8, 52-53, 67-69 (580-703);
  - §2.5 `credentials_changed` and `settings_updated`;
  - §2.7 loading and precedence (1093-1110);
  - proto `Credential` and `RuntimeConfig`;
  - `api.ts` `SettingsView`, `RuntimeSettings`, `CredentialView` (`:1080-1160`);
  - P3-PLAN D15 and D23;
  - D29-D33, D37, D41, D42, D44, D76-D81, D84, D90.
- **Owns:**
  - `internal/credentials/**` except `verify*` and `pkce*`; `internal/runtimecfg/**` except `push*`; `internal/{authz,config}/**`;
  - `internal/store/{authz_keys,authz_settings,credentials,settings}.go`, with tests;
  - `cmd/proxy/{main,session_api,setup_api,keys_api,cli,internal_token,status_api,runtime_wiring,settings_api,settings_guard,export_api}.go`, with their tests, including `{status_api,internal_token,cli,ws,authz_api,credentials_leak,settings_api}_test.go`.
- **Consumes:** CP-SEC3 for SEC-8.

### Tasks

1. **SEC-1 (M): credentials core.**
   - **`cipher.go`:**
     - `New(master)` uses `Derive(InfoCredentials)` (`masterkey.go:19,51`).
     - `Seal(name, provider, origins, secret)` returns `(ct, nonce[12], key_id)` with the D78 AAD.
     - `Open(row, origins)` returns `ErrUndecryptable`.
   - **`fingerprint.go`:** D30.
   - **`origins.go`:**
     - `Canonical(url)`;
     - `AllowedOrigins(name, provider, baseURL, openRouterBase)`;
     - `CheckURL`, which starts from `jev/endpoint.go:116-140` and adds the D78 refusals.
   - **`state.go`:** D32.
   - **`Masked`:** `config.MaskSecret`.
2. **SEC-2 (M): store.**
   - **`credentials.go`:**
     - `List`, `Get`;
     - `Put(ctx, rec, settingsPatch map[string]json.RawMessage, actor, audit)`: one transaction that upserts the credential, sets `rotated_at` on replace, applies the settings patch, bumps the version (D31), writes the audit rows and runs `NOTIFY credentials:<name>`;
     - `Delete(ctx, name, actor)` with the D36 unbind;
     - `MarkVerified`, `Touch`.
   - **`settings.go`:**
     - `Settings()`;
     - `PatchSettings(ifVersion, rows, actor, diff)` returns `ErrStaleVersion`; it bumps only per D31 and writes the audit row in the same transaction;
     - `RuntimeEpoch()`.
   - **`authz_keys.go`:**
     - `IngestKeyExists` and `ActiveAdminKeyCount`, modelled on `AdminKeyExists` (`:171`);
     - scrubbing in `insertAdminAudit` (D44).
   - **`authz_settings.go`:** `PutSetupToken` (`:60`) writes `setup_token.mint`.
   - **Reuse:** `authz_settings.go:60-89, 197` and `notify.go:30,43`.
3. **SEC-3 (M): `runtimecfg.Manager`.**
   - **Snapshot:** per D33.
     - `EnvManaged` comes from `cfg.Entries()` (`config.go:250`) plus the engine names delivered through `SetEngineEnvManaged`.
     - Credentials are decrypted; undecryptable ones are listed.
     - Vendor origins are recomputed at load (D78).
     - Warnings: master key changed, store unavailable, `fail_closed_unprotected` (D77).
   - **`Reload`** runs at boot, after each local write, on hub subscriptions to `settings:` and `credentials:` (D84), and on a poll every `POLICY_RELOAD_INTERVAL`.
   - **`KeyFunc(name)`** asserts the origin (`ErrHostNotAllowed`) and touches `last_used_at` through a debouncer (`authz.go:441-466`).
   - **Redaction:** `redactor.Add` for every decrypted secret.
   - **`Subscribe`** fans each new snapshot out to subscribers.
   - **`ApplyBindings(settings, targets, envManaged, backendPinned)`** is pure. It returns `(settings, applied, skipped)` and does the D76 flip.

   **→ CP-SEC1.** Frozen from here: `Manager.{Current,Subscribe,KeyFunc,SetEngineEnvManaged}`, `Snapshot`, `ApplyBindings`, and the store `Put` and `Delete` signatures.
4. **SEC-4 (S): redaction wiring and the leak-test skeleton (D42).**
   - `apiRedact = redactor.Redact`.
   - `s.redactErr(err)` for gRPC `status.Message()`.
   - `Redactor.Add` exported for received secrets.
   - `credentials_leak_test.go` starts with the variant that needs no database:
     - an env `TRUTHTABLE_JEV_API_KEY` holding the no-pattern secret;
     - an httptest vendor that echoes it in its body and headers;
     - assert absence in the slog (`logs`), every response body, and the WS frames.

     It grows with each endpoint.
5. **SEC-5 (L): settings #52 and #53 (`settings_api.go`, `settings_guard.go`).**
   - **GET:**
     - `groups` and `keys` come from the registry and `cfg.Entries()`, with D79 masking by caller;
     - engine-only keys take their source from `EngineInfo.env_managed`;
     - `editable` holds only for runtime-settable keys that are not env-managed, with a database and an admin caller;
     - `warnings` = `cfg.Diagnostics` plus the snapshot warnings;
     - `dotenv_path` (null for non-admins);
     - `ETag: "s<v>"`.
   - **PATCH** (admin, writable, bodyLimit):
     - If-Match handling reuses `policies_api.go:28-29,340-345`.
     - 422 field errors for:
       - enums;
       - URLs (`CheckURL`);
       - budgets below 0;
       - overrides (`costs.ValidateOverrides`);
       - a missing or undecryptable credential;
       - `reenter_secret` (D37);
       - D41b disagreement;
       - `upstream.RuntimeRisks` (D41/D23; stub until GCONN fills it).
     - 409 `env_managed` (D33), 409 `ingest_key_required` (D41), 409 `confirmation_required` unless `?confirm=true` (D77, through `settings_guard.go`, reading the current policy from `s.policies`).
     - Persist, Reload, then send WS `settings_updated{version, keys}`. Swap, upstream rebuild, push and ledger caps all follow from `Subscribe` (GCONN and GCOST); this API never calls them.
   - **Errors:** 503 `persistence_disabled` (reuse `keysUnavailable`, `keys_api.go:51`); 403 `readonly`.

   **→ CP-SEC2** (together with GCONN CN-7). Frozen: the #52 and #53 behaviour and fixtures.
6. **SEC-6 (S): status.** In `status_api.go`:
   - `credentials.store` and `undecryptable` come from the Manager (`:291`);
   - the judge's `credential_source` comes from the engine (`:390-415`);
   - the decision credential gains `name`, plus `masked` for admins only (`:481`);
   - push state comes from the `push` hook;
   - the new warnings are passed through.
7. **SEC-7 (S): keys and audit coverage.**
   - #69 last-admin guard (D81).
   - `session.sign_in` in `session_api.go`.
   - `export.replay` in `export_api.go`.
   - The CLI `-new-setup-token` path writes `setup_token.mint`.
8. **SEC-8 (S): leak test complete (after CP-SEC3).**
   - **`credentials_leak_test.go`** with Postgres. It PUTs the no-pattern secret and a `sk-or-v1-e2e-…` key, runs PKCE against an httptest OpenRouter double, and asserts absence in:
     - captured slog;
     - every body of #8, #11, #52-60 and #69;
     - #61 SSE `error` frames;
     - `/metrics`;
     - WS frames;
     - `admin_audit_log`;
     - a `/v1` relay of an upstream 401 that echoes the key (GGW's D42 piece, once merged).
   - **`internal/credentials/leak_test.go`:** cipher, `Open` and origin errors never contain the secret.
   - Also run `TestSessionEpochBump` and `TestNoSecretInLogs` (`internal/authz`).

### Tests

- **`internal/credentials`:**
  - round trip; nonces unique across 10k seals;
  - **swapped AAD:** another name, another provider, **or a changed origins list** → undecryptable;
  - a `key_id` mismatch is undecryptable;
  - an ephemeral master gives `unavailable`;
  - the origins table, including the env OpenRouter base and a port change;
  - `CheckURL` refuses userinfo, http on non-local hosts and link-local addresses;
  - the fingerprint is stable and differs across master keys.
- **`internal/runtimecfg`:**
  - env wins and is listed;
  - `bindings.judge` is env-managed when `LLM_PROVIDER` is set;
  - `backend_pinned`;
  - `ApplyBindings` flips mock to auto (D76);
  - reload after a hub dispatch and after the poll;
  - an undecryptable row is skipped and listed;
  - an origin mismatch is refused for every kind;
  - the redactor masks a loaded secret.
- **Store (`TEST_DATABASE_URL`):**
  - put, rotate, delete with unbind;
  - the version bump scope (a `setup` write does not bump);
  - `ErrStaleVersion`;
  - NOTIFY payloads;
  - scrubbing (a userinfo URL is stored redacted, not refused);
  - the `setup_token.mint` row.
- **API:**
  - every response validates through `contractstest`;
  - ETag round trip, 412 and 428;
  - 409 for `env_managed`, `ingest_key_required` and `confirmation_required`;
  - 422 for `reenter_secret`, D41b and the D23 condition;
  - read-scope GET has no `…` in secret rows and a null `dotenv_path`;
  - 503 for no database and for `unavailable`;
  - readonly gives 403; CSRF;
  - the last-admin 409;
  - SEC-8.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
test -z "$(gofmt -l backend-go)" && $L go -C backend-go vet ./...
$L go -C backend-go test -race -p 2 ./internal/credentials/... ./internal/runtimecfg/... ./internal/authz/... ./internal/config/... ./cmd/proxy/...
$L go -C backend-go test -p 1 -v ./internal/store/... ./internal/credentials/... ./cmd/proxy/... \
   -run 'Credential|Setting|Epoch|AdminAudit|IngestKey|Secret|Leak|LastAdmin' 2>&1 | tee /tmp/gsec.out; ! grep -q -- '--- SKIP' /tmp/gsec.out
make contracts-check config-docs-check
```

---

## 4. GCONN: credentials API, verify, PKCE, hot apply, #60, #70, CORS

- **Branch:** `feat/v2-w5-gconn`.
- **Size:** L, about 7.5 days, including a possible park before CN-7.
- **Read first:**
  - §7.5; §2.4 rows 54-60 and 70;
  - proto `ApplyRuntimeConfig*`;
  - `jev/{http,endpoint,probe,keepwarm,classify}.go`;
  - `internal/upstream/upstream.go`;
  - `grpc/health.go:89-156`;
  - `mockupstream/openrouter.go:278-370`;
  - D34, D36, D38, D40-D43, D75-D78, D80, D82.
- **Owns:**
  - `internal/{upstream,middleware,grpc}/**`;
  - `internal/credentials/{verify,pkce}*.go`, `internal/runtimecfg/push*.go`, `internal/jev/swap*.go`;
  - `internal/store/admin_audit.go`;
  - `cmd/mock-upstream/**`;
  - `cmd/proxy/{decision_client,upstreams,apply_wiring,credentials_service,credentials_api,connect_api,decision_model_api,admin_audit_api}.go`, with tests (`upstreams_test.go` and new files).
- **Consumes:** CP-SEC1 before CN-7. CP-COST for probe cost (a no-op before it).

### Tasks

1. **CN-1 (M): decision-model hot swap.**
   - `jev/swap.go` `Swappable` wraps `atomic.Pointer[Client]` and delegates `Client`, `StatusReporter`, `KeepWarmer`, `CPTSource`, `KeyChecker` and `io.Closer`.
   - `decision_client.go` subscribes to the Manager:
     - A change to `decision_model` or `bindings.decision_model` rebuilds the client with `Binding{Backend, Name, BaseURL, Key: mgr.KeyFunc(name)}`, swaps it in, and closes the old one after 30 s.
     - The rebuild reuses the same `Costs`, `Budget`, `Prices` and `OnSnapshotChange`, and carries the keep-warm state over.
     - A secret-only change relies on `keySource` (`http.go:268-300`) plus an explicit `ResetBreaker`.
2. **CN-2 (M): upstream credentials (D41, D78).**
   - `upstream.Config.Key func(ctx, origin)`, used by `OutboundHeaders` (`upstream.go:176-185`).
   - `upstreamSource` rebuilds the Set atomically on any `upstream.*` change and calls `auth.SetInjecting`.
   - Fill in `RuntimeRisks`: the D23 condition becomes a 422, and D15 stays a warning.
   - Tell GGW the Playground wording.
3. **CN-3 (M): engine push (`runtimecfg/push.go`).**
   - Build `pb.RuntimeConfig` per D34 with `s.costs.effectivePriceJSON(snapshot.pricing_overrides)`, falling back to the contracts table.
   - Push on every snapshot change, with full-jitter backoff from 250 ms to 30 s.
   - Add a `Pinger.OnHealth` hook (`grpc/health.go:89-127`) and re-push on a `(version, epoch)` mismatch.
   - Feed `EngineInfo.env_managed` into `mgr.SetEngineEnvManaged`.
   - UNIMPLEMENTED → one warning, then retry at the poll interval.
   - Expose the push state through the status `push` hook.
4. **CN-4 (M): verify (`credentials/verify.go`, D40).** Uses a throwaway jev client with `Enforcing:false`; reads key info (`label`, `limit`, `usage`); applies the status mapping; truncates and redacts error text; books probe cost.
5. **CN-5 (S): PKCE core (`credentials/pkce.go`, D38).**
   - Verifier, challenge, and the nonce store with actor, callback origin, cap, TTL and single use whatever the outcome.
   - `return_to` validation.
   - mock-upstream: `handleAuthorize` (`openrouter.go:306-333`) records `mock_key` from the query, and `handleAuthKeys` then returns `sk-or-v1-mock-nocredits-<hex>`.
6. **CN-6 (S): #60, #70 and CORS.**
   - **#60** `decision_model_api.go`: admin, writable, `limitBy(1, 10 s)`. Runs `jev.Probe` on the Swappable client with `Enforcing:false` and maps the result to `DecisionModelTestResult`. The noul question is never echoed.
   - **#70** `admin_audit_api.go` with `store/admin_audit.go` `ListAdminAudit(limit, cursor)` (keyset on `id DESC`).
   - **CORS:** D43 in `middleware/cors.go`.

   Park here if CP-SEC1 has not landed.
7. **CN-7 (M): credentials #54-57 and `writeCredential`** (D29, D32, D36, D75-D77, D80).
   - **#54** lists Go-env rows, engine-env rows (D80) and dashboard rows, each with `used_by` and `undecryptable`.
   - **#55 and #57** go through `writeCredential`: `Redactor.Add`, verify, `ApplyBindings`, the store `Put`, WS, audit, push.
   - **#56** applies D36 with the D77 confirmation.
   - #57 has a per-name rate limit.

   **→ CP-SEC2** (with SEC-5).
8. **CN-8 (S): connect #58 and #59 (`connect_api.go`)** on `writeCredential` (D38), with WS and audit `credential.connect_openrouter`.

   **→ CP-SEC3.** Frozen: the #58 and #59 behaviour and fixtures.

### Tests

- **`swap_test.go`:** a swap under concurrent `Decide` with `-race`; the breaker resets on a key change; keep-warm carries over; spend stays continuous across a rebuild.
- **`internal/upstream`:**
  - the injected key comes from the dashboard;
  - the proxy key is never forwarded;
  - an origin change without a new secret is refused;
  - `RuntimeRisks` returns the D23 422.
- **Push:**
  - the built push equals `contracts/fixtures/grpc/RuntimeConfig.openrouter-judge.json`;
  - only the judge credential is sent;
  - a push after the judge is unbound has no judge;
  - re-push after the engine reports version 0 or a new epoch;
  - UNIMPLEMENTED warns once;
  - backoff;
  - an override PATCH shows up in the pushed table (D53).
- **Verify:** mock-upstream keys `nocredits` → `no_credits` and `forbidden` → `unauthorized`; key-info fields are filled; long error text is truncated and redacted.
- **PKCE:**
  - the RFC 7636 App. B vector (`dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk` → `E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM`);
  - a nonce is single use (also after a failed complete), expires, and is bound to its actor and origin;
  - the cap evicts the oldest;
  - every `return_to` attack form gives 422;
  - a `bind` outside the allowed set gives 422;
  - Origin `http://127.0.0.1:3000` yields that `callback_url`.
- **API (Postgres):**
  - PUT with `bind:[decision_model, judge]` and `LLM_PROVIDER` set → decision bound, judge skipped;
  - a failed verify on a bound name gives `stored:false` and the old credential still works;
  - 402 on an unbound name is stored unbound;
  - mock → auto after a bind;
  - a delete with a fail-closed policy gives 409, then `?confirm=true` gives 204, and the next gated request on that route is blocked;
  - engine-env rows;
  - #60 ok, 402 and rate limit; #70 paging; CORS headers exact.
- **In-process PKCE end to end:**
  - start `mockupstream` under httptest, run #58, then `/auth`, then #59;
  - assert Ready, and that a gated request then decides through `openrouter-decisions` on mock-upstream;
  - `TestEnginePushAtBoot`: with a database, the fake engine receives version ≥ 1.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
test -z "$(gofmt -l backend-go)" && $L go -C backend-go vet ./...
$L go -C backend-go test -race -p 2 ./internal/credentials/... ./internal/runtimecfg/... ./internal/upstream/... ./internal/middleware/... \
   ./internal/grpc/... ./internal/jev/... ./cmd/mock-upstream/... ./cmd/proxy/...
$L go -C backend-go test -p 1 -v ./internal/store/... ./cmd/proxy/... -run 'Credential|Connect|Verify|AdminAudit|Push|DecisionModelTest' 2>&1 | tee /tmp/gconn.out; ! grep -q -- '--- SKIP' /tmp/gconn.out
make contracts-check
```

---

## 5. GCOST: calculator, cost vectors, ledger, caps, ROI, pricing

- **Branch:** `feat/v2-w5-gcost`.
- **Size:** L, about 8 days.
- **Read first:**
  - §7.4; §2.9 caller rules (≈1662-1676); §2.3 `cost_events`;
  - §2.4 rows 15, 47-49, 62-65;
  - §2.6 `RoiSummary`, `RoiTimeseries`, `PricingTable`, `SystemStatus.budgets`;
  - §7.6; Appendix B;
  - `design-smart.md` near line 711;
  - D45-D53, D82, D85, D90.
- **Owns:**
  - `internal/{costs,worker}/**`; `internal/jev/**` except `swap*`;
  - `internal/decision/{cost.go,cost_test.go}` and `internal/decision/vectors/cost.go` (contract-owned; the lead reviews at CP-COSTV);
  - `cmd/vectorgen/**`, `contracts/vectors/cost/**`;
  - `internal/store/{audits,stats,costs}.go`;
  - `cmd/proxy/{costs_wiring,stats_api,roi_api,pricing_api,audits_api,kb_api,sink,status_events}.go`, with tests.
- **Consumes:** CP-SEC1 before COST-10 and COST-11. Until then `NewStatic` supplies the env values.

### Tasks

1. **COST-1 (M): the calculator and the PriceBook.**
   - `decision/cost.go`: exact decimals and `cost_source`. Move the arithmetic, `globMatch` and the provider-first loop from `jev/cost.go:67-151`.
   - `costs/prices.go`: the `PriceBook`, swapped atomically, with D53 precedence, `Lookup`, `Effective()` and `EffectiveJSON(overrides)`.
   - `ValidateOverrides` (D53), reusing the strict decoder at `pricing_api.go:38-55`.
   - `jev.Config.Prices` replaces `loadPriceTable` and `priceFor`.
2. **COST-2 (M): cost vectors (D85).**
   - At least 8 inputs, each with `fn`:
     - reported vs computed;
     - precedence override > file > contracts, provider first;
     - Ollama `free_local`, unknown, mock `simulated`;
     - exact decimals;
     - `per_1k` at about $3.0 with the default policy.
   - `cmd/vectorgen` writes `expected`. `vectors/cost.go` declares `costKind`. Freeze them.

   **→ CP-COSTV.**
3. **COST-3 (L): Ledger (`budget.go`, `ledger.go`).**
   - Two budgets (decision and LLM), generalized from `MemoryBudget` (`jev/spend.go:55-199`):
     - an enforcing reserve; a per-key cap;
     - UTC rollover that keeps holds;
     - `CapHitAt(kind)`; `Headroom(enforcing)`;
     - `metrics.SetSpend` for both kinds.
   - Implements `jev.Budget`, `jev.SpendReporter` and `jev.CostSink`.
   - Applies D46.
   - `SetCaps` through a `func(budgets, overrides)` callback that `initCosts` adapts from `Manager.Subscribe` (D90); env wins.
   - `AuditEstimator` (D48).
4. **COST-4 (M): writer and store.**
   - `store/costs.go`: `InsertCostEvents` (pgx.Batch), `SpendToday(dayStartUTC)` (real rows only), `Status24h`.
   - Writer: flush every 250 ms or 200 rows; drain at shutdown; a full buffer drops the row, counts it (D51), and still updates the in-memory spend.
   - `Ledger.Seed()` before serving; resync every 60 s with `max(memory, db)`.
5. **COST-5 (S): wiring (`costs_wiring.go`).**
   - Fill `costHooks` (including `decisionHeadroom` and `effectivePriceJSON`).
   - `knownSnapshot` from `idx_gd_snapshot`; `uniformSeed` from `UniformToday` (`stats.go:635-654`).
   - `status.spend`.
   - `s.pool.SetBudget(ledger.LLM())` and `s.pool.SetCostRecorder(...)`. Both are new methods in `worker`, safe to call before start.
   - `startCostLoops` and `stopCosts`.

   **→ CP-COST.**
6. **COST-6 (M): LLM cap (D48).**
   - `Pool.Submit` (`pool.go:192-223`) reserves, and releases on `ErrQueueFull` or `ErrStopped`. `Pool.run` settles on `res.Costs`.
   - `ErrLLMBudget`, with `DropReason` returning `llm_budget`; `audit_jobs_total{outcome="llm_budget"}`; trace flag `llm_budget`.
   - The near-cap flag.
   - #15 (`audits_api.go:240-252`) returns 503 `budget_exhausted`.
   - `kb_api.go`: refuse while exhausted, book `kb_ingest`, and write the audit row `kb.upload`.
7. **COST-7 (S): `audit_id` link (D45).**
   - In `audits.go:160-176, 399-425` and `worker/result.go`: link by `audit_id`; write `attempt`, `price_table_version` and `key_label`; stamp `occurred_at` at completion.
   - Jev rows write `hedge`, `late` and `estimated`.
8. **COST-8 (M): ROI #47.**
   - **Spend sources:**
     - decision cost from `component='gate'` rows (D50), with Overview aligned (`stats.go:199,233`);
     - the LLM query bounded by `occurred_at` or joined on `idx_cost_events_audit`.
   - **Same checks:**
     - judge price per D47;
     - token-model validation from `measured.json`;
     - `fitted_from_audits` (cut line): bootstrap of 200 resamples with a fixed seed, method order uniform → fitted → static.
   - **Sampling:** `llm_cap_hit_at`.
   - **Never** `shadow_sample`.
9. **COST-9 (M): #48 `roi_api.go`.** Move the ROI handlers there. 422 for disallowed combinations; zero-filled UTC buckets; points with `same_checks_usd`.
10. **COST-10 (S): #49** serves `PriceBook.Effective()`, with overrides first.
11. **COST-11 (S): status, WS and metrics.**
    - LLM transitions (`status_events.go:99-140`, D49).
    - Budgets and the 24 h counters reach status through the `s.status.spend` hook. `budgets()` (`status_api.go:511`) belongs to GSEC and reads that hook.

### Tests

- **`internal/costs`** (`-race`):
  - concurrent reserve and settle stay within the cap plus one estimate;
  - observe stops at 75%, enforcing calls reach 100%;
  - `Headroom(false)`;
  - per-key cap; rollover with holds in flight;
  - precedence;
  - writer flush, drain and drop;
  - seeding after a restart (`storetest`);
  - simulated rows are excluded.
- **Proxy wiring:** a hedged request writes N rows; a late attempt writes a row; the rows' sum equals the increase in `jev_cost_usd_total`.
- **Worker:**
  - every lane is refused past the cap;
  - over- and under-estimates reconcile;
  - a full queue releases its hold;
  - the near-cap flag is set;
  - `audit_dropped.reason=llm_budget`.
- **#15:** 503 `budget_exhausted`, and the trace is dropped.
- **ROI:** hedge losers; method order; a deterministic bootstrap; the validated and not-validated basis; the weakest label wins.
- **#48:** 422 combinations, zero-fill, `include_mock`.
- **#49:** the override is listed first.
- **Status events:** LLM transitions.
- **Fixtures:** validate against the schema.
- **Import graph** (§2.5).

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
test -z "$(gofmt -l backend-go)" && $L go -C backend-go vet ./...
$L go -C backend-go test -race -p 2 ./internal/costs/... ./internal/jev/... ./internal/worker/... ./internal/decision/... ./cmd/proxy/... ./cmd/vectorgen/...
go -C backend-go test ./internal/decision/... -run TestVectors
$L go -C backend-go test -p 1 -v ./internal/store/... ./internal/costs/... ./cmd/proxy/... -run 'Cost|Spend|Budget|Roi|Pricing|Seed' 2>&1 | tee /tmp/gcost.out; ! grep -q -- '--- SKIP' /tmp/gcost.out
test "$(go -C backend-go list -deps ./internal/costs | grep -c internal/runtimecfg)" = 0
make contracts-check
```

---

## 6. GEVAL: lint and validate, evaluator writes, labels, calibration store and API, signals

- **Branch:** `feat/v2-w5-geval`.
- **Size:** L, about 8 days.
- **Read first:**
  - §2.8 hard rules and lints (1300-1537); §2.11 (1725-1769); §7.2-7.3;
  - §2.4 rows 17, 26-33, 39-46;
  - P3-PLAN D14 and D28;
  - D44, D54-D57, D59-D62, D83, D84.
- **Owns:**
  - `internal/decision/**` except `cost*` and `vectors/cost.go`, and `src/lib/decision/**` (both contract-owned; the lead reviews at CP-LINT and CP-EVAL);
  - `internal/{evaluators,calibration,policy}/**`;
  - `internal/store/{evaluators,reliability,policies,replay,labels,calibrations}.go`;
  - `cmd/proxy/{evaluators_api,decision_wiring,policy_wiring,policies_api,gate_api,eval_wiring,labels_api,calibration_api}.go`, with tests;
  - `cmd/calibrate/**`, `contracts/vectors/{lint,decide}/**`, `config/{calibration,evaluators}/**`.
- **Consumes:** nothing.

### Tasks

1. **EV-1 (M): Go validate and lint (D54).**
   - `decision/lint.go` `ValidateSpec(doc, LintOptions)`, plus the schema checks.
   - `vectors/lint.go` `lintKind`; freeze the 19 lint vectors.
   - `registry.go:271` `newVersion` runs the hard rules.

   **→ CP-LINT.** Frozen: `ValidateSpec`, `LintOptions`, `Spec.Hashes()`.
2. **EV-2 (M): evaluator writes #30-33 (D57).**
   - `CreateEvaluatorVersion` with a CAS on the latest version; `SetEvaluatorArchived`.
   - A synchronous `registry.Reload` before replying.
   - An archive change emits `evaluator_updated`.
   - Audit rows `evaluator.*`.
   - Remove the duplicate constants at `evaluators_api.go:24-31`.
3. **EV-3 (M): labels #39-41 (D59, D60).**
   - PUT calls `s.traceBarrier` first (D83), then:
     - validates the target and label pair;
     - freezes `sample` from `GetTraceBody` (`traces.go:609`);
     - copies `risk_raw` and `snapshot`;
     - upserts; 409 `body_expired`; 204.
   - DELETE removes the caller's row. The queue follows D60.
   - `traceLabels` fills the traces hook; `ProxyLabels` is filled; #17 `human` now returns rows.
4. **EV-4 (L): calibration store and API #42-46b (D55, D62).**
   - `store/calibrations.go`: insert with the swap; activate and deactivate (the file row comes back); list; get; active rows; file mirroring under the 0005 unique index.
   - `internal/calibration`: a DB source, a hub subscription to `calibrations:` (D84) and the 30 s poll; `Change.Imported` feeds `calibration_updated`.
   - `calibration_api.go`:
     - #44 import, re-deriving quality and applying the rejects;
     - #45 fit (422 below 8 labels or 3 per class);
     - #46 deactivate; #46b activate;
     - audit rows `calibration.*`.
   - #26 `calibration_ids` (`policy/dryrun.go:427`, `policies_api.go:80`).
   - `cmd/calibrate --source api` test against httptest (`api.go:61`).
5. **EV-5 (M): calibration signals (D56).**
   - The lookup returns `other_snapshot`.
   - `decide.go` (`:106`, `:169`, `:419-433`, `:519`) and `enums.go:147` gain `calibration_missing`, and `calibration_warning` gets its value.
   - Mirror it in `src/lib/decision/decide.ts`. New decide vectors, frozen on a Go pass.
   - The "never `other_snapshot` in `decision.v1`" test, in Go and TS.

   **→ CP-EVAL.**
6. **EV-6 (S): readiness link.** The `?tab=calibration` links (`policy/readiness.go:436,439`) match WEB-EV's `validateSearch`.

### Tests

- **Lint:** the lint vectors; `builtin-*-is-clean`; `make vectors-ts` stays green; a hard-rule violator is excluded from the registry.
- **Writes:**
  - 201, 403, 409, `file_managed`, 412, 422;
  - readonly; CSRF; schema-valid responses;
  - reload visibility: an immediate PUT `/api/policies` resolves the new ref;
  - concurrent CAS; the immutability trigger.
- **Labels:** pair validation; idempotent upsert; delete; 409 for an expired body; redaction without the content scope; queue order; `TraceDetail.labels`; #17 human.
- **Calibration:**
  - the activate swap is atomic and the unique index holds;
  - deactivating re-activates the file row;
  - a changed file inserts inactive while a fit row is active;
  - DB beats file; the family fallback is still forced to provisional;
  - import re-derives quality;
  - fit with measured, provisional and proxy inputs;
  - WS on fit and on import;
  - #26 with `calibration_ids`.
- **Signals:** decide vectors for missing, other-snapshot and warning, in Go and TS.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
test -z "$(gofmt -l backend-go)" && $L go -C backend-go vet ./...
$L go -C backend-go test -race -p 2 ./internal/decision/... ./internal/evaluators/... ./internal/calibration/... ./internal/policy/... ./cmd/proxy/... ./cmd/calibrate/...
go -C backend-go test ./internal/decision/... -run TestVectors
$L go -C backend-go test -p 1 -v ./internal/store/... ./cmd/proxy/... -run 'Evaluator|Label|Calibration|Reliability|DryRun' 2>&1 | tee /tmp/geval.out; ! grep -q -- '--- SKIP' /tmp/geval.out
$L make vectors-ts && make contracts-check gen-types-check
```

---

## 7. GSTUDIO: Studio runner and APIs, decision-request, evidence suggestions

- **Branch:** `feat/v2-w5-gstudio`.
- **Size:** L, about 7 days.
- **Read first:**
  - §7.3; §2.4 rows 12, 34-38, 51; §2.9 compile and decide; §7.1;
  - `contracts/prompts/evaluator-drafter.v1.json`;
  - `api.ts` `:929-1067`;
  - D48, D58, D63, D64, D82, D83.
- **Owns:**
  - `internal/studio/**`, `cmd/gate-eval/**`;
  - `internal/evidence/suggest*.go`;
  - `internal/store/{decisions_list,studio,suggestions}.go`;
  - `cmd/proxy/{studio_api,decision_request_api,evidence_api}.go`, with tests.
- **Consumes:** CP-LINT and CP-COST. It starts after both in the queue.

### Tasks

1. **ST-1 (L): `internal/studio` runner.**
   - Extract `evaluate`, `call`, `spend` and `estimateUSD` (`cmd/gate-eval/eval.go:40,170,257,333`) into `Runner`.
   - Unsaved specs follow D58.
   - Calls use `ComponentStudioTest`/`DryRun` with `Enforcing:false` (D82).
2. **ST-2 (M): #35, #36, #37.**
   - **#35:**
     - admin, `limitBy(20, time.Minute)`, at most 50 samples;
     - returns `fires` and `agrees`;
     - writes `studio_results` rows;
     - 503 `decision_model_unavailable`; 503 `budget_exhausted` when the estimate exceeds `decisionHeadroom(false)` or on `jev.KindBudget`.
   - **#36:** returns `{samples}`. It calls `traceBarrier` and reuses the predicate at `decisions_list.go:121`.
   - **#37:** pure; wraps `QuickCalibrate`.
3. **ST-3 (M-L): #38 dry-run over SSE.**
   - The `estimate` event first.
   - Before the stream is committed:
     - an estimate above `StudioConfirmAboveUSD` without `confirm_cost` → 409 `confirmation_required` with D82 `details`;
     - an estimate above the observe headroom → 503 `budget_exhausted`.
   - Clamp to `DryRunMaxSamples`.
   - Events: `progress` per batch with a cumulative `cost_usd`, then `done`.
   - Wilson CI; inclusion-weighted extrapolation (labelled an estimate); overlap; agreement with audits.
   - Abort through ctx.
   - Audit row `studio.dry_run` with `cost_usd`.
4. **ST-4 (S-M): #34 draft.**
   - admin, `limitBy(10, time.Minute)`, description at most 2,000 characters.
   - Reserves through `reserveLLM`, then calls `DraftEvaluator`.
   - Parses the draft and runs `ValidateSpec`; `draft` is null when the schema still fails.
   - Records the `studio_draft` cost, with `cost_source` in the response.
   - Never saves or attaches.
5. **ST-5 (M): #12 decision-request (D63).**
   - Calls `traceBarrier`, then recompiles through the gate exports with the recorded `cpt`.
   - Sets `matches_recorded` and `mismatch_reason`.
   - Mounted with `guards.content`.
6. **ST-6 (M): #51 (D64).** `evidence/suggest.go` and `evidence_api.go` (admin).

### Tests

- **Runner:** the hash-mode mock is deterministic; `gate-eval` tests stay green.
- **#35:**
  - the 50-sample cap; 422 on an invalid spec; `agrees`;
  - 503 when over the observe headroom; 503 when the backend is off;
  - rows persisted;
  - calls are `Enforcing:false`.
- **#37:** equals the `quick-*` vector outputs.
- **#38:**
  - SSE event order; 409 with details; abort stops calls;
  - the 200 clamp; the CI matches Wilson;
  - `inclusion_prob < 1`; the audit row is written.
- **#34:**
  - ok; still invalid after the repair;
  - `no_judge` → 503; unreachable → `engine_unavailable`;
  - rate limit; `cost_source`; schema-valid.
- **#12:**
  - a harness round trip with matching hashes;
  - truncated; expired gives 409; `attempt=1`;
  - 403 without the content scope;
  - a trace still pending in the async writer (D83).
- **#51:** a table test per kind; the scan cap; admin only.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
test -z "$(gofmt -l backend-go)" && $L go -C backend-go vet ./...
$L go -C backend-go test -race -p 2 ./internal/studio/... ./internal/evidence/... ./cmd/gate-eval/... ./cmd/proxy/...
$L go -C backend-go test -p 1 -v ./internal/store/... ./cmd/proxy/... -run 'Studio|DecisionRequest|Suggest|Samples' 2>&1 | tee /tmp/gst.out; ! grep -q -- '--- SKIP' /tmp/gst.out
make contracts-check
```

---

## 8. GGW: P3-deferred gateway items (cut-line stream)

- **Branch:** `feat/v2-w5-ggw`.
- **Size:** M, about 5.5 days. GW-1 to GW-3 (about 4 days) are the cut line (§16).
- **Read first:** §7.1; §2.3 writers; §2.4 rows 10, 11, 61, 72, 74; §2.12; P3-PLAN D12, D17, D26; D42, D65-D69, D83.
- **Owns:**
  - `internal/{proxy,llmapi,gate}/**`; `internal/evidence/**` except `suggest*`;
  - `internal/store/{traces,views,outcome}.go` and `internal/store/tracewriter/**`;
  - `cmd/proxy/{traces_api,playground_api,v1_routes,decisions_longpoll_api,knowledge_wiring,trace_writer}.go`, plus `scenarios_e2e_test.go`, `memstore_test.go` and the other tests of these files;
  - `cmd/trafficgen/**`, `config/policies*.yaml`, `contracts/demo/**`, `contracts/README.md`.
- **Consumes:** nothing. GCONN supplies the Playground wording.

### Tasks

1. **GW-1 (M): knowledge on the hot path (D65).**
2. **GW-2 (M): Playground streaming (D69).**
   - `playground.go:160` streams through `stream.go:94`.
   - `playground_api.go:143-190` relays the events, with `s.playgroundHandler()`.
   - Decision calls are `Enforcing:false`.
   - Update the wording at `playground.go:212-213`.
3. **GW-3 (M): trace writer, LRU and barrier (D66, D83).**
   - Decorates the Store through `wrapTraceWriter` and sets `s.traceBarrier`. #11 and #74 read through the LRU first.
   - Fix the stale comments at `store/traces.go:19-20`. GSEC fixes `main.go:84`.
4. **GW-4 (S): #10 `reason` (D68)** in `traces_api.go:128` and `traces.go:494-511`.
5. **GW-5 (S): `/v1/models` family (D67)** in `handler.go:159-160`.
6. **GW-6 (S): D42 upstream-body redaction.** In inject mode, non-2xx upstream bodies up to 64 KiB pass through `Options.Redact` before relay.
7. **GW-7 (S): phase.** `cmd/trafficgen/scenario.go:136` `defaultPhase = "p4"` (there are no p4-tagged scenarios today, so nothing changes).

### Tests

- **Knowledge:** a fake retriever; a deadline miss gives no knowledge and no failure; knowledge-only caps at warn.
- **Streaming:** event order with an httptest SSE upstream; the decision can arrive before done; chat mode works with a fake dashboard-credential Set.
- **Writer:**
  - ordering under concurrency;
  - group drop and the metric;
  - row-by-row retry; a database outage; drain;
  - an audit that completes before the first flush (D83);
  - #11 and #74 from the LRU.
- **#10:** a store test with whole codes and prefixes.
- **`/v1/models`:** both families.
- **Redaction:** a `/v1` upstream 401 that echoes the injected key is relayed without it.
- **Regression:** `TestDemoScenariosEndToEnd` and `TestE2EScenariosAgainstMockUpstream`, parameterised over `TRACE_FLUSH` `sync` and `async`.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
test -z "$(gofmt -l backend-go)" && $L go -C backend-go vet ./...
$L go -C backend-go test -race -p 2 ./internal/proxy/... ./internal/llmapi/... ./internal/gate/... ./internal/evidence/... ./internal/store/tracewriter/... ./cmd/proxy/... ./cmd/trafficgen/...
$L go -C backend-go test ./cmd/proxy -run 'TestDemoScenariosEndToEnd|TestE2EScenariosAgainstMockUpstream' -count=1
$L go -C backend-go test -p 1 ./internal/store/... -run 'Trace|Reason|Writer'
```

---

## 9. PY: runtime config, drafter, `CostRecord`s, redaction, vectors

- **Branch:** `feat/v2-w5-py`.
- **Size:** M, about 5.5 days.
- **Read first:** §3.2 (1935-2001); §7.5 hot reload and redaction; proto `RuntimeConfig` and `DraftEvaluator*`; the drafter prompt and `evaluator-draft.v1`; D34, D39, D42, D48, D54, D78, D85.
- **Owns:** `backend-python/**`. Run `make proto-python` first.
- **Consumes:** CP-COSTV before PY-6.

### Tasks

1. **PY-1 (M): `runtime/state.py` and `manager.py`.**
   - **`RuntimeState`:** accepts a new epoch, or a version above the applied one; anything else is a no-op.
   - **`ProviderManager`:**
     - rebuilds only on a real change;
     - an env key or `LLM_PROVIDER` wins (`env_managed`);
     - never combines a pushed setting with an env credential;
     - allowed origins are intersected with the engine's own table (D34, D78);
     - drains, then closes the old client;
     - the mock judge for provider `mock`;
     - **a push without a judge reverts** to env resolution.
2. **PY-2 (M): servicer.**
   - A `SwappableProvider` under `LimitedProvider`; the graph is built lazily.
   - `ApplyRuntimeConfig` applies the price table and returns the D34 statuses.
   - Fill `HealthResponse` fields 9 and 10, and `EngineInfo` fields 12, 15 and 16.
   - Update `server.py:390` and `test_server_v2.py:55-58,315-334`.
3. **PY-3 (S): redaction (D42).** Handler filters plus `formatException`.
4. **PY-4 (M): drafter.**
   - `studio/drafter.py` with at most one repair turn.
   - The seven hard-rule codes.
   - Costs recorded through `complete_recorded`.
   - No judge → `no_judge`.
   - The mock provider answers with `examples[0].output`, marked simulated. If an example fails its schema, report it to the lead.
5. **PY-5 (S): `IngestDocuments`** returns its `costs` (field 8).
6. **PY-6 (S): `tests/unit/test_contract_vectors.py`.** Runs the cost vectors with `fn=call_cost` (D85) against `providers/pricing.py`.

### Tests

- **`test_runtime_config.py`:**
  - the epoch and version rules;
  - env wins; nothing is ever mixed;
  - rebuild only on a real change; drain then close;
  - a no-judge start gains a judge; mock and none;
  - **revert:** after a push with a judge, a push without one means no further call uses the old fingerprint;
  - the origin intersection (a pushed foreign origin is ignored);
  - the price table is applied;
  - Health and Info report version, epoch and `credential_source`;
  - `env_managed` lists names only;
  - **applies `contracts/fixtures/grpc/RuntimeConfig.openrouter-judge.json`** → `configured`, `dashboard`;
  - **redaction:** a child logger with a no-pattern secret, `logger.exception`, and `logger.debug("%s", proto)` leave nothing in `caplog`.
- **`test_drafter.py`:**
  - a valid first answer; a repair; still invalid after the repair;
  - fenced and invalid JSON; injection text stays inside the data tags;
  - the cost is recorded; the mock fixture; no judge.
- **Elsewhere:** `test_server_v2.py` covers both RPCs; `test_contract_vectors.py`.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
cd backend-python && poetry run ruff check . && poetry run black --check . \
 && $L poetry run mypy src/truthtable/runtime src/truthtable/studio src/truthtable/scoring src/truthtable/text src/truthtable/providers \
 && poetry run pytest tests/unit/test_runtime_config.py tests/unit/test_drafter.py tests/unit/test_contract_vectors.py -q \
 && $L poetry run pytest tests/unit -q
```

---

## 10. WEB-ON: security plane, cross-origin auth, wizard, Settings, onboarding spec

- **Branch:** `feat/v2-w5-won`.
- **Size:** L, about 8.5 days, including a park before ON-9.
- **Read first:** §3.3 setup and Settings (2002-2117); §7.5; §6; `DESIGN.md`; D29-D41, D53, D70, D71, D75-D81, D86-D88.
- **Owns:**
  - `src/components/{setup,settings,common}/**` except `settings/{BudgetsSection.tsx,sectionComponents.ts}`;
  - `src/routes/{app/setup.tsx,connect/**}`;
  - `src/data/{http.ts,errors.ts,live/streamClient.ts}`;
  - `src/data/control/securityPlane.ts`, `src/data/queries/security.ts`, `src/domain/words/security.ts`, `src/domain/snippets.ts`, `src/test/control/securityHandlers.ts`;
  - `src/components/overview/{ConnectYourApp.tsx,overviewModel.ts}`;
  - `e2e/stack/{onboarding.spec.ts,shell.spec.ts,stack.ts}`.
- **Consumes:** CP-SEC2 for live checks; CP-SEC3 before ON-9. Everything before that runs on the msw seeds.

### Tasks

1. **ON-1 (M): security plane.**
   - Bodies, normalizers, msw handlers, `qkcSecurity`.
   - Side effects: `settings_updated` invalidates settings and status; `credentials_changed` invalidates credentials, settings and status.
   - Words with the `sec` prefix for provider, verify status, `used_by`, backend cards, store states and skip reasons (raw-enum lint).
2. **ON-2 (M): cross-origin auth (D71).**
   - `http.ts:88-108`, `streamClient.ts:188-196`, and `SetupPage.tsx:229-236` ("signed in for this tab").
   - Forbidden: `?api_key=`, a `bearer.` subprotocol, `VITE_TRUTHTABLE_API_KEY`.
3. **ON-3 (M): Access.**
   - Keys: list, create (`content` reads "can see prompts and answers"), revoke with the D81 warning.
   - The secret is shown once, with "I saved it".
   - "Sign out everywhere".
   - The #70 table, with words for every D44 action.
4. **ON-4 (S-M): Add-ons, and All configuration.** All configuration shows D79 read-scope values; the degraded fallback renders the registry defaults.
5. **ON-5 (M): Connections.**
   - Cards for OpenRouter, TypeSafe, Anthropic, OpenAI and "Other gateway".
   - Each card: Connect or Paste (D75, with `bind`), Test (#57 with "check again" plus bind), and Remove (a `ConfirmDialog` that names the D77 routes and retries with `?confirm=true`).
   - "Used for" chips.
   - States: env-managed, engine-env (D80), undecryptable, `unavailable`, `no_database`.
6. **ON-6 (M): Models.**
   - Three backend cards, Advanced, the judge, and Test (#60, with Retry-After).
   - "Disconnect" with the D77 confirmation.
   - PATCH with If-Match, and the 412 reload flow.
   - The last error is the target of `RouteCard.tsx:133`.
7. **ON-7 (L): wizard and `/connect/callback`.**
   - **a.** `validateSearch` `step: secure|connect|try`, and `components/setup/{SetupWizard,StepSecure,StepConnect,StepSeeItWork,useSetupProgress}.tsx`.
   - **b. Step 1, Secure:**
     - token paste;
     - the `setup_token_from_logs` command next to `setup_url`;
     - "I saved it" gates Next;
     - skipped when an admin session exists;
     - keep the accessible names `stack.ts:78-98` relies on, and the `shell.spec.ts:15` H1, or update both here.
   - **c. Step 2, Connect:**
     - **Connect** (#58, then `window.location.assign`; not a form, because of CSP `form-action 'self'`). `CONNECT_PRIMARY` per D88.
     - **Paste a key instead:** one #55 call with `verify:true, bind:[decision_model, judge]`.
     - **Try it without a key** (D39).
     - The privacy line, the prerequisite line, the no-credits state with "check again", the no-limit callout, and "Simulated checks are now off" (D76).
   - **d. Step 3, See it work:**
     - disabled until bound or simulated;
     - the `EXAMPLES` refund example (`playgroundModel.ts:142`) with `modeOverride:'flag'` in audit mode, shown with `ResultTimeline`, plus a link to the trace;
     - mint an ingest key only when none is active, and show it once.
   - **e. `/connect/callback`:**
     1. read `code`;
     2. `history.replaceState`;
     3. read and clear the pending state;
     4. call #59;
     5. show the result;
     6. navigate to the server's `return_to`, through the router.

     Error states: missing nonce (with the "open the dashboard at <public_url>" hint), `invalid_nonce`, expired.
8. **ON-8 (S): Connect your app, checklist and fix links.**
   - `ConnectYourApp.tsx:102` links to Access and shows the key's `prefix`.
   - `overviewModel.ts:204-233` gains "Secure this dashboard", and the fast-check fix points to `/app/setup?step=connect`.
   - Remove `SetupPage.tsx:253-255`. The `planned` field is removed at I-3.
9. **ON-9 (M): `e2e/stack/onboarding.spec.ts`** (D70). `describe('onboarding')`, serial, `retries: 0`:
   1. claim with `TRUSTAGENT_SETUP_URL`, copy, tick "I saved it", Next;
   2. Paste `sk-or-v1-e2e-nocredits-…` → the no-credits copy, nothing bound, "check again" shown;
   3. Connect with `mock_key=nocredits` → 402, stored unbound;
   4. Try it without a key → the Simulated label and the top-bar badge;
   5. Connect OpenRouter (PKCE through the routed mock-upstream) → callback → "Ready · …":
      - backend no longer mock;
      - Connections shows "Used for: Fast check" and the judge as set by the environment (skipped);
      - `/api/status` shows `engine.runtime_config_version > 0` and equal to the settings version;
   6. Connections → replace with a pasted ok key (verify plus bind) → Ready, judge skipped;
   7. See it work: refund, Flag, contradicted claim marked;
   8. Connect your app goes from waiting to received after one `/v1` request with the minted ingest key;
   9. axe.

   When `P4_EVIDENCE_DIR` is set, the spec writes the JSON of `/api/credentials`, `/api/settings` and `/api/status` there. The happy path uses at most 8 user actions (D87), counted by a `step()` helper.

### Tests

- **vitest:**
  - each section in every state (empty, loading, error, degraded, demo, readonly);
  - the wizard: gating, paste, saved checkbox, 402 and check-again, env-managed, step 3 disabled, simulated off after a bind, demo, readonly;
  - the callback: no nonce, `invalid_nonce`, `replaceState`, server `return_to`;
  - D77 dialogs; D81 warning;
  - cross-origin `http`, `streamClient` and `liveDataSource`;
  - axe.
- **Playwright:** `--list` compiles.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
cd frontend-react && $L npm run lint && $L npx tsc -b \
 && $L npx vitest run --maxWorkers=2 src/components/setup src/components/settings src/data src/routes src/domain \
 && npx playwright test --list
```

---

## 11. WEB-EV: evaluators, labels, calibration, trace actions, glass box

- **Branch:** `feat/v2-w5-wev`.
- **Size:** M, about 5.5 days.
- **Read first:** §3.3 Evaluators and Trace detail; §2.11 adopt; `DESIGN.md`; D54-D63, D72.
- **Owns:**
  - `src/components/{evaluators,trace,policies,charts}/**` and `src/components/overview/EvidenceCoverageCard.tsx`;
  - `src/routes/app/{evaluators/** except studio/**,policies.tsx,traffic/$traceId.tsx}`;
  - `src/data/control/evaluatorPlane.ts`, `src/data/queries/evaluators.ts`, `src/domain/words/evaluators.ts`, `src/test/control/evaluatorHandlers.ts`;
  - `e2e/stack/labels.spec.ts`.
- **Consumes:** CP-EVAL for live wiring.

### Tasks

1. **EVW-1 (M): evaluator plane.**
   - Writes, labels, calibration (including #46b), decision-request and suggestions.
   - `evaluatorSideEffects`: labels and progress are invalidated locally; `calibration_updated` invalidates calibration.
2. **EVW-2 (M): label queue and "Was this right?" (D59).**
   - `LabelQueuePage` with keys C, F and S, a note, undo, focus management, `body_expired`, and the redacted state.
   - "Was this right?" in `OutcomeCard`, with existing labels and Undo.
   - Enable both `CalibrationMeter` actions.
   - `evaluators/$id.tsx` gains `validateSearch` `tab`.
3. **EVW-3 (M): Calibrate now and Adopt (D62).**
   - The fit dialog shows the honesty copy.
   - Adopt runs Preview (with `calibration_ids`), then #46b, then PUT. It is disabled until the preview has loaded.
   - A records list with deactivate, and an import action.
   - Remove `P4_REASONS.adopt` and `.labelling`.
4. **EVW-4 (M): glass box.** `JevRequestPanel.tsx` in `TraceFacts` (`:116-135`):
   - the summary line and `ProbabilityBars`;
   - #12 on open, with a match badge cross-checked against `CompileInfo.request_sha256`;
   - states: no content scope, expired, 404, simulated.
5. **EVW-5 (S-M): Open evaluator and Adjust threshold.**
   - Links from `OutcomeCard` and `ChecksList.tsx:68`.
   - `policies.tsx` gains `validateSearch` `route` and `evaluator`.
   - A threshold slider in `RouteCard.tsx:302`, then Preview, then Review and save.
6. **EVW-6 (S): Benchmarks and tabs.**
   - Benchmarks (D72), with a measured badge on every figure.
   - `components/evaluators/EvaluatorsTabs.tsx`, rendered by each page; there is no layout route. WEB-ST imports it.
   - "New check" and "Duplicate and edit" link to Studio routes. Remove `P4_REASONS.studio`.
7. **EVW-7 (S): evidence suggestion one-click.** `policyDraft.addTagPattern`, then `ReviewSaveDialog`, from `EvidenceCoverageCard.tsx:71-115`.

### Tests

- Queue keyboard, optimistic progress, `body_expired`.
- Adopt is blocked before the preview.
- Glass box: match, mismatch, redacted, 404.
- Every page in every state.
- Replace `EvaluatorsPage.test.tsx:27-29,101`.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
cd frontend-react && $L npm run lint && $L npx tsc -b \
 && $L npx vitest run --maxWorkers=2 src/components/evaluators src/components/trace src/components/policies src/lib/decision \
 && $L make -C .. vectors-ts && npx playwright test --list
```

---

## 12. WEB-ST: Studio

- **Branch:** `feat/v2-w5-wst`.
- **Size:** M, about 5 days.
- **Read first:** §7.3 (the seven steps); §3.3 Studio; `DESIGN.md`; D54, D58, D72, D82.
- **Owns:**
  - `src/components/studio/**`, `src/routes/app/evaluators/studio/**`, `src/lib/{studio,evaluator}/**`;
  - `frontend-react/scripts/gen-evaluator-validator.ts`;
  - `src/data/control/studioPlane.ts`, `src/data/queries/studio.ts`, `src/domain/words/studio.ts`, `src/test/control/studioHandlers.ts`;
  - `e2e/stack/studio.spec.ts`.
- **Consumes:** CP-LINT (for parity only). The live spec runs at I-5.

### Tasks

1. **STW-1 (S): Studio plane.** #34-38, with the dry-run through `readSse` (`data/sse.ts:106`).
2. **STW-2 (M): spec plumbing.**
   - `scripts/gen-evaluator-validator.ts` (copy `gen-policy-validator.ts`) writes `lib/evaluator/evaluatorValidator.gen.ts`, plus a drift test.
   - A lazy CodeMirror JSON tab with live `lintSpec` diagnostics.
   - `draftToSpec.ts`.
   - `simpleForm.ts`: a round trip that preserves unknown fields.
3. **STW-3 (L): Studio steps 1-5** (`studio/index.tsx`, `studio/$id.$version.tsx`):
   1. **Describe:** the judge model, "≈ $0.005" as an Estimate, and Simulated when `cost_source` says so; templates are primary when there is no judge.
   2. **Review:** Simple, Advanced and JSON tabs.
   3. **Test bench:** the estimate before Run; a free threshold what-if.
   4. **Quick calibration:** shows the message verbatim and never says "calibrated".
   5. **Save:** `DiffView` plus a changelog; 412 reloads.
4. **STW-4 (M): step 6, Save and watch, and Test on past traffic.**
   - The policy edits live in `lib/studio/policyEdits.ts`: `attachEvaluator`, `setEvaluatorStage` and `replaceEvaluatorRef`. They import `policyDraft.ts` read-only, then open `ReviewSaveDialog`.
   - **Dry-run (D82):**
     - the first call never sends `confirm_cost`;
     - the dialog shows the server's estimate and "leaves $X of today's budget";
     - progress, then Abort, then "Cancelled · spent ≈ $X";
     - the result uses `copy.studio.dry_run_result`.
   - Step 7 links to the label queue and to Calibrate.
5. **STW-5 (M): `e2e/stack/studio.spec.ts`.** `describe('studio')` on the demo-shaped stack:
   1. Draft with AI (mock drafter);
   2. lints are shown;
   3. test on the draft's 6 samples;
   4. quick calibration says "needs 8 labels";
   5. Save as `e2e_<base36 time>` (matching `^[a-z][a-z0-9_]{1,47}$`), 201;
   6. Save and watch on `support-bot` as shadow;
   7. Test on past traffic, plus a cancel case;
   8. roll back, as `policy-edit.spec.ts` does.

### Tests

- Form ↔ JSON round trip; lint parity on the lint vectors; validator drift.
- Quick-calibration gating.
- SSE parsing, cancel, the 409 dialog, 412 on save.
- Every state.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
cd frontend-react && $L npm run lint && $L npx tsc -b \
 && $L npx vitest run --maxWorkers=2 src/components/studio src/lib/studio src/lib/evaluator \
 && node scripts/gen-evaluator-validator.ts --check \
 && $L npm run build && npm run size && npx playwright test --list
```

---

## 13. WEB-SV: Savings, Budgets and prices, banners, badge, `reason`, Playground

- **Branch:** `feat/v2-w5-wsv`.
- **Size:** M, about 5 days.
- **Read first:** §7.4; §3.3 Overview, Savings, Budgets and Playground; D46, D49, D53, D68, D69, D73, D77, D89.
- **Owns:**
  - `src/components/{savings,overview,playground,traffic,layout}/**` except `overview/{ConnectYourApp,overviewModel,EvidenceCoverageCard}`;
  - `src/components/settings/BudgetsSection.tsx`;
  - `src/domain/{banner,health}.ts`;
  - `src/data/{replay/**,source.ts,live/liveDataSource.ts}`;
  - `src/data/control/costPlane.ts`, `src/data/queries/costs.ts`, `src/domain/words/costs.ts`, `src/test/control/costHandlers.ts`;
  - `src/routes/app/{overview,savings,playground,traffic/index}.tsx`;
  - `e2e/stack/savings.spec.ts`.
- **Consumes:** nothing. It uses the Step-0b settings plane.

### Tasks

1. **SV-1 (S): cost plane.** #48 and #49; replay `getRoiTimeseries` (`data/replay/stats.ts:276`); `costSideEffects` (D49).
2. **SV-2 (M): `SavingsPanel`.**
   - The headline from `savingsLine`.
   - `components/savings/CostBandChart.tsx` with D73 bands and a table view.
   - An `EvidenceBadge` on every number.
   - Stopped vs flagged; gate latency; sampling status.
   - Every state.
3. **SV-3 (M): Budgets and Prices.**
   - Inputs with spend bars and the 75% marker.
   - `env_managed` lock; If-Match.
   - A cap below today's spend shows the D77 confirmation.
   - The Prices panel shows the `ValidateOverrides` errors and warnings.
4. **SV-4 (S): banners.** The `llm_exhausted` rule (one-banner priority), and `fix` links in `BannerView` (`banner.ts:12-18`, `Banner.tsx:33-40`).
5. **SV-5 (S): simulated badge (D89)** in the layout top bar.
6. **SV-6 (S): traffic `reason`.** `source.ts:36-50` and `trafficRows.ts:46-53`.
7. **SV-7 (S): Playground.**
   - TTFT, and a decision that arrives before `done`.
   - `formHolds` allows knowledge scenarios when `status.knowledge.enabled`.
   - `LATER_PHASES = {'p5'}` (`playgroundModel.ts:106`).
8. **SV-8 (S-M): `e2e/stack/savings.spec.ts`.**
   - The tab is selected; the headline is labelled (Simulated on demo).
   - The chart and its table render.
   - Every row carries a label.
   - Stopped and flagged are separate.
   - Sampling status is shown.
   - axe.

   Remove `OverviewPage.test.tsx:152`.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
cd frontend-react && $L npm run lint && $L npx tsc -b \
 && $L npx vitest run --maxWorkers=2 src/components/savings src/components/overview src/components/settings/BudgetsSection src/components/playground src/components/traffic src/components/layout src/data/replay src/domain \
 && npx playwright test --list
```

---

## 14. DX: onboarding stack, make, CI, doctor, Grafana, nginx, docs

- **Branch:** `feat/v2-w5-dx`.
- **Size:** M, about 5.5 days.
- **Read first:** §3.4-3.5; §4.2 P4; §6; D35, D43, D70, D74, D86.
- **Owns:**
  - `Makefile`, `mk/**`, repo-root `scripts/**`, `.github/**`, `docker-compose*.yml`, `.gitleaks.toml`;
  - `frontend-react/{playwright.config.ts,nginx/**}`;
  - `config/grafana/**`, `config/prometheus.yml`;
  - `docs/**` except `docs/v2/SPEC.md`; `README.md`, `CHANGELOG.md`, `CLAUDE.md`, `backend-go/README.md`.
- **Consumes:** CP-SEC2 before the in-process check in DX-1.

### Tasks

1. **DX-1 (M): onboarding stack (D70).**
   - `docker-compose.onboarding.yml`.
   - `mk/dx.mk` targets `onboarding-up`, `onboarding-down` (`down -v`), `onboarding-logs` and `onboarding-setup-url`, with `ONBOARDING_PROJECT ?= truthtable-onboarding` and `ONBOARDING_BUILD ?= --build`.
   - `config -q` in `compose-check`, and `test_compose.py` knows the new file and key.
   - An in-process check that `proxy -check-config` passes under `TRUTHTABLE_STRICT_CONFIG` with backend `auto` and no key.
2. **DX-2 (S): `configure --with-secrets` (D74)**, with tests.
3. **DX-3 (M): doctor.**
   - New checks: credentials (state and undecryptable rows), runtime config (Go version vs engine version and epoch), budgets, and the judge's `credential_source`.
   - `--expect demo` stays green.
   - `test_doctor.py`.
4. **DX-4 (S): make.** `gen-evaluator-validator` and `-check` in `mk/web.mk`, mirroring `:44-47`, added to `check-web-ci`. It joins `make check` at I-2.
5. **DX-5 (M): CI** (keep job names stable).
   - **The demo-stack grep** gains `studio|savings` and runs with `--workers=1`.
   - **After `:181`, an onboarding step:**
     ```bash
     export PROXY_HOST_PORT=28080 DASHBOARD_HOST_PORT=23000 POSTGRES_HOST_PORT=25432 MOCK_UPSTREAM_HOST_PORT=28090
     make onboarding-up ONBOARDING_BUILD=
     url="$(make -s onboarding-setup-url)"
     (cd frontend-react && env -u TRUSTAGENT_ADMIN_KEY TRUSTAGENT_SETUP_URL="$url" CI=1 npx playwright test --project=stack --workers=1 -g onboarding)
     ```
   - **Logs and teardown:** add the onboarding project's logs to the collection at `:184-193`, and add `onboarding-down` to the teardown at `:211`.
   - **Other workflows:**
     - `ci-web.yml` runs `gen-evaluator-validator-check`;
     - `security.yml`: gitleaks allow-lists fake keys under `*_test.go`, `tests/unit/` and `e2e/` only.
6. **DX-6 (S): Grafana (cut line).**
   - A Spend row: `spend_usd_today{kind}`, `spend_reserved_usd`, `sum by (component)(increase(jev_cost_usd_total[24h]))`, `uniform_samples_today`, `audit_jobs_total{outcome="llm_budget"}`, `cost_write_dropped_total`.
   - `test_grafana.py` asserts that every named metric is registered.
7. **DX-7 (S): nginx.** `location = /connect/callback { access_log off; … }`, so `?code=` never reaches the logs.
8. **DX-8 (M): docs.**
   - **`JEV-ACCESS.md`:** Connect, paste, env precedence, simulated mode, credit limits on PKCE keys, and the localhost result (D88).
   - **`SECURITY.md`:**
     - the master key states;
     - back up the `trustagent-secrets` volume or set `TRUTHTABLE_SECRET_KEY` (a Postgres backup alone means re-entering keys);
     - the AAD and origin binding;
     - the redaction surfaces;
     - PKCE nonces and sticky sessions;
     - caps are per process (N replicas can overspend for up to the 60 s resync);
     - the setup-token takeover path and its audit row;
     - `TRUTHTABLE_UPSTREAM_URL` in `.env` pins the upstream (the operator's lock; `CONFIG_LOCK` covers policy only);
     - the internal gRPC plaintext gap;
     - CORS;
     - `localhost:5173` in the default origins (dev only; set `TRUTHTABLE_ALLOWED_ORIGINS` in production).
   - **`GATEWAY.md`**, **`EVALUATORS.md`**.
   - Regenerate `CONFIGURATION.md` and `.env.example`.
   - Update `README.md`, `DEMO.md`, `docs/README.md`, the `CLAUDE.md` commands and `backend-go/README.md`.
9. **DX-9 (S): CHANGELOG.**
   - New endpoints and WS events.
   - **Breaking:** CORS (D43); D35; #55/#57/#59 response shapes (D75); `ApplyRuntimeConfig` and `DraftEvaluator` implemented.
   - The ledger and the LLM cap; the trace writer.
10. **DX-10 (S):** the full `make check` on the DX branch.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
make lint-scripts test-scripts compose-check config-docs-check
docker compose -f docker-compose.yml -f docker-compose.e2e.yml -f docker-compose.onboarding.yml --profile e2e config -q
node scripts/check-forbidden.mjs --self-test && node scripts/check-forbidden.mjs
$L make check     # DX-10 only
```

---

## 15. Ownership matrix (after Step 0)

| Path | Owner |
|---|---|
| `cmd/proxy/{server,gateway,api,p3_api,notify_hub,harness_test,wiring_test,api_test,e2e_test}.go`, `internal/store/{store,postgres,open,notify}.go`, `internal/store/migrations/`, `internal/metrics/gateway.go`, `internal/decision/vectors/kinds.go`, `proto/`, `contracts/{schema,config,ui}/**`, `contracts/{superseded.v1.json,validate.mjs,vectors/README.md}`, `contracts/fixtures/grpc/**`, `docs/v2/SPEC.md`, `frontend-react/{DESIGN.md,package.json,package-lock.json}`, and the web frozen list (§0 rule 2) | lead (frozen) |
| `internal/credentials/**` (except `verify*`, `pkce*`), `internal/runtimecfg/**` (except `push*`), `internal/{authz,config}/**`, `internal/store/{authz_keys,authz_settings,credentials,settings}.go`, `cmd/proxy/{main,session_api,setup_api,keys_api,cli,internal_token,status_api,runtime_wiring,settings_api,settings_guard,export_api}.go` and their tests (incl. `ws_test`, `authz_api_test`, `credentials_leak_test`) | GSEC |
| `internal/{upstream,middleware,grpc}/**`, `internal/credentials/{verify,pkce}*`, `internal/runtimecfg/push*`, `internal/jev/swap*`, `internal/store/admin_audit.go`, `cmd/mock-upstream/**`, `cmd/proxy/{decision_client,upstreams,apply_wiring,credentials_service,credentials_api,connect_api,decision_model_api,admin_audit_api}.go` and their tests | GCONN |
| `internal/{costs,worker}/**`, `internal/jev/**` (except `swap*`), `internal/decision/{cost.go,cost_test.go,vectors/cost.go}`, `cmd/vectorgen/**`, `contracts/vectors/cost/**`, `internal/store/{audits,stats,costs}.go`, `cmd/proxy/{costs_wiring,stats_api,roi_api,pricing_api,audits_api,kb_api,sink,status_events}.go` and their tests | GCOST |
| `internal/decision/**` (other files), `src/lib/decision/**`, `internal/{evaluators,calibration,policy}/**`, `contracts/vectors/{lint,decide}/**`, `config/{calibration,evaluators}/**`, `cmd/calibrate/**`, `internal/store/{evaluators,reliability,policies,replay,labels,calibrations}.go`, `cmd/proxy/{evaluators_api,decision_wiring,policy_wiring,policies_api,gate_api,eval_wiring,labels_api,calibration_api}.go` and their tests | GEVAL |
| `internal/studio/**`, `cmd/gate-eval/**`, `internal/evidence/suggest*`, `internal/store/{decisions_list,studio,suggestions}.go`, `cmd/proxy/{studio_api,decision_request_api,evidence_api}.go` and their tests | GSTUDIO |
| `internal/{proxy,llmapi,gate}/**`, `internal/evidence/**` (except `suggest*`), `internal/store/{traces,views,outcome}.go`, `internal/store/tracewriter/**`, `cmd/trafficgen/**`, `config/policies*.yaml`, `contracts/demo/**`, `contracts/README.md`, `cmd/proxy/{traces_api,playground_api,v1_routes,decisions_longpoll_api,knowledge_wiring,trace_writer}.go` and their tests, `cmd/proxy/{scenarios_e2e_test,memstore_test}.go` | GGW |
| `cmd/export-replay/**`, `internal/{recording,websocket}/**`, `cmd/proxy/ready_api.go`, `cmd/jevcheck/**` | nobody (no P4 change) |
| `backend-python/**` | PY |
| `components/{setup,settings,common}/**` (except `BudgetsSection.tsx`, `sectionComponents.ts`), `routes/{app/setup.tsx,connect/**}`, `data/{http.ts,errors.ts,live/streamClient.ts}`, `data/control/securityPlane.ts`, `data/queries/security.ts`, `domain/words/security.ts`, `domain/snippets.ts`, `test/control/securityHandlers.ts`, `overview/{ConnectYourApp.tsx,overviewModel.ts}`, `e2e/stack/{onboarding,shell}.spec.ts`, `e2e/stack/stack.ts` | WEB-ON |
| `components/{evaluators,trace,policies,charts}/**`, `overview/EvidenceCoverageCard.tsx`, `routes/app/{evaluators/** (except studio/**),policies.tsx,traffic/$traceId.tsx}`, `data/control/evaluatorPlane.ts`, `data/queries/evaluators.ts`, `domain/words/evaluators.ts`, `test/control/evaluatorHandlers.ts`, `e2e/stack/labels.spec.ts` | WEB-EV |
| `components/studio/**`, `routes/app/evaluators/studio/**`, `lib/{studio,evaluator}/**`, `frontend-react/scripts/gen-evaluator-validator.ts`, `data/control/studioPlane.ts`, `data/queries/studio.ts`, `domain/words/studio.ts`, `test/control/studioHandlers.ts`, `e2e/stack/studio.spec.ts` | WEB-ST |
| `components/{savings,overview,playground,traffic,layout}/**` (overview except the three files above), `settings/BudgetsSection.tsx`, `domain/{banner,health}.ts`, `data/{replay/**,source.ts,live/liveDataSource.ts}`, `data/control/costPlane.ts`, `data/queries/costs.ts`, `domain/words/costs.ts`, `test/control/costHandlers.ts`, `routes/app/{overview,savings,playground,traffic/index}.tsx`, `e2e/stack/savings.spec.ts` | WEB-SV |
| `Makefile`, `mk/**`, repo-root `scripts/**`, `.github/**`, `docker-compose*.yml`, `.gitleaks.toml`, `frontend-react/{playwright.config.ts,nginx/**}`, `config/grafana/**`, `config/prometheus.yml`, `docs/**` (except SPEC), `README.md`, `CHANGELOG.md`, `CLAUDE.md`, `backend-go/README.md` | DX |
| `contracts/fixtures/api/**` | append-only; new files by the endpoint owner |

---

## 16. Dependencies, critical path and schedule

```
Step 0a (1.5 d) ─┬─ GSEC  SEC-1..3 ─► CP-SEC1 ─► SEC-4,5 ─┐(CP-SEC2)─► SEC-6,7 ─► (CP-SEC3) SEC-8 ─┐
                 ├─ GCONN CN-1..6 ─(park)─► CN-7 ─────────┘           CN-8 ─► CP-SEC3 ─► WEB-ON ON-9 ┤
                 ├─ GCOST COST-1,2 ─► CP-COSTV ─► COST-3..5 ─► CP-COST ─► COST-6..11 ────────────────┤
                 │                       └─► PY-6               └─► GSTUDIO, GCONN probe cost        │
                 ├─ GEVAL EV-1 ─► CP-LINT ─► EV-2..5 ─► CP-EVAL ─► WEB-EV live wiring ────────────────┤
                 ├─ PY PY-1..5 ─► (CP-COSTV) PY-6 ───────────────────────────────────────────────────┤
Step 0b (+1 d) ──┼─ WEB-ON ON-1..8 (msw) ─(park)─► ON-9 ─────────────────────────────────────────────┤
                 └─ queue: GSTUDIO → WEB-ST → WEB-EV → WEB-SV → DX → GGW ─────────────────────────────┴─► Integration (≈3 d)
```

**Chain timings:**

| Segment | Ends at |
|---|---|
| Step 0a | day 1.5 |
| SEC-1..3, then CP-SEC1 | ≈ day 6 |
| SEC-5 and CN-7, then CP-SEC2 | ≈ day 8.5 |
| CN-8, then CP-SEC3 | ≈ day 9.25 |
| ON-9 ready | ≈ day 10.5 |

**Capacity is the real limit.**
- The work is about 79 stream-days in 5 slots (6 when memory allows), so the queue drains around **day 16**.
- With integration, P4 takes **≈19 working days**, or about 17.5 with the cut line.
- The draft's 12.5 days left out SEC-10..12 and the memory cap.

**Slot plan (5-6 slots):**

| Slot | Streams in order |
|---|---|
| 1 | GSEC → DX |
| 2 | GCONN → GGW |
| 3 | GEVAL → WEB-EV |
| 4 | GCOST → WEB-SV |
| 5 | WEB-ON → WEB-ST |
| 6 (if memory allows) | PY → GSTUDIO |

With only five slots, PY and GSTUDIO take the first two slots that free up.

**Cut line.** Decided at CP-SEC2 (≈ day 8.5). If the queue projects integration to start after day 16, these move to P5 or P6, each recorded in §19 and the errata:

| Item | Moves to | Saves |
|---|---|---|
| GW-1 (knowledge on the hot path) | P5 | 1.5 d |
| GW-3 (trace writer and LRU) | P5 | 1.5 d |
| GW-2 (Playground streaming) | P5 | 1 d |
| COST-8 `fitted_from_audits` | P6 | 0.5 d |
| DX-6 (Grafana) | P5 | 0.5 d |
| EVW-6 Benchmarks | P5 | 0.5 d |

None of these is in the §4.2 P4 gate.

**If GW-3 is cut, D83's barrier goes with it.** The `traceBarrier` hook stays a no-op, and synchronous writes need no barrier.

---

## 17. Integration (lead, on `feat/v2-int5`)

1. **I-1: merge.**
   - Order: GSEC → GCONN → GCOST → GEVAL → GSTUDIO → GGW → PY → WEB-ON → WEB-SV → WEB-EV → WEB-ST → DX. Checkpoint merges already done stay as they are.
   - Run each stream's gate after its merge. A conflict means an ownership violation: the work goes back to the owner.
2. **I-2: turn-on.**
   - `make check` includes `gen-evaluator-validator-check`.
   - `ci-e2e` runs the onboarding step and the extended demo grep with `--workers=1`.
   - The demo default `TRACE_FLUSH=async` holds; otherwise apply the D66 fallback.
   - Flip `CONNECT_PRIMARY` if the owner reported a localhost PKCE failure (D88).
   - `TestEnginePushAtBoot` passes.
3. **I-3: end-of-P4 deletions and errata.**
   - **Phase grep.** The lead writes `scripts/phase-refs.allow` with the P5 exceptions:
     - `replay/StaticPlayground.tsx`, `landing/TryIt.tsx`, `landing/WriteChecks.tsx`, `routes/index.tsx`;
     - the static Studio "Coming soon" and its tests;
     - SPEC and archive docs.

     Then this must print nothing:
     ```bash
     rg -n '\bP4\b|"p4"|in P4|P4_REASONS' frontend-react/src backend-go backend-python/src scripts | grep -v -F -f scripts/phase-refs.allow
     ```
     It covers the `(P4)` comments in `jev/cost.go:52`, `jev/spend.go:57`, `decision/doc.go:18`, `evaluators_api.go:22` and `status_api.go:476`; `queries.ts:212`; `EvidenceCoverageCard.tsx:107`; `scripts/gen_config_docs.py:49`; and `judge.py:56`.
   - **Remove** the `planned` field (`settings/sections.ts:3-8`) and the remaining P4 `PhasePlaceholder` uses.
   - **Fix the stale comments:** `TraceFacts.tsx:6-7`, `OutcomeCard.tsx:4-5`, `EvaluatorsPage.tsx:4-5`, `CalibrationMeter.tsx:4,67`, `EvaluatorDetailPage.tsx:4-5`, `evaluatorWords.ts:3,74`, `DisabledReason.tsx:59-60`, `sse.ts:3`, `nav.ts:2-4`, `status_api.go:390-391,504`, `worker/pool.go:9-10`, `DESIGN.md:351,354,392`.
   - **SPEC Appendix F "P4 errata"**, citing the decisions in the sections they change:

     | SPEC section | Decisions |
     |---|---|
     | §2.2 | D34 |
     | §2.3 | D31, D45, D83 |
     | §2.4 | #10 D68; #12 D63; #26/#45/#46b D62; #31/#32 D57; #34 and #65 D48; #35/#38 D82; #39 D60; #53 D77, D41; #55/#57/#59 D75; #56 D77; #58/#59 D38; #69 D81 |
     | §2.5 | D49 |
     | §2.6 | new types |
     | §2.7 | D35 |
     | §2.11 | D55, D56 |
     | §3.1 | `runtimecfg` does not own the pinger; redaction lives in `config.Redactor`; D90 file split |
     | §3.3 | D53, D72 |
     | §3.4 | D70, D74 |
     | §4.2 gate | D70 |
     | §6 | D74, D86 ($5) |
     | §7.4 | D46, D50, D53 |
     | §7.5 | D30, D37, D38, D78, D79 |
     | §7.6 | D85 |
     | Appendix B | D51 |
     | Appendix D | D88 |
     | P3-PLAN §13 | D65 |

     Also mark D28 resolved in Appendix E.
   - **No new Appendix A identifiers:** run `node scripts/check-forbidden.mjs`.
4. **I-4: local gates.** From the repo root, one after another, under the lock, with `TEST_DATABASE_URL` set; `truthtable-w5db` stays up until I-8.
   ```bash
   L="flock /tmp/trustagent-heavy.lock"
   test -z "$(gofmt -l backend-go)" && $L go -C backend-go vet ./...
   $L env -u TEST_DATABASE_URL go -C backend-go test -race -p 2 ./...
   go -C backend-go test ./internal/decision/... -run TestVectors
   $L go -C backend-go test -p 1 ./internal/store/... ./internal/costs/... ./cmd/proxy/... ./cmd/export-replay/...
   (cd backend-python && poetry run ruff check . && poetry run black --check . \
     && $L poetry run mypy src/truthtable/runtime src/truthtable/studio src/truthtable/scoring src/truthtable/text src/truthtable/providers \
     && $L poetry run pytest tests/unit -q && poetry run python -m evals.run_eval --dataset golden_v2 --provider mock --check-baseline)
   $L make check && $L make vectors-ts && $L make test-e2e-static
   ```
   Then run the §18 Go and Python lines.
5. **I-5: Docker proof A** (demo-shaped; project `truthtable-p4proof`; never `truthtable-demo`):
   ```bash
   export EVID=<lead scratchpad>/p4-evidence; mkdir -p $EVID
   export DEMO_PROJECT=truthtable-p4proof PROXY_HOST_PORT=18080 DASHBOARD_HOST_PORT=13000 POSTGRES_HOST_PORT=15432
   export TRUSTAGENT_ADMIN_KEY="$(openssl rand -hex 24)"; export TRUTHTABLE_ADMIN_KEYS="proof=$TRUSTAGENT_ADMIN_KEY"
   flock /tmp/trustagent-heavy.lock make demo OPEN=0 DEMO_PROJECT=$DEMO_PROJECT
   make demo-scenarios DEMO_PROJECT=$DEMO_PROJECT | tee $EVID/a-scenarios.txt                     # 26 × ok, 0 × skipping
   (cd frontend-react && CI=1 npx playwright test --project=stack --workers=1 \
      -g "trace detail|inline claims|policy edit|rollback|fallback|studio|savings|labels") | tee $EVID/a-playwright.txt
      # "labels" (the label queue specs) as ci-e2e.yml runs it; erratum (1cfc60c)
   H="X-TrustAgent-Key: $TRUSTAGENT_ADMIN_KEY"; P=http://127.0.0.1:18080
   curl -fsS -H "$H" $P/api/status > $EVID/a-status.json
   curl -fsS -H "$H" $P/api/pricing > $EVID/a-pricing.json
   curl -fsS -H "$H" "$P/api/roi/timeseries?window=7d&bucket=1d" > $EVID/a-roi-ts.json
   T=$(curl -fsS -H "$H" "$P/api/traces?limit=1" | jq -r '.items[0].trace_id')
   curl -fsS -H "$H" "$P/api/traces/$T/decision-request" > $EVID/a-decision-request.json
   docker compose -p $DEMO_PROJECT exec -T postgres psql -U truthtable -tAc \
     "select 'studio_results',count(*) from studio_results union all select component,count(*) from cost_events group by component union all select action,count(*) from admin_audit_log group by action" > $EVID/a-db.txt
   python3 scripts/doctor.py --target docker --expect demo --project $DEMO_PROJECT --proxy-url $P --dashboard-url http://localhost:13000 | tee $EVID/a-doctor.txt
   # screenshots now: Studio, Savings, glass box, Connections → $EVID/
   make demo-clean DEMO_PROJECT=$DEMO_PROJECT
   ```
6. **I-6: Docker proof B** (onboarding, then the e2e regression on the same ports):
   ```bash
   unset TRUTHTABLE_ADMIN_KEYS TRUSTAGENT_ADMIN_KEY
   export ONBOARDING_PROJECT=truthtable-p4onb PROXY_HOST_PORT=28080 DASHBOARD_HOST_PORT=23000 POSTGRES_HOST_PORT=25432 MOCK_UPSTREAM_HOST_PORT=28090
   flock /tmp/trustagent-heavy.lock make onboarding-up
   url="$(make -s onboarding-setup-url)"
   (cd frontend-react && TRUSTAGENT_SETUP_URL="$url" P4_EVIDENCE_DIR=$EVID CI=1 npx playwright test --project=stack --workers=1 -g "onboarding") | tee $EVID/b-playwright.txt
   docker compose -p $ONBOARDING_PROJECT exec -T postgres psql -U truthtable -tAc \
     "select name, masked, last_verify_status from credentials; select action, count(*) from admin_audit_log group by action" > $EVID/b-db.txt
   # The boot log prints the one-time setup link by design (spent by now): mask it in the evidence.
   docker compose -p $ONBOARDING_PROJECT logs --no-color proxy engine dashboard postgres \
     | sed -E 's/#token=[A-Za-z0-9_-]+/#token=<masked>/g' > $EVID/b-logs.txt
   ! grep -qE 'sk-or-v1-[A-Za-z0-9_-]{8,}|sk-test-[A-Za-z0-9_-]{8,}|cmp0e2eZq7Lw4Rt9Yx2Vb6Nm|mockcode_[0-9a-f]{8,}|#token=[A-Za-z0-9_-]{8,}' $EVID/b-logs.txt \
     && echo "no secrets in logs" | tee $EVID/b-leak.txt
   # screenshots now: wizard step 2, Connections → $EVID/
   make onboarding-down
   make e2e-up E2E_PROJECT=truthtable-p4e2e E2E_BUILD= && make e2e-scenarios E2E_PROJECT=truthtable-p4e2e E2E_PHASE=p3 | tee $EVID/b-e2e.txt \
     && make e2e-down E2E_PROJECT=truthtable-p4e2e                                                   # 23 × ok
   ```
7. **I-7: independent verifier** (a read-only agent). It reads `$EVID`, the repo and the test logs only; it never touches a running stack.
   - **Credentials:**
     - the swapped-AAD (including origins), undecryptable and origin-binding tests exist and run under `-race` without `--- SKIP`;
     - `b-db.txt` shows masked values only;
     - `b-leak.txt` exists;
     - the 402 path left the key unbound;
     - `engine.runtime_config_version` equals the settings version (evidence JSON);
     - env-managed PATCH gives 409 and a stale If-Match gives 412 (tests);
     - CORS headers are exact;
     - every D44 action appears in `admin_audit_log` (a-db and b-db, plus tests);
     - the append-only trigger exists;
     - no secret in `/metrics` (SEC-8);
     - `/connect/callback` has `access_log off`;
     - session cookies are HttpOnly and Strict.
   - **Costs:**
     - hedge-loser rows exist;
     - spend survives a restart;
     - the LLM-cap 503 and `audit_dropped{llm_budget}`;
     - `a-roi-ts.json` and `a-pricing.json` validate against their schemas;
     - every Savings number is labelled.
   - **Studio and calibration:**
     - `studio_results` > 0 in a-db;
     - `a-decision-request.json` has `matches_recorded: true`;
     - the lint, cost and D56 decide vectors are frozen;
     - #45 refuses fewer than 8 labels;
     - the activate swap keeps the unique index.
   - **Cleanup:**
     - the I-3 grep is empty;
     - `ApplyRuntimeConfig` and `DraftEvaluator` are implemented;
     - no Playwright `test.skip` path runs under `CI=1`.
8. **I-8: finish.**
   - `COMPOSE_PROJECT_NAME=truthtable-w5db docker compose down -v`.
   - Merge `feat/v2-int5` into `feat/v2-jev-gateway`.
   - Do not rebuild the owner's demo. Give the owner the commands in §21.

---

## 18. P4 gate (exact commands, from the repo root)

```bash
L="flock /tmp/trustagent-heavy.lock"     # TEST_DATABASE_URL points at truthtable-w5db
# SPEC §4.2: swapped-AAD, undecryptable, host (origin) binding, no secret in logs/API/WS, session revocation
$L go -C backend-go test -race -v ./internal/credentials/... ./internal/authz/... ./internal/costs/... 2>&1 | tee /tmp/p4-gate-go.out
$L go -C backend-go test -race -v ./cmd/proxy -run 'Secret|Leak|Credential|Settings|Connect|SessionEpoch' 2>&1 | tee -a /tmp/p4-gate-go.out
! grep -q -- '--- SKIP' /tmp/p4-gate-go.out
(cd backend-python && poetry run pytest tests/unit/test_runtime_config.py tests/unit/test_drafter.py -q)
# Playwright "onboarding|studio|savings", as two invocations on two stacks (D70), PKCE via mock-upstream OpenRouter:
#   I-5 (proof A): CI=1 npx playwright test --project=stack --workers=1 -g "studio|savings"
#   I-6 (proof B): CI=1 npx playwright test --project=stack --workers=1 -g "onboarding"
# Also at the end of the phase:
$L make check && $L make vectors-ts      # lint, decide and cost vectors frozen in Go; TS lint parity
#   I-5: make demo-scenarios → 26 × ok, 0 × skipping;  I-6: make e2e-scenarios E2E_PHASE=p3 → 23 × ok
```

---

## 19. Deferred

**P5**
- **Static site:**
  - static Studio (templates, editor, quick calibration, then "Draft with the browser's OpenRouter key");
  - `src/lib/openrouter/pkce.ts` and its vectors;
  - the prerendered static `/connect/callback/`;
  - the TS cost runner with the landing calculator (D85).
- **Knowledge:** the Docker proof (`COMPOSE_PROFILES=knowledge`, a `handbook` overlay, `knowledge-only-capped`) (D65).
- **Specs:** `first-run.spec.ts` and `golden-path.spec.ts`. The five-minute time-to-Ready target is measured with GHCR images (D87).
- **Multi-replica:** a shared PKCE nonce store (D38) and shared spend caps (per-process today).
- **Labels:** re-scoring frozen label samples on a new snapshot (paid).
- **Cut-line items**, if they slip (§16).

**P6**
- A measured calibration replaces the mock one.
- Bench-lite validation of the token model (`measured.json`).
- `fitted_from_audits`, if it was cut.
- The real-OpenRouter localhost check, if the owner has not done it.

**P7 (v2.1)**
- `SECRET_KEY_PREVIOUS` rotation.
- `RuntimeConfig.decision` push and engine-side Jev.
- `JudgeConfig.base_url`.

---

## 20. Risks and how the plan handles them

- **Scope and memory.**
  - About 79 stream-days against the SPEC's 28 engineer-days.
  - Five active slots, one heavy lock, parking, a queue that protects the security chain, and a cut line decided at CP-SEC2.
  - The honest estimate is about 19 days.
- **Secret leakage.**
  - D42 (received and decrypted secrets, gRPC messages, upstream bodies, Python handler filters) and D79 (read scope).
  - Leak tests use a secret that matches no pattern, and SEC-4 starts them early.
  - The proof-B grep, with `LOG_LEVEL=debug`, covers four services.
  - nginx `access_log off` on the callback; masked-only fixtures; gitleaks allow-lists test paths only.
- **Tampering with the database.**
  - The AAD binds origins (D78), and vendor origins are never read from the database.
  - The audit log is append-only.
  - Push can never widen the engine's origins (D34).
- **Self-inflicted outages.** D77 confirmations and warnings; D81 last-admin guard; D82 keeps Studio and probes out of the enforcing reserve and the observe line.
- **Hot swap.**
  - A Swappable client, tested under `-race`.
  - Shared Budget and Costs across rebuilds.
  - A 30 s grace close.
  - Push idempotency through the epoch and version rules on both sides, with a shared push fixture.
  - D34 revert on unbind.
- **The PKCE redirect in e2e.** `route.fetch` plus `fulfill`, with a `page.goto` fallback. Paste-a-key is always available. The callback origin follows the browser (D38).
- **PKCE on localhost against real OpenRouter.** An owner check at the start (D88). The wizard ships both paths.
- **Async writer correctness.** The D83 barrier. e2e stays `sync`. The D66 fallback for the demo.
- **Contract drift.**
  - The lead reviews `internal/decision/**` and `src/lib/decision/**` at CP-LINT, CP-COSTV and CP-EVAL.
  - Vectors freeze only on a Go pass.
  - `make vectors-ts` runs in the WEB-EV gate and at integration.
- **Gates that pass without testing anything.** `-v` with no `--- SKIP`; `CI=1` for Playwright; the test database stays up through §18; the verifier reads captured evidence (closes P3-PLAN D18 again).
- **Shared-file conflicts.** Step 0 creates every route, plane, hook, notify hub and constant. Copy is frozen. The `Store` interface is frozen.
- **Playwright flakes.** `--workers=1` on shared stacks; unique Studio ids; serial onboarding with `retries: 0`.

---

## 21. Owner actions (step by step)

1. **At the start of the wave (optional, about 5 minutes, about $0.00001; needs your real OpenRouter account).**
   1. Run `make demo-down`.
   2. In `.env`, comment out `OPENROUTER_API_KEY` if it is set.
   3. Run `make up` and open the setup link it prints.
   4. In step 2 (once P4 lands; until then, tell the lead you are ready and it will give you a one-off build), click **Connect OpenRouter**.
   5. Tell the lead whether OpenRouter accepted the `http://localhost:3000/connect/callback` address. If you use another dashboard port, also say whether that worked.
   6. Afterwards, `make down`, uncomment the key, and `make demo` again.
2. **After any PKCE connect.** Open openrouter.ai/settings/keys and set a credit limit (for example $2) on the new key. Keys created through PKCE have no limit; the wizard also warns about this.
3. **Before entering keys in the dashboard on your real stack.** Either back up the `trustagent-secrets` Docker volume or set `TRUTHTABLE_SECRET_KEY` in `.env`. A Postgres backup alone means re-entering every key.
4. **Refresh your demo when you are ready.** The wave never touches it:
   - `make demo-down && make demo`;
   - or `make demo-clean && make demo` for a fresh database.

---

## 22. Critique disposition

**Accepted. Feasibility critique:**

| # | Change | Where |
|---|---|---|
| 1 | constants moved to `api.go`; `apiGuards` moved to `server.go` | §2.3e, §2.3a |
| 2 | import cycle broken | D90, §2.5 |
| 3 | list types and `calibration_ids` | §2.2f, §2.4 |
| 4 | `decide-input`-only state | D56 |
| 5 | missing seams: upload method, `newUpstreams`, `PriceTableFile`, `CostLink`, notify hub, `p3_api` frozen with a hook, test-file owners | §2.3, D84 |
| 6 | vectors before CP-COSTV | §5 |
| 7 | `fn` and `test_contract_vectors` | D85 |
| 8 | no-skip, `CI=1`, database kept until §18 | §17, §18 |
| 9 | evidence capture | I-5, I-6 |
| 10 | commands from the root, the CI URL, `env -u`, teardown and logs | §14 DX-5, I-4 |
| 11 | branch base `e07c455` | §2.1 |
| 12 | ownership fixes | §15; `planned` removal at I-3; `CostBandChart` in savings; AppShell test in Step 0b; tabs component |
| 13 | D33 and D39 consistency | D33, D39 |
| 14 | `stored:false` and ON-9 order | D75, ON-9 |
| 15 | barrier | D83 |
| 16 | `effectivePriceJSON(overrides)` | §2.3a |
| 17 | CP-SEC3; settings plane in Step 0b; GGW with a fake Set; the studio spec runs at I-5 | §0 |
| 18 | Step 0a/0b; security chain shortened by the GCONN split | §0, §16 |
| 19 | 5-slot cap, wider lock coverage, per-language proto | §0 |
| 20 | `--workers=1` and unique ids | STW-5, I-5 |
| 21 | unique index and insert rule | D45, D55 |
| 22 | errata | I-3 |
| 23 | grep and allow-list; `defaultPhase` and `LATER_PHASES` in GW-7 and SV-7 | I-3 |
| 24 | leak grep | I-6 |
| 25 | overlay hygiene | D70 |
| 26 | D41b | D41 |
| 27 | bump scope | D31 |
| 28 | older engine | D34 |
| 29 | prefixes and `errors.ts` in Step 0b | D86 |
| 30 | Step-0 gate after each checkpoint | §0 |
| 31 | `configure --ensure` | §2.1 |
| 32 | inaccuracies | D62, D67, D54; COST-11 wording |

**Accepted. Security and product critique:**

| # | Change | Where |
|---|---|---|
| 1 | bind on #55 | D75 |
| 2 | leaving simulated mode | D76, D33 |
| 3 | confirmations | D77 |
| 4 | origins and AAD | D78 |
| 5 | revert on a push without a judge | D34 |
| 6 | callback origin | D38 |
| 7 | read-scope masking | D79 |
| 8 | engine-env rows | D80 |
| 9 | received secrets, upstream bodies, gRPC messages, no-pattern leak tests, proof-B grep | D42, SEC-4/8, GW-6, I-6 |
| 10 | handler filters | D42, PY-3 |
| 11 | D23 as a 422 | D41 |
| 12 | trigger, more actions, scrubbing | D44, D45 |
| 13 | last admin | D81 |
| 14 | PKCE hardening and nginx | D38, DX-7 |
| 15 | Studio spend | D82 |
| 16 | 402 path and prerequisites | D87 |
| 17 | owner check first | D88, §21 #1 |
| 18 | push fixture | D34 |
| 19 | copy seeding and plain language | D86 |
| 20 | badge | D89 |
| 21 | time target in P5; ingest-key minting | D87 |
| 22 | secret-runtime rule, override checks, link-local refusal, probe truncation, #57 rate limit | D35, D53, D78, D40 |
| 23 | docs | DX-8 |
| 24 | leak test early, verifier additions | SEC-4, I-7 |

**Rejected or changed:**

| Critique item | Disposition | Reason |
|---|---|---|
| Feasibility 19 | A 2-slot semaphore: **rejected**. | The task sets one lock, `/tmp/trustagent-heavy.lock`, and measured headroom (about 3.2 GB available, swap in use) does not support two concurrent heavy builds. One heavy command at a time, with wider coverage. |
| Feasibility 5 | `worker.SetBudget` as a Step-0 seam: **not needed**. | GCOST owns both `worker` and `costs_wiring.go`, and `initCosts` runs after `worker.New`, so GCOST adds it in COST-5. |
| Feasibility 15 | "409/404 with Retry-After while a trace is pending": **replaced** by the D83 barrier hook. | Simpler, and readers never see a transient error. |
| Feasibility 18 | Moving SEC-10 and CORS to GGW: **replaced** by the GCONN split. | It shortens the security chain more, and GGW stays a pure cut-line stream. |
| Feasibility 25 | `--host-resolver-rules` as the primary PKCE redirect: **kept as a fallback only**. | `auth_url` carries port 8090, and a host-only MAP needs the mock published on 8090. `route.fetch` plus `fulfill` works on any port. |
| Feasibility 12 | Per-stream sections in `contracts/ui`: **not adopted**. | Copy is frozen with a lead seed, which avoids merge conflicts in one JSON file. |
| Security 17 | Record the localhost result in `contracts/fixtures/vendor/`: **rejected**. | There is no capture to sanitize; `JEV-ACCESS.md` and Appendix D record it. |
| Security 17 | Making the owner check a blocking Step 0.0: **changed to non-blocking**. | The wizard ships both paths, and D88 flips the primary button at I-2. |
| Security 22 | Drop `http://localhost:5173` from the default allowed origins: **rejected**. | `make dev-react` does not set it (`docker-compose.yml:258` is the only place). Removing it breaks the dev loop, and the exposure needs a local process on port 5173. SECURITY.md documents setting `TRUTHTABLE_ALLOWED_ORIGINS` in production. |