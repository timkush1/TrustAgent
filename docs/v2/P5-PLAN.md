# P5: implementation plan to the P5 gate (demo, static site, landing, release)

This is the final plan: the draft with both critiques applied. §22 lists what was accepted, changed or rejected, and why. The plan was checked read-only against these heads:

| Branch | Head | Notes |
|---|---|---|
| `feat/v2-jev-gateway` | `e07c455` | P3 merge. **P5 is cut from here after P4 I-8**, which merges int5 into it. |
| `feat/v2-int5` | `ac16702` | P4 checkpoints CP-COSTV, CP-COST, CP-SEC2+3 and CP-EVAL are merged. P4 I-1 to I-8 have not run. |
| `feat/v2-w5-{won,wev,wst,wsv,dx,gstudio,ggw,py,gsec,gconn,gcost}` | `9eaff01`, `1df2eb1`, `4adf983`, `5e4090a`, `1c8f287`, `d218b02`, `68d1344`, `a6fcb6b`, `4a56323`, `295a0be`, `743e883` | Still moving (`won`, `wst` and `gsec` moved while this plan was written). They merge at P4 I-1. P5 starts from their union with int5. |

- **References.** `file:line` points at int5 `ac16702`. A file that exists only on a P4 branch is written `<short> path`, for example `wst lib/studio/estimate.ts` (`git show feat/v2-w5-wst:frontend-react/src/lib/studio/estimate.ts`). If a line has moved, find it by symbol.
- **Paths.** Go paths are under `backend-go/` and web paths under `frontend-react/`, unless a path says otherwise.
- **Binding decisions.** These stay binding:
  - P3-PLAN D1–D28;
  - P4-PLAN D29–D90;
  - SPEC Appendix F: D91–D98 and D76b (the P4 integration and fix rounds; also in the int5 and lead commit messages).

  In P5 the lead decisions are **D99–D141**. The draft numbered them D93–D135; Step 0a moved every one up by 6 (§23 CO-1), because P4 had already defined D93–D98 in SPEC Appendix F. The mapping table is in §23 under CO-1. The critiques amended some of them. D135–D141 (draft D129–D135) are new.
- **Naming.** The wave is called "w6":
  - integration branch `feat/v2-int6`;
  - stream branches `feat/v2-w6-<id>`;
  - worktrees `/home/timkushmar/projects/TrustAgent-w6-<id>`.

---

## 0. How the wave runs

**Step 0a (lead, about 1.75 d).** In this order:
1. the branch, the test database, the free CORS checks (D100) and the memory reading;
2. the contract PR;
3. the Go and build seams commit.

The GO and DX streams start when 0a is done. At the end of 0a, the lead picks the live scope, L or R (D100), and re-baselines the cut line (§16).

**Picked at Step 0a (2026-10-09): scope L, and no cut line.** Every OpenRouter endpoint the browser needs allows CORS from the Pages origin and returns readable error bodies (§2.1, SPEC Appendix D). The owner keeps every task in P5: nothing moves to P6 for capacity (§16, §19).

**Step 0b (lead, about 2 d, while the GO and DX streams run).** It covers:
- the web seams;
- the shared browser gate runner (D135);
- the dataset move (D110);
- Vite plugin registration;
- static test guards, the lint guards and size budgets;
- one dashboard image build.

The web streams start when 0b is done. **The 0a streams run `git merge feat/v2-int6` as soon as 0b lands.** Until then they leave `ci-web.yml` alone.

**Seven streams.** Each stream works on its own branch, `feat/v2-w6-<id>`, in its own worktree. The branch is cut from `feat/v2-int6` after Step 0a (GO, DX) or Step 0b (web).

| id | Stream | Size | Starts |
|---|---|---|---|
| `wlive` | WEB-LIVE: browser key store, caps, transport, errors, Jev and chat clients, gate runner completion, PKCE, static `/connect/callback/`, live Playground, 402 screen | L, about 11 d | after 0b |
| `wsst` | WEB-STUDIO: static Studio (browser drafter, replay Studio plane, export, honest "own stack" steps) | M, about 5.5 d | queue (live wiring needs CP-LIVE) |
| `wland` | WEB-LANDING: TS cost runner and calculator, comparison, Try it, Write your own checks, glass-box panel, self-host, OG meta, landing budget | L, about 7.5 d | after 0b (5th slot) or queue |
| `wrep` | WEB-REPLAY: dataset plugin, richer mock tape, complete ReplayDataSource, replay glass box, honest static states, guide, first-visit spec, `/og` | L, about 6.75 d | after 0b |
| `go` | GO: fault channel and fault scenarios, generated stamp, trafficgen `--record`, knowledge in process, k6 fixes and publishing, `derive_measured`, Tier-0 benchmarks, jevcheck, build SHA | L, about 11.5 d | after 0a |
| `dxrel` | DX-RELEASE: version tooling, images, pull-based quickstart, `pages.yml`, `release-images.yml`, `eval-live.yml`, workflow hardening, `record-demo`, doctor complete, first-run and golden-path | L, about 10.25 d | after 0a |
| `dxdocs` | DX-DOCS: `check-claims`, README hero, docs, ADRs, CHANGELOG, launch drafts, capture tooling | M–L, about 7.5 d (about 5 with DD-3 cut) | part A after 0a (light), part B after CP-REL |

**Concurrency.** The machine has 8 GB. At planning time `free -m` showed 1,271 MB available and 3,834 of 4,096 MB swap used, with the P4 agents alive.
- **Heavy slots:** **4** active stream sessions by default.
- **5th heavy slot:** only while `free -m` shows ≥ 2 GB available **and** ≥ 1 GB swap free.
- **Light slot:** DX-DOCS runs no heavy command, so it may run as an extra session at ≥ 1.5 GB available.
- **What counts:** agent sessions count, not only heavy commands.
- **Readings:** the lead reads memory at Step 0a, and again before every start or restart.
- **Step 0a reading (2026-10-09, P4 agents gone):** `free -m` showed 3,323 MB available and 4,014 of 4,096 MB swap used (81 MB free). Swap is below the 1 GB the 5th slot needs, so **4 heavy slots**, plus the light DX-DOCS slot at ≥ 1.5 GB available. The 5th slot opens only on a later reading that meets both thresholds.
- **Parking:** a stream waiting on a checkpoint commits, ends its session, and the lead restarts it later.
- **Start order:**
  1. GO, DX-RELEASE and DX-DOCS part A, after 0a;
  2. WEB-LIVE and WEB-REPLAY, after 0b, plus WEB-LANDING if the 5th slot is open;
  3. then the queue: WEB-LANDING → WEB-STUDIO → DX-DOCS part B.

**Checkpoints.**
- The lead merges a producer into `feat/v2-int6` once that checkpoint's gate passes.
- Before announcing it, the lead runs the Step-0 gate (§2.6) on int6.
- Consumers then run `git merge feat/v2-int6`. A conflict means an ownership violation, and the work goes back to its owner.

| Checkpoint | Producer, after | Consumers |
|---|---|---|
| **CP-TAPE** | WEB-REPLAY after RP-1 (dataset plugin, richer simulated tape) | WEB-LANDING (LD-5 digest examples), WEB-STUDIO (SS-2 samples), lead (I-5b screenshots) |
| **CP-COSTTS** | WEB-LANDING after LD-1 (TS cost runner, every cost vector passes) | WEB-LIVE (running cost, `{max_spend}`), WEB-STUDIO (estimates) |
| **CP-LIVE** | WEB-LIVE after LV-3 (key store, ledger, transport, Jev and chat clients, completed runner) | WEB-STUDIO (real transport in SS-2, SS-5) |
| **CP-SCEN** | GO after GO-3 (`generated` stamp, trafficgen `--record` and `--trace-ids-out`) | DX-RELEASE (DR-6) |
| **CP-REL** | DX-RELEASE after DR-3 (images, quickstart, version tooling, `pages-build`) | DX-DOCS part B |

**Copy hotfix (D136).** A stream that needs a copy key nobody seeded asks the lead. The lead adds it to int6 the same day, and the stream merges int6.

**Integration (lead):** I-1 to I-10 (§17), plus one mid-wave Docker check, M-1 (§17).

### Rules for every stream

1. **Ownership.** Edit only files you own (§15). New files go only in directories you own, or at paths this plan names.
2. **Frozen after Step 0.** No stream edits these files:
   - **Contracts:**
     - `proto/` (no P5 change);
     - `contracts/{schema,config,ui}/**`;
     - `contracts/claims/{value-claims.v1.json,measured.json}`;
     - `contracts/{superseded.v1.json,validate.mjs,vectors/README.md}`;
     - `contracts/vectors/pkce/**`;
     - `contracts/fixtures/{vendor,prompts,load}/**`.
   - **Go:**
     - `internal/decision/**` (contract-owned);
     - `internal/jev/testheaders.go` (the test-header seam, D119);
     - `internal/version/version.go`;
     - `internal/store/**`;
     - `cmd/proxy/{server,gateway,api,p3_api}.go`.
   - **Web:**
     - `vite.config.ts`, `vite.site.ts`, `playwright.config.ts`, `eslint.config.js`, `tsconfig*.json`;
     - `package.json` and the lockfile;
     - `scripts/check-size.ts`;
     - `src/{main.tsx,env.d.ts,routeTree.gen.ts}`, `src/config/runtime.ts`, `src/types/api.ts`, `src/domain/presentation.ts`;
     - `src/lib/decision/**`, except WEB-LANDING's cost files;
     - `src/lib/{openrouter,gate}/types.ts`, `src/domain/liveScenarios.ts`;
     - `src/components/layout/TopBar.tsx`;
     - `src/test/{setup.ts,lint/**}`;
     - `e2e/static/{site.ts,openrouter.ts,csp-network.spec.ts}`.
   - **Build:**
     - `mk/{go,web,py,contracts}.mk`;
     - `scripts/phase-refs.allow`;
     - the root `.gitignore`.

   If you need a change in one of these, stop and report it. The lead decides. If your change breaks an assertion in a frozen spec (for example `csp-network.spec.ts`), report it; the lead edits the spec at the next merge.
3. **Seams.**
   - These Step-0 items keep fixed signatures:
     - the `lib/openrouter` and `lib/gate` types, including the full `runGate` input (D135);
     - the `virtual:trustagent/*` module ids;
     - the replay plane stubs;
     - the `kinds.ts` stubs;
     - `lib/decision/cost.ts`;
     - the stub components named in §2.4.
   - Their bodies belong to the owners named in §15.
4. **Fixtures and vectors.**
   - `contracts/fixtures/**` is append-only.
   - Vendor fixtures are **synthetic** and say so in their `source` field. They are never presented as captures (D122).
   - A vector moves `draft → frozen` only when the Go reference passes it.
   - GO may only **add** `faultTable` entries (D122).
5. **Copy (D86 and D136).**
   - `contracts/ui/**` is frozen, and Step 0a seeds every P5 key.
   - Extra keys go through the copy hotfix.
   - Prose used only on the landing page stays in `src/landing/**`.
   - New web words go in prefixed modules: `src/domain/words/{live,replay,studio}.ts`.
6. **No paid API calls.**
   - Stream shells never hold a real key. Run tools with `env -u OPENROUTER_API_KEY -u TRUTHTABLE_JEV_API_KEY -u ANTHROPIC_API_KEY -u OPENAI_API_KEY`.
   - Worktree `.env` files come only from `python3 scripts/configure.py --ensure`. Never copy the owner's `.env`.
   - Tests never reach openrouter.ai:
     - vitest fails any unmocked absolute `fetch` (`src/test/setup.ts`);
     - every static Playwright spec calls `blockUnmockedExternal(page)` and, for live paths, `mockOpenRouter(page, …)`;
     - Go tests use `httptest` or mock-upstream.
   - The only real calls to OpenRouter in P5 are the lead's Step 0a CORS checks. They cost $0: no key, or a fake one.
   - WEB-LANDING may read public vendor documentation for the comparison (D113). That is read-only, with no account.
7. **Machine limits.**
   - **The lock.** One heavy command at a time across the machine: `flock /tmp/trustagent-heavy.lock <cmd>`.
   - **What counts as heavy:**
     - every `go test` (including `-bench` and `-fuzz`), `go vet ./...` and `go run ./cmd/proxy`;
     - `npx tsc -b`, `npm run lint`, `npx vitest`, `npm run build*`, `npx playwright test`, `npm ci`;
     - `make check`, `make vectors-ts`, `make recording-check`;
     - `poetry run mypy` and a full `pytest`;
     - every `docker build` or `docker buildx`.
   - **Scope:**
     - `go test -race -p 2` on your packages;
     - fuzzing with `-parallel 2` (`nproc` = 10);
     - `vitest --maxWorkers=2`.
   - **Docker:** none in streams, except `docker compose … config -q`. Docker proofs are the lead's (§17, D141).
   - **Static previews** run on each stream's own port:

     | Port | Who |
     |---|---|
     | 24173 | lead |
     | 24174 | wlive |
     | 24175 | wsst |
     | 24176 | wland |
     | 24177 | wrep |
     | 24178 | dxdocs |

   - **Test DB:** `export TEST_DATABASE_URL=$(/home/timkushmar/.cache/trustagent-orch/pg-url.sh)` (pgserver, no Docker, one schema per test).
   - **Python in worktrees:** `poetry run` maps to `/home/timkushmar/.cache/trustagent-orch/poetry-shim-wt`.
8. **Ports, projects and image tags (lead proofs only, D141).** Never touch the owner's stacks: projects `truthtable` and `truthtable-demo`, ports 3000, 8080 and 5432, and the image tags `:1.0.0` and `:edge`. Every proof exports `TRUSTAGENT_VERSION=p5local`.

   | Project | Proxy | Dashboard | Postgres | Mock-upstream | Used for |
   |---|---|---|---|---|---|
   | `truthtable-p5proof` | 18080 | 13000 | 15432 | – | Proof A (demo-shaped, SPEC gate line 1) |
   | `truthtable-p5first` | 28080 | 23000 | 25432 | 28090 | Proof B (first-run, golden-path, onboarding) and M-1 |
   | `truthtable-p5e2e` | 28080 | 23000 | 25432 | – | e2e and faults; only after `p5first` is down (asserted) |
   | `truthtable-p5load` | – | – | – | – | k6 (no host ports) |
   | `truthtable-p5pull` | – | – | – | – | pull fallback (`up --no-start --no-deps`) |

9. **Secrets.**
   - Fake keys only: `sk-or-v1-e2e-…` and `sk-test-…`.
   - Every leak test also uses the no-pattern secret `cmp0e2eZq7Lw4Rt9Yx2Vb6Nm`.
   - Never print a secret; assert that it is absent.
10. **Honest numbers.**
    - Every new figure carries a label from `value_labels`.
    - Mock or scripted output is never labelled measured.
    - A claim with a caveat shows the caveat next to its chip (D128).
11. **Commits.** Conventional commits with the attribution line. No push, no `VERSION` bump, no tag.

---

## 1. Lead decisions (binding; D99 onward)

### Static site: live mode and Studio

| # | Question | Decision | Owner |
|---|---|---|---|
| D99 | Where "Try Jev live" lives, and how the landing links to it | **Placement:**<br>• in `/app/playground` only, as a `#live` section below the recorded examples;<br>• replay stays primary (SPEC §7.5);<br>• in the full build the Playground is unchanged, and the live panel exists only when `appMode === 'static'`.<br>**`src/domain/liveScenarios.ts`** (frozen, written in Step 0b from the Step 0a results) holds:<br>• `LIVE_SCENARIOS = ['grounded-pass','refund-contradiction-flag','pii-leak-flag']`;<br>• `liveHref(base, id)` → `app/playground/?scenario=<id>#live`;<br>• the capability constants `LIVE_CHECKS`, `CHAT_FROM_BROWSER`, `PKCE_FROM_BROWSER`, `ERRORS_READABLE`, `STATIC_CONNECT_PRIMARY`, `DECISIONS_PATH` and `ALLOWED_REQUEST_HEADERS`.<br>**Landing links:**<br>• the landing shows live links only when `LIVE_CHECKS` is true;<br>• otherwise it shows "Run it yourself: `make demo`", so no dead buttons;<br>• it never imports `lib/openrouter`, `lib/gate` or `components/replay/live` (import test, plus the D112 walk).<br>**Query:** `?scenario=` is honoured only for an id in `LIVE_SCENARIOS` (`validateSearch` in `routes/app/playground.tsx`). | lead, WEB-LIVE, WEB-LANDING |
| D100 | Browser transport, CORS, headers, live scope | **Step 0a checks (free):**<br>• curl preflights from `Origin: https://timkush1.github.io` against `alpha/decisions`, `v1/systemone`, `v1/chat/completions` and `v1/auth/keys`;<br>• a GET+`authorization` preflight for `v1/key`;<br>• a keyless POST that reads ACAO on an error;<br>• **one headless Chromium run** from `http://localhost:24173` that sends the exact header set with `Authorization: Bearer sk-or-v1-e2e-invalid…` to `decisions` and `auth/keys`, and records whether the 401 body is readable;<br>• a read of OpenRouter's public PKCE docs for `key_label`.<br>The results go into SPEC Appendix D and the D99 constants.<br>**Endpoint:** chosen at build time. `DECISIONS_PATH` is `alpha/decisions`, else `v1/systemone` (Appendix D: the bodies are equivalent). There is never a runtime fallback, which would double billable calls.<br>**Scopes:**<br>• **L:** a decision path allows CORS.<br>• **R:** none does. Then `LIVE_CHECKS=false`; the panel shows `playground_live.live_unavailable` with `make demo`; Studio drafting ships only if `CHAT_FROM_BROWSER`; and with `!PKCE_FROM_BROWSER`, Connect is hidden and Paste is primary.<br>The lead picks the scope at the end of 0a. Under R, WEB-LIVE drops the decision run path (about −3 d) and the cut line is re-baselined.<br>**Request:**<br>• base `https://openrouter.ai/api`;<br>• headers exactly `ALLOWED_REQUEST_HEADERS` (`authorization`, `content-type`, `accept`, `http-referer`, `x-openrouter-title`; no `X-Title`);<br>• `HTTP-Referer: location.origin + BASE_URL` (never `VITE_SITE_URL`);<br>• `X-OpenRouter-Title: TrustAgent demo`;<br>• `credentials:'omit'`, `referrerPolicy:'no-referrer'`, `redirect:'error'`, `cache:'no-store'`;<br>• timeouts: decisions 10 s, chat 60 s;<br>• the key is attached only when the URL host is exactly `openrouter.ai`;<br>• no `session_id` and no provider preferences.<br>**Retries:** none automatic and no hedging; a retryable error offers **Retry**.<br>**Errors:**<br>• the `classify.go` taxonomy, mapped to `ApiError` with jev codes and rendered through `playground_live.errors.*` (D136), never the server banners;<br>• a 401 forgets the key automatically;<br>• a `TypeError` becomes `network` with the "browser blocked the request" hint;<br>• if `ERRORS_READABLE` is false, a `TypeError` on a billable call triggers one free `GET /api/v1/key`: no credits → the 402 screen; 401 → key invalid.<br>**P6 switch:** `STATIC_CONNECT_PRIMARY` flips Connect to Paste after the P6 Pages PKCE check (the D88 pattern). | lead (checks, constants), WEB-LIVE |
| D101 | Caps and what counts | **Counts:**<br>• **billable** = a response with status 2xx, or any response carrying `usage`. Only billable calls consume `VITE_PLAYGROUND_MAX_CALLS` (100).<br>• **rejected** = other 4xx or 5xx except 402, with no `usage`. They go to a separate counter and do not consume the cap. After **10 rejected in a row** the panel pauses with "OpenRouter keeps refusing these requests" (Retry, Forget key).<br>• never counted: 402, network, CORS, timeout or abort, a refusal before fetch, the `auth/keys` exchange and `GET /api/v1/key`.<br>**Before every billable fetch, `reserve()` refuses when:**<br>• billable ≥ max;<br>• or an estimate exceeds `VITE_PLAYGROUND_MAX_TOKENS` (8,000): Jev `est_tokens` from `compile()` with budget `min(24000, maxTokens)`, or the chat prompt estimate + `max_tokens`.<br>A multi-request check reserves all of its slots or none; `settle()` releases any slot that did not bill.<br>**State:** `sessionStorage["ta.playground.usage"] {calls, rejected, input_tokens, cost_usd, cost_source}`, per tab. It survives reload and Forget key.<br>**Display:**<br>• running cost from `usage.cost` labelled **vendor**, else TS `priceCall` (CP-COSTTS) labelled **estimate**;<br>• counter: "{n} of {max} calls to OpenRouter (checks and AI drafts)";<br>• consent worst case: "At most ≈ {max_spend} with these caps [estimate]" = `maxCalls × max(priceCall(Jev, maxTokens), priceCall(judge, maxTokens))`.<br>**Honesty:** the caps are a courtesy, not a security boundary (SECURITY.md). The key's credit limit is the real limit. | WEB-LIVE |
| D102 | Browser storage, key lifetime, lint guards | **sessionStorage only:**<br>• `ta.openrouter.key` `{key, stored_at}` (TTL **2 h**);<br>• `ta.pkce.verifier` `{verifier, created_at}` (TTL **10 min**; expired → `callback.expired`);<br>• `ta.pkce.return`;<br>• `ta.playground.usage`.<br>Live mode writes nothing to localStorage, cookies or IndexedDB.<br>**Expiry and restore:** on `pageshow` with `persisted`, the store re-reads sessionStorage and applies the TTL, so a bfcache restore never shows a forgotten or expired key.<br>**Blocked storage:** in-memory fallback plus `playground_live.storage_blocked`. Connect is disabled with that reason, because the verifier would not survive the redirect.<br>**Forget key:**<br>• aborts in-flight requests (`AbortController`);<br>• clears the key, verifier and return path, and keeps usage;<br>• then says "This removes it from this tab; delete it at openrouter.ai/settings/keys".<br>**Key shape:** `^sk-or-[A-Za-z0-9_-]{16,}$`.<br>**Links:** every link that leaves the app (including other `github.io` paths) uses `target=_blank rel="noopener noreferrer"`. A noopener tab gets no copy of sessionStorage.<br>**ESLint (Step 0b):**<br>• one merged `no-restricted-syntax` list per config object: `[...rawEnumSelectors, ...secretKeySelectors, ...linkSelectors]`. `keyStore.ts` has its own object without the secret-key selectors. In flat config, a later object's options **replace** an earlier object's options, so lists are never split across objects.<br>• the secret-key selector bans `^ta\.(openrouter|pkce)\.` literals outside `src/lib/openrouter/keyStore.ts`;<br>• `no-restricted-globals` and `no-restricted-properties` ban `localStorage` and `document.cookie` in `src/lib/{openrouter,gate}/**`, `src/components/replay/live/**`, `StaticConnectCallback.tsx` and `replayStudioPlane.ts`;<br>• the network bans are in D138;<br>• `src/test/lint/` gains fixture tests for every rule, in the style of `rawEnums.test.ts`.<br>**Shared origin:** all of `timkush1.github.io` is one origin. SECURITY.md records it as a known gap, and §21 adds the owner check. | lead (lint), WEB-LIVE |
| D103 | PKCE vectors | **Kind `pkce`, ops:**<br>• `challenge {verifier} → {challenge}`;<br>• `verifier_from_bytes {bytes_hex} → {verifier}` (32 bytes → 43-character base64url, no padding);<br>• `auth_url {callback_url, challenge, key_label?} → {url}`. `key_label` is used only if Step 0a finds it in OpenRouter's docs; otherwise the field is absent from vectors and code.<br>**Vectors:**<br>• RFC 7636 App. B (`dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk` → `E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM`, and its octets → that verifier; the draft's pair was garbled, Step 0a erratum);<br>• all-zero bytes;<br>• all-0xff bytes;<br>• the Pages callback `https://timkush1.github.io/TrustAgent/connect/callback/`.<br>**Go reference (Step 0a):** `internal/decision/vectors/pkce.go`, stdlib only, replaces the skip at `kinds.go:38`. Plus `TestPKCEVectors` in `internal/credentials/pkce_test.go` against `CodeChallenge` (gconn `pkce.go:73`). The vectors freeze in Step 0a.<br>**TS runner:** LV-4.<br>**Erratum:** SPEC §7.6, Go column. | lead, WEB-LIVE |
| D104 | Live policy, evaluators, calibration | **Routes:** `config/policies.yaml` (the recording's policy), parsed at build time into `virtual:trustagent/policy`. No YAML parser ships to the browser.<br>**Evaluators:** `BUILTIN_TEMPLATES` (`lib/decision/templates.ts:11`).<br>**Scenarios:** the three D99 scenarios use only their pack `request`/`response`, never `mock_answers`. The custom form runs on `support-bot`.<br>**Calibration:** `{}` (raw scores), with the chip `playground_live.live_label` ("Live · {snapshot} · uncalibrated · your key"). `require_calibration` downgrades show the existing downgrade words.<br>**Comparison:** a live result is never compared with a scenario `expect`. | WEB-LIVE |
| D105 | Static `/connect/callback/` | **Prerender:** `connect/callback` is in `STATIC_APP_PAGES` (Step 0b), and `main.tsx` skips the replay and stream boot on that path.<br>**Flow:**<br>1. read `code`/`error` from the raw query;<br>2. `history.replaceState` before any fetch;<br>3. never render `error` or `error_description`: they map to fixed states (`access_denied` → `refused`; anything else → `refused`);<br>4. `takePending()`: one-shot, with the 10 min TTL;<br>5. `POST /api/v1/auth/keys {code, code_verifier, code_challenge_method:"S256"}` with no auth header;<br>6. store the key;<br>7. `location.replace(BASE_URL + returnPath)`, so Back never returns to the spent callback.<br>**Return path:** router-relative and checked against `^app/(playground|evaluators/studio)/(\?scenario=(grounded-pass|refund-contradiction-flag|pii-leak-flag))?(#live)?$`. Default `app/playground/#live`. Won's `safeReturnPath` requires `/app/`, so it would refuse every path under `/TrustAgent/`; it is not reused.<br>**Single-flight:** a module-level promise guard, because StrictMode runs effects twice.<br>**Reload after success** (key present, no verifier): show `callback.already_connected`.<br>**Login CSRF:** a code without this tab's verifier is never exchanged.<br>**Connect** is a `<button>`, so a middle-click cannot open the flow without the verifier.<br>**States:** `callback.{working,missing_verifier,expired,refused,network,framed,already_connected}`, each with "Paste a key instead". | WEB-LIVE |
| D106 | Frame-busting | The consent dialog, PasteKey and the callback check `window.top !== window.self`. When framed, they render only "Open in its own tab" (`target=_blank rel="noopener noreferrer"`): no key input and no exchange. A meta CSP cannot set `frame-ancestors`, and Pages sends no `X-Frame-Options`. | WEB-LIVE |
| D107 | Static Studio scope | **Ships:**<br>• templates;<br>• editor (Simple, Advanced, JSON) with local lints;<br>• Test bench through `runGate` (D135) on a one-evaluator route, with `CompileUnsaved` semantics (version = latest + 1) and the real Jev client after CP-LIVE;<br>• the free threshold what-if;<br>• local quick calibration on labels made in the page;<br>• **Draft with AI** through the browser key and `VITE_OPENROUTER_JUDGE_MODEL`, only when `CHAT_FROM_BROWSER` (D108);<br>• **Save** becomes **Copy JSON** / **Download .json** (validated `evaluator.v1`).<br>**Under scope R:** the Test bench offers only the what-if, plus the own-stack card.<br>**Own stack only:** Watch, dry-run, make active and calibrate-for-blocking become one "On your own stack" card with `make demo`.<br>**Samples:** up to 6 bodies from the tape on the chosen route, labelled `studio.static_samples_source`.<br>**Removed:** `StudioStaticNote` and both static `PhasePlaceholder` branches. | WEB-STUDIO |
| D108 | Browser drafter parity and validators | **Port:** `src/lib/studio/drafter.ts` ports py `truthtable/studio/drafter.py`:<br>• `render_user_text:222`, `parse_draft:158`, `check_draft:185`, `render_repair_text:235`;<br>• the repair loop `:284-321`, with one repair.<br>The prompt is `contracts/prompts/evaluator-drafter.v1.json`.<br>**Parity:** Step 0a emits `contracts/fixtures/prompts/drafter-render.{basic,repair,escape}.json` from Python, with `backend-python/tests/unit/test_drafter_fixtures.py` as the drift test. TS must render byte-identical text.<br>**Validator:** `scripts/gen-evaluator-validator.ts` emits the evaluator and draft validators into **one** generated module with shared ajv runtime helpers. That keeps it CSP-safe (no runtime ajv under `script-src 'self'`) and stops a second copy of the helpers inflating a lazy chunk.<br>**Caps:** every turn counts (D101). | WEB-STUDIO, lead (fixtures) |
| D109 | Glass box on replayed traces | **Rebuilt in the browser; no `recording.v1` change.** `ReplayDataSource.getDecisionRequest(id)` runs the TS `compile()` over the recorded request body, with the route from `virtual:trustagent/policy` and specs from the templates.<br>**On `kind: mock`** the badge reads "Rebuilt in your browser · simulated recording" (`recording.glassbox.rebuilt_simulated`), because the simulated tape was itself built by TS `compile()`, so a match proves nothing.<br>**On `kind: recorded`:**<br>• `matches_recorded` = equality with `trace.compile.request_sha256` (`api.ts:212`);<br>• `mismatch_reason`: `body_truncated`, `policy_missing`, `evaluator_missing`, or `null` when the sanitizer changed the body ("differs from the recorded request").<br>**Tests:** the simulated-tape test is named "rebuild is deterministic". TS/Go compile parity is proven by `runner.test.ts` against the Go golden request. | WEB-REPLAY |
| D110 | Datasets: simulated vs recorded | **Sources move out of `public/`:**<br>• `frontend-react/demo-data/simulated/` holds the deterministic mock tape (`npm run recording`; drift-checked by `recording-check` and `demoRecording.test.ts`);<br>• `frontend-react/demo-data/recorded/` holds only a `kind: recorded` export from `make record-demo`, and is absent until P6.<br>**Plugin `vite/plugins/dataset.ts`:**<br>• picks the directory from `VITE_DEMO_DATASET`: `mock` → simulated; `recorded` → recorded, failing if absent; `auto` → recorded if `recorded/manifest.json` exists, else simulated;<br>• copies only that directory to **`dist/demo/`** (`writeBundle`) and serves it at `demo/` in dev (middleware);<br>• fails the build if a `recorded/` manifest is not `kind: recorded`;<br>• exports `virtual:trustagent/dataset` (`{expected}`) instead of a new `VITE_` variable.<br>**Runtime:** every reader uses `demo/manifest.json` and `demo/traces/…`.<br>**Glob users** assert they found at least one file.<br>**Where each value is used:**<br>• CI, static e2e and proofs use `mock`;<br>• `pages.yml` uses `auto` on main and `recorded` on tags, with `check_recording --require-recorded`. | lead (move, Step 0b), WEB-REPLAY (plugin body), DX-RELEASE |
| D111 | Benchmarks path and static visibility | **Path:** SPEC §2.13 wins: `docs/benchmarks/results/<run_id>.json` plus `index.json` (`evals/report.py:509-513`). Step 0b changes wev `benchmarkRuns.ts:7` to `@bench/results/*.json` (a D72 erratum).<br>**Static site:** Benchmarks stays hidden; SPEC §3.3 fixes the static nav at six items.<br>**Load runs:** `docs/benchmarks/load/`, not read by the frontend. | lead |
| D112 | Bundle budgets | **Measuring:** Step 0b re-measures on int6 after P4 I-8 (wsv's Savings and wst's Studio change the chunks) and records the figures in the `scripts/check-size.ts` header.<br>**Blocking budgets:**<br>• landing first load ≤ **110 KB** gzip (SPEC); all landing headroom belongs to WEB-LANDING;<br>• app entry ≤ measured + **6 KB**: WEB-REPLAY 4, WEB-LIVE 1, WEB-STUDIO 1;<br>• every lazy chunk ≤ **130 KB**, or the measured largest + 5 KB if P4 already exceeds 130;<br>• no module from `src/lib/{openrouter,gate}/**` or `src/components/replay/live/**` in the landing first load or the app entry (manifest walk); `LiveKeyChip` only behind a dynamic import;<br>• no `rel=preconnect` or `dns-prefetch` to openrouter.ai in any `dist` HTML, since a preconnect would contact openrouter.ai before consent;<br>• `dist` total ≤ **10 MB**.<br>**Landing growth:** new landing sections are lazy or offset by trims (LD-3). | lead, all web streams |

### Landing and launch kit

| # | Question | Decision | Owner |
|---|---|---|---|
| D113 | Comparison table | **Schema:** `contracts/schema/compare.v1.schema.json` (`trustagent.compare/v1`): columns × rows from `Compare.tsx:14-21`. Each cell is `{value: yes\|no\|partial\|varies\|unverified, text, source_url\|null, as_of\|null, reviewed: boolean}`. The TrustAgent row stays on value claims.<br>**Data file:** Step 0a creates `contracts/claims/compare.v1.json` with every vendor cell `unverified`, `reviewed:false`. **WEB-LANDING owns that data file**; the schema stays frozen.<br>**Research rules:**<br>• a non-`unverified` cell needs a `source_url` the agent actually fetched (public docs only, read-only), with `as_of` = the fetch date;<br>• the text says what the vendor documents, never a ranking;<br>• `no` only when the doc says so; otherwise `varies` with "Not documented (as of …)";<br>• each fetched cell goes into `compare-fetch.tsv` (cell, URL, HTTP status, date) in the checkpoint report;<br>• without web access the cell stays `unverified`.<br>**Rendering:**<br>• `unverified` shows "Not yet verified", never "TODO";<br>• at 400 px the table becomes stacked cards, or a named, focusable scroll region (DESIGN.md:402).<br>**Checks:**<br>• `validate.mjs` enforces the source rule;<br>• `check-claims --launch` refuses any `unverified` or `reviewed:false` cell;<br>• the owner reviews every vendor cell before launch (§21); P6 re-verifies. | lead, WEB-LANDING |
| D114 | Claims, consent copy, commands, config | **Claims** (`value-claims.v1.json`):<br>• `fast_check_cost_per_check`: `$0.0002`, estimate, basis `fast_check_cost_per_1k / 1000`; `validate.mjs` checks the derivation;<br>• `studio_draft_cost_estimate`: `≈ $0.005`, estimate, from `wst estimate.ts`;<br>• caveats on `proxy_overhead_p{50,95}_measured` (D118).<br>**Consent:** the existing `playground_static.consent` is **replaced**, not duplicated, with four lines:<br>1. "Your key stays in this browser tab and is sent only to openrouter.ai. It is cleared after 2 hours or when you press Forget key."<br>2. "Requires credits on your OpenRouter account; one check costs about {cost_per_check} [estimate]; this demo stops after {max_calls} calls to OpenRouter (checks and AI drafts)."<br>3. "What you type and the example documents are sent to OpenRouter and the model's provider (TypeSafe for checks, Anthropic for drafts)."<br>4. "Delete the key any time at openrouter.ai/settings/keys."<br>Plus the worst-case line (D101). Errata on SPEC §7.5 bullets 1 and 2.<br>**Command:** `commands.v1.json` `compose_up` = `python3 scripts/configure.py --ensure && docker compose up -d --wait`.<br>**Registry:**<br>• `VITE_SITE_URL` (default `https://timkush1.github.io/TrustAgent/`), used only for absolute `og:*` and canonical;<br>• `VITE_LAUNCH_ROUTES` (D115);<br>• the `VITE_DEMO_DATASET` description rewritten for D110. | lead |
| D115 | `/og`, hero GIF, screenshots | **`/og` route:**<br>• `src/routes/og.tsx` in the dashboard entry, outside AppShell and nav, `noindex`;<br>• it renders the not-found page unless the build sets `VITE_LAUNCH_ROUTES=1`, which only `npm run og` sets;<br>• it is **not** in `STATIC_APP_PAGES`, so neither the Pages artifact nor the dashboard image serves it;<br>• `OgCard` (lazy) is 1280×640 with 64 px margins, built from `featuredTrace()`; colour only for verdict and outcome.<br>**Capture:** `npm run og` (Playwright project `launch`, `e2e/launch/og.spec.ts`) writes `frontend-react/public/og.png` and `docs/launch/og.png` (< 1 MB). It **refuses** `kind: mock`, unless `OG_ALLOW_MOCK=1`, which stamps a Simulated chip and writes only to `test-results/launch/`.<br>**OG meta:** the landing injects `og:image` only when `public/og.png` exists at build. In P5 it does not.<br>**Hero:**<br>• `e2e/launch/hero.spec.ts` (SPEC §8.2 erratum: was `e2e/hero.spec.ts`);<br>• `scripts/make_hero_gif.py` (ffmpeg two-pass palette, ≤ 8 MB, 1200 px, 12 fps, plus MP4);<br>• both refuse mock unless `HERO_ALLOW_MOCK=1` (scratch only).<br>**Screenshots:** `e2e/launch/screenshots.spec.ts` may use the simulated dataset, captioned "Simulated". | WEB-REPLAY (route), DX-DOCS (capture) |
| D116 | Landing Studio strip and glass-box panel | **Studio strip:** **no invented results.** It shows Describe → Draft (`studio_draft_cost_estimate`) → Test on samples → Watch in shadow as steps with their cost labels, plus "Open the Studio" (`app/evaluators/studio/`).<br>**"What Jev was asked" panel** in `HowItWorks`:<br>• reads a **committed** `src/landing/glassbox.gen.json`, written by `scripts/gen-landing-glassbox.ts` (run with `scripts/lib/aliasHooks.ts`; TS `compile()` over the pack scenario `refund-contradiction-flag`);<br>• `--check` is its drift test, in WEB-LANDING's gate and in `make check` from I-2;<br>• latency and cost use the `hero_card_*` claims (Simulated).<br>**Video:** none in P5. | WEB-LANDING |
| D117 | Replay honesty, guide, first visit | **Overview caveat:** "Scripted showcase mix: rates are not representative of real traffic" (`recording.showcase_caveat`). Counts keep the dataset label.<br>**Guide:**<br>• a dismissible in-page `ReplayGuide` with four steps (the caught answer → "60 days" vs "30 days" → Show exact Jev request → Savings);<br>• step 1 is a link anchored to the `data-featured` row, and the guide never covers that row at 400 px;<br>• its dismissal is stored in localStorage `ta.replay.guide` (a non-secret preference).<br>**Banner:** its words move to `copy.recording.*` and say "simulated" once.<br>**Proven by** `e2e/static/first-visit.spec.ts` (RP-7) and WEB-LANDING's first-viewport checks (LD-8). | WEB-REPLAY, WEB-LANDING |

### Go: load, faults, scenarios

| # | Question | Decision | Owner |
|---|---|---|---|
| D118 | Load-run format and publishing | **Schema:** `contracts/schema/load-run.v1.schema.json` (`trustagent.load-run/v1`):<br>• `run_id`, `created_at`, `git_sha`, `dirty:false`;<br>• `vendors:"simulated (mock-upstream)"`;<br>• `environment{cpu_model, cores, mem_gb, os, kernel, docker, proxy_limits{cpus,mem}, trace_flush, k6_image, rate, duration}`;<br>• `scenarios[{id, route, mode, streaming, n, p50_ms, p95_ms, p99_ms, max_ms, source: header\|metrics\|total, target, ok, report_only, jev{attempts, hedged, mock_requests}}]`;<br>• `errors{rate_5xx, non_200}`.<br>**Files:** `docs/benchmarks/load/<run_id>.json`, written only by `make load-test-gate LOAD_PUBLISH=1` → `scripts/load/report.py`.<br>**`report.py` refuses:**<br>• a dirty tree (`git status --porcelain --untracked-files=no`);<br>• `n < 500`;<br>• a missing environment;<br>• a missed target, unless `--report-only`;<br>• a missing overhead header outside observe.<br>**Headline:** scenario `c` (flag, Jev 0 ms) feeds `proxy_overhead_ms_p50/p95`. `bench-publish` (P6) folds it through `derive_measured` (`system: proxy`, `run_id`). Other modes appear only in the run file and in the generated GATEWAY.md block.<br>**`measured.v1`:** unchanged except its description, which now also names `docs/benchmarks/load`. `measured.json` stays `not_measured` in P5.<br>**Claim caveat** (shown next to the chip, D128): "Gateway's own time with simulated vendors (mock-upstream answers at once); excludes the fast check (~0.2 s [third-party]); {machine}". Proxy overhead is never shown next to the hero's fast-check latency chip.<br>**New scenario `g`:** block, JSON, not streamed, p95 < 10 ms.<br>**Image:** `K6_IMAGE` is pinned in Step 0a. | lead (schema), GO |
| D119 | Fault channel, fault pack, expectations | **Pack:**<br>• a separate pack `contracts/demo/faults.v1.json`, with the same `scenarios.v1` schema and sets `["faults","p5"]`;<br>• the main pack never holds fault scenarios, so Python `test_demo_scenarios.py`, `playgroundModel.ts` "Load an example" and the demo sets never see them;<br>• trafficgen loads it with `--pack faults` (embedded); mock-upstream embeds both packs; `validate.mjs` and `reference_test.go` cover both.<br>**Schema additions:** `mock_faults {decision?, upstream?}` (pattern `^[a-z0-9_]+(@[0-9]+)?$`), `expect.degraded`, `expect.decision_model {attempts?, hedged?, late?}`.<br>**Headers:**<br>• Step 0a moves the test-header forwarding (`http.go:657-659`) into a frozen `internal/jev/testheaders.go`;<br>• it sends `X-TrustAgent-Scenario` and `X-Mock-Key: <trace id>` (attempt and hedge share it) only when `ForwardScenario` is on **and** `localHost()` (`endpoint.go:154-163`: localhost, loopback, single-label; private IPs are not added) accepts the endpoint.<br>**k6 scenario f:**<br>• the gateway's scenario-id regex (`handler.go:218`) drops the `fault:hang@1` id that k6 sends, so forwarding alone does not fix f;<br>• GO-1 therefore accepts, in test mode only, `^fault:[a-z0-9_]+(@[0-9]+)?$` as a **forward-only** value: passed to the decision backend through the seam, never stored as `Trace.Scenario`.<br>**Breaker guard:** at most 4 consecutive vendor-fault scenarios between successes.<br>**Where each fault runs:** stateless F1–F10 and U1–U2 in the faults pack (`make e2e-faults`, plus in process); stateful S1–S6 as in-process tests only.<br>**Expectations:** each row is confirmed against `decide()` in `reference_test.go`. A disagreement is never fixed by editing the expectation silently: the lead rules on it against SPEC §2.12 and GATEWAY.md. | lead (schema, seam), GO |
| D120 | Tier-0 benchmarks (budget statement and guard superseded by D167, SPEC Appendix G) | **Location:** `internal/gate/tier0_{bench,fuzz,budget}_test.go` (package `gate_test`), using the exported `decision.Detect`.<br>**Budget statement:** "Tier-0 p50 < 2 ms for ≤ 32k-token evidence and ≤ 1k-token answers, on the stated machine".<br>**Guard:** `TestTier0Budget` (8k evidence, 1k answer) < 4 ms locally and < 12 ms when `CI=true` (×3 for shared runners). It is skipped under `-race` and `-short`. The 1 MiB case is report-only.<br>**Fuzzing:** `-parallel 2`.<br>**Publication:** GATEWAY.md, labelled "measured (Go benchmark, {cpu})". No new value claim. | GO |
| D121 | Generated answers and recording runs | **Stamp:** `gate.AnswerGenerated` is set in test mode when the pack's `answer_source` is `generated` and there is no `test_response`.<br>**trafficgen:**<br>• `--expect` waits for an unchecked trace to settle;<br>• under `--upstream all` (mock-upstream echoes the scenario `response`), the stand-in expectations apply automatically, with no new flag, so `make e2e-scenarios` needs no recipe change;<br>• `--record` requires a live backend and a non-fallback judge, reports `expect` drift without failing, and writes `--trace-ids-out`.<br>**Sets and phase:** the two generated scenarios join `e2e`, and `defaultPhase = "p5"`. | GO |
| D122 | jevcheck and vendor fixtures | **Image and make:** `jevcheck` and `export-replay` ship in the proxy image (DR-2). `make jev-check` (Step 0a).<br>**Gateway mode:** `--gateway URL --admin-key-file F` calls `POST /api/decision-model/test`. When env has no key, the hint says: "a key connected in the dashboard is not visible here; use --gateway or Settings → Test". The output says the probe is not booked on the cost ledger.<br>**Fixtures:**<br>• P5 vendor fixtures are synthetic, from `mockupstream faultTable` (`faults.go:30-49`) and the SPEC §6 error table, and their `source` says so;<br>• they carry the `access-control-*` headers observed in Step 0a;<br>• `vendor_fixtures_test.go` keeps them equal to `faultTable` **and** to the PKCE, key and chat handlers in `openrouter.go`.<br>**Captures:** `--capture` and real captures are **P6** (owner key, ≤ $0.01). | GO, lead |

### DX, release and docs

| # | Question | Decision | Owner |
|---|---|---|---|
| D123 | `make record-demo` | **Overlay** `docker-compose.record.yml`, project `truthtable-record`:<br>• real backends from `.env`;<br>• `TRUTHTABLE_TEST_MODE=true` (scripted bodies through `test_response`, generated ones through the real upstream);<br>• `TRACE_FLUSH=sync`, `DEMO_MODE=false`;<br>• caps `SPEND_CAP_DECISION_USD_DAY=0.50` and `SPEND_CAP_LLM_USD_DAY=3`;<br>• no mock-upstream and no published DB port. The D119 seam keeps test headers away from openrouter.ai.<br>**`scripts/record_demo.py` steps:**<br>1. dry run by default (plan plus estimate ≈ $1);<br>2. `CONFIRM=1` required;<br>3. preflight `doctor --only decision_model,judge` must show no mock or fallback;<br>4. `trafficgen --record --scenarios demo --exclude knowledge --upstream generated`, twice;<br>5. in-container `/export-replay --since … --source trafficgen`, then `docker compose cp`;<br>6. `check_recording --require-recorded`;<br>7. atomic swap into `frontend-react/demo-data/recorded/`;<br>8. print the label and spend;<br>9. `down -v`.<br>**Strict config:** that it accepts test mode with real backends is proved without Docker by `proxy -check-config` (fake keys). If it refuses, the lead records the exception.<br>**P5** tests it dry; **P6** runs it. | DX-RELEASE |
| D124 | Pages workflow never deploys without the owner | **Triggers:** `push` (main, `v*`), `pull_request` (build only) and `workflow_dispatch` (input `deploy`).<br>**Build job** (always runs):<br>1. `pip install jsonschema==4.26.0`;<br>2. `check_recording` (with `--require-recorded` on tags);<br>3. static build (`VITE_BASE_PATH=/TrustAgent/`, dataset per D110);<br>4. `npm run size`;<br>5. file checks;<br>6. **gitleaks** over `frontend-react/dist` (pinned and checksummed, as in `security.yml`);<br>7. `upload-pages-artifact`.<br>**Deploy job:**<br>• `if:` requires `vars.PAGES_ENABLED == 'true'`, the event not being `pull_request`, and `github.ref == 'refs/heads/main' \|\| startsWith(github.ref, 'refs/tags/v')` on **every** event, plus `inputs.deploy` on dispatch;<br>• `pages: write` and `id-token: write` on this job only;<br>• `concurrency: {group: pages, cancel-in-progress: false}`.<br>Hardening per D140. | DX-RELEASE |
| D125 | Release images | **Workflow:** `release-images.yml`, one job per service, `platforms: linux/amd64,linux/arm64` via QEMU. Go and dashboard cross-compile; only the engine emulates. Native arm runners are used only if the engine takes more than 30 min (recorded).<br>**Timeouts:** 60 min; 90 on tags.<br>**Triggers:**<br>• `push` (main, `v*.*.*`);<br>• `pull_request` with a path filter on Dockerfiles, the workflow and the lockfiles (build only, never push);<br>• dispatch with `push=false` (builds only).<br>**Tags:** `edge` (main), semver, `sha-<short>`.<br>**Tag builds:** `no-cache: true` and `pull: true`.<br>**Attestations:**<br>• BuildKit `provenance: mode=max` and `sbom: true`;<br>• plus `actions/attest-build-provenance` on each pushed digest (`push-to-registry: true`; `attestations: write` and `id-token: write` on the image job only);<br>• release notes list each digest and `gh attestation verify oci://ghcr.io/timkush1/truthtable-<svc>:<v> -R timkush1/TrustAgent`;<br>• no cosign (P7).<br>**Guards:** `github.repository == 'timkush1/TrustAgent'`; `check_version.py --tag` on tags; no `secrets.` in `build-args`.<br>**`first-run` job:** `needs: images`, only when images were pushed. It runs `docker/login-action` (`packages: read`) and pulls `sha-<short>`.<br>**`anonymous-pull` job:** gated on `vars.GHCR_PUBLIC == 'true'`; runs `docker logout` and then pulls every service.<br>**Base images:** pinned by **index** digest (multi-arch) with a version comment, so the existing Dependabot `docker` ecosystem keeps them fresh. | DX-RELEASE |
| D126 | Pull-based quickstart | **Compose default:** `${TRUSTAGENT_VERSION:-<VERSION>}`, a version mirror kept by `bump_version.py`.<br>**Build or pull:** `make demo`/`make up` add `--build` only off a release tag (`RELEASE_CHECKOUT`); on a release tag they pull. Compose builds pass `GIT_SHA` (already on w5-dx).<br>**Engine tag:** `…truthtable-engine:${TRUSTAGENT_VERSION:-…}${ENGINE_EXTRAS:+-${ENGINE_EXTRAS}}`. No knowledge variant is published; the knowledge profile builds locally (documented).<br>**Missing tag:** I-7 proves how plain compose behaves when the tag does not exist (`up --no-start --no-deps`). If compose fails instead of building, GETTING-STARTED's "without make" path says to run `docker compose build` first. | DX-RELEASE |
| D127 | Version tooling | **Mirrors:**<br>• `VERSION`;<br>• `pyproject.toml:3`;<br>• `truthtable/__init__.py:4`;<br>• `package.json:4`;<br>• `package-lock.json` (`version`, `packages[""].version`);<br>• `internal/version/version.go:7`;<br>• the compose default tag.<br>**README:** the version badge becomes the dynamic GitHub release badge.<br>**`bump_version.py X.Y.Z`:** refuses a downgrade or non-semver, renames the CHANGELOG `Unreleased — X.Y.Z` heading, and re-runs `npm run recording`.<br>**`check_version.py [--tag vX.Y.Z]`.**<br>**No bump in P5** (P6 step 7). | DX-RELEASE |
| D128 | `check-claims` | **Scan:** `README.md`, `docs/**/*.md` and `docs/launch/*.md` for `<!-- claim:ID -->`.<br>**Rules:**<br>• the ID exists with surface `readme` or `docs`;<br>• the text that follows holds `display` (or `empty_display` while the measured value is null) plus a label chip `[estimate\|vendor\|third-party\|measured\|simulated]`;<br>• a claim with a `caveat` has the caveat text, or a link to its anchor, within 3 lines after the marker;<br>• a measured value is allowed only from `measured.json` with `mock:false`;<br>• `third_party` older than 120 days warns.<br>**Flags:**<br>• `--write` regenerates `<!-- measured:start/end -->`;<br>• `--launch` refuses a missing or mock `recorded/` manifest, `not_measured`, any `unverified` or `reviewed:false` compare cell, or a missing `og.png`;<br>• `--self-test` (one fixture per rule).<br>**Wiring:** `make check` and `ci-contracts` (I-2). | DX-DOCS |
| D129 | Doctor complete | **Default stays `--no-probe`** (the gate).<br>**`--probe`:** opt-in, needs `--admin-key-file`, ≈ $0.00001, maps to the SPEC §6 error table, and is never used in gates.<br>**New checks:**<br>• RPM headroom;<br>• add-on profiles (knowledge, local-llm, observability, redis);<br>• environment (Docker ≥ 25, Compose ≥ 2.24, memory < 4 GB warns, busy ports, `.env` mode 0600);<br>• images (one version, matching `org.opencontainers.image.revision` labels, pulled vs built).<br>**New modes:** `--expect fresh`; `--target pages --pages-url`; read key from a file.<br>**Never prints a key.** | DX-RELEASE |
| D130 | first-run and golden-path in P5 | **P5 proof:** a fresh stack from **locally built** images `ghcr.io/timkush1/truthtable-<svc>:p5local` (built once at I-6, never pulled).<br>• `FIRST_RUN_T0` is taken before `up`. The assertion is < 5 min, labelled "measured locally, images prebuilt".<br>• golden-path: ≤ 8 actions from the setup link to the flagged trace's detail, in simulated mode, with no shell-outs and no logs.<br>• the refactored onboarding spec re-runs on the same fresh stack.<br>• **M-1:** the lead runs `make first-run` once mid-wave, after DR-9, while at most 3 streams are active.<br>**P6:** the pulled-image timing runs in the `release-images.yml` `first-run` job.<br>**Shared helpers:** `e2e/stack/journey.ts`, used by onboarding, first-run and golden-path. | DX-RELEASE, lead |
| D131 | Knowledge Docker proof (D65) | **The Docker run moves to P6.** It needs a CPU-torch engine image, Qdrant and a model download, on a laptop with nearly full swap. The first run happens in CI (`ci-knowledge.yml`, dispatch) after the P6 push.<br>**P5 keeps:**<br>• GO-6 in process (`TestKnowledgeScenarioEndToEnd`);<br>• `config/policies.knowledge.yaml`, **generated** from `policies.yaml` + the `handbook` route of `policies.pending.yaml`, with `TestKnowledgePolicyInSync`;<br>• `trafficgen --seed-knowledge --admin-key-file F`.<br>**Cut line (DR-10):** the overlay `docker-compose.knowledge.yml` (`COMPOSE_PROFILES=knowledge`, `ENGINE_EXTRAS=knowledge`, `TRUTHTABLE_KNOWLEDGE_ENABLED=true`, Jev on mock-upstream, mock judge), the `knowledge-{up,seed,scenarios,down}` targets and the non-required `ci-knowledge.yml`. | GO, DX-RELEASE |
| D132 | Make fragments and shared build files | **Fragments:**<br>• `mk/load.mk` (GO; Step 0a moves the load block out of `dx mk/dx.mk:236-282`);<br>• `mk/release.mk` (DX-RELEASE: release, recording and doctor targets);<br>• `mk/launch.mk` (DX-DOCS).<br>The root `Makefile:72` includes `mk/*.mk` automatically.<br>**Lead-only in Step 0:** `mk/{go,web}.mk` gain `jev-check`, `bench-go`, `bench-go-smoke` and the D110 paths. `mk/dx.mk` gains `e2e-faults`, then belongs to DX-RELEASE.<br>**Stub overlays:** `docker-compose.{record,knowledge}.yml` start as valid stubs listed in `compose-check` and `test_compose.py`.<br>**CI turn-on edits** to `ci-{go,e2e,web,contracts}.yml` are the lead's at I-2. | lead |
| D133 | Deferrals | **P6:**<br>• `jevcheck --capture` and real vendor fixtures;<br>• the Pages PKCE check;<br>• GHCR visibility;<br>• the `record-demo` run;<br>• OG and GIF from a recorded tape;<br>• compare re-verification and owner review;<br>• the knowledge Docker run (D131);<br>• cut-line items, if cut.<br>**P7:**<br>• a shared PKCE nonce store and shared caps for multi-replica (D38; documented in SECURITY.md);<br>• re-scoring label samples on a new snapshot (paid);<br>• the #66 zip bundle (record-demo uses the in-image `export-replay`);<br>• cosign;<br>• a recorded Studio session. | lead |
| D134 | End-of-P5 phase grep | `rg -n '\bP5\b\|in P5\|P5_' frontend-react/src backend-go backend-python/src scripts contracts \| grep -v -F -f scripts/phase-refs.allow` prints nothing.<br>The allow-list keeps only SPEC, archive and P6/P7 pointers. Lowercase scenario phase tags such as `"p5"` are legitimate.<br>The Step 0a rewrite of `contracts/vectors/README.md:33,37-38,301` already removes its "P5" wording, because the file is frozen afterwards. | lead (I-3) |

### New decisions

| # | Question | Decision | Owner |
|---|---|---|---|
| D135 | One browser gate pipeline | **Extraction (Step 0b, lead):** the TS pipeline already inside the recorder (`demoRecording.ts:198-248` `resolveRoute`/`specMap`, and the evidence → detect → compile → decide part of `buildTrace:358-437`) moves into `src/lib/gate/{route,runner}.ts` behind the frozen signature:<br>`runGate(input: {route: ResolvedRoute; specs: ReadonlyMap<string, Spec>; request: unknown; apiFormat: ApiFormat; call: JevCall; ledger: UsageLedger; maxTokens: number; calibration?: CalibrationMap; clock?: () => number}) → Promise<BrowserGateResult>`.<br>**Proof of a behaviour-preserving move:** the recorder calls it with a scripted `JevCall` (the scenario's `mock_answers`), and `make recording-check` stays byte-identical.<br>**WEB-LIVE completes it in LV-3:**<br>• gstudio `Runner.Evaluate` parity: decide-evaluators, reserve-before-call, budget `min(24000, maxTokens)`;<br>• the error paths;<br>• `playgroundResult.ts`.<br>**Drift:** any WEB-LIVE change that alters the tape fails WEB-LIVE's gate (`make recording-check`) and goes to the lead.<br>**Consumers:** WEB-STUDIO's test bench uses `runGate` from day one with a fake `JevCall`. | lead, WEB-LIVE |
| D136 | Copy namespaces and words | **Namespace:** new web copy goes in `playground_live.*`. The landing imports the `playground_static` subtree (`content.ts:15`), so new keys there would cost landing bytes. `playground_static` keeps only consent, `forget_key`, `running_cost` and `cap_reached` (reworded per D114).<br>**`playground_live.errors.*`:** browser wording for `unauthorized`, `no_credits`, `credits_inflight`, `data_policy`, `rate_limited`, `too_large`, `moderation`, `bad_response`, `network` and `timeout`. Actions: "Paste a new key", openrouter.ai/settings/privacy, openrouter.ai/settings/credits, Retry. No string mentions `TRUTHTABLE_`, `Settings →`, `.env` or `make`.<br>**Words modules:** Step 0b creates `src/domain/words/{live,replay,studio}.ts`, re-exported from `presentation.ts` with prefixes (D86).<br>**Copy hotfix:** same day, on request (§0). | lead, web streams |
| D137 | Build-time guards | **Secret-name guard:** `vite.config.ts` fails any build in which a `VITE_*` name matches `/KEY\|TOKEN\|SECRET\|PASSWORD/`, except the explicitly allowed `VITE_PLAYGROUND_MAX_TOKENS`.<br>**Runtime clamps** (`runtime.ts`, zod/mini):<br>• `maxCalls` 1–100;<br>• `maxTokens` ≤ 8,000;<br>• `jevModel` must match `^typesafe/jev-`, `judgeModel` `^anthropic/claude-haiku`;<br>• all four come only from build-time `import.meta.env`, never from `config.json` or the URL.<br>**Pages artifact:** `pages.yml` and `make pages-build` run gitleaks over `dist` (D124). | lead, DX-RELEASE |
| D138 | Network invariant | **Network lint:** `no-restricted-globals`/`-properties` for `fetch`, `window.fetch`, `globalThis.fetch`, `XMLHttpRequest`, `WebSocket`, `EventSource` and `navigator.sendBeacon` across `src/**`, outside this allow-list:<br>• `src/config/runtime.ts`;<br>• `src/data/http.ts`;<br>• `src/data/live/**`;<br>• `src/data/replay/{replayDataSource,recording}.ts`;<br>• `src/lib/openrouter/http.ts`;<br>• `src/landing/TryIt.tsx` until LD-5 removes its fetch (the lead drops it from the list at I-3).<br>**Mocked OpenRouter:** `e2e/static/openrouter.ts` (Step 0b, frozen) **fails** any routed request whose header names fall outside `ALLOWED_REQUEST_HEADERS`. A test mock fulfils preflights itself, so a new header would otherwise pass in tests and break on Pages.<br>**Unit check:** `http.test.ts` asserts that the headers sent equal that set.<br>**Images:** `img-src 'self' data:` already blocks external images, so there is no image lint. | lead, WEB-LIVE |
| D139 | A non-streamed block scenario | The TS recorder refuses streamed scenarios (`demoRecording.ts:764`), and the only block scenario (`help-center-link-block`) is streamed. Step 0a adds **`help-center-link-block-json`**: `stream:false`, sets `["p3","demo","e2e"]`, otherwise the same as the streamed one, confirmed by `reference_test` and `TestDemoScenariosEndToEnd`.<br>**Counts:**<br>• demo 26 → **27** (p3 6 → 7);<br>• e2e 23 → **24**, then **26** when GO-3 adds the generated pair.<br>`setSizes` (`scenarios_e2e_test.go:155`), `contracts/README.md` and any Python count change with it. | lead |
| D140 | Privileged workflow hardening | **Applies to** `pages.yml`, `release-images.yml`, `eval-live.yml` and `ci-knowledge.yml`:<br>• every `uses:` is pinned to a 40-hex SHA with a `# vX.Y.Z` comment (the existing Dependabot `github-actions` ecosystem updates them);<br>• `persist-credentials: false` on every checkout;<br>• top-level `permissions: {}`; each job grants only what it needs;<br>• no `pull_request_target`;<br>• no `${{ github.event.* }}` or `${{ inputs.* }}` inside `run:` (they go through `env:`);<br>• no `secrets.` in the Pages build job or in `build-args`.<br>**Lint:** `make lint-workflows` runs actionlint (`go run …@<pin>`) and **zizmor** (pip `==<pin>`, high-severity audits blocking).<br>**Tests:** `test_workflows.py` asserts each rule per file. | DX-RELEASE |
| D141 | Proof isolation | **Image tags:** every Docker proof exports `TRUSTAGENT_VERSION=p5local`, so it never rebuilds or overwrites `:1.0.0` or `:edge`, which the owner's stacks resolve (`pull_policy: missing`).<br>**Builds:**<br>• images are built once, at I-6; I-7 and I-8 reuse them (`E2E_BUILD=`, `LOAD_BUILD=`);<br>• the Step 0b image check uses a scratch tag `trustagent-p5check/dashboard:step0` and removes it.<br>**Ports and projects:** projects and ports as in Rule 8. Before `p5e2e` reuses 28080/23000/25432, the lead asserts that `p5first` is down.<br>**Clean-up:** at I-10 the lead removes every `p5local`, `0.0.0-none` and scratch image. | lead |

---

## 2. Step 0 (lead)

### 2.1 Step 0a: branch, test database, free checks

1. After P4 I-8, create the branch and its worktree:
   ```bash
   git worktree add /home/timkushmar/projects/TrustAgent-int6 -b feat/v2-int6 feat/v2-jev-gateway
   cd /home/timkushmar/projects/TrustAgent-int6 && python3 scripts/configure.py --ensure
   export TEST_DATABASE_URL=$(/home/timkushmar/.cache/trustagent-orch/pg-url.sh)
   free -m      # sets the slot count (§0)
   ```
2. **Free CORS checks (D100).** $0: no key, or a fake one.
   ```bash
   H='authorization,content-type,accept,http-referer,x-openrouter-title'
   for p in alpha/decisions v1/systemone v1/chat/completions v1/auth/keys; do echo "== $p"
     curl -s -o /dev/null -D - -X OPTIONS "https://openrouter.ai/api/$p" -H 'Origin: https://timkush1.github.io' \
       -H 'Access-Control-Request-Method: POST' -H "Access-Control-Request-Headers: $H" | grep -iE '^HTTP|^access-control'; done
   curl -s -o /dev/null -D - -X OPTIONS https://openrouter.ai/api/v1/key -H 'Origin: https://timkush1.github.io' \
     -H 'Access-Control-Request-Method: GET' -H 'Access-Control-Request-Headers: authorization' | grep -iE '^HTTP|^access-control'
   curl -s -o /dev/null -D - -X POST https://openrouter.ai/api/alpha/decisions -H 'Origin: https://timkush1.github.io' \
     -H 'Content-Type: application/json' -d '{}' | grep -iE '^HTTP|^access-control-allow-origin'
   ```
   Then run the browser check:
   - serve a blank page from the scratchpad on 24173 (`python3 -m http.server 24173`);
   - from Playwright Chromium, `fetch` `DECISIONS_PATH` candidates and `auth/keys` with the exact header set and `Authorization: Bearer sk-or-v1-e2e-invalid0000000000`;
   - record status, readability of the body and ACAO.

   Also read OpenRouter's public PKCE documentation for `key_label` (D103).

   Then:
   - add a SPEC Appendix D row "OpenRouter browser CORS (decisions, systemone, chat, auth/keys, key; error readability)" with the results and the date;
   - write the D99 constants for Step 0b;
   - **pick scope L or R** (D100) and re-baseline the cut line (§16).
   **Results (Step 0a, 2026-10-09; $0, fake key `sk-or-v1-e2e-invalid…` only):**
   - every curl preflight (`alpha/decisions`, `v1/systemone`, `v1/chat/completions`, `v1/auth/keys`, and the GET+`authorization` preflight of `v1/key`) answered `204` with `access-control-allow-origin: *`, `access-control-allow-methods: GET,OPTIONS,PATCH,DELETE,POST,PUT`, and an `access-control-allow-headers` list that holds all five `ALLOWED_REQUEST_HEADERS` (`Authorization`, `Content-Type`, `Accept`, `HTTP-Referer`, `X-Openrouter-Title`); `access-control-expose-headers: X-Generation-Id,X-Provider-Name,request-id,cf-ray`;
   - the keyless POST to `alpha/decisions` answered `401` with `access-control-allow-origin: *`;
   - headless Chromium from `http://localhost:24173` with the exact header set: `alpha/decisions` 401, `v1/systemone` 401, `v1/chat/completions` 401, `GET v1/key` 401 (body `{"error":{"message":"User not found.","code":401}}`), `v1/auth/keys` with an invalid code 400 (`{"error":{"message":"Invalid code","code":400}}`); every body was readable (`application/json`);
   - OpenRouter's public PKCE page (`openrouter.ai/docs/use-cases/oauth-pkce`) documents `key_label` ("Prefills the label of the API key that will be created"), so the D103 `auth_url` op carries `key_label`.

   **Constants for Step 0b** (`src/domain/liveScenarios.ts`): `LIVE_CHECKS = true`, `CHAT_FROM_BROWSER = true`, `PKCE_FROM_BROWSER = true`, `ERRORS_READABLE = true`, `DECISIONS_PATH = 'alpha/decisions'`, `STATIC_CONNECT_PRIMARY = 'connect'` (from the CORS result alone, never from D88; the P6 Pages PKCE check may flip it to `paste`, §23 CO-14), `ALLOWED_REQUEST_HEADERS` as in D100. **Scope L.**

   **Test database (substitution, recorded at Step 0a):** wherever this plan or a stream gate names a Docker test Postgres, the wave uses the local PostgreSQL 16 from `export TEST_DATABASE_URL=$(/home/timkushmar/.cache/trustagent-orch/pg-url.sh)` (pgserver, no Docker; `internal/storetest` gives each test its own schema).

3. **Confirm P4 §19.** At P4 I-1, check that no cut-line item slipped: GW-1, GW-2, GW-3, COST-8, DX-6 and EVW-6 all exist on their branches.

### 2.2 Step 0a: contract PR

**a. Schemas** (then `make gen-types`):
- `scenarios.v1`: `mock_faults`, `expect.degraded` and `expect.decision_model` (D119); the sets description gains `faults`.
- New `load-run.v1` (D118, including `jev{attempts, hedged, mock_requests}`).
- New `compare.v1` (D113, including `reviewed`).
- `measured.v1`: description only (D118).

All of them go into `contracts/README.md` and `embed.go`.

**b. `validate.mjs`:**
- validate `contracts/demo/faults.v1.json` with the main pack's rules; the `faults` set appears only in that file;
- the consecutive-fault guard;
- compare cells need `source_url` and `as_of` unless `unverified`; `reviewed` must be boolean;
- `fast_check_cost_per_check` = `fast_check_cost_per_1k`/1000;
- `contracts/fixtures/load/load-run.example.json` validates;
- self-test fixtures for each rule.

**c. Registry.** Add `VITE_SITE_URL` and `VITE_LAUNCH_ROUTES`, rewrite the `VITE_DEMO_DATASET` description (D110), then run `make config-docs`.

**d. Copy seeds** (`contracts/ui/copy.v1.json`, D86 and D136):
- `playground_static.consent`: replaced with the four D114 lines, using `{cost_per_check}` and `{max_calls}`.
- `playground_static.cap_reached`: reworded to "calls to OpenRouter (checks and AI drafts)".
- `playground_live`:
  - `connect`, `paste_key`, `key_invalid`, `key_no_limit`, `storage_blocked`;
  - `token_cap`, `frame_blocked`, `live_label`, `live_unavailable`, `browser_blocked`;
  - `run_check`, `checks_used`, `retry_busy`, `rejected_pause`, `forget_note`, `worst_case`, `check_again`;
  - `deep_audit_not_in_browser`, `recording_is_simulated`, `run_locally`;
  - `callback.{working,missing_verifier,expired,refused,network,framed,already_connected}`;
  - `errors.*` (D136).
- `studio.static_{export_copy,export_download,needs_stack,draft_needs_key,samples_source}`.
- `disabled_reasons.static`.
- `recording`:
  - `banner_simulated`, `banner_recorded`, `showcase_caveat`, `nav_hint_caught`;
  - `guide.{title,step1,step2,step3,step4,dismiss}`;
  - `glassbox.{rebuilt_simulated,matches,differs,mismatch.{body_truncated,policy_missing,evaluator_missing}}`.
- `launch.og.*` (the card's words).
- `compare.not_verified`.
- `studio.static_soon` is removed at I-3, after WEB-STUDIO lands.

**e. Claims and commands.**
- New claims `fast_check_cost_per_check` and `studio_draft_cost_estimate`.
- Caveats on `proxy_overhead_p{50,95}_measured` (D118).
- `commands.v1.json` `compose_up` (D114).

**f. Vectors.**
- `contracts/vectors/pkce/*.json` (4 files, D103).
- `internal/decision/vectors/pkce.go` and the `kinds.go:38` map entry, plus `TestPKCEVectors` in `internal/credentials/pkce_test.go`. Freeze once Go passes.
- At least 4 new `cost/per_1k` vectors from `cmd/vectorgen`, covering: flag rate 0, 5 and 20%; sampling off; a judge price override; 1k and 20k tokens. All frozen.
- Rewrite the `vectors/README.md` rows: pkce has a Go stdlib reference plus TS; cost now has TS. No "P5" wording (D134).

**g. Fixtures.**
- **Vendor:** `contracts/fixtures/vendor/openrouter/` with these files, each carrying `{source:"synthetic: mock-upstream faultTable + SPEC §6", status, headers (with the observed access-control-*), body}`:
  - `decisions.{200,402_credits,402_inflight,401,403_moderation,404_data_policy,413,429,200_html}.json`;
  - `auth_keys.{200,400,403}.json`;
  - `chat.200_usage_cost.json`;
  - `key.200.json`.

  `cmd/mock-upstream/mockupstream/vendor_fixtures_test.go` (`-update`) keeps them equal to `faultTable` and to the handlers in `openrouter.go`.
- **Prompts:** `contracts/fixtures/prompts/drafter-render.{basic,repair,escape}.json`, plus `backend-python/tests/unit/test_drafter_fixtures.py` (D108).
- **Load:** `contracts/fixtures/load/load-run.example.json`.

**h. Compare data.** The `contracts/claims/compare.v1.json` skeleton, every vendor cell `unverified`, `reviewed:false`.

**i. Demo packs.**
- Add `help-center-link-block-json` (D139). Update `setSizes` to demo `{27, 7}` and e2e `{24, 6}`, plus `contracts/README.md`.
- Create an empty `contracts/demo/faults.v1.json` (`scenarios: []`), which GO fills.

**j. Recording check fix.** `scripts/check_recording.py` keeps RFC 2606/6761 reserved e-mail domains, mirroring Go `reservedHost` (`internal/recording/sanitize.go:161`), with tests in `scripts/tests/test_check_recording.py`.

Without this fix, RP-1 would fail `check_recording`: `pii-leak-flag` answers with `jordan.miller@example.net`, and the e-mail rule (`check_recording.py:52`) applies to every kind.

**k. SPEC.** Appendix D:
- add the CORS row;
- move "PKCE accepts the Pages callback", "GHCR multi-arch publishing permissions" and "Pages availability" to **P6 (owner)**.

**l. Ignore and dockerignore.**
- `.gitignore` += `backend-go/bench/`.
- `frontend-react/Dockerfile.dockerignore` += `!config/` with only `config/policies.yaml`. The policy plugin reads it, and the full build bundles the replay code (`main.tsx:30-38` chooses at runtime).

### 2.3 Step 0a: Go and build seams commit

The only behaviour changes are the restriction of test headers to local hosts and the engine-less load overlay.

- **`internal/jev/testheaders.go`** (new, frozen):
  - `forwardTestHeaders(h http.Header, endpoint *url.URL, opt CallOptions, enabled bool)` sets `X-TrustAgent-Scenario` and `X-Mock-Key: <opt.TraceID>` only when `enabled && localHost(endpoint.Hostname())`;
  - `http.go:657-659` calls it.
  - Tests in `testheaders_test.go`:
    - attempt and hedge share the key;
    - no header when forwarding is off;
    - no header for `openrouter.ai`;
    - no header for a private IP (D119).
- ~~`internal/version/version.go`~~ dropped at Step 0a (CO-2): P4 already has `version.GitSHA` and `resolveBuildSHA` (`3c6ef7d`).
- **`mk/load.mk`:**
  - move the load block of `dx mk/dx.mk:236-282` here;
  - `K6_IMAGE ?= grafana/k6:<pinned x.y.z>`, `LOAD_PROJECT ?= truthtable-load`, `LOAD_OUT ?= tmp/load`.
- **`docker-compose.load.yml`:** `TRUTHTABLE_GRPC_ADDRESS: ""` and no engine `depends_on` (`cmd/proxy/main.go:95` supports this).
- **`mk/go.mk`:**
  - `jev-check`: `docker compose exec -T proxy /jevcheck` when a stack runs, else `go -C backend-go run ./cmd/jevcheck`;
  - `bench-go`: `-run '^$' -bench . -benchmem -count 5`, output to `backend-go/bench/<sha>.txt`, which is ignored;
  - `bench-go-smoke`: `-benchtime 1x`.
- **`mk/dx.mk`:** `e2e-faults` posts to `mock-upstream:8090/_mock/reset` from inside the proxy container, then runs `trafficgen --pack faults --scenarios faults --expect --upstream all`.
- **Empty fragments** `mk/{release,launch}.mk`, each with a `##@` header.
- **Overlay stubs** `docker-compose.{record,knowledge}.yml` (`services: {}` plus a header), registered in `compose-check` and `scripts/tests/test_compose.py`.

### 2.4 Step 0b: web seams, runner, dataset move, guards, budgets (lead, about 2 d)

1. **Config (D137).**
   - `src/env.d.ts` declares:
     - `VITE_PLAYGROUND_MAX_CALLS`, `_MAX_TOKENS`, `VITE_JEV_MODEL`, `VITE_OPENROUTER_JUDGE_MODEL`, `VITE_SITE_URL`, `VITE_LAUNCH_ROUTES`;
     - the `virtual:trustagent/{policy,dataset,tape-digest,landing-data}` modules.
   - `src/config/runtime.ts` gets `playground {maxCalls, maxTokens, jevModel, judgeModel}` (zod/mini, clamps and model patterns) and `siteUrl`, both build-time only.
   - `vite.config.ts` gets the secret-name guard.
2. **Frozen types.**
   - **`src/lib/openrouter/types.ts`:**
     - `OpenRouterErrorKind` (the `jev.go:168-184` codes);
     - `JevCall` and `JevCallResult {answers, usage{input_tokens, cost}, snapshot, latency_ms}`;
     - `ChatCall` and `ChatResult`;
     - `UsageLedger {reserve(est[]), settle(status, usage?)}`;
     - `BrowserKeyStore`.
   - **`src/lib/gate/types.ts`:** the D135 `runGate` input and `BrowserGateResult → PlaygroundResult` (`api.ts:1416`).
3. **Runner extraction (D135).** Create `src/lib/gate/{route,runner}.ts`. The recorder calls `runGate` with a scripted `JevCall` and a no-op ledger. `make recording-check` must show no diff. WEB-LIVE owns these files afterwards.
4. **`src/domain/liveScenarios.ts`** (D99) with the Step 0a constants.
5. **`kinds.ts:285-286`.**
   - `cost: costKind` from `./cost`: the stub keeps the skip text, and `src/lib/decision/cost.ts` gets the signatures `priceCall`, `lookupEffective`, `costPer1k` and `globMatch`, each throwing "LD-1".
   - `pkce: pkceKind` from `./pkce` (a stub with skip).
6. **Plugins and virtual modules.**
   - Register **five** plugin stubs in `vite.config.ts` (`:33-41`; that file also feeds vitest): `frontend-react/vite/plugins/{policy,dataset,digest,landingData,ogMeta}.ts`.
   - `landingData` exports the one-string example policy (replacing `content.ts:9 ?raw`) and the `outcomes.v1` subset.
   - Add `vite/**` to the `tsconfig.node.json` include.
   - Owners are in §15.
7. **`vite.site.ts`.** `STATIC_APP_PAGES` (`:30`) += `connect/callback` and `app/savings`. `og` is not added (D115).
8. **Dataset move (D110).**
   - `git mv public/demo/recorded demo-data/simulated`. The dataset plugin body copies the chosen directory to `dist/demo/` and serves it in dev.
   - Edit every reader to `demo/`:
     - `recording.ts:9,83`;
     - `landing/content.ts:32`;
     - `e2e/static/site.ts:45`;
     - `e2e/static/csp-network.spec.ts:54`;
     - `components/replay/staticBuild.test.tsx:137`;
     - `data/replay/replay.test.ts:196`.
   - Point the glob users at `demo-data/simulated/**` with a non-empty assertion: `src/test/recording.ts:8,25,30` and `data/replay/recorder/demoRecording.test.ts:35`.
   - Producers and docs:
     - `scripts/build-demo-recording.ts:31`;
     - `mk/web.mk:78,91`;
     - `.github/workflows/ci-web.yml:80`;
     - `scripts/check_recording.py:4`;
     - `cmd/export-replay/main.go:17`;
     - `DESIGN.md:377`.
9. **Benchmarks path (D111).** wev `benchmarkRuns.ts:7` → `@bench/results/*.json`.
10. **Replay plane seams.** `src/data/replay/{replayStudioPlane,replayEvaluatorReads}.ts` are stubs that keep today's `unavailable*` behaviour, wired into `replayDataSource.ts`.
11. **Routes, slots and stub components.**
    - `src/routes/og.tsx`: not-found unless `VITE_LAUNCH_ROUTES`, with a lazy stub `components/launch/OgCard.tsx`. Regenerate `routeTree.gen.ts`.
    - `routes/connect/callback.tsx` branches on `useIsStatic()` to a stub `components/setup/StaticConnectCallback.tsx`.
    - `main.tsx:30-41` skips the replay and stream boot on the callback path.
    - `TopBar.tsx:32` renders `lazy(() => import('@/components/replay/live/LiveKeyChip'))` inside `<Suspense fallback={null}>`, only when static. The stub returns null.
    - Stub signatures under `components/replay/live/`:
      - `ConsentDialog({open, onClose, returnTo})`;
      - `PasteKey({onDone})`;
      - `useBrowserKey(): {key, state, forget}` in `lib/openrouter/useBrowserKey.ts`.
    - Export `JevRequestBody` from wev `components/trace/JevRequestPanel.tsx`.
    - Create the empty `src/domain/words/{live,replay,studio}.ts` and re-export them (D136).
12. **Test guards and the launch project.**
    - `src/test/setup.ts` fails any unmocked absolute `fetch`.
    - `e2e/static/site.ts` gains `blockUnmockedExternal(page)`, used by the existing static specs.
    - New `e2e/static/openrouter.ts`: `mockOpenRouter(page, fixtures)` over `page.route('https://openrouter.ai/**')`. It:
      - serves the vendor fixtures;
      - answers the `/auth?…` navigation with a 302 to `connect/callback/?code=…`;
      - records each request's method, URL, headers and body;
      - fails on header names outside `ALLOWED_REQUEST_HEADERS` (D138).
    - Remove the "Coming soon" assertions at `staticBuild.test.tsx:142,160` and `e2e/static/replay.spec.ts:84-86`. Their owners add real ones.
    - `playwright.config.ts`: a `launch` project (testDir `e2e/launch`), and `wantsStaticPreview` (`:61-67`) also starts the preview for `launch`.
    - `package.json` scripts: `og`, `hero`, `screenshots`.
13. **`eslint.config.js`:** D102 merged lists and D138 network bans. Extend `src/test/lint/` with fixtures proving that each of these fires in `components/replay/live/X.tsx`: a raw enum, a `ta.openrouter.` literal, `window.localStorage`, `document.cookie`, a bare `fetch`, and `target=_blank` without `rel`.
14. **`scripts/check-size.ts`:** the D112 budgets, measured on int6 and recorded; the forbidden-module walk; the lazy-chip check; the preconnect scan.
15. **Dashboard image check (D141).**
    ```bash
    flock /tmp/trustagent-heavy.lock docker build -t trustagent-p5check/dashboard:step0 -f frontend-react/Dockerfile . \
      && docker image rm trustagent-p5check/dashboard:step0
    ```

### 2.5 Step-0 import checks

```bash
cd frontend-react && ! grep -rE "from '@/(lib/openrouter|lib/gate|components/replay/live)" src/landing   # D99
test "$(go -C ../backend-go list -deps ./internal/costs | grep -c internal/runtimecfg)" = 0                # D90 still holds
```

### 2.6 Step-0 gate (also on `feat/v2-int6` after every checkpoint merge)

Run from the repo root, one command after another:

```bash
L="flock /tmp/trustagent-heavy.lock"; export TEST_DATABASE_URL=$(/home/timkushmar/.cache/trustagent-orch/pg-url.sh)
test -z "$(gofmt -l backend-go)" && $L go -C backend-go vet ./...
$L env -u TEST_DATABASE_URL go -C backend-go test -race -p 2 ./...
$L go -C backend-go test ./internal/decision/... ./internal/credentials/... ./internal/jev/... -run 'TestVectors|TestPKCEVectors|TestForwardTestHeaders'
$L go -C backend-go test -p 1 ./cmd/proxy -run 'TestDemoScenariosEndToEnd|TestE2EScenariosAgainstMockUpstream' -count=1
(cd backend-python && $L poetry run pytest tests/unit -q)
$L make check && $L make vectors-ts && $L make recording-check
python3 scripts/check_recording.py frontend-react/demo-data/simulated
PLAYWRIGHT_STATIC_PORT=24173 $L make test-e2e-static && (cd frontend-react && npm run size)
```

---

## 3. WEB-LIVE: key store, caps, transport, Jev, PKCE, callback, live Playground

- **Branch:** `feat/v2-w6-wlive`.
- **Size:** L, about 11 days. CP-LIVE comes after LV-3, about 4.75 d in.
- **Read first:** SPEC §7.5 (static demo bullets), §3.3, the §6 error table, `DESIGN.md`; D99–D106, D112, D135–D138.
- **References:**
  - Go `internal/jev/wire.go:18-160`, `classify.go:36-90,98,156,276`, `jev.go:168-184`, `http.go:731-737`;
  - gstudio `internal/studio/runner.go:145-219`;
  - won `components/setup/connectFlow.ts` (the storage pattern only; not `safeReturnPath`);
  - wsv `components/playground/ResultTimeline.tsx`;
  - wev `JevRequestPanel.tsx:113`.
- **Owns:**
  - `src/lib/openrouter/**` and `src/lib/gate/**` (except `types.ts`), `src/lib/decision/vectors/pkce.ts`;
  - `src/components/replay/StaticPlayground.tsx`, `src/components/replay/live/**`, `src/components/playground/**`;
  - `src/components/setup/StaticConnectCallback.tsx`, `src/routes/connect/callback.tsx`, `src/routes/app/playground.tsx`;
  - `src/domain/words/live.ts`;
  - `vite/plugins/policy.ts`;
  - `e2e/static/{live-playground,callback,frame,csp-live}.spec.ts`.
- **Consumes:** CP-COSTTS. Until then, code against the stub signature.

### Tasks

1. **LV-1 (S–M): key store and ledger.**
   - `keyStore.ts` (D102): TTLs, `pageshow` re-read, blocked-storage fallback, shape check, `useSyncExternalStore`, and the abort registry for Forget.
   - `usage.ts` (D101): billable and rejected counts, the pause after 10 rejected, `reserve`/`settle`, the token cap.
   - `useBrowserKey.ts`.
2. **LV-2 (M): transport and errors.**
   - `endpoints.ts` re-exports the D99 constants.
   - `http.ts` (D100).
   - `errors.ts` ports `classify.go` to `ApiError` (`data/errors.ts:23`) with jev codes. It maps each kind to `playground_live.errors.*`, forgets the key on a 401, and runs the `ERRORS_READABLE=false` diagnosis.
   - `keyInfo.ts`: the free `GET /api/v1/key`; `limit === null` shows `key_no_limit`. Fields stay optional until P6 captures the shape.
3. **LV-3 (M): Jev, chat, runner completion → CP-LIVE.**
   - **`decisions.ts`:** ports the `wire.go:34-55` encode, `decodeResponse:92-126` and `checkAnswers:128-160`, on `DECISIONS_PATH`.
   - **`chat.ts`:** `POST /v1/chat/completions {model, messages, temperature, max_tokens, usage:{include:true}}`.
   - **`gate/runner.ts`:** gstudio `Runner.Evaluate` parity (D135).
   - **`gate/playgroundResult.ts`:** builds `PlaygroundResult` and `DecisionRequestView` from `Compiled` (`compile.ts:116-132`).
   - **`vite/plugins/policy.ts`:** parses `config/policies.yaml` at build time, as `build-demo-recording.ts:24,59` already does.
   - **Scope R:** skip `decisions.ts` wiring; keep `chat.ts` only if `CHAT_FROM_BROWSER`.
4. **LV-4 (S): PKCE.**
   - `pkce.ts` (pure): `verifierFromBytes`, `challengeS256` through `lib/decision/sha256.ts:23`, `authUrl`.
   - `random.ts`: `createVerifier()` via `crypto.getRandomValues`.
   - `savePending` / `takePending` (10 min TTL).
   - The TS runner `vectors/pkce.ts` replaces the skip.
5. **LV-5 (S–M): static callback.** `StaticConnectCallback.tsx` (D105, D106).
6. **LV-6 (L): live Playground.**
   - **Layout:** `StaticPlayground.tsx` keeps the recorded examples first; the `#live` section replaces `:91-118`. `routes/app/playground.tsx` gets `validateSearch` for `?scenario=`.
   - **Components** in `components/replay/live/`:
     - `ConsentDialog`: the four D114 lines, the `fast_check_cost_per_check` chip, `{max_spend}`, Connect as a primary `<button>` (unless `STATIC_CONNECT_PRIMARY` says paste), Paste always visible, frame-busting, focus trap, Esc, and focus returned to the trigger;
     - `PasteKey`: `type="password" autocomplete="off" spellcheck={false} autoCapitalize="off" data-1p-ignore data-lpignore`, no `<form>` submit path, Enter never navigates, and the free key check;
     - `LivePanel`: the three D99 scenarios, preselected from `?scenario=`; a small custom form; "Run check (≈ … [estimate])"; `{n} of {max}`; running cost; Forget key; Retry;
     - `NoCreditsScreen`: a non-inflight 402 links to `openrouter.ai/settings/credits` with "check again" and is not counted; an inflight 402 shows `retry_busy`;
     - `CapReached`, the token-cap message and the rejected pause;
     - `LiveKeyChip`.
   - **Result:** `ResultTimeline` with its deep-audit lane replaced by `deep_audit_not_in_browser`; "Show exact Jev request" through `JevRequestBody`; snapshot and cost chips.
   - **States:**
     - no key, connecting, connected;
     - running (200 ms delay), error with Retry, 402, cap reached, rejected pause;
     - storage blocked, framed, live unavailable (scope R);
     - the note that the tape is simulated.
7. **LV-7 (M): specs.**
   - **`live-playground.spec.ts`:**
     - consent → connect → callback (200, code removed from the URL) → run the refund example → Flagged with a contradiction reason, the cost and `1 of 100`;
     - a reload keeps the key, and a new context has none;
     - Forget clears sessionStorage, and a `goBack()` from bfcache shows no key;
     - the paste path, where Enter in PasteKey leaves `location` unchanged;
     - a 402 shows the screen and leaves the counter unchanged;
     - a variant where the mocked decisions error has no ACAO (diagnosis path);
     - every new surface passes the 400 px no-sideways-scroll check, a dark-theme render and `@axe-core/playwright`.
   - **`callback.spec.ts`:**
     - 200 with no verifier;
     - `?error=Paste+your+key+at+evil.example` is never rendered;
     - each of `//evil`, `/\evil`, `app/../x` and `javascript:` falls back to the default;
     - reload after success shows `already_connected`;
     - history length and Back behaviour;
     - a verifier older than 10 min shows `expired`.
   - **`frame.spec.ts`:** inside an iframe, the controls are absent.
   - **`csp-live.spec.ts`:**
     - only self and `https://openrouter.ai` are contacted;
     - `Authorization` appears only on openrouter.ai requests;
     - the key and `cmp0e2eZq7Lw4Rt9Yx2Vb6Nm` never appear in a same-origin URL or body, `document.body.innerText`, `page.content()` or console messages;
     - localStorage and `document.cookie` hold no key;
     - no `securitypolicyviolation`, the callback included;
     - nothing reaches openrouter.ai before consent.
8. **LV-8 (S): clean-up and report.**
   - Remove the P5 references in owned files (`StaticPlayground.tsx:5`).
   - Hand DX-DOCS the SECURITY.md inputs in the checkpoint report: TTLs, Forget, caps and the rejected pause, frame-busting, the CORS result, the shared origin.

### Tests

- `pkce.test.ts`: length and charset; take is one-shot and has a TTL; nothing in localStorage. `vectors.test.ts`: pkce is no longer skipped.
- `keyStore.test.ts`: TTL expiry; `pageshow` restore; blocked storage; Forget aborts in-flight requests.
- `usage.test.ts`:
  - the 100th billable call is allowed and the 101st refused;
  - 402 and 401 do not consume the cap;
  - a 200 with `bad_json` counts;
  - the pause comes after 10 rejected;
  - the token cap refuses before any fetch;
  - Forget keeps usage.
- `errors.test.ts`, table-driven over every vendor fixture (kind, retryable, billable):
  - Content-Type cases (charset, `+json`, html, missing);
  - timeout, abort, `TypeError`;
  - an opaque failure followed by a `/key` 402 shows `NoCreditsScreen`;
  - no rendered string matches `/TRUTHTABLE_|Settings →|\.env|make /`;
  - the key never appears in a URL.
- `http.test.ts`: the headers sent equal `ALLOWED_REQUEST_HEADERS`; the key is attached only to `openrouter.ai`.
- `runner.test.ts` with a fake `JevCall`:
  - `grounded-pass`, `refund-contradiction-flag` and `pii-leak-flag` reproduce their `expect` blocks;
  - the request body equals `internal/jev/testdata/request_openrouter-decisions.golden.json` as parsed JSON, minus `session_id` (TS/Go parity, D109);
  - a missing answer;
  - a 402 mid-run;
  - over the cap before any fetch.
- `StaticConnectCallback.test.tsx`: `replaceState` before fetch; verifier deleted; StrictMode sends one POST; a CSRF code is rejected; the raw error is never rendered.
- `ConsentDialog.test.tsx`: every number has a chip; `{max_spend}` equals the `priceCall` output.
- `StaticPlayground.test.tsx`: every state, axe, and every disabled action says why.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
cd frontend-react && $L npm run lint && $L npx tsc -b \
 && $L npx vitest run --maxWorkers=2 src/lib/openrouter src/lib/gate src/lib/decision/vectors src/components/replay src/components/setup src/components/playground src/test/lint \
 && $L env VITE_APP_MODE=static VITE_BASE_PATH=/TrustAgent/ VITE_DEMO_DATASET=mock npm run build && npm run size \
 && PLAYWRIGHT_STATIC_PORT=24174 CI=1 $L npx playwright test --project=static --workers=1 -g "live|callback|frame|csp"
cd .. && $L make vectors-ts && $L make recording-check      # runner output unchanged (D135)
```

---

## 4. WEB-STUDIO: static Studio

- **Branch:** `feat/v2-w6-wsst`.
- **Size:** M, about 5.5 days.
- **Read first:** SPEC §7.3, §3.3; P4-PLAN §12 (WEB-ST); D101, D107, D108, D135.
- **References:**
  - py `studio/drafter.py`;
  - gstudio `unsaved.go` (`CompileUnsaved`);
  - wst `lib/studio/{templates,simpleForm,draftToSpec,testBench,estimate}.ts`, `StudioPage.tsx:80-88,123,206-210`.
- **Owns:**
  - `src/components/studio/**`, `src/routes/app/evaluators/studio/**`, `src/lib/{studio,evaluator}/**`;
  - `scripts/gen-evaluator-validator.ts`;
  - `src/data/replay/{replayStudioPlane,replayEvaluatorReads}.ts`;
  - `src/components/common/download.ts`, `src/domain/words/studio.ts`;
  - `e2e/static/studio.spec.ts`.
- **Consumes:** CP-TAPE (samples), CP-COSTTS (estimates), CP-LIVE (real transport). SS-1 to SS-4 run on fakes and `runGate` from Step 0b.

### Tasks

1. **SS-1 (M): drafter.** `lib/studio/drafter.ts` (D108) against a fake `ChatCall`. Extend `gen-evaluator-validator.ts` to emit one validators module (shared helpers), with its `--check`.
2. **SS-2 (M): replay Studio plane.**
   - `replayEvaluatorReads.ts`: `listEvaluators`, `getEvaluator` and `getEvaluatorVersion` from `BUILTIN_TEMPLATES`; route names from `virtual:trustagent/policy`.
   - `replayStudioPlane.ts`:
     - `draftEvaluator` → SS-1;
     - `testEvaluator` → `runGate` on a one-evaluator route (`CompileUnsaved`: version = latest + 1), with a fake `JevCall` until CP-LIVE and the real one after;
     - `getStudioSamples` → up to 6 tape bodies;
     - `quickCalibrate` → the local TS;
     - `dryRunEvaluator` stays unavailable.
3. **SS-3 (M): StudioPage in static mode** (D107):
   - Describe: with a key and `CHAT_FROM_BROWSER`, Draft with AI shows the judge model and the estimate. Otherwise "Start from a template" is primary, and drafting is a disabled action that says why and links to the consent dialog.
   - Test bench: the estimate and remaining calls before Run. Under scope R, the what-if only.
   - Save becomes Copy JSON / Download `.json` (new `download.ts`: Blob plus `<a download>`; `clipboard.ts:7`). Neither path puts the key in the output.
   - The "On your own stack" card.
   - Delete `StudioStaticNote.tsx` and both static `PhasePlaceholder` branches.
4. **SS-4 (S): unit tests.** Rewrite `StudioStatic.test.tsx`.
5. **SS-5 (M): `e2e/static/studio.spec.ts`** (after CP-LIVE).
   - template → edit → mocked Jev test → quick calibrate → copy (clipboard permission) and download (`waitForEvent('download')`), then validate the JSON as `evaluator.v1`. Neither the clipboard nor the file contains the fake key or the no-pattern secret.
   - a mocked draft raises the counter by 1 or 2;
   - no Save, Watch or Activate buttons;
   - 400 px, dark theme, axe.
6. **SS-6 (S): clean-up.** Remove P5 references in owned files; list the `phase-refs.allow` entries to remove.

### Tests

- Rendering is byte-equal to the `drafter-render.*` fixtures.
- The repair path. A broken repair keeps the first draft. At the cap before the repair, the first draft is returned with its problems.
- At the cap, Run is disabled with a reason.
- An exported JSON round-trips through `simpleForm`.
- Lint parity on the lint vectors (unchanged).

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
cd frontend-react && $L npm run lint && $L npx tsc -b \
 && $L npx vitest run --maxWorkers=2 src/components/studio src/lib/studio src/lib/evaluator src/data/replay \
 && node scripts/gen-evaluator-validator.ts --check \
 && $L env VITE_APP_MODE=static VITE_BASE_PATH=/TrustAgent/ VITE_DEMO_DATASET=mock npm run build && npm run size \
 && PLAYWRIGHT_STATIC_PORT=24175 CI=1 $L npx playwright test --project=static --workers=1 -g "studio"
```

---

## 5. WEB-LANDING: cost runner, calculator, comparison, Try it, story

- **Branch:** `feat/v2-w6-wland`.
- **Size:** L, about 7.5 days.
- **Read first:** SPEC §8.1, §7.6 (cost vectors), §3.3 (budget); `DESIGN.md`; D85, D99, D112–D117.
- **References:**
  - Go `internal/decision/cost.go`: `GlobMatch:241`, `TokenCostUSD:293`, `PriceCall:399`, `LookupEffective:445`, `CostPer1k:566`;
  - `landing/costModel.ts:63,106`, `Calculator.tsx:107-109`, `Compare.tsx:12-29,92`, `TryIt.tsx:72-76,157,166-191`, `WriteChecks.tsx:66-71`, `HeroCard.tsx:112,136-138`, `SelfHost.tsx:35-41`, `content.ts:7,9,15`.
- **Owns:**
  - `src/landing/**` (including `glassbox.gen.json`), `landing.html`, `public/landing/**`;
  - `src/lib/decision/{cost.ts,cost.test.ts,vectors/cost.ts}` (contract-owned; the lead reviews at CP-COSTTS);
  - `contracts/claims/compare.v1.json` (data only);
  - `vite/plugins/{digest,landingData,ogMeta}.ts`, `scripts/gen-landing-glassbox.ts`;
  - `e2e/static/landing.spec.ts`.

### Tasks

1. **LD-1 (M): TS cost core → CP-COSTTS.**
   - `lib/decision/cost.ts`: `globMatch`, `lookupEffective`, `priceCall` and `costPer1k`, with exact decimals as integer micro-dollars via BigInt (`call-exact-decimals.json`).
   - `vectors/cost.ts` runs every `fn`.
   - Update `compare.test.ts:36`.
2. **LD-2 (S–M): calculator.**
   - `costModel.ts` becomes an adapter over `costPer1k`.
   - A "Deep-audit judge" select from `prices.v1.json`: Haiku 4.5, Sonnet, local Ollama $0, custom $/MTok.
   - A "≈ N× less than asking an LLM judge the same questions" line with the Estimate chip.
   - Everything stays inside the lazy chunk.
3. **LD-3 (S–M): budget headroom.**
   - Lazy-load the below-fold sections (Compare, WriteChecks, TryIt, SelfHost, sources).
   - Fill the `landingData` plugin (the example-policy string and the outcomes subset).
   - Report each section's chunk.
4. **LD-4 (M): comparison (D113).**
   - Research and fill `compare.v1.json`, with `reviewed:false` throughout.
   - Attach `compare-fetch.tsv` to the checkpoint report.
   - `Compare.tsx` renders from the contract, with the 400 px layout and a "Comparison sources" footer group.
   - Remove `COMPARE_AS_OF` (`:12`) and `<Todo/>`.
5. **LD-5 (S–M): Try it.**
   - When `LIVE_CHECKS`: three links through `liveHref`, the cost chip `fast_check_cost_per_check` and `{max_calls}`. Otherwise "Run it yourself: `make demo`".
   - No "Coming soon".
   - Tape card from `virtual:trustagent/tape-digest`. `vite/plugins/digest.ts` builds it from the chosen manifest: kind, label, date, snapshot, judge, outcome counts and 5 examples. `useTape()` stops fetching the manifest, which also removes `TryIt.tsx` from the D138 allow-list.
6. **LD-6 (M): story panels (D116).**
   - The WriteChecks step strip and "Open the Studio".
   - HowItWorks "What Jev was asked" from `glassbox.gen.json`, linking to the featured trace's glass box.
7. **LD-7 (S): hero, self-host and meta.**
   - **Hero:** one chip per number (`HeroCard.tsx:136-138`); "Checked against: refund-policy.md" in beat 2; "Example · Simulated" (`:112`); "not yet measured" said once.
   - **SelfHost:** `compose_up`, "keys encrypted at rest", "pulls prebuilt images when published, otherwise builds locally".
   - **Header:** nav anchors "Try it" and "Checks".
   - **Snippet:** a compact one-line integration snippet from `snippet()`.
   - **`ogMeta.ts`:** `og:*`, `twitter:card`, canonical and `theme-color`, using `VITE_SITE_URL`; `og:image` only when `public/og.png` exists.
   - **Copy:** trim the leads.
8. **LD-8 (S–M): tests.**
   - `landing.test.tsx`:
     - every vendor cell has a link and a date, or "Not yet verified";
     - no `data-compare-todo` and no "Coming soon";
     - `LIVE_CHECKS` true and false both render, with no live link when false;
     - no "checks" wording next to `{max_calls}`;
     - forbidden imports include `@/lib/openrouter`, `@/lib/gate` and `@/components/replay/live` (`:203`);
     - at defaults the calculator equals `default_total_cost_per_1k` within tolerance, and changing the judge changes only the deep-audit row;
     - every number has a chip.
   - `landing.spec.ts`:
     - a scenario link opens the Playground with that scenario preselected;
     - the Studio link answers 200;
     - the meta tags are absolute;
     - the 400 px no-horizontal-scroll test includes `#compare`;
     - first viewport: at 1280×800 the H1, subhead, "Try the demo" and hero beat 2 are visible; at 400×860 the H1 and "Try the demo" sit inside the first viewport.
   - A unit test for each plugin and for `gen-landing-glassbox.ts --check`.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
cd frontend-react && $L npm run lint && $L npx tsc -b && $L npx vitest run --maxWorkers=2 src/landing src/lib/decision \
 && node --import ./scripts/lib/aliasHooks.ts scripts/gen-landing-glassbox.ts --check \
 && $L env VITE_APP_MODE=static VITE_BASE_PATH=/TrustAgent/ VITE_DEMO_DATASET=mock npm run build && npm run size \
 && PLAYWRIGHT_STATIC_PORT=24176 CI=1 $L npx playwright test --project=static --workers=1 -g "landing"
cd .. && $L make vectors-ts && node contracts/validate.mjs     # cost no longer skipped; compare cells valid
```

---

## 6. WEB-REPLAY: dataset, mock tape, replay completeness, glass box, first visit, `/og`

- **Branch:** `feat/v2-w6-wrep`.
- **Size:** L, about 6.75 days.
- **Read first:** SPEC §2.13, §3.3, §8.2 (og-spec); D109, D110, D115, D117, D139.
- **References:**
  - `replayDataSource.ts:76-92,145,268-286`;
  - wsv `stats.ts` `roiTimeseries`, wsv `trafficRows.ts:40` (`hasReason`);
  - `demoRecording.ts:150-151,358-437,764`;
  - wev `OutcomeCard.tsx:38-54`;
  - `useWriteAccess.ts`, `StaticNavRail.tsx:63`, `ReplayBanner.tsx:36-40`.
- **Owns:**
  - `src/data/replay/**` (except the two WEB-STUDIO files and the D135 runner), `scripts/build-demo-recording.ts`, `demo-data/simulated/**`;
  - `src/components/replay/**` (except `StaticPlayground.tsx` and `live/**`);
  - `src/components/{trace,overview,savings,traffic,launch}/**`, `src/components/common/useWriteAccess.ts`;
  - `src/routes/og.tsx`, `src/routes/app/savings.tsx`;
  - `vite/plugins/dataset.ts` (body), `src/domain/words/replay.ts`, `src/test/recording.ts`;
  - `e2e/static/{replay,first-visit}.spec.ts`.

### Tasks

1. **RP-1 (M): dataset and a richer mock tape → CP-TAPE.**
   - The `vite/plugins/dataset.ts` body (D110), including the `recorded`-kind check.
   - `demoRecording.ts` grows beyond s0 to these non-streamed scenarios:
     - support-bot;
     - account-bot (`pii-leak-flag`; passes `check_recording` after the Step 0a fix);
     - billing-bot;
     - faq-bot (observe);
     - payments-faq (fallback, delivered from the scenario's `fallback` block);
     - help-center (`help-center-link-block-json`, D139).
   - Scenarios whose text trips `SECRET_PATTERNS` (for example `secret-leak-flag`) stay out.
   - If fallback delivery cannot be modelled faithfully, that scenario stays out and the lead is told.
   - Spread the traces over ≥ 2 h of synthetic time, so the Savings series has more than one bucket.
   - Regenerate `demo-data/simulated/`.
   - Key the `src/test/recording.ts` helpers by scenario id, not by position.
2. **RP-2 (S–M): ReplayDataSource completeness.**
   - `matchesTrace` honours `reason` (D68).
   - Evidence suggestions are computed from untagged traces, else an honest empty state.
   - The `roiTimeseries` bucket is chosen from the recording window.
   - `routes/app/savings.tsx` and the Overview Savings tab work.
3. **RP-3 (M): glass box (D109).** `getDecisionRequest` rebuilds the request. The simulated badge wording is per D109. JevRequestPanel's `not_implemented` branch (`:151`) stays only for `mismatch_reason`.
4. **RP-4 (S): honest static states.**
   - `useWriteAccess` gets a `static` reason (`disabled_reasons.static`).
   - OutcomeCard's "Open evaluator" and "Adjust threshold" become static disabled actions with that reason.
5. **RP-5 (S–M): first-visit UX (D117).**
   - `ReplayGuide`;
   - banner copy moved to `copy.recording.*`;
   - the nav hint "caught" for Trace;
   - `data-featured` on the featured row;
   - the Overview caveat.
6. **RP-6 (M, cut line with DD-7): `/og` (D115).** `OgCard` built from `featuredTrace()` with `OutcomePill`, the trace answer marks, `checkedAgainst()` and the contradicted evidence line; `data-og-ready`.
7. **RP-7 (M): static specs.**
   - **`replay.spec.ts`:**
     - the label is visible;
     - six nav items; hidden pages guarded;
     - the Savings tab is labelled Simulated, and `app/savings/` answers 200;
     - the guide's four links work;
     - on the featured trace, "Show exact Jev request" shows its questions and the simulated badge;
     - the outcome mix includes Blocked and Fallback;
     - no dependency on trace positions.
   - **`first-visit.spec.ts`:**
     - "Try the demo" → within 3 actions, the featured flagged trace shows "Flagged", the "60 days" mark and the "30 days" evidence, with no network beyond self;
     - the guide opens on the first visit, anchored to the featured row, without covering it at 400 px.
   - Every new surface: 400 px, dark theme, axe.

### Tests

- `replay.test.ts`:
  - the reason filter, paging and counts;
  - "rebuild is deterministic" on every simulated trace;
  - the evaluator reads;
  - dataset resolution for all three `VITE_DEMO_DATASET` values, and the build failure on a mislabelled `recorded/`.
- `demoRecording.test.ts`: drift (non-empty glob), and every scenario's `expect` reproduced through `runGate`.
- `staticBuild.test.tsx`: the static reason; the OutcomeCard actions are disabled with it.
- `OgCard.test.tsx` with axe.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
cd frontend-react && $L npm run lint && $L npx tsc -b \
 && $L npx vitest run --maxWorkers=2 src/data/replay src/components/replay src/components/trace src/components/overview src/components/launch \
 && $L env VITE_APP_MODE=static VITE_BASE_PATH=/TrustAgent/ VITE_DEMO_DATASET=mock npm run build && npm run size \
 && PLAYWRIGHT_STATIC_PORT=24177 CI=1 $L npx playwright test --project=static --workers=1 -g "replay|first-visit"
cd .. && $L make recording-check && python3 scripts/check_recording.py frontend-react/demo-data/simulated
```

---

## 7. GO: faults, scenarios, recording mode, knowledge, load, benchmarks, jevcheck

- **Branch:** `feat/v2-w6-go`.
- **Size:** L, about 11.5 days.
- **Read first:**
  - SPEC §2.12, §2.13, §4.2 P5 gate, §5 risks 2 and 18, §8.3 #2;
  - GATEWAY.md §6 and §13; design-gateway §14;
  - D118–D122, D131, D139.
- **References:**
  - `mockupstream/{faults.go:30-49,server.go:255-300,openrouter.go:44-49,llm.go:43}`;
  - `internal/proxy/{handler.go:217-218,exchange.go:117-128,testfallback.go:15-38}`, `internal/gate/types.go:21-25`;
  - `cmd/trafficgen/{main.go:68-118,194-306,scenario.go:134-140,expect.go,reference_test.go:650}`;
  - `cmd/proxy/scenarios_e2e_test.go:122-186,321`;
  - `decision/{decide.go:404-423,774-795,detectors.go:44-73,143}`;
  - `scripts/load/gateway-overhead.js:10-30,125-130,153-175`, `scripts/tests/test_load.py:159-182`;
  - `internal/proxy/enforce.go:21-27,150-158`, `metrics/gateway.go:23-48`;
  - `cmd/proxy/status_api.go:670-681`, `cmd/jevcheck/main.go`, `evals/report.py:436-505`.
- **Owns:**
  - `contracts/demo/**` (pack data, including `faults.v1.json`; the lead reviews), `contracts/README.md`, `contracts/vectors/cost/**`;
  - `cmd/{trafficgen,mock-upstream,export-replay,vectorgen,jevcheck}/**`;
  - `internal/{gate,proxy,recording,jev}/**` (except `jev/testheaders.go`), `internal/{evidence,llmapi}/*_bench_test.go`;
  - `config/policies*.yaml`;
  - `cmd/proxy/{faults_e2e_test,knowledge_e2e_test,scenarios_e2e_test,overhead_bench_test}.go`, and `cmd/proxy/status_api.go` (`buildSHA` lines only);
  - `scripts/load/**`, `scripts/tests/test_load.py`, `mk/load.mk`, `docker-compose.load*.yml`;
  - `backend-python/evals/report.py` and `backend-python/tests/unit/test_bench_report.py` (named exception).

### Tasks

1. **GO-1 (S–M): fault channel (D119).**
   - Test-mode forward-only `fault:<spec>` values in `exchange.go`: never stored, and sent through the Step 0a seam.
   - mock-upstream `decisions()` and `llm()` look up `mock_faults` by scenario id from the embedded faults pack: decisions by the forwarded id, LLM by conversation.
   - `trafficgen --pack faults`.
   - `TestPackFaultSpecsKnown`.
   - `faultTable` add-only, with `vendor_fixtures_test.go` kept green.
2. **GO-2 (S): `generated` stamp** (D121). `AnswerGenerated` stamped in `exchange.go`; the recording label reads "generated answers".
3. **GO-3 (M): trafficgen → CP-SCEN.**
   - Settle-wait for unchecked traces.
   - Automatic stand-in expectations under `--upstream all`.
   - `--record` and `--trace-ids-out`.
   - The generated pair joins `e2e`, and `setSizes` e2e becomes `{26, 6}`.
4. **GO-4 (M): stateless faults.** Fill `faults.v1.json` with F1–F10 and U1–U2. Grounded answers are used, so only the fault drives the result.

   | # | Fault | Route | Status / degraded | Action |
   |---|---|---|---|---|
   | F1 | `429@1` | support-bot | `error:retryable` / error | pass |
   | F2 | `429@1` | payments-faq | ok, 2 attempts | pass |
   | F3 | `503@1` | support-bot | ok, 2 attempts | pass |
   | F4 | `hang@1` | payments-faq | ok, hedged | pass |
   | F5 | `hang` | support-bot | timeout | pass (fail open) |
   | F6 | `slow` | support-bot | timeout, late decision `enforced:false` | pass |
   | F7 | `hang` | payments-faq | timeout | block `fail_closed` |
   | F8 | `402_inflight@1` | support-bot | `error:credits` | pass |
   | F9 | `bad_json` | support-bot | `error:bad_response` | pass |
   | F10 | `413@1` | support-bot | ok (context shrunk once) | pass |
   | U1 | upstream `503` | support-bot | `skipped:upstream_error` | 503 relayed |
   | U2 | upstream `html200` | payments-faq | skipped | block (fail closed) |

   - `expect.go` handles `degraded` and `decision_model`.
   - `reference_test.go` loads the faults pack and confirms each row against `decide()` (D119).
   - New in-process `TestFaultScenariosAgainstMockUpstream` (modelled on `scenarios_e2e_test.go:321`) runs the whole pack in order. It proves the breaker and cooldown ordering, for example F8 `402_inflight` against the credits cooldown.
5. **GO-5 (M–L): stateful faults in process** (`cmd/proxy/faults_e2e_test.go`, a fresh `newScenarioEnv` each):
   - **S1 breaker:** opens, skips without a mock request, half-open succeeds. Checks `/api/status`, the WS `status` events and the breaker gauge.
   - **S2 auth and credits:** the credits, auth and `html200` cooldowns.
   - **S3 budget:** skip; fail-closed block; the reserve-only band; the status states; the LLM cap pausing audits.
   - **S4 rate limit.**
   - **S5 engine down:** decision unchanged; audit failed or dropped per `worker/pool.go:395-470`.
   - **S6 off and missing credentials.**
6. **GO-6 (S–M): knowledge in process** (D131).
   - `TestKnowledgeScenarioEndToEnd`: `enginetest.RetrieveContextFunc` returns `knowledge[]`; the test expects `checked_against knowledge:1` and the warn cap.
   - The generated `config/policies.knowledge.yaml` plus `TestKnowledgePolicyInSync`.
   - `trafficgen --seed-knowledge --admin-key-file F`.
7. **GO-7: dropped at Step 0a (CO-2).** P4 already stamps the build SHA (`version.GitSHA`, `resolveBuildSHA`, `3c6ef7d`); proof A still checks `a-status.json` `build_sha` against `git rev-parse --short HEAD` (§18).
8. **GO-8 (M): k6.**
   - **Scenario f:** keeps `X-TrustAgent-Scenario: fault:hang@1` on `HANG_RATE` of requests. With GO-1 and the Step 0a `X-Mock-Key`, attempt 0 of exactly those requests hangs.
   - **New scenario g** (`load-g`: block, JSON) in `scripts/load/policies.yaml`, `SCENARIOS` and the passes.
   - **`teardown()`:** reads `/metrics` `trustagent_gateway_overhead_seconds{route}` deltas (the observe figure), `gate_seconds{stage="detectors"}` (Tier-0 under load), the hedge and attempt counters, and `mock-upstream:8090/_mock/requests` (`mock_requests`).
   - **`handleSummary`:** prints one `TRUSTAGENT_LOAD_RESULT {json}` line. The `mk/load.mk` recipe `tee`s each pass to `$(LOAD_OUT)/pass{1,2}.log`.
9. **GO-9 (M): `scripts/load/report.py`** (stdlib).
   - Merges the result lines.
   - Collects the environment (`/proc/cpuinfo`, `docker info`, `git status --porcelain --untracked-files=no`, the proxy limits from the compose config).
   - Applies the D118 refusals.
   - Writes `docs/benchmarks/load/<run_id>.json`.
   - `--gateway-md [--check]` writes or checks the block between `<!-- load:start -->` and `<!-- load:end -->` in GATEWAY.md. DX-DOCS adds the markers; the lead writes the block at I-8.
10. **GO-10 (S–M): `derive_measured`** reads the newest clean load run for `proxy_overhead_ms_p50/p95` (`system: proxy`, `run_id`). Publishing still requires a bench run, and the bench keys are untouched.
11. **GO-11 (M): benchmarks (D120).**
    - `BenchmarkDetect/{evidence=8k,32k,1MiB; answer=200,1k,6k; dense}` and `BenchmarkDetectOne/<id>`.
    - `FuzzDetect`: no panic; UTF-16 spans in bounds and half-open; seeded from `contracts/vectors/detectors`.
    - `TestTier0Budget`, with the CI multiplier.
    - `BenchmarkExtract`, `BenchmarkGatePreJev`, `BenchmarkDecide`.
    - Cut line: `BenchmarkSSERelayPerMB` and `BenchmarkHandlerOverhead/{observe,flag,block,buffer}`.
12. **GO-12 (S–M): jevcheck** (D122): `--gateway` mode, the dashboard-key hint and the "not booked" note.
13. **GO-13 (S): phase and docs.** `defaultPhase = "p5"`, `phase_test.go`, the `contracts/README.md` sets table.

### Tests

- `test_load.py`:
  - scenario g, the result line, the pinned image, the metrics read;
  - the parser on canned k6 output;
  - every refusal;
  - the environment block validates against `load-run.v1` (`jsonschema==4.26.0`).
- `test_bench_report.py`: derivation from a fixture load run; tier handling.
- `cmd/jevcheck`: gateway mode against an httptest proxy; the hints.
- `exchange_test`: the generated stamp; `fault:` values are forwarded and never stored.
- `recording_test`: the "generated answers" label.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"; export TEST_DATABASE_URL=$(/home/timkushmar/.cache/trustagent-orch/pg-url.sh)
$L go -C backend-go test -race -p 2 ./internal/proxy/... ./internal/gate/... ./internal/recording/... ./internal/jev/... \
   ./cmd/trafficgen/... ./cmd/mock-upstream/... ./cmd/export-replay/... ./cmd/jevcheck/...
$L go -C backend-go test -p 1 -v ./cmd/proxy -run 'Fault|Breaker|Budget|RateLimit|EngineDown|Knowledge|DemoScenarios|E2EScenarios|BuildSHA|Status' -count=1 2>&1 | tee /tmp/w6-go.out
! grep -q -- '--- SKIP' /tmp/w6-go.out
$L go -C backend-go test -p 1 -run 'TestTier0Budget' ./internal/gate/...
$L go -C backend-go test -p 1 -run '^$' -fuzz '^FuzzDetect$' -fuzztime 30s -parallel 2 ./internal/gate/
$L make bench-go-smoke && $L make contracts-check
python3 -m unittest scripts/tests/test_load.py -v
(cd backend-python && $L poetry run pytest tests/unit/test_bench_report.py tests/unit/test_demo_scenarios.py tests/unit/test_contracts.py -q)
docker compose -f docker-compose.yml -f docker-compose.load.yml --profile load config -q
```

The live load run is the lead's (I-8).

---

## 8. DX-RELEASE: version, images, quickstart, workflows, recording ops, doctor, first-run

- **Branch:** `feat/v2-w6-dxrel`.
- **Size:** L, about 10.25 days.
- **Read first:**
  - SPEC §2.13, §3.4, §4.2 P5 and P6, §6, the §8.2 checklist, Appendix D;
  - D110, D123–D127, D129–D131, D137, D140, D141.
- **References:**
  - `ci-docker.yml:36-74`, `ci-e2e.yml:91-138,154,181`, `ci-web.yml:24-85`, `security.yml:94-101`, `.github/dependabot.yml:33,38`;
  - dx `backend-go/Dockerfile:73-82,124-127` (GIT_SHA and the revision label are already there), `backend-python/Dockerfile:104-107`, `frontend-react/Dockerfile:86-89`;
  - `docker-compose.yml:23-26,227-237,292-301,367-375`, `scripts/tests/test_compose.py:146-156`;
  - `scripts/check_recording.py:45-53,303,309-315`;
  - dx `scripts/doctor.py:12-50,114,274,447-457,971-1030,1309-1314`, `cmd/proxy/apply_wiring.go:88`;
  - won `e2e/stack/onboarding.spec.ts:34,64-68,183-198,255-261,274`, `e2e/stack/stack.ts:16,56,64,78`.
- **Owns:**
  - `.github/**` (the I-2 turn-on lines are the lead's);
  - the three Dockerfiles and their `.dockerignore` files;
  - `docker-compose.yml` and the overlays (except `load*`), including `docker-compose.{record,knowledge}.yml`;
  - `mk/{dx,release}.mk`;
  - `scripts/{bump_version,check_version,check_recording,record_demo,doctor}.py` and their tests, `scripts/tests/{test_workflows,test_compose}.py`;
  - `e2e/stack/{journey.ts,first-run.spec.ts,golden-path.spec.ts,onboarding.spec.ts}`.
- **Consumes:** CP-SCEN (DR-6).

### Tasks

1. **DR-1 (S–M): version tooling.** `bump_version.py` and `check_version.py` (D127); the `version-bump` and `check-version` targets.
2. **DR-2 (S): images.**
   - `CMDS="proxy trafficgen mock-upstream jevcheck export-replay"`.
   - Base images pinned by index digest (D125).
   - The D126 compose default tag, the `ENGINE_EXTRAS` suffix, and `RELEASE_CHECKOUT`/`DEMO_BUILD`.
3. **DR-3 (S) → CP-REL: Pages.**
   - `pages.yml` (D124, D140).
   - `make pages-build`: same order as the workflow, including gitleaks from a pinned, checksummed download into `tmp/tools/`.
4. **DR-4 (M): release workflows.**
   - `release-images.yml` (D125) with the `first-run` and `anonymous-pull` jobs.
   - `eval-live.yml`: dispatch only, `environment: eval-live`, `gate-eval` → `bench-lite`; it uploads results and never runs `bench-publish`. `eval.yml` stays as the free Ollama tier.
   - `ci-docker.yml`: comment update; an optional arm64 smoke build for proxy and dashboard.
5. **DR-5 (S–M): workflow lint and hardening.**
   - `make lint-workflows`: actionlint and zizmor, both pinned.
   - `test_workflows.py`, text-based like `test_compose.py`, asserting:
     - triggers, including `pull_request` as build only;
     - `VITE_BASE_PATH`;
     - `check_recording` before the build, and gitleaks before the upload;
     - permissions held by jobs, with top-level `{}`;
     - the deploy and repo guards; semver tags;
     - SHA pins and `persist-credentials: false`;
     - no `pull_request_target`, and no `secrets.` in the Pages build job or `build-args`;
     - the attest step after each push, and no cache on tags;
     - every required job name unchanged: Go (proxy), Python, Frontend (dashboard), Docker builds, Contracts, DX scripts, E2E (compose stack), gosec, ….
6. **DR-6 (M): `make record-demo`** (D123), after CP-SCEN.
   - Overlay, script and target.
   - A `proxy -check-config` proof of the overlay env with fake keys (no Docker).
   - `check_recording.py`: rules for `simulated/` and `recorded/`, and clearer `--require-recorded` messages.
7. **DR-7 (M–L): doctor complete** (D129).
8. **DR-8 (M): first-run.**
   - `journey.ts`: `step()`, COPY loading and the outcome helpers, moved out of the onboarding spec, which is refactored to use it.
   - `first-run.spec.ts`.
   - `make first-run`: local mode `FIRST_RUN_LOCAL=1` uses the `p5local` images with no rebuild; CI mode pulls `FIRST_RUN_TAG`.
   - Writes `first-run.json` (pull, healthy, ready and first-flag timings, labelled).
   - Skips via `skipOrFail` without its env.
9. **DR-9 (M): `golden-path.spec.ts`** (D130): ≤ 8 actions, listed on failure; no shell-outs; never touches the logs fallback.
10. **DR-10 (S, cut line): knowledge overlay** (D131): `docker-compose.knowledge.yml`, the `knowledge-*` targets, and `ci-knowledge.yml` (non-required; dispatch, weekly, kb path filter).

### Tests

- `test_check_recording.py`: example domains kept; real domains refused; the directory rules.
- `test_record_demo.py`:
  - the dry-run plan;
  - refusal without a key, of a mock or fallback preflight, and without `CONFIRM=1`;
  - caps present;
  - `since` handling;
  - an export failure propagates.
- `test_doctor.py`, with fake HTTP for each new check (PASS, WARN, FAIL, SKIP):
  - `--no-probe` never POSTs;
  - the admin key is never printed;
  - a revision mismatch warns;
  - `--expect demo` stays green;
  - `--expect fresh` and `--target pages` work.
- `test_compose.py`: the tag expression; the extras suffix only on the engine; every overlay; every Dockerfile has the revision label and an `@sha256:` on its `FROM` lines.
- `test_bump_version.py`, `test_check_version.py`.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
make test-scripts lint-scripts compose-check && $L make lint-workflows
python3 scripts/check_version.py && python3 scripts/bump_version.py --dry-run 1.0.1 >/dev/null
python3 scripts/check_recording.py frontend-react/demo-data/simulated
python3 scripts/record_demo.py --dry-run && env -u OPENROUTER_API_KEY python3 scripts/doctor.py --help >/dev/null
make -n pages-build first-run version-bump record-demo >/dev/null
(cd frontend-react && npx playwright test --list --project=stack | grep -cE 'first-run|golden-path|onboarding')
```

The `proxy -check-config` proof runs under `$L` with the overlay env and fake keys only.

---

## 9. DX-DOCS: check-claims, README, docs, ADRs, launch kit, capture tooling

- **Branch:** `feat/v2-w6-dxdocs`.
- **Size:** M–L, about 7.5 days (about 5 if DD-3 is cut).
  - **Part A** (from 0a; light, no heavy commands): DD-1, DD-5, DD-6.
  - **Part B** (after CP-REL; reads the web checkpoint reports): DD-2, DD-3, DD-4, DD-7.
- **Read first:** SPEC §3.5, §8.1–§8.3, §6; P3-PLAN §13; D113–D115, D118, D128, D133.
- **Owns:**
  - `docs/**` (except `docs/v2/{SPEC,P5-PLAN}.md` and `docs/benchmarks/**`);
  - `README.md`, `CHANGELOG.md`, `CLAUDE.md`, `backend-go/README.md`, `frontend-react/DESIGN.md`;
  - `scripts/{check-claims.mjs,make_hero_gif.py}` and their tests, `mk/launch.mk`;
  - `frontend-react/e2e/launch/**`.

### Tasks

1. **DD-1 (M): `check-claims.mjs`** (D128) with `--self-test` fixtures (including caveat adjacency and compare `reviewed`); the `make check-claims` target in `launch.mk`.
2. **DD-2 (M): README hero** in SPEC §8.2 order:
   - badges: CI, MIT, release (dynamic), live demo;
   - "Try the demo | `make demo`";
   - three `<!-- claim:… -->` bullets with adjacent caveats;
   - a Mermaid tier diagram (from ARCHITECTURE);
   - `<!-- measured:start/end -->`;
   - the pull-based quickstart and SDK snippets;
   - Evaluators, Security, Limitations (`copy.limits`), Roadmap;
   - a screenshot block left as a marked placeholder for I-5b.

   The GIF waits for P6.
3. **DD-3 (L, cut line): `WALKTHROUGH.md` v2 rewrite**, removing `:9-14` and `:269-271`. If cut, a pointer page stays.
4. **DD-4 (M): guides.**
   - **GETTING-STARTED and DEMO:** the pull path and the D126 fallback; Docker ≥ 25 / Compose ≥ 2.24; the static demo URL; "Try it live in your browser"; the datasets and their labels; `record-demo`, `first-run`.
   - **ARCHITECTURE:** static topology, the release and GHCR flow, image contents.
   - **SECURITY, known gaps:**
     - the key in sessionStorage, with the TTLs and Forget;
     - the shared `github.io` origin;
     - courtesy caps and the rejected pause;
     - meta CSP with no headers; frame-busting;
     - the CORS result; the Pages PKCE status;
     - provenance, SBOM and GitHub attestations, no cosign;
     - pinned actions;
     - D38 multi-replica.
   - **CI-CD:** a table refresh, plus a **Releasing** runbook that includes `gh attestation verify`.
   - **EVALUATORS:** the static Studio.
   - **JEV-ACCESS:** browser live mode and the CORS row.
   - **GATEWAY:** `<!-- load:start/end -->` and the Tier-0 section.
5. **DD-5 (M): records.**
   - DECISIONS ADR-005..010.
   - PLAN.md phases 8–15 with status.
   - CHANGELOG: the SPEC §3.5 breaking items (`VITE_TRUTHTABLE_API_KEY` removed, ingest keys by default, `audit-engine → engine`, `127.0.0.1` binds, GHCR images) plus P5 "Added". `bump_version.py` writes the release heading.
   - CLAUDE.md new commands, the docs/README index, `backend-go/README.md`, DESIGN.md §12b (static pages and live states).
6. **DD-6 (S–M): `docs/launch/*` drafts.**
   - Files: `og-spec`, `demo-script` (the SPEC 60 s table), `github-metadata` (SPEC wording), `linkedin-post` (placeholders from `measured.json` only), `interview-talking-points`, `launch-checklist`.
   - The checklist includes the compare review, the same-origin check and `check-claims --launch`.
   - Every figure carries a claim marker.
7. **DD-7 (M, cut line): capture tooling.** `e2e/launch/{og,hero,screenshots}.spec.ts` (D115), `scripts/make_hero_gif.py` and its test, and `make og`, `hero-gif`, `screenshots`.

DD-8 (screenshots) moves to the lead as I-5b.

### Gate

```bash
node scripts/check-claims.mjs --self-test && node scripts/check-claims.mjs && make config-docs-check
python3 -m unittest scripts/tests/test_make_hero_gif.py -v
(cd frontend-react && npx playwright test --list --project=launch)
```

---

## 10–14. Reserved

The stream sections end at §9. §15 onward keep P4's numbering.

---

## 15. Ownership matrix (after Step 0)

| Path | Owner |
|---|---|
| Everything listed under Rule 2 (frozen); `docs/v2/{SPEC,P5-PLAN}.md`; `scripts/phase-refs.allow`; `docs/benchmarks/load/**` (written at I-8); `src/components/evaluators/benchmarkRuns.ts` (Step 0b); the turn-on lines in `ci-{go,e2e,web,contracts}.yml` at I-2; the README screenshot block at I-5b | lead |
| `src/lib/openrouter/**`, `src/lib/gate/**` (except `types.ts`), `src/lib/decision/vectors/pkce.ts`, `src/components/replay/{StaticPlayground.tsx,live/**}`, `src/components/playground/**`, `src/components/setup/StaticConnectCallback.tsx`, `src/routes/connect/callback.tsx`, `src/routes/app/playground.tsx`, `src/domain/words/live.ts`, `vite/plugins/policy.ts`, `e2e/static/{live-playground,callback,frame,csp-live}.spec.ts` | WEB-LIVE |
| `src/components/studio/**`, `src/routes/app/evaluators/studio/**`, `src/lib/{studio,evaluator}/**`, `scripts/gen-evaluator-validator.ts`, `src/data/replay/{replayStudioPlane,replayEvaluatorReads}.ts`, `src/components/common/download.ts`, `src/domain/words/studio.ts`, `e2e/static/studio.spec.ts` | WEB-STUDIO |
| `src/landing/**`, `landing.html`, `public/landing/**`, `src/lib/decision/{cost.ts,cost.test.ts,vectors/cost.ts}`, `contracts/claims/compare.v1.json`, `vite/plugins/{digest,landingData,ogMeta}.ts`, `scripts/gen-landing-glassbox.ts`, `e2e/static/landing.spec.ts` | WEB-LANDING |
| `src/data/replay/**` (other files), `scripts/build-demo-recording.ts`, `demo-data/simulated/**`, `src/components/replay/**` (other files), `src/components/{trace,overview,savings,traffic,launch}/**`, `src/components/common/useWriteAccess.ts`, `src/routes/{og.tsx,app/savings.tsx}`, `vite/plugins/dataset.ts`, `src/domain/words/replay.ts`, `src/test/recording.ts`, `e2e/static/{replay,first-visit}.spec.ts` | WEB-REPLAY |
| `contracts/demo/**`, `contracts/README.md`, `contracts/vectors/cost/**`, `cmd/{trafficgen,mock-upstream,export-replay,vectorgen,jevcheck}/**`, `internal/{gate,proxy,recording,jev}/**` (except `jev/testheaders.go`), `internal/{evidence,llmapi}/*_bench_test.go`, `config/policies*.yaml`, `cmd/proxy/{faults_e2e_test,knowledge_e2e_test,scenarios_e2e_test,overhead_bench_test}.go`, `cmd/proxy/status_api.go` (`buildSHA`), `scripts/load/**`, `scripts/tests/test_load.py`, `mk/load.mk`, `docker-compose.load*.yml`, `backend-python/evals/report.py`, `backend-python/tests/unit/test_bench_report.py` | GO |
| `.github/**`, Dockerfiles and their `.dockerignore` files, `docker-compose.yml` and the other overlays (`record`, `knowledge`, `demo`, `e2e`, `onboarding`), `mk/{dx,release}.mk`, `scripts/{bump_version,check_version,check_recording,record_demo,doctor}.py` and their tests, `scripts/tests/{test_workflows,test_compose}.py`, `e2e/stack/{journey.ts,first-run.spec.ts,golden-path.spec.ts,onboarding.spec.ts}`, `demo-data/recorded/**` (P6) | DX-RELEASE |
| `docs/**` (other files), `README.md` (except the I-5b block), `CHANGELOG.md`, `CLAUDE.md`, `backend-go/README.md`, `frontend-react/DESIGN.md`, `scripts/{check-claims.mjs,make_hero_gif.py}` and their tests, `mk/launch.mk`, `e2e/launch/**` | DX-DOCS |
| `contracts/fixtures/**` | append-only; new files by the lead (Step 0) or the endpoint owner |
| Anything not listed (for example `internal/{credentials,runtimecfg,costs,calibration,studio}/**`, `backend-python/src/**`, `components/{setup,settings,evaluators,policies}/**` except as named) | nobody (no P5 change; report if needed) |

---

## 16. Dependencies, critical path and schedule

```
Step 0a (1.75 d) ─┬─ GO GO-1..3 ─► CP-SCEN ─► GO-4..13 ──────────────────────────────────────────────┐
                  ├─ DX-RELEASE DR-1..3 ─► CP-REL ─► DR-4,5 ─► DR-6 (needs CP-SCEN) ─► DR-7..10 ───────┤
                  ├─ DX-DOCS part A (light) ─(park)──────────────────► part B (after CP-REL) ──────────┤
Step 0b (+2 d)  ──┼─ WEB-LIVE LV-1..3 ─► CP-LIVE ─► LV-4..8 ──────────────────────────────────────────┤
                  ├─ WEB-REPLAY RP-1 ─► CP-TAPE ─► RP-2..7 ──────────────────────────────────────────┤
                  ├─ WEB-LANDING LD-1 ─► CP-COSTTS ─► LD-2..8 (5th slot, else queued) ───────────────┤
                  └─ queue: WEB-STUDIO (runGate on fakes; real transport after CP-LIVE) ──────────────┴─► M-1, Integration (≈3.75 d)
```

**Critical path.** Step 0 (3.75 d) → LV-1..3 (≈4.75 d) → CP-LIVE (≈ day 8.5) → WEB-STUDIO's live wiring and SS-5. Capacity binds before the path does: WEB-STUDIO waits for a free slot.

**Capacity.**

| Stream | Stream-days |
|---|---|
| GO | 11.5 |
| DX-RELEASE | 10.25 |
| DX-DOCS | 7.5 |
| WEB-LANDING | 7.5 |
| WEB-REPLAY | 6.75 |
| WEB-STUDIO | 5.5 |
| WEB-LIVE | 11 |
| **Total** | **≈ 60** |

**Slot plan (5 slots, when memory allows):**

| Slot | Streams in order |
|---|---|
| 1 | GO (d1.75–13.25) → M-1 (lead, mid-wave Docker run, once ≤ 3 streams are active) |
| 2 | DX-RELEASE (1.75–12) → DX-DOCS part B (12–15) |
| 3 | DX-DOCS part A (1.75–3.75, light) → WEB-LIVE (3.75–14.75) |
| 4 | WEB-REPLAY (3.75–10.5) → WEB-STUDIO (10.5–16) |
| 5 | WEB-LANDING (3.75–11.25) |

- **5 slots:** the last stream ends around **day 16**. With integration (about 3.75 d), P5 takes **about 20 working days**, or about 18.5 with the cut line.
- **4-slot fallback:** WEB-LANDING runs after DX-RELEASE in slot 2 (12–19.5), and DX-DOCS part B after GO in slot 1. That gives **about 23 days**, or about 21 with the cut line.
- The SPEC estimated 16 engineer-days; P4 showed the same gap.

**No cut line: every task stays in P5** (owner decision, recorded at Step 0a on 2026-10-09 with 4 measured slots and scope L). The table below only records what each item would have saved; nothing in it moves to P6. Where a stream section says "cut line", the task is in scope.

**Cut line (draft, not applied).** It was to be re-baselined at the end of Step 0a (measured slots, CORS scope) and checked again at CP-LIVE.
- With 4 slots, everything below is cut by default.
- With 5 slots and scope L, DD-3 is cut by default.
- Scope R frees about 3 d of WEB-LIVE time and keeps more.

Each cut is recorded in §19 and in the errata.

| Item | Moves to | Saves |
|---|---|---|
| DD-3 WALKTHROUGH full rewrite (a pointer page stays) | P6 | 2.5 d |
| RP-6 `/og` and DD-7 OG, hero and screenshot capture tooling | P6 | 2 d |
| DR-7 add-on profile and image checks | P6 | 1 d |
| DR-10 knowledge overlay, targets and workflow (GO-6 in process stays) | P6 | 0.5 d |
| GO-11 SSE-relay and handler-overhead benchmarks | P6 | 0.5 d |

None of these is in the SPEC §4.2 P5 gate.

---

## 17. Integration (lead, on `feat/v2-int6`)

**M-1 (mid-wave, after DR-9, at most 3 streams active).** Build the `p5local` images once (D141). Run `make first-run FIRST_RUN_LOCAL=1` on `truthtable-p5first`, then `down -v`. Findings go back to DX-RELEASE before I-1.

1. **I-1: merge.**
   - Order: GO → DX-RELEASE → WEB-LANDING → WEB-LIVE → WEB-REPLAY → WEB-STUDIO → DX-DOCS. Checkpoint merges already done stay as they are.
   - Run each stream's gate after its merge. A conflict means an ownership violation: the work goes back to the owner.
   - After the web merges, `npm run recording` must produce no diff. If it does, the lead finds the runner change. Only a reviewed, mechanical regeneration is committed (D135).
2. **I-2: turn-on.**
   - **`ci-go`:** `make bench-go-smoke`; `TestTier0Budget` outside `-race` with `CI=true`.
   - **`ci-e2e`:** `make e2e-up E2E_BUILD= && make e2e-scenarios && make e2e-faults && make e2e-down`. Remove `E2E_PHASE=p3` (`:181`); the trafficgen default is now p5.
   - **`ci-contracts`:** `node scripts/check-claims.mjs`, `python3 scripts/check_version.py`.
   - **`ci-web`:** `VITE_DEMO_DATASET=mock` for `build:static`; `check_recording.py frontend-react/demo-data/simulated`; `gen-landing-glassbox --check`.
   - **DX scripts job:** `make lint-workflows`.
   - **`make check`** includes `check-claims`, `check-version` and the glassbox drift check.
3. **I-3: end-of-P5 deletions and errata.**
   - Run the D134 grep with the emptied P5 allow-list.
   - Delete `StudioStaticNote.tsx`, `copy.studio.static_soon` and the remaining static `PhasePlaceholder` uses.
   - Drop `src/landing/TryIt.tsx` from the D138 allow-list.
   - Fix the stale comments: `StaticPlayground.tsx:5`, `TryIt.tsx:5`, `WriteChecks.tsx:4`, `vite.site.ts:28-30`, `kinds.ts:285-286`.
   - Run `node scripts/check-forbidden.mjs`.
   - **SPEC Appendix G "P5 errata":**

     | SPEC section | Decisions |
     |---|---|
     | §2.13 | D110, D111, D118, D119 |
     | §3.3 | D99, D107, D109, D112, D115, D117 |
     | §3.4 | D123–D127, D131, D132, D140 |
     | §4.2 P5 gate | D130, D139 |
     | §5 | risk 24 (P6) |
     | §7.5 | D100–D102, D105, D106, D114 (consent bullets 1–2), D137, D138 |
     | §7.6 | D103 |
     | §8.1 | D113, D114, D116 |
     | §8.2 | D115 |
     | Appendix D | CORS row; three rows → P6 |
     | P4-PLAN D65 | D131 (Docker run → P6) |
     | P4-PLAN D72 | D105 (static callback), D107, D111 |
     | P4-PLAN D87 | D130 (local images in P5) |
     | P3-PLAN §13 | #66 zip → P7 |

4. **I-4: local gates.** Under the lock, one after another, with `TEST_DATABASE_URL` set:
   ```bash
   L="flock /tmp/trustagent-heavy.lock"
   test -z "$(gofmt -l backend-go)" && $L go -C backend-go vet ./...
   $L env -u TEST_DATABASE_URL go -C backend-go test -race -p 2 ./...
   $L go -C backend-go test ./internal/decision/... ./internal/credentials/... -run 'TestVectors|TestPKCEVectors'
   $L go -C backend-go test -p 1 -v ./cmd/proxy -run 'Fault|Breaker|Budget|Knowledge|DemoScenarios|E2EScenarios' -count=1 2>&1 | tee /tmp/p5-i4.out
   ! grep -q -- '--- SKIP' /tmp/p5-i4.out
   $L go -C backend-go test -p 1 -run TestTier0Budget ./internal/gate/... && $L make bench-go-smoke
   (cd backend-python && poetry run ruff check . && poetry run black --check . && $L poetry run pytest tests/unit -q)
   $L make check && $L make vectors-ts && $L make recording-check && make test-scripts && $L make lint-workflows
   node scripts/check-claims.mjs && python3 scripts/check_version.py
   ```
5. **I-5: static proof** (`$EVID` = `<lead scratchpad>/p5-evidence`; preview port 24173):
   ```bash
   cd frontend-react; L="flock /tmp/trustagent-heavy.lock"
   $L env VITE_APP_MODE=static VITE_BASE_PATH=/TrustAgent/ VITE_DEMO_DATASET=mock npm run build
   npm run size | tee $EVID/s-size.txt        # landing ≤110, entry ≤ measured+6, chunks, live code not in landing/entry, no preconnect
   for f in index.html 404.html app/index.html app/overview/index.html app/traffic/index.html app/playground/index.html \
            app/evaluators/studio/index.html app/savings/index.html connect/callback/index.html demo/manifest.json
   do test -f dist/$f || echo "missing $f"; done | tee $EVID/s-missing.txt; test ! -s $EVID/s-missing.txt
   test ! -e dist/og && test "$(find dist -name manifest.json -path '*demo*' | wc -l)" = 1
   python3 -c "import json;assert json.load(open('dist/demo/manifest.json'))['kind']=='mock'"
   grep -L 'Content-Security-Policy' $(find dist -name '*.html') | tee $EVID/s-no-csp.txt; test ! -s $EVID/s-no-csp.txt
   du -sk dist | tee $EVID/s-dist.txt         # ≤ 10240
   grep -rhoE 'https?://[A-Za-z0-9.-]+' dist --include='*.js' --include='*.html' | sort -u > $EVID/s-hosts.txt
   ! grep -rqE 'sk-or-v1-[A-Za-z0-9]{16,}|cmp0e2eZq7Lw4Rt9Yx2Vb6Nm' dist
   PLAYWRIGHT_STATIC_PORT=24173 CI=1 $L npx playwright test --project=static --workers=1 2>&1 | tee $EVID/s-playwright.txt
   ! grep -qE '[0-9]+ skipped' $EVID/s-playwright.txt
   cd .. && $L make pages-build 2>&1 | tee $EVID/s-pages-build.txt && (cd frontend-react && find dist -type f | sort) > $EVID/s-pages-files.txt   # gitleaks clean; same file set
   ```
   **I-5b (screenshots, moved from DD-8):**
   - capture `docs/images/v2/{traffic,trace,studio,savings,policies}-{light,dark}.png` from the simulated build with the `launch` screenshot spec, captioned "Simulated" (erratum: the static site hides Policies, D111, so the Playground takes its place);
   - fill the README screenshot block;
   - if DD-7 was cut, use a one-off Playwright script in the scratchpad.
6. **I-6: Docker proof A** (demo-shaped; SPEC gate line 1; project `truthtable-p5proof`):
   ```bash
   export TRUSTAGENT_VERSION=p5local DEMO_PROJECT=truthtable-p5proof PROXY_HOST_PORT=18080 DASHBOARD_HOST_PORT=13000 POSTGRES_HOST_PORT=15432
   export TRUSTAGENT_ADMIN_KEY="$(openssl rand -hex 24)"; export TRUTHTABLE_ADMIN_KEYS="proof=$TRUSTAGENT_ADMIN_KEY"
   flock /tmp/trustagent-heavy.lock make demo OPEN=0 DEMO_PROJECT=$DEMO_PROJECT        # builds p5local once
   python3 scripts/doctor.py --target docker --no-probe --json --expect demo --project $DEMO_PROJECT \
     --proxy-url http://127.0.0.1:18080 --dashboard-url http://localhost:13000 | tee $EVID/a-doctor.json   # no FAIL
   make demo-scenarios DEMO_PROJECT=$DEMO_PROJECT | tee $EVID/a-scenarios.txt                        # 27 × ok, 0 × skipping
   (cd frontend-react && CI=1 npx playwright test --project=stack --workers=1 \
      -g "trace detail|inline claims|policy edit|rollback|fallback|studio|savings|labels") | tee $EVID/a-playwright.txt
   docker compose -p $DEMO_PROJECT exec -T proxy /jevcheck -json > $EVID/a-jevcheck.json; echo "exit=$?" >> $EVID/a-jevcheck.exit   # mock backend; free
   curl -fsS -H "X-TrustAgent-Key: $TRUSTAGENT_ADMIN_KEY" http://127.0.0.1:18080/api/status > $EVID/a-status.json  # build_sha ≠ unknown
   docker inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' ghcr.io/timkush1/truthtable-proxy:p5local > $EVID/a-revision.txt
   make demo-clean DEMO_PROJECT=$DEMO_PROJECT
   ```
7. **I-7: Docker proof B** (a fresh stack; then e2e and faults on the same ports; then the pull fallback):
   ```bash
   unset TRUTHTABLE_ADMIN_KEYS TRUSTAGENT_ADMIN_KEY; export TRUSTAGENT_VERSION=p5local
   export FIRST_RUN_PROJECT=truthtable-p5first PROXY_HOST_PORT=28080 DASHBOARD_HOST_PORT=23000 POSTGRES_HOST_PORT=25432 MOCK_UPSTREAM_HOST_PORT=28090
   flock /tmp/trustagent-heavy.lock make first-run FIRST_RUN_LOCAL=1 FIRST_RUN_TAG=p5local EVIDENCE_DIR=$EVID | tee $EVID/b-first-run.txt   # < 5 min; golden-path ≤ 8; onboarding
   test -z "$(docker compose -p truthtable-p5first ps -q)"                                           # D141: p5first is down
   make e2e-up E2E_PROJECT=truthtable-p5e2e E2E_BUILD= && make e2e-scenarios E2E_PROJECT=truthtable-p5e2e | tee $EVID/b-e2e.txt \
     && make e2e-faults E2E_PROJECT=truthtable-p5e2e | tee $EVID/b-faults.txt; make e2e-down E2E_PROJECT=truthtable-p5e2e     # 26 × ok; 12 × ok
   TRUSTAGENT_VERSION=0.0.0-none flock /tmp/trustagent-heavy.lock docker compose -p truthtable-p5pull up --no-start --no-deps proxy 2>&1 | tee $EVID/b-pull-fallback.txt   # D126: builds, or records the failure
   docker compose -p truthtable-p5pull down; docker image rm ghcr.io/timkush1/truthtable-proxy:0.0.0-none 2>/dev/null || true
   ```
   `make first-run` runs `first-run.spec.ts`, `golden-path.spec.ts` and `onboarding.spec.ts` (`-g "first-run|golden-path|onboarding"`).

   > **Erratum (D165, SPEC Appendix G).** The three specs each claim a fresh stack's one-time setup link, and the target refuses `FIRST_RUN_LOCAL=1` with `FIRST_RUN_TAG`. I-7 runs `make first-run-proof FIRST_RUN_LOCAL=1` (first-run, golden-path and first-run-onboarding, each on its own fresh stack); `make first-run` is the timed first-run alone.
8. **I-8: load proof.** It runs alone. Commit everything first, because the publish refuses a dirty tree. The knowledge Docker run is P6 (D131).
   ```bash
   export TRUSTAGENT_VERSION=p5local
   flock /tmp/trustagent-heavy.lock make load-test-gate LOAD_PROJECT=truthtable-p5load LOAD_BUILD= LOAD_PUBLISH=1 | tee $EVID/c-load.txt   # writes docs/benchmarks/load/<run_id>.json
   python3 scripts/load/report.py --gateway-md docs/benchmarks/load/<run_id>.json    # then commit the run file and the GATEWAY.md block
   ```
   > **Erratum (SPEC Appendix G).** `report.py --gateway-md` reads the published runs and takes no file argument: run `python3 scripts/load/report.py --gateway-md`.
9. **I-9: independent verifier.** A read-only agent reads `$EVID`, the repo and the test logs only, and never touches a stack. It checks:
   - **Static:**
     - `s-hosts.txt` holds only self, openrouter.ai and documentation hosts, or inert text (default values and example names no code requests; erratum, SPEC Appendix G);
     - no Playwright skips;
     - CSP on every HTML file;
     - the size budgets hold;
     - only the simulated tape ships, under `demo/`, and `/og` is absent;
     - gitleaks is clean;
     - `csp-live` asserts `Authorization` only to openrouter.ai, no key in localStorage, cookies, DOM or console, and nothing before consent;
     - the callback spec covers StrictMode single-flight, `replaceState` first, error text never rendered and the return allow-list;
     - the 402 and 401 paths do not consume the cap.
   - **Contracts:**
     - pkce vectors are frozen and run in Go and TS;
     - cost vectors are no longer skipped in TS;
     - compare cells have sources or are `unverified`, with `compare-fetch.tsv` matching;
     - vendor fixtures say "synthetic".
   - **Docker:**
     - `a-doctor.json` has no FAIL;
     - ok counts of 27, 26 and 12;
     - `build_sha` is not `unknown`, and the revision label is set;
     - the jevcheck exit code is recorded;
     - `first-run.json` < 300 s and labelled;
     - golden-path ≤ 8 actions;
     - onboarding passed;
     - the pull-fallback result is recorded.
   - **Load:**
     - the run file validates against `load-run.v1`;
     - `n ≥ 500`;
     - scenario f shows `jev.hedged > 0` and `mock_requests` greater than responses (the f bug is fixed);
     - `measured.json` is still `not_measured`.
   - **Honesty:**
     - no number without a chip on the landing or static pages;
     - caveats sit next to their claims;
     - no "Coming soon" or "TODO";
     - the D134 grep is empty.
10. **I-10: finish.**
    - Merge `feat/v2-int6` into `feat/v2-jev-gateway`. No push.
    - Remove the `p5local`, `0.0.0-none` and scratch images and the P5 proof projects (D141).
    - Do not rebuild the owner's demo; give the owner §21.

---

## 18. P5 gate (exact commands, from the repo root)

```bash
L="flock /tmp/trustagent-heavy.lock"; export TRUSTAGENT_VERSION=p5local
# SPEC §4.2 P5 line 1 (I-6, proof A, project truthtable-p5proof, never truthtable-demo):
$L make demo OPEN=0 DEMO_PROJECT=truthtable-p5proof PROXY_HOST_PORT=18080 DASHBOARD_HOST_PORT=13000 POSTGRES_HOST_PORT=15432 \
  && python3 scripts/doctor.py --target docker --no-probe --json --expect demo --project truthtable-p5proof \
       --proxy-url http://127.0.0.1:18080 --dashboard-url http://localhost:13000
# SPEC line 2 (I-5): label visible; network only self + openrouter.ai (mocked); CSP meta present
(cd frontend-react && VITE_APP_MODE=static VITE_BASE_PATH=/TrustAgent/ VITE_DEMO_DATASET=mock $L npm run build \
  && PLAYWRIGHT_STATIC_PORT=24173 CI=1 $L npx playwright test --project=static --workers=1)
# SPEC line 3 (I-8): budgets; p50/p95 overhead published as a load-run file (scenario e report-only)
(cd frontend-react && npm run size) && $L make load-test-gate LOAD_PROJECT=truthtable-p5load LOAD_BUILD= LOAD_PUBLISH=1
# P5 additions:
$L make check && $L make vectors-ts && $L make recording-check     # pkce and cost run in TS; check-claims, check-version
#   I-5:  make pages-build (gitleaks clean, same file set)
#   I-7:  make first-run (local images) → < 5 min, golden-path ≤ 8 actions, onboarding; e2e-scenarios 26 × ok; e2e-faults 12 × ok
```

---

## 19. Deferred

**P4 deferrals that this plan covers:**

| P4 §19 item | Where in P5 |
|---|---|
| static Studio | WEB-STUDIO, D107 |
| `pkce.ts` and vectors | LV-4, D103 |
| static `/connect/callback/` | LV-5, D105 |
| TS cost runner (D85) | LD-1 |
| knowledge Docker proof (D65) | GO-6 in process; the Docker run → P6 (D131) |
| first-run and golden-path (D87) | DR-8, DR-9, D130, M-1, I-7 |
| shared nonce store and caps; label re-scoring | P7, D133 |

**P6**
- **Owner-run measurements and captures:**
  - `bench-lite`, `calibrate WRITE=1`, `record-demo CONFIRM=1`, and `bench-publish` (which folds the load run);
  - `jevcheck --capture` and real vendor fixtures (the Appendix D P1g rows).
- **Owner checks:**
  - the Pages PKCE callback check;
  - the localhost PKCE check, if not done in P4;
  - GHCR visibility and the multi-arch check;
  - Pages enabled;
  - first-run timing on pulled images in CI;
  - the shared-origin check (§21).
- **Launch:**
  - compare re-verification and owner review;
  - OG, GIF and README from a recorded tape;
  - `bump_version.py 2.0.0`;
  - the PR to main (which exercises `pages.yml` and `release-images.yml` build-only) and the tag.
- **Knowledge:** the Docker run in `ci-knowledge.yml` (D131).
- No cut-line items: every task stays in P5 (§16, Step 0a).

**P7 (v2.1)**
- Multi-replica PKCE nonce store and shared caps (D38).
- A keyed limiter for credential verify across replicas: the per-name verify limit is per replica today (CO-16; `cp:SEC2-SEC3#5`).
- Calibration fits filtered by route keep their labels when `TRUTHTABLE_RETENTION_DAYS` is under 30: labels have no route column, so a route filter loses the labels whose traces expired (CO-17; `cp:EVAL#5`).
- Label re-scoring.
- The #66 zip bundle.
- cosign signing.
- A recorded Studio session.
- Native arm runners, if QEMU proves too slow.
- Earlier P7 items: `SECRET_KEY_PREVIOUS`, engine-side Jev, `JudgeConfig.base_url`.

---

## 20. Risks and how the plan handles them

- **OpenRouter browser CORS is unverified** (high impact).
  - The free checks come first in Step 0a: curl plus a real browser.
  - Scopes L and R are written in advance (D100).
  - The replay stays primary either way, and landing links follow `LIVE_CHECKS` (D99).
- **Error responses may lack CORS headers**, which would make the 402 screen unreachable. `ERRORS_READABLE` triggers the free `/key` diagnosis (D100), and a spec variant covers it.
- **Mocked preflights hide CORS regressions.** `ALLOWED_REQUEST_HEADERS` is enforced in `http.test.ts` and in `mockOpenRouter` (D138).
- **Live, uncalibrated scores disagree with the scripted demo.** The "Live · uncalibrated · your key" label, and no comparison with `expect` (D104).
- **The PKCE code is spent twice under StrictMode.** The single-flight guard and its test (D105).
- **Callback abuse.** The error text is never rendered, the return path has an exact allow-list, the CSRF code is refused, and `location.replace` keeps Back off the spent callback (D105).
- **`github.io` is one origin shared with the owner's other Pages sites.**
  - sessionStorage only, with TTLs and Forget key.
  - External links open as noopener.
  - The SECURITY.md note, and the owner check before Pages is enabled (D102, §21).
- **PKCE-created keys have no spending limit.** The `key_no_limit` hint, the worst-case line in consent, the courtesy caps and owner action #5.
- **A lint guard silently disappears.** Merged `no-restricted-syntax` lists and fixture tests for every rule (D102).
- **Landing headroom is about 6 KB.** Lazy sections, trims, `playground_live` outside the landing copy subset, the import test and the manifest walk (D112, D136).
- **Budgets measured on the wrong tree.** They are re-measured on int6 at Step 0b, with per-stream allowances and one shared validators module (D108, D112).
- **Mock shown as measured.**
  - The D110 dataset split and labels; vendor fixtures labelled synthetic.
  - The honest simulated glass-box badge (D109).
  - The OG and hero tools refuse mock.
  - `check-claims --launch`, and caveats placed next to chips (D128).
  - `measured.json` stays `not_measured` in P5.
- **Compare claims about named vendors are wrong or stale.** Fetched sources only, conservative wording, `compare-fetch.tsv`, `reviewed:false` until the owner reviews, and P6 re-verification (D113).
- **Tape drift between streams.** One runner, extracted in Step 0b and guarded by `recording-check` in WEB-LIVE's gate, plus the I-1 regeneration rule (D135).
- **The dashboard image breaks on new build inputs.** The dockerignore fix and an image build in Step 0b (§2.2 l, §2.4 15).
- **Proofs overwrite the owner's images.** `TRUSTAGENT_VERSION=p5local`, separate projects and ports, and clean-up at I-10 (D141).
- **Strict config refuses test mode with real keys.** Proved without Docker in DR-6; the lead records any exception (D123).
- **The engine arm64 build under QEMU is slow.** Timeouts of 60 and 90 min; native runners if it takes more than 30 min (D125). It is never built locally.
- **Supply chain.** SHA-pinned actions, zizmor, scoped permissions, attestations, digest-pinned bases, no cache on tags, and gitleaks on the Pages artifact (D125, D137, D140).
- **New workflows are untested before P6.** actionlint, zizmor and text tests in P5. The P6 PR runs them build-only through `pull_request` triggers (D124, D125).
- **Compose does not fall back to build on a missing tag.** The I-7 proof records the real behaviour; the docs give the `docker compose build` path (D126). *Disproved at I-7 (`b-pull-fallback.txt`): on a tag the registry refuses, Compose warns "denied" and builds locally (SPEC Appendix G).*
- **Fault expectations are wrong.** Each row is confirmed by `reference_test` against `decide()`, with lead rulings. The in-process pack run comes before Docker, and stateful faults stay in process (D119).
- **The k6 numbers are noisy on the laptop.** The environment is recorded, the proxy is capped at 1 CPU, `n ≥ 500`, the run happens alone under the lock, and the label names the machine (D118).
- **Memory.**
  - 4 heavy slots by default; a 5th only with headroom.
  - Agent sessions are counted; parking.
  - Docker only at M-1 and integration, one proof at a time.
  - The knowledge torch image is out of P5.
- **Paid calls by accident.** Rule 6: unset env keys, the fetch guard in vitest, `blockUnmockedExternal` and `mockOpenRouter` in Playwright, httptest in Go, and the D119 seam that keeps test headers off openrouter.ai.
- **Gates that pass without testing.** `-v` with no `--- SKIP`; `CI=1` with no Playwright skips; non-empty glob assertions; the verifier reads captured evidence.

---

## 21. Owner actions (step by step)

**During P5: nothing is required.**
- The lead makes the free CORS checks (no key, or a fake one).
- Afterwards, refresh your demo when you are ready: `make demo-down && make demo`. P5 never touches your `truthtable` or `truthtable-demo` stacks or their images.

**At P6 (only with you; nothing is pushed or deployed before then):**

1. **Shared origin.** List your other sites under `https://timkush1.github.io/`.
   - If any of them loads third-party scripts (analytics, widgets), tell the lead before Pages is enabled.
   - The options then are a custom domain or an organisation Pages site for the demo. The callback URL follows `location.origin`, so either works without code changes.
2. **Repository visibility.** Confirm the repo is public. GitHub Pages, arm64 runners and artifact attestations are free only for public repos (risk 24).
3. **Push and PR.** Push `feat/v2-jev-gateway` and open the PR to `main` (P6 step 8). The PR runs `pages.yml` and `release-images.yml` as **build only**, so both are tested before anything deploys or publishes.
4. **Enable Pages.**
   1. Settings → Pages → Source: **GitHub Actions**.
   2. Settings → Environments → `github-pages` → Deployment branches and tags → add the tag rule `v*`.
   3. Settings → Secrets and variables → Actions → Variables → `PAGES_ENABLED` = `true`.
5. **GHCR.**
   1. After the first `release-images` run, open each package (`truthtable-proxy`, `-engine`, `-dashboard`) → Package settings → visibility **Public**. The repository link comes from the image's `org.opencontainers.image.source` label; just confirm it shows.
   2. Set the Actions variable `GHCR_PUBLIC` = `true`, so the `anonymous-pull` job runs.
   3. Tell the lead whether `docker buildx imagetools inspect ghcr.io/timkush1/truthtable-proxy:edge` lists two platforms.
6. **Pages PKCE check.**
   1. Open `https://timkush1.github.io/TrustAgent/app/playground/#live` → **Connect OpenRouter**.
   2. Report whether OpenRouter accepted the `/TrustAgent/connect/callback/` address. If it refused, the lead flips `STATIC_CONNECT_PRIMARY` to Paste.
   3. Then set a credit limit on the new key at openrouter.ai/settings/keys (for example $2), or delete it.
   4. If you skipped it in P4, also do the localhost PKCE check.
7. **Comparison review.** Read every vendor cell in `contracts/claims/compare.v1.json`, with its source link. Mark the cells you accept as `reviewed: true`, or ask for changes. `check-claims --launch` refuses anything unreviewed.
8. **Measurements.** Run these with your key in `.env`:
   1. `make bench-lite` (hard cap $5);
   2. `make calibrate WRITE=1`;
   3. `make record-demo CONFIRM=1` (≈ $1; caps $0.50 decision and $3 LLM);
   4. `go -C backend-go run ./cmd/jevcheck --capture contracts/fixtures/vendor/captured` (≤ $0.01);
   5. `make bench-publish`.
9. **Optional.** Create the environment `eval-live` with the secret `OPENROUTER_API_KEY` and a required reviewer.
10. **Social preview.** After `npm run og` on the recorded tape, upload `docs/launch/og.png` under Settings → General → Social preview.

---

## 22. Critique disposition

**Accepted. Feasibility critique:**

| # | Change | Where |
|---|---|---|
| 1 | `LiveKeyChip` lazy in TopBar, plus the manifest check | §2.4 11, D112 |
| 2 | every hard-coded `demo/recorded` path updated; sources out of `public/`; `virtual:trustagent/dataset` instead of a new `VITE_` name; non-empty glob assertions; registry description | D110, §2.4 8, §2.2 c |
| 3 | dashboard dockerignore admits `config/policies.yaml`; an image build in Step 0b | §2.2 l, §2.4 15 |
| 4 | plugins registered in `vite.config.ts`; `landingData` virtual modules; `vite/**` in tsconfig; `launch` starts the preview | §2.4 6, §2.4 12 |
| 5 | full `runGate` signature (specs, apiFormat, calibration, clock) | D135 |
| 6 | no `load-jev-hang` pack entry, no `load` set, no CP-FAULTS (see the change below) | D119, §0 |
| 7 | a separate faults pack | D119 |
| 8 | non-streamed block scenario; counts 27 / 24 / 26 | D139 |
| 9 | stand-in expectations applied automatically under `--upstream all` | D121 |
| 10 | scopes L and R, picked at the end of 0a; `STATIC_CONNECT_PRIMARY` | D100, §16 |
| 11 | `playground_live` namespace; words modules; copy hotfix | D136, §0 |
| 12 | owners for `routes/app/playground.tsx`, `routes/app/savings.tsx`, dockerignore, words | §15 |
| 13 | `csp-network.spec.ts` frozen; new `csp-live.spec.ts` | Rule 2, LV-7 |
| 14 | only the forwarding seam is frozen (`testheaders.go`); `localHost` reused exactly | D119, §2.3 |
| 15 | screenshots move to the lead (I-5b) | §17 I-5b |
| 16 | WEB-REPLAY starts at 0b; CP-TAPE | §0, §16 |
| 17 | tape drift: one runner plus the I-1 regeneration rule | D135, I-1 |
| 18 | onboarding re-run at I-7; mid-wave M-1; in-process fault pack run | D130, §17, GO-4 |
| 19 | 0a streams merge int6 right after 0b | §0 |
| 20 | `faultTable` add-only; the fixture test covers PKCE, key and chat handlers | Rule 4, D122 |
| 21 | `first-run` job only when pushed, with login; jsonschema in `pages.yml` | D124, D125 |
| 22 | a real pull-fallback proof; the D126/§20 contradiction resolved | D126, I-7 |
| 23 | `TRUSTAGENT_VERSION=p5local`; build once; `p5first`-down assertion | D141, I-6, I-7 |
| 24 | DR-2 trimmed (GIT_SHA work already on w5-dx); citation fixed | DR-2, D132 |
| 25 | 4 slots by default; thresholds; agent sessions counted; fuzz `-parallel 2`; knowledge Docker run cut | §0, Rule 7, D120, D131 |
| 26 | budgets re-measured on int6; allowances; one validators module | D112, D108 |
| 27 | `--untracked-files=no`; `backend-go/bench/` ignored | D118, §2.2 l |
| 28 | `jev{…}` in `load-run.v1`; `measured.v1` description; no new "P5" wording in the vectors README | D118, D134 |
| 29 | errata rows for D65, D72, D87 | I-3 |
| 30 | router-relative return path | D105 |
| 31 | CI multiplier for `TestTier0Budget` | D120 |
| 32 | jevcheck exit code recorded | I-6 |
| 33 | `compare-fetch.tsv` | D113, I-9 |
| 34 | Step-0 seams listed explicitly | §2.4 |
| 35 | GO-SCEN + GO-LOAD and DX-RELEASE + DX-OPS merged: 7 streams | §0 |

**Accepted. Security and product critique:**

| # | Change | Where |
|---|---|---|
| 1 | merged lint lists and guard tests | D102, §2.4 13 |
| 2 | key and verifier TTLs; noopener links; SPEC erratum; SECURITY note; owner check; callback URL from `location.origin` | D102, D100, D114, §21 #1 |
| 3 | browser CORS run; `ERRORS_READABLE`, `PKCE_FROM_BROWSER`; `/key` diagnosis | D100, LV-2 |
| 4 | landing CTAs gated by `LIVE_CHECKS` | D99, LD-5 |
| 5 | browser error copy; a 401 forgets the key | D136, D100 |
| 6 | consent replaced, not duplicated; privacy line; worst case; "calls" wording | D114, D101 |
| 7 | billable vs rejected counting; pause after 10 | D101 |
| 8 | SHA pins, `persist-credentials: false`, `permissions: {}`, env-passing, zizmor | D140 |
| 9 | `attest-build-provenance`; no cache on tags; verify command | D125 |
| 10 | base images pinned by digest; the owner's "link the repository" step dropped | D125, §21 #5 |
| 11 | `first-run` login; `anonymous-pull` gated on `GHCR_PUBLIC` | D125, §21 #5 |
| 12 | gitleaks on dist; `VITE_*` secret-name guard; clamps and model patterns | D137, D124 |
| 13 | deploy guard on every event; concurrency | D124 |
| 14 | callback hardening | D105 |
| 15 | PasteKey attributes; no form; Forget aborts; network lint; leak assertions; preconnect ban | LV-6, LV-7, D138, D112 |
| 16 | `ALLOWED_REQUEST_HEADERS` enforced in the mock | D138 |
| 17 | honest simulated glass-box badge | D109 |
| 18 | `reviewed` field; owner sign-off; 400 px layout | D113, §21 #7 |
| 19 | caveat adjacency; never beside the hero latency chip | D128, D118 |
| 20 | extra paths; `dataset.ts` kind check | D110 |
| 21 | `first-visit.spec.ts`; first-viewport checks; 400 px, dark and axe on every new surface | RP-7, LD-8, LV-7, SS-5 |
| 22 | `/og` behind `VITE_LAUNCH_ROUTES`, not prerendered, not in Pages or the image | D115 |

**Answers to the draft's open points.**
- **Only `v1/systemone` allows CORS:** it is chosen at build time, with no runtime fallback (D100).
- **Counting 4xx:** they do not consume the cap; the panel pauses after 10 in a row (D101).
- **D110 and the P6 path:** fine with the extra paths, the kind check and the served path `demo/`.
- **D118:** accepted, with caveat adjacency.
- **D130:** local images in P5; pulled images in P6 CI.
- **Number of streams:** 7.

**Rejected or changed:**

| Critique item | Disposition | Reason |
|---|---|---|
| Feasibility 6: "the `X-Mock-Key` forward alone fixes scenario f" | **Changed.** | The gateway keeps an `X-TrustAgent-Scenario` value only if it matches `^[a-z0-9][a-z0-9-]{1,63}$` (`handler.go:218`), so k6's `fault:hang@1` is dropped before forwarding. GO-1 adds test-mode forward-only `fault:` values, sent only to local hosts through the Step 0a seam (D119). Dropping the pack entry and CP-FAULTS stands. |
| Feasibility 2: "runtime paths do not change" under option (a) | **Changed.** | Option (a) is adopted, but the served path becomes `demo/`. Keeping `demo/recorded/` would label the simulated tape "recorded" in every URL. The six runtime references are edited once in Step 0b, before any web stream branches. |
| Feasibility 3: plugins emit `null` in full mode as a second guard | **Rejected.** | The full build still bundles the replay path, chosen at runtime (`main.tsx:30-38`), so a `null` policy would half-break it. The dockerignore fix plus the Step 0b image build is the guard. |
| Feasibility 4: compile the glass-box panel inside Vite's config loader | **Changed** to a committed `glassbox.gen.json` with `--check`. | The config loader does not resolve the decision core's `@/` aliases. A generated file run with `aliasHooks.ts` is simpler, and its drift is visible in review. |
| Feasibility 13: freeze `newScenarioEnv` signatures and require prefixed helpers | **Not needed.** | After the GO merge, one stream owns `cmd/proxy` and `internal/gate` tests. The `test_compose.py` split also disappears with the DX merge. |
| Feasibility 19: move the CI path edit into 0a | **Changed** to the merge rule. | The dataset move is one self-consistent 0b commit. The 0a streams merge int6 right after 0b, and DX does not touch `ci-web.yml` before then. |
| Feasibility 21: owner dispatches the workflows on a non-default branch before the PR | **Replaced** by `pull_request` build-only triggers. | `workflow_dispatch` works only for workflow files present on the default branch, so new workflows cannot be dispatched before the merge. The PR itself runs them without deploying or pushing. |
| Feasibility 22: use `docker compose create` for the pull proof | **Changed** to `up --no-start --no-deps proxy`. | `create` would also create and build the dependent services (engine), which is a heavy build. `up --no-start --no-deps` limits the proof to one image. |
| Feasibility 25: no session below 2 GB available | **Changed.** | The DX-DOCS session runs no heavy command and may run as a light slot at ≥ 1.5 GB. Heavy slots keep the critique's thresholds. |
| Feasibility 35: splitting WEB-LIVE was not proposed; not adopted | **Kept as one stream.** | With 4–5 slots, capacity binds, not the WEB-LIVE path. The D135 extraction and the task reordering already bring CP-LIVE forward to about day 8.5. |
| Security 2: wipe the key on `pageshow` with `persisted` | **Changed** to a re-read plus TTL. | Wiping on every bfcache restore would drop the key on ordinary Back/Forward between the landing and app pages. Re-reading the store, plus the TTLs, covers a Forget in the same tab and "reopen closed tab". |
| Security 10: "no revision label on the images" | **Rejected for that part.** | w5-dx already has `ARG GIT_SHA` and `org.opencontainers.image.revision` in all three Dockerfiles. Digest pinning is accepted. |
| Security 14: always add `key_label` to the auth URL | **Conditional.** | It is used only if OpenRouter's public PKCE docs list it (checked read-only in Step 0a). Otherwise the vector field and code stay out. |
| Security 15: lint `Image.src` | **Dropped.** | The CSP `img-src 'self' data:` already blocks external images, and a reliable lint for property writes on images is impractical. The other network globals are linted (D138). |
| Security 22: a data-free brand OG card from mock before P6 | **Rejected.** | Nothing is public before P6, and P6 produces the real card from the recorded tape. |
| Security 9: no cache on tag builds, within the existing timeout | **Accepted, with a 90 min timeout on tags.** | An uncached engine arm64 build under QEMU can exceed 60 min. |

---

## 23. Carry-overs from P4 (binding addendum)

**How this table was checked.** It was checked read-only against `feat/v2-int5` at `486e6c8`, after P4 I-6 (Docker proof B). I-6 closes no row here: its only fix, `7919ba7`, changes only `StepSeeItWork.tsx`. P4 steps I-7 (the verifier) and I-8 (finish and merge) have not run. `feat/v2-jev-gateway` is still at `e07c455`. The `p4-done` references below use the form `entry#n` and point to `open_issues[n]` in `~/.cache/trustagent-orch/p4-done.json`.

**Rules for using it:**
- At Step 0a, re-check each row against the merged `feat/v2-jev-gateway` head. Drop any row that I-7 or I-8 fixed, and note why.
- Decision numbers here are the P5 draft numbers. CO-1 renumbers them.
- Owners follow §15. A row that touches a frozen file (Rule 2) or an unowned file goes to the lead, at Step 0a or Step 0b.
- New copy keys arrive through the Step 0a seeds or the D136 copy hotfix.

| id | item | evidence | owner | acceptance test |
|---|---|---|---|---|
| CO-1 | **Decision numbers clash.** The P4 integration and fix rounds defined D91–D98 and D76b in SPEC Appendix F (D97: the published full audit stays an Estimate over simulated traffic; D98: corroboration warnings). The draft of this plan used D93–D135 for other things: draft D93 was live placement but is the strict mock judge in P4, draft D94 CORS but "a bind moves the model" in P4, draft D95 caps but app keys in P4, draft D96 browser storage but the dial-time guard in P4, and draft D97/D98 PKCE vectors and the live policy. The draft header also said D91/D92 exist only in commit messages (`ac16702`). | `SPEC.md:3075-3077,3112-3135`; `p5-plan.md:14-18,195-261,1260-1275,1585` | lead, **Step 0a**, before committing `docs/v2/P5-PLAN.md`: **done.** Every P5 decision moved up by **6** (draft D93–D135 → D99–D141; the row's original +4 would have collided with Appendix F D97 and D98), including the citations in §17 I-3 (the Appendix G table) and §22; the mapping table follows this table. The header points at SPEC Appendix F for D91–D98 and D76b. | A script lists every `D<n>` defined in P3-PLAN, P4-PLAN, P5-PLAN and SPEC Appendix F, and finds no number defined twice. `grep -nE '\bD9[3-8]\b' docs/v2/P5-PLAN.md` finds only citations that mean the Appendix F decisions. |
| CO-2 | **P4 already did the build-SHA work** (`3c6ef7d`). `version.GitSHA` exists, `resolveBuildSHA` prefers it and then falls back to `vcs.revision`, and a test covers it. Proof A already showed the stamp: `a-status.json` has `build_sha` `80277f8`, the build commit. The plan's §2.3 `version.go` bullet and GO-7 repeat this work. | `internal/version/version.go:15`; `cmd/proxy/status_api.go:685-702`; `status_api_test.go:326-345`; `p4-evidence/a-status.json`; `p5-plan.md:394,938` | lead, **Step 0a**: drop the §2.3 `version.go` bullet and drop GO-7. The image stamp stays in the §18 proof A check. | Step 0a changes nothing in `version.go`. P5 proof A `a-status.json` `build_sha` equals `git rev-parse --short HEAD`. |
| CO-3 | **`words/studio.ts` already exists.** D136 and Step 0b say "create the empty `words/{live,replay,studio}.ts`". But `studio.ts` came from P4 (Step 0b `fd47dda`, then split at STW-3 `5e585de`). `presentation.ts` re-exports it, and it keeps Studio-only words in `components/studio/studioWords.ts` so the landing chunk stays small. | `src/domain/words/studio.ts:1-9`; `src/domain/presentation.ts:476`; `p5-plan.md:137,463` | lead, **Step 0b**: create and re-export only `live.ts` and `replay.ts`. **WEB-STUDIO** (owns `words/studio.ts` and `components/studio/**`): SS-1/SS-3 extend the existing `studio.ts` and `studioWords.ts`. | Step 0b does not delete or recreate `studio.ts` (check `git log --follow`). The `scripts/check-size.ts` landing budget passes. The prefix test passes. |
| CO-4 | **Static Benchmarks conflict.** P4 I-1 kept `/app/evaluators/benchmarks` visible in the static build (D72), and SPEC Appendix F §3.3 says the static build "renders committed benchmark runs". P5 D111 says Benchmarks stays hidden, and moves the glob from `@bench/runs` to `@bench/results`. | `src/components/replay/staticPages.ts:13-17` (`7af34f8`); `staticBuild.test.tsx:68,167`; `SPEC.md:3101`; `benchmarkRuns.ts:7`; `p5-plan.md:213` | lead, **Step 0b**: decide, record the SPEC erratum, and edit `benchmarkRuns.ts`. **WEB-REPLAY** (owns `components/replay/**`): RP-4 changes `staticPages.ts` and `staticBuild.test.tsx` if the page becomes hidden. | `staticBuild.test.tsx` asserts the chosen visibility. A committed measured `docs/benchmarks/results/*.json` renders and a mock run does not. The static nav item count matches SPEC §3.3. |
| CO-5 | **Policy warnings have no machine-readable codes.** The web matches Go's English text with regexes. | `components/policies/policyWarnings.ts:1-10,53-79`; `internal/policy/check.go:226,421,523`; `engine.go:350`; `api.ts:895` (`warnings: string[]`); `SPEC.md:3148`; `int:i1-3#4` | lead, **Step 0a** (`internal/policy` is unowned; `api.ts` and `contracts/ui` are frozen): Go emits a `code`, `path` and details for each warning. Add an additive `PolicyState.warning_details[]` to `api.ts`, the schema and new fixtures, with copy keys `policies.warnings.<code>`. **Step 0b** (`components/policies` is unowned): `policyWarnings.ts` words warnings by code and falls back to the text. | Go test: every warning has a code from a closed list. Vitest: a fixture with the same code and reworded text gives identical words. `make gen-api-schema-check` passes. |
| CO-6 | **Contract copy keys for words still in prefixed modules** (D86): counterfactual, method, budget and price-source words; quality and calibration words; security words; Studio words; the AdoptPanel literal. | `words/costs.ts:24-41,122`; `words/evaluators.ts:63,93-125`; `words/security.ts` (about 60 literals); `components/studio/studioWords.ts`; `AdoptPanel.tsx:143`; `SPEC.md:3148-3149`; `int:i1-3#4` | lead, **Step 0a** §2.2.d: seed `copy.savings.*`, `copy.settings.budgets.*`, `copy.evaluators.*`, `copy.security.*`, `copy.studio.*`. **Step 0b**: rewire `words/{costs,evaluators,security}.ts` (unowned) and add the lint guard (`src/test/lint/**` is frozen). **WEB-STUDIO** SS-6: rewire `studio.ts` and `studioWords.ts`. | The `src/test/lint` guard fails on any user-facing literal of 3 or more words in `src/domain/words/**` or `studioWords.ts` that does not come from `copy`. `make contracts-check` passes. The landing size budget passes. |
| CO-7 | **`price_far_above_contracts` loses its details** (ratio, contracts price, index). PATCH `/api/settings` keeps only the code and message, so the Prices panel cannot attach the warning to its row. | `cmd/proxy/settings_api.go:659-664` (drops `Path` and `Details`), `:87-91` (`warningDetails` holds only names and routes); `api.ts:574-579`; `components/savings/PricesPanel.tsx:232`; `int:i1-3#4` | lead, **Step 0a** (`settings_api.go` is unowned; `api.ts` is frozen): send `path` and `details{ratio, contracts_per_mtok, index}`, extend `WarningDetails` in `api.ts` and the schema, and add the fixture `SettingsView.price-far-above.json`. **WEB-REPLAY** (owns `components/savings`): attach the warning to row `index`. | Go test: an override at 150× gives a warning with `path:"pricing_overrides[0].input_per_mtok"`, `details.ratio≈150` and `details.index:0`. Vitest: PricesPanel shows the `copy.settings.price_far_above_contracts` words on that row. |
| CO-8 | **Studio "Draft with AI" says "engine unavailable" when the judge answers 401 or 402.** It should say "out of credits" or "key refused". | `backend-python/src/truthtable/grpc/server.py:1129-1133` (every other error becomes `UNAVAILABLE judge_unavailable`); `cmd/proxy/studio_api.go:1199-1210` (becomes 503 `engine_unavailable`); `data/errors.ts:169`; `SPEC.md:3150`; `int:i1-3#4` | lead, **Step 0a** (`backend-python/src` and `studio_api.go` are unowned; `contracts/ui` is frozen): Python sorts provider 401/402 into statuses that carry `judge_unauthorized` / `no_credits` (D40 kinds). Go maps them to 503 with that code and `settle(0)`. Add the copy keys. **Step 0b**: add the `data/errors.ts` hints (unowned). **WEB-STUDIO** SS-3: word them, with a link to Settings → Connections. | Pytest: a 402 provider gives `RESOURCE_EXHAUSTED no_credits` and a 401 gives `PERMISSION_DENIED judge_unauthorized`. Go fake-drafter test: 503 `no_credits`, and the reservation is released. Vitest: Studio never shows "engine unavailable" for these. |
| CO-9 | **Docker run of the `handbook` knowledge scenario** (D65). D131 already covers it, and §19 P6 already lists `ci-knowledge.yml`. | `SPEC.md:3151-3152`; `P4-PLAN.md:1845`; `p5-plan.md:248,934-937,1053,1440` | **GO** GO-6 (in process). **DX-RELEASE** DR-10 (cut line). The Docker run itself is P6. | `TestKnowledgeScenarioEndToEnd` and `TestKnowledgePolicyInSync` pass in §18. §19 keeps the P6 row for `ci-knowledge.yml`. |
| CO-10 | **The app shell opens the live stream before sign-in**, including on `/app/setup`. The ticket is refused with 401 and retried with backoff, which fills the console. | `src/main.tsx:42` (always calls `startLiveStream`); `data/liveStream.ts`; `data/live/streamClient.ts:14,70-92`; `data/live/liveDataSource.ts:93-94`; `int:i1-3#5` ("minor, not changed") | lead, **Step 0b**: `main.tsx` is frozen, and `data/liveStream.ts` and `data/live/**` are unowned. Start or resume the stream only with a session, and pause on 401 until sign-in. | Proof B Playwright: on `/app/setup` with no cookie, no WS ticket request, no WebSocket and no console error for 10 s. After the setup link is claimed, the stream connects within 5 s. Vitest covers the gating. |
| CO-11 | **`llm_cap_hit_at` shows the restart time** when the boot seed finds today's cap already spent. | `internal/costs/budget.go:44-46,227-238` (`hitLocked` uses now); `ledger.go:389` (`Seed`); `gcost:s2:review#5`, `cp:COST#4`, `int:i1-3#5` | lead, **Step 0a** (`internal/costs` is unowned; `internal/store` is frozen): `Seed` takes the time from the stored row whose running cap cost first reached the cap. Unpriced rows are priced by D91. | DB test: rows cross the cap at 10:05 and `Seed` runs at 12:00, so `CapHitAt` is 10:05. With no crossing it is nil. `/api/roi/summary` `sampling.llm_cap_hit_at` equals it. |
| CO-12 | **`TRUTHTABLE_PRICE_TABLE_FILE` is not checked at config load.** A bad file only logs an error, and the embedded table is used. | `internal/config/config.go:119`; `cmd/proxy/costs_wiring.go:54-61`; `int:i1-3#5` | lead, **Step 0a** (`internal/config`, the wiring, and the status and settings warnings are unowned). **DX-RELEASE** DR-7: doctor reports it. DR-7 is on the cut line; if it is cut, the `/api/status` warning alone meets the test. | Go test: a missing or invalid file gives a diagnostic keyed `TRUTHTABLE_PRICE_TABLE_FILE` in SettingsView and in the `/api/status` warnings. Under `TRUTHTABLE_STRICT_CONFIG=true`, `proxy -check-config` exits 2. A valid file gives no diagnostic. `scripts/tests/test_doctor.py` expects WARN. |
| CO-13 | **The demo Savings "full LLM audit" figure reads $0.** In the demo, `include_mock` is on and the mock judge costs $0, so the uniform-sample mean is $0 and is labelled estimate. The static replay does not produce this figure at all. | `cmd/proxy/roi_api.go:274-286`; `internal/store/stats.go:575-582`; `data/controlPlane.ts:61`; `providers/pricing.py:16`; `data/replay/stats.ts` (no `llm_full_claim_audit`) | lead, **Step 0a** (`roi_api.go` is unowned; `internal/store` is frozen): when every sampled audit is simulated, never use a $0 mean. Fall back to `static_assumptions` (estimate), or price the tokens at the reference judge and label the figure simulated. **WEB-REPLAY** RP-2: add the same figure to the replay `roiSummary`. Re-check after P4 I-7. | `roi_api_test`: 40 uniform mock audits at $0 give an `llm_full_claim_audit.usd` that is not 0 (or null with a reason), labelled something other than measured. Proof A shows a figure above zero. Vitest: replay `roiSummary` includes the figure. |
| CO-14 | **D88: the real OpenRouter localhost PKCE check is still unverified.** The self-hosted `CONNECT_PRIMARY` stays `connect` (`45d2550`). | `SPEC.md:3041,3109`; `P4-PLAN.md:1855`; `p5-plan.md:1430,1523-1525`; `int:i5#7` | Owner action: §21 step 4, otherwise P6 per §19. lead **Step 0a** sets `STATIC_CONNECT_PRIMARY` from the CORS result and the owner's Pages-callback report only, never from D88. **DX-DOCS** DD-1. | The Appendix D row says "unverified" or gives a dated result. `check-claims` fails on any "verified PKCE" wording while that row says unverified. LV-7 asserts that Paste is always visible. |
| CO-15 | **The reason filter always looks back exactly 30 days**, which is right only at the default retention. | `components/traffic/trafficRows.ts:61-70` (`REASON_LOOKBACK_DAYS`); `int:i1-3#5` | **WEB-REPLAY** RP-2 (owns `components/traffic`): show the window on the filter chip. A `retention_days` field would need Step 0a. | Vitest: the reason chip says "last 30 days". If the field is added, `since` follows it. |
| CO-16 | **No keyed limiter for credential verify across replicas.** It was "deferred to P5" at CP-SEC2/3 but is missing from the P5 plan. | `cp:SEC2-SEC3#5`; `SPEC.md:3146-3147` ("the per-name verify limit is per replica") | lead, **Step 0a**: add it to §19 P7 next to "Multi-replica PKCE nonce store and shared caps (D38)" (`server.go` is frozen). **DX-DOCS** DD-4: a `docs/SECURITY.md` known-gaps line next to the D38 multi-replica bullet. | §19 P7 lists it. `docs/SECURITY.md` says the verify limit is per replica. |
| CO-17 | **Calibration fits filtered by route lose labels** when `TRUTHTABLE_RETENTION_DAYS` is under 30, because labels have no route column. P4 flagged it as a §19 candidate, but neither plan lists it. | `cp:EVAL#5`, `geval:s2:review#10` | lead, **Step 0a**: add it to §19 P7. **DX-DOCS** DD-4: one line in the calibration guide (`docs/EVALUATORS.md`). | The §19 row and the docs line exist. |
| CO-18 | **A calibrated `warn_at` below the route's `escalate_from` is refused with 422**, and the web only says "Preview impact failed". | `internal/policy/check.go:533-550`; `internal/decision/calibrate_fit.go` (does not check `escalate_from`); `components/evaluators/AdoptPanel.tsx:143`; `wev:s1:review#6` | lead, **Step 0a** (`internal/decision` is contract-owned and frozen; `internal/policy` is unowned): the fit carries a warning code, or adopt answers with a code. **Step 0b** (`components/evaluators` is unowned): AdoptPanel words the reason with the fix. | Go test: such a fit carries the code. Vitest: AdoptPanel shows the specific reason, not "Preview impact failed". |
| CO-19 | **P5 must follow three new P4 decisions.** D93: the mock judge fails any audit it has no script for with `mock_fixture_missing`, and `judge` is optional in `scenarios.v1`. D95: binding an upstream key for every caller needs a key with the `ingest` scope (an admin key does not count; `9c83272`). D96: outbound dials go through netguard (`d54d956`). | `SPEC.md:3121-3131`; `providers/mock.py:18-23`; `contracts/schema/scenarios.v1.schema.json:18` (`judge` not required) | lead, **Step 0a** §2.2.b (`validate.mjs` is frozen): require an explicit `judge` (fixtures or null) on faults-pack scenarios and on the D139 twin. **GO**: GO-1/GO-4 pack content; GO-12 `jevcheck` uses the `internal/jev` transport. **DX-RELEASE**: in DR-8/DR-9, `journey.ts` creates the ingest app key before any such bind. | `make e2e-faults` and `make demo-scenarios` leave 0 audits with `error_kind=mock_fixture_missing` (SQL count in the evidence). A `jevcheck` test refuses 169.254.169.254. A `validate.mjs` self-test covers a missing `judge`. |
| CO-20 | **Screenshot runs changed the stack.** They saved `proof_*` checks without rolling them back, so the glass box showed "Competitor mentions" twice. | `int:i5#6`; `36e0bb1` | **DX-DOCS** DD-7 (capture tooling, `e2e/launch/**`; on the cut line) and lead I-5b: capture on a fresh stack, or roll back after each step. If DD-7 is cut, only the I-5b rule applies. | The `/api/policy` version and hash are the same before and after a capture run. The capture spec asserts that no question appears twice in the glass box. |
| CO-21 | **`docs/WALKTHROUGH.md` points to the deleted `hooks/useWebSocket.ts`.** | `docs/WALKTHROUGH.md:277,414,435`; `dx:s1:review#9` (`docs/PLAN.md:68` is a historical 1.0 plan mention) | **DX-DOCS** DD-3. If DD-3 is cut, the pointer page replaces the file. | `grep -n useWebSocket docs/WALKTHROUGH.md` finds nothing. The link check passes. |
| CO-22 | **The rest of the P4 §19 P5 list** is already covered by P5 §19: static Studio (WEB-STUDIO), `pkce.ts` and its vectors (LV-4), the static `/connect/callback/` (LV-5), the TS cost runner (LD-1), and first-run and golden-path (DR-8, DR-9). The shared nonce store and caps, and label re-scoring, move to P7 (D133). | `P4-PLAN.md:1837-1849`; `src/lib/decision/vectors/kinds.ts:285` (cost skip); `a76c658` (vectors-ts: 1 skipped) | As listed in P5 §19. | `make vectors-ts` shows 0 skipped, and `kinds.ts` has no cost skip. `static_soon` is removed at I-3. DR-8 asserts under 5 minutes. §19 P7 has both rows. |
| CO-23 | **The Playground offers the `knowledge-only-capped` example whenever the knowledge add-on is on, even if the policy has no `handbook` route.** The example then runs against a route the gateway does not serve. The `formHolds` doc comment says "on a route the running gateway already serves", but the code never checks `routes`. | `components/playground/playgroundModel.ts:113-130,159-160`; `PlaygroundForm.tsx:33,43-44`; `PlaygroundPage.tsx:191`; `config/policies.pending.yaml:17`; `contracts/demo/scenarios.v1.json:1462-1470`; `wsv:s1:review#8` | **WEB-LIVE** (owns `components/playground/**`), in LV-6 or LV-8: offer a scenario only when its `route` is in the served `routes` (or the routes are unknown). **GO** GO-6: the generated `config/policies.knowledge.yaml` defines `handbook`. | Vitest: with `knowledge: true` and `routes` without `handbook`, `knowledge-only-capped` is not offered. With `handbook` in `routes`, it is offered. `TestKnowledgePolicyInSync` passes. |

**Step 0a re-check (2026-10-09, against `feat/v2-jev-gateway` `a03ecb3`, after P4 I-8).** Every row was re-read against the merged head. One row's lead part was already fixed in P4 (CO-13); the others stand, and the lead's Step 0a parts are done:

| id | status after Step 0a |
|---|---|
| CO-1 | **Done** (`8c68cda`): +6, not +4, because Appendix F also defines D97 and D98; the mapping table follows. |
| CO-2 | **Done:** the §2.3 `version.go` bullet and GO-7 are dropped; `version.go` is unchanged. |
| CO-3 | **Step 0b done** (`84c4724`): only `words/live.ts` and `words/replay.ts` are new; `studio.ts` is untouched (`git log --follow` unchanged), the D86 prefix test covers all six modules, and the landing budget holds (104.9 of 110 kB). WEB-STUDIO SS-1/SS-3 open. |
| CO-4 | **Step 0b decided** (`84c4724`): D111 holds, Benchmarks stays hidden in the static build (the six-item nav); `benchmarkRuns.ts` reads `@bench/results/*.json`; SPEC Appendix G starts with that erratum. WEB-REPLAY RP-4 (`staticPages.ts`, `staticBuild.test.tsx`) open. |
| CO-5 | **Step 0a part done** (`6615192`, `12486e8`): Go `WarningCodes` with details, `PolicyState.warning_details`, `ValidateResult` warnings with codes (both now in `api.v1`), fixtures `PolicyState.coded-warnings.json` and `ValidateResult.coded-warnings.json`, copy `policies.warnings.<code>`. Step 0b part open. **Step 0b done** (`17f926f`): `policyWarnings.ts` words by code (fallback: the text, same templates); the coded fixtures with reworded messages give identical words. |
| CO-6 | **Step 0a seeds done** (`12486e8`): `copy.costs.*` (the cost words; **not** `copy.savings.*`, which the landing bundles), `copy.settings.budgets.*`, `copy.evaluators.*` (with `adopt_reasons`), `copy.security.*`, `copy.studio.*`. Step 0b and SS-6 rewires open. **Step 0b done** (`48d04b6`): `words/{costs,evaluators,security}.ts` read copy; `src/test/lint/wordsFromCopy.test.ts` guards `words/**` and `studioWords.ts`, with `studio.ts` and `studioWords.ts` pending SS-6 (the lead drops both entries at I-3). |
| CO-7 | **Step 0a part done** (`6615192`): `path` and `details{ratio, contracts_per_mtok, index}`, fixture `SettingsView.price-far-above.json`. WEB-REPLAY part open. |
| CO-8 | **Step 0a part done** (`6615192`): engine `RESOURCE_EXHAUSTED no_credits` / `PERMISSION_DENIED judge_unauthorized`, gateway 503 `no_credits` / `judge_unauthorized` with the reservation released, copy `studio.draft_no_credits` and `studio.draft_key_refused`. Step 0b hints and SS-3 open. **Step 0b done** (`17f926f`): `data/errors.ts` hints for `no_credits` and `judge_unauthorized`. |
| CO-9 | Open (GO GO-6, DX-RELEASE DR-10). |
| CO-10 | **Step 0b done** (`84c4724`): `startLiveStream(…, {gateOnReadAccess})`, on in the full build, opens the socket only once `GET /api/status` answers and pauses on 401 until a status or session read succeeds; vitest covers it. The proof B Playwright check runs at I-7. |
| CO-11 | **Done** (`6615192`): `store.CapCrossedAt`, used by `Ledger.Seed` only when the seeded spend reaches the cap with no hit recorded. |
| CO-12 | **Step 0a part done** (`6615192`). DR-7 (doctor WARN) open. |
| CO-13 | **Lead part dropped: fixed in P4.** `4c24274` makes free mock audits fall through to `static_assumptions` instead of scaling $0, and Appendix F D97 keeps that published figure an Estimate (`roi_api_test` and `stats_test` cover it). WEB-REPLAY RP-2 (the replay `roiSummary` figure) stays open. |
| CO-14 | **Step 0a part done:** `STATIC_CONNECT_PRIMARY = 'connect'` from the CORS result alone (§2.1); the Appendix D localhost PKCE row still says unverified. DD-1 open. |
| CO-15 | Open (WEB-REPLAY). |
| CO-16 | **Done:** §19 P7 row. DD-4 open. |
| CO-17 | **Done:** §19 P7 row. DD-4 open. |
| CO-18 | **Step 0a part done** (`6615192`): the policy check refuses such thresholds with the code `escalate_not_below_warn` and `{route, ref, warn_at, escalate_from}` ("adopt answers with a code"; `internal/decision` and the frozen `calibrate_fit` vectors stay unchanged); copy `evaluators.adopt_reasons.escalate_not_below_warn`. Step 0b AdoptPanel open. **Step 0b done** (`17f926f`): AdoptPanel words `escalate_not_below_warn` with the route and both thresholds, never "Preview impact failed". |
| CO-19 | **Step 0a part done:** `validate.mjs` requires an explicit `judge` on every scenario of either pack (the D139 twin included), with a self-test. GO and DX-RELEASE parts open. |
| CO-20 to CO-23 | Open (owners as listed). |

**CO-1 mapping (draft → P5-PLAN).** Citations of P3, P4 and Appendix F decisions (D1–D98, D76b) are unchanged.

| Draft | Now | Subject |
|---|---|---|
| draft D93 | D99 | Where "Try Jev live" lives, and how the landing links to it |
| draft D94 | D100 | Browser transport, CORS, headers, live scope |
| draft D95 | D101 | Caps and what counts |
| draft D96 | D102 | Browser storage, key lifetime, lint guards |
| draft D97 | D103 | PKCE vectors |
| draft D98 | D104 | Live policy, evaluators, calibration |
| draft D99 | D105 | Static `/connect/callback/` |
| draft D100 | D106 | Frame-busting |
| draft D101 | D107 | Static Studio scope |
| draft D102 | D108 | Browser drafter parity and validators |
| draft D103 | D109 | Glass box on replayed traces |
| draft D104 | D110 | Datasets: simulated vs recorded |
| draft D105 | D111 | Benchmarks path and static visibility |
| draft D106 | D112 | Bundle budgets |
| draft D107 | D113 | Comparison table |
| draft D108 | D114 | Claims, consent copy, commands, config |
| draft D109 | D115 | `/og`, hero GIF, screenshots |
| draft D110 | D116 | Landing Studio strip and glass-box panel |
| draft D111 | D117 | Replay honesty, guide, first visit |
| draft D112 | D118 | Load-run format and publishing |
| draft D113 | D119 | Fault channel, fault pack, expectations |
| draft D114 | D120 | Tier-0 benchmarks |
| draft D115 | D121 | Generated answers and recording runs |
| draft D116 | D122 | jevcheck and vendor fixtures |
| draft D117 | D123 | `make record-demo` |
| draft D118 | D124 | Pages workflow never deploys without the owner |
| draft D119 | D125 | Release images |
| draft D120 | D126 | Pull-based quickstart |
| draft D121 | D127 | Version tooling |
| draft D122 | D128 | `check-claims` |
| draft D123 | D129 | Doctor complete |
| draft D124 | D130 | first-run and golden-path in P5 |
| draft D125 | D131 | Knowledge Docker proof (D65) |
| draft D126 | D132 | Make fragments and shared build files |
| draft D127 | D133 | Deferrals |
| draft D128 | D134 | End-of-P5 phase grep |
| draft D129 | D135 | One browser gate pipeline |
| draft D130 | D136 | Copy namespaces and words |
| draft D131 | D137 | Build-time guards |
| draft D132 | D138 | Network invariant |
| draft D133 | D139 | A non-streamed block scenario |
| draft D134 | D140 | Privileged workflow hardening |
| draft D135 | D141 | Proof isolation |

**Reviewed and not carried (no P5 action):**
- **Fixed in P4:**
  - D91 cap pricing (`5cc8dec`; `costs_wiring.go:95`, `studio_api.go:1277`);
  - D94 bind model reset (`2f07a2b`);
  - D96 netguard (`d54d956`; `internal/jev/transport.go:24`, `internal/upstream/upstream.go:38,119`);
  - the `mock_judge` flag (`c9f43b0`);
  - the Playground `credentials_unavailable` wording (`internal/proxy/playground.go:235`);
  - the strict `-check-config` test for an auto backend with no key (`cli_test.go:95-99`);
  - the Studio `tab=calibration` link (`79b4c87`);
  - the probe-booking test (`cmd/proxy/probe_cost_test.go`; `cp:SEC2-SEC3#8`);
  - copy keys for the settings PATCH field codes and the calibration reject reasons and origins, now in `contracts/ui/copy.v1.json` (`gsec:s2:review#2`, `geval:s2:review#7`);
  - the proof B onboarding spec: 9 passed against a stack (`486e6c8`; `int:i5#1`);
  - D66: no writer-caused flake at proof A (`36e0bb1`) or proof B (`486e6c8`), so the trace flush stays async;
  - cut-line items GW-1/2/3, COST-8, DX-6 and EVW-6, all merged.
- **Already in the P5 plan:** the labels spec is in the proof A grep (`p5-plan.md:1323`; `int:i5#2`).
- **By design or no realistic path:**
  - late decision attempts after the final flush are dropped and counted (D51);
  - Python token-count and float overflow (`py:s2:review#1-2`);
  - the trace memory copy can evict traces when more than 2,000 arrive in one flush (`ggw:s1:review#5`);
  - `studio_results` is not written when the caller disconnects, and nothing reads it (`gstudio:s1:review#4`);
  - verifying a non-env name without a database answers 404 (lead decision, `cp:SEC2-SEC3#6`);
  - the legacy `WsEvent.status.budget.json` detail sentence (fixtures are append-only; the `budget-decision` and `budget-llm` fixtures are correct);
  - the engine's own price table, used before the first runtime-config push, replaces the contracts table with `TRUTHTABLE_PRICE_TABLE_FILE`, where Go layers the file over contracts (D53; `gcost:s1:review#3`). The push installs the gateway's effective table (`grpc/server.py:1057-1059`).
- **Candidates for P7 if wanted:**
  - the label-queue picker filters by each check's latest version only. Fixing it needs a server change to the #39 `evaluator` parameter (`wev:s1:review#7`);
  - a time budget on the #51 `would_gain` pass. `internal/evidence/suggest.go` limits only the counts (500 bodies × 20 candidates); at those limits the pass can take tens of seconds (`gstudio:s1:review#5`).
