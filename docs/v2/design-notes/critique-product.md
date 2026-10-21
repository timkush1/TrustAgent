# TrustAgent v2 design review: product and owner-goals lens

I checked these against the working tree:
- `docker-compose.yml:181-182` publishes the proxy on `0.0.0.0:8080`.
- `handler.go:63` declares `Content string`.
- `handler.go:117` honours `test_response` unconditionally.
- `main.py::init_provider` falls back to Ollama.
- `scorer.py:163-169` divides by `total`.
- `client.ts:21` bakes `VITE_TRUTHTABLE_API_KEY` into the bundle.
- `pyproject.toml` has no `addopts`.
- The frontend is currently on Vite 5, Vitest 4 and TS 5.9.
- `ts_sdk_python*.md` is untracked at the repo root.

## HIGH

**H1. The auth model is an open relay before setup and breaks all traffic after setup.**
- Canonical G2.3 says `auth_mode=open` when there are no keys, and unauthenticated callers then get `{ingest, read}`. Compose publishes the proxy on all interfaces (verified). Once a user binds an upstream credential in the wizard (inject mode), anyone on the network can spend that key through `/v1/*` and read trace previews.
- `auth_mode` flips to `keys` as soon as any live `api_keys` row exists. The setup claim creates a `setup-admin` row, so finishing onboarding instantly 401s every keyless app request. In `make demo` it also kills trafficgen, which sends no key, so the live feed dies silently.
- Fix:
  - Add an explicit `TRUTHTABLE_INGEST_AUTH=keys|open`. Default it to `keys`. Allow `open` only when no upstream credential is bound.
  - Admin keys must never change the ingest mode.
  - The wizard's "first request" step generates an ingest key and shows the `X-TrustAgent-Key` snippet.
  - `configure --ensure` provisions `TRUTHTABLE_API_KEYS=trafficgen=<rand>`, and trafficgen sends it.
  - Compose binds `127.0.0.1:${PROXY_HOST_PORT}` and `127.0.0.1:${DASHBOARD_HOST_PORT}` by default. `BIND_ADDR=0.0.0.0` is opt-in, and startup refuses it in open mode.
  - Add a test to the P4 auth matrix: "inject mode + no ingest key → 401".

**H2. The headline "hallucination rate" is a biased-sample metric presented as a traffic rate.**
- `OverviewStats.hallucination_rate` is hallucinated divided by audits with GROUNDED or HALLUCINATED. Audits are 100% of flagged or escalated responses (high lane) plus 20% of passes plus the 5% uniform sample. The rate is therefore inflated by construction, and the same applies to `by_route`, `by_model` and `claim_verdicts`. This directly violates "no fake metrics".
- Fix:
  - Store `audits.inclusion_prob` (1.0 for the high lane, `pass_sample_rate` for sampled passes, the uniform rate for uniform samples).
  - Report a Horvitz–Thompson estimate with n and a 95% CI, or compute it only over `uniform_sample` audits.
  - Otherwise label the value "among audited responses (not a traffic rate)".
  - Add a decide/aggregate vector for it.

**H3. The cost story is apples to oranges, and the default config costs 25–60× the $0.2/1k headline.** All figures below are my estimates.
- The gate runs a response-level check (~5k tokens, about $0.21 per 1k answers). It is compared against about $35 per 1k for a full claim-level LLM audit. The honest counterfactual is the same response-level check done by Haiku: about 5k in + 150 out ≈ $0.006 per response, or about $6 per 1k. That is still roughly 27× cheaper and still compelling, but defensible.
- The shipped defaults also include `pass_sample_rate 0.2`, `uniform_sample_rate 0.05` (forced LLM-only) and a deep audit on every non-pass. That is roughly $5–12 per 1k in LLM spend, which dominates the $0.21 gate.
  - The ROI instrumentation alone (5% × $35.5/1k ≈ $1.8/1k) costs about 8× the gate it measures.
  - The $5/day LLM cap is hit after about 700–1,000 responses. After that only the high lane runs, so the uniform-sample ROI method stops silently.
  - dx's guide claim "$5 buys ≈26,000 gated responses" ignores that the same OpenRouter key also pays the judge.
- Fix:
  - `value-claims` gets two counterfactual rows: `llm_same_checks` (the headline) and `llm_full_claim_audit` (the $35 row, captioned "different output: claim-level explanations").
  - `RoiSummary.counterfactual` gains a `kind` field.
  - Change the defaults: `pass_sample_rate: 0.02`, `uniform_sample_rate: 0`, plus `roi.shadow_samples_per_day: 20` (capped, not a percentage). Use `fitted_from_audits` or `static_assumptions` otherwise.
  - The landing calculator shows "total cost with default policy" next to "gate cost".
  - The JEV-ACCESS cost table includes judge spend.
  - Enforce the LLM cap per lane, with a banner when ROI sampling stops.

**H4. `make demo` shows an empty or mislabelled dashboard.**
- `GET /api/overview` defaults to `include_mock=false`. In demo mode every trace is `mock_decision`/`mock_judge`, so Overview is zero or rate-less.
- `ValueLabel` has no mock value, so mock Jev costs computed from the price table appear in Savings as `label:"measured"`.
- Fix:
  - `include_mock` defaults to `SystemStatus.demo`, and every surface shows the "Simulated" banner.
  - Add `'simulated'` to `ValueLabel` and `CostRecord.cost_source`.
  - The ROI API returns `label:'simulated'` whenever any contributing row came from a mock.
  - Define exactly what `include_mock=false` excludes (counts and rates).

**H5. The "recorded real audits" are scripted, and the real recording can never show a block or fallback.**
- `record-demo` uses trafficgen in test mode, so every upstream answer is hand-written (`test_response`). Only the Jev and judge verdicts are real. The label "Recorded {date} · {snapshot}" implies real LLM traffic.
- `require_calibration:true` caps block and fallback to flag until a `measured` calibration exists, and that only comes from P6 `make calibrate`. A real-key recording made before then shows zero blocks and fallbacks. The 60-second script's "switch to block → fallback message" beat and the landing hero then cannot be produced.
- The quality of the mock calibration file is unspecified. If it is `provisional`, the `make demo` block path is dead too.
- Fix:
  - Add a per-trace `answer_source: scripted|generated` and put it in the label ("scripted prompts & answers; live Jev + judge verdicts").
  - Add a `generated` scenario subset where a cheap upstream model on the same OpenRouter key actually answers from the scenario docs.
  - Ship `mock__jev-heuristic-v1.json` with `quality: measured`, scoped to that snapshot and flagged `kind: mock`.
  - Sequence `record-demo` after the first `calibrate WRITE=1`. Alternatively, the showcase route sets `require_calibration:false` with a visible "uncalibrated" chip.
  - The risk register must list this dependency.

**H6. The owner's Jev access and guide come last, while the owner needs them on day one.**
- The key is only an OpenRouter account plus about $5 of credit, yet the plan defers it to P6. The access guide is written in P6, and ten or more [A] items (PKCE on localhost and Pages, `usage.cost`, the 32k limit, `/alpha` vs `/v1/systemone` parity, `/api/v1/key` shape, ZDR) stay assumptions through P2–P5.
- Fix:
  - Move "owner creates OpenRouter account, adds $5, creates a key with a $2 limit" into P0.
  - Write `docs/JEV-ACCESS.md` in P1 with a 3-line TL;DR at the top:
    1. Sign in at openrouter.ai.
    2. Add $5 of credit at /settings/credits.
    3. In TrustAgent, click Connect OpenRouter.
  - Run the P1g spikes live, capped at $1.
- Guide content fixes:
  - Lead with the dashboard Connect button. Paste-a-key and `make configure` are fallbacks.
  - Drop the stale dx `TRUTHTABLE_JEV_MONTHLY_BUDGET_USD`; the daily caps are canonical.
  - Tell the user to set a credit limit on the PKCE-created key at openrouter.ai/settings/keys, since it is created without one unless P1g finds a `limit` param.
  - Add an error-map row: a 404 whose message mentions data policy / "No endpoints found" means "check openrouter.ai/settings/privacy or unset `TRUTHTABLE_JEV_ZDR`". TypeSafe ZDR is enterprise-only and there is a single provider, so `zdr:true` may exclude it [A]. Do not map every 404 to "wrong base URL" (dx) or trip the breaker for 300 s (engine).
  - State that Jev is not free and that 402 means no credits.

**H7. The sequencing delivers the showcase last and ships admin writes before auth exists.**
- P3 ships REST #22, #23 and #61 (admin-scoped writes) while `internal/authz` only lands in P4. The "shippable at end of P3" cut therefore has unauthenticated or broken writes.
- Studio, Savings, onboarding, landing, static site and recording all sit in P4–P6. Given risk #12 (H/H), the likely overrun outcome is a gateway with no demo.
- P1 is a serial mega-phase: proto, full contracts, ≥30 hand-authored vectors, all fixtures, scenarios, value claims, migrations, api.ts and CI. It blocks every worktree.
- Fix:
  - Move authz core (env keys, scopes, setup token, session, CSRF) into P2 W-GO. P4 keeps the credential store and PKCE.
  - Define an "M-Demo" gate at the end of P3:
    - `make demo` works end to end with flag and fallback visible.
    - Trace detail shows inline claims.
    - The static build replays the mock recording.
    - A landing skeleton renders from `value-claims`.
  - Split P1: P1a blocks (proto, core api.ts, evaluator/decision/policy schemas, the 4 default templates, decide/normalize vectors, migration 0002). P1b runs in parallel with P2 (remaining templates, fixtures, scenarios, pricing, claims).

**H8. The default evidence extraction leaves groundedness `not_assessed` on most real RAG traffic, and the demo hides it.**
- The most common prompt shape is a single-string user message such as "Use the following context… {context}… Question: …" with no tags and no content-part array. `request_context` finds nothing there, `system_mode: documents_only` finds no tags, and R9 removed the "whole system prompt" rule.
- Trafficgen always uses the `trustagent.context` extension, so the flagship check silently never runs for new users and nobody notices in the demo.
- Fix:
  - Add an Overview and Live KPI: "Evidence found on X% of traffic; groundedness not assessed on Y%", with a CTA.
  - Add an onboarding "Evidence check" step. It shows what was extracted from the first trace, suggests `tag_patterns` with one click, and saves the route.
  - Add a conservative marker pattern for a line that reads only `Context:`, `Sources:` or `Documents:`.
  - Quickstart shows `extra_body={"trustagent":{"context":[…]}}` for the OpenAI and Anthropic SDKs, plus LangChain and LlamaIndex snippets.
  - Add at least 3 scenarios with untagged context that expect `not_assessed`.

**H9. The superseded subsystem designs will be implemented as written.** The worktrees read the subsystem docs, and conflicting identifiers survive in them. Fix: add a "Superseded identifiers — do not implement" appendix to the canonical, and a CI `forbidden-identifiers` grep over the code. Items to list:
- Gateway: `X-TrustAgent-Band`/`-Grounded`/`-Decided-By`/`-Gate`/`-Policy`, `enforcement:`, `custom` backend id, `/api/jev/*`, `TRUTHTABLE_JEV_RPM=1000`, `TRUTHTABLE_DEMO_READONLY`, the `Audit` RPC and `GateSummary`, and `gate-v1` bands.
- Engine: `batched_state_path` default, `config/jev/thresholds.json`, `trustagent_jev_*` and `trustagent_cascade_*` without `engine_`, and `ContextSource`.
- Frontend:
  - Code-point spans, `bearer.` WS subprotocol, `?api_key=`, `X-API-Key` on `/v1`, 409 on duplicate `X-Request-ID`, underscore `JevBackend`, `VITE_DEMO`, `TRUSTAGENT_API_BASE`, `/api/system/status`, `/api/gate/policies`, and an IA with no Traces, Evaluators, Studio, Savings or Setup.
  - Stale copy: "Without Qdrant, claims come back UNSUPPORTED" and the "fallback: llama3.2:1b" badge.
- dx: `docs/JEV.md`, `/api/admin/*`, `jev_spend`/monthly budget, `JEV_MAX_RPS=18`, ADR-009 "env-only secrets", `scripts/traffic.py`, `runtime-config.js`, `VITE_APP_MODE=static-demo`.

## MEDIUM

**M1. `disagreement` is semantically wrong.** "Groundedness critical and contradiction ok" is the normal signature of a baseless (unsupported, not contradicted) hallucination. Today it is labelled disagreement, which floods the disagreement filter, Studio samples and reliability data, and blurs the project's own CONTRADICTED ≠ UNSUPPORTED rule.
- Fix: add `outcomes.implies: [ref]` (contradiction implies groundedness). Set `disagreement` only when an implied evaluator is assessed `ok` while its implier is critical. Rename the current condition `uncorroborated` (flag and reason, not "disagreement"). Update the decide vectors.

**M2. Unreachable blocks are silently accepted.**
- `commitments` (evidence `none`) is corroborated only by `contradiction` (excluded without evidence) or the number/date detectors, which "run only when evidence is present". On rubric-only routes it can never block.
- The numbers detector ignores `rules`. A permitted "30 days" in `allowed_commitments` that is absent from the evidence becomes false corroboration.
- Integers ≤10 are skipped, so "refund within 7 days" is missed.
- Fix:
  - Add `rules` values to the known set.
  - Make `min_integer` configurable per route; default it to 1 when the route has commitments.
  - `POST /api/policies/validate` computes per-route block readiness (evidence × corroborator reachability × calibration state) and returns a warning. The Gateway UI shows a "Block readiness" checklist.

**M3. Flag mode is effectively invisible to streaming clients.** Default `streaming: passthrough` puts the flag in SSE comments, which OpenAI and Anthropic SDKs drop, and in HTTP trailers, which browser `fetch` doesn't expose and nginx doesn't relay.
- Fix: add `GET /v1/trustagent/decisions/:trace_id` (ingest scope, long-poll up to `deadline_ms`) and a 10-line client helper in the Quickstart. Document the trailer limits in GATEWAY.md.

**M4. Calibration requirements make "calibrated block" unattainable for most templates.**
- `measured` needs n≥200 with n_pos≥30. For low base-rate rubric evaluators (PII, injection, regulated advice), 30 human-labelled positives is unrealistic, and deep-audit labels don't map to rubric evaluators.
- Fix:
  - Before launch, ship committed measured calibrations for `groundedness@1` and `contradiction@1` on the pinned snapshot (RAGTruth, owner-run).
  - Add per-evaluator "calibration progress n_pos/30, n_neg/30" in the UI.
  - Allow labels from the LLM judge for rubric evaluators, labelled "agreement with judge" and capped at `provisional` unless ≥50 human labels are included.
  - Say plainly on the landing page that block is unlocked per evaluator.

**M5. Two Jev clients (Go and Python) duplicate breaker, retry, RPM, error taxonomy, snapshot tracking, cost, token EWMA and mock.**
- The two mocks will disagree in demo, with the gate saying X and the cascade saying Y.
- The Python spend cap is only a submit-time flag (`decision_model_disabled`), and the rate limits are split statically (800/300).
- Fix: Go hosts an internal `DecisionService.Decide` gRPC on the compose network; the engine cascade and KB call it. Python keeps a thin direct-HTTP client (timeout, retry, normalize) only for offline evals. That gives one mock, one ledger, one snapshot tracker, and no Jev credential push to Python.

**M6. Three-language parity is over-scoped.**
- Python needs neither `compile` nor `decide` (R16: only Go decides). Its GateJudge should call a Go `cmd/gate-eval` CLI.
- Go needs neither `calibrate_fit` nor `lint`: Studio calibrate and lint are client-side TS, and Python handles offline fitting.
- Hand-authoring expected outputs for `compile` (packing and truncation offsets) is error-prone. Designate Go as the reference implementation: `cmd/vectorgen` generates the vectors, a human reviews them, and they are frozen. TS and Py conform.

**M7. The gate decision cannot be reproduced or inspected.**
- `compile` depends on the runtime EWMA `cpt`, which isn't stored, so recompiling later yields different packing.
- The UI can't show the exact Jev request.
- Fix: add `cpt`, `packed_evidence_ids[]` and `truncated_ids[]` to `decision/v1` and `DecisionModelUsage`. Add a "Show exact Jev request" glass-box panel on trace detail. It recompiles deterministically from `trace_bodies` plus the evaluator versions, with state, questions and probability bars, and a line such as "1 Jev call · 4 questions · 212 ms · $0.0002 · typesafe/jev-1.13-20260917". That line makes Jev visibly central.

**M8. The flagship UI has no page specs.**
- The frontend design predates the evaluator requirements and specs none of Setup, Evaluators, Studio, Savings or the Traces-first IA. Canonical R42 only names routes.
- The static-mode behaviour of Studio and Savings is undefined. The `Recording` schema carries no synthetic bodies, so the in-browser Studio test bench has no samples, and it has no `RoiSummary`, so Savings is empty on Pages.
- Fix:
  - Add page specs to the canonical, covering states and empty, error, static and read-only behaviour.
  - Nav order: Overview, Live, Traces, Evaluators (Studio), Policies, Savings, Evals, Playground, Knowledge (only when enabled), Settings.
  - `Recording` gains `bodies` (scripted content only) and `roi: RoiSummary` (label simulated or recorded).

**M9. The Evals data contract is unresolved.** There are three shapes: frontend `trustagent.evalrun/v1` via the `@evals` glob, engine `docs/benchmarks/results/*.json`, and `contracts/claims/measured.json`. None is in `contracts/schema`.
- Fix: add `benchmark-run.v1.schema.json`, make one output directory canonical, and hide Evals in the static build until at least one measured run exists. Mock CI baselines are never shown as results.

**M10. The static "Try Jev live" flow has unhandled friction.** Most LinkedIn visitors have no OpenRouter credits.
- The consent copy must say "requires credits on your OpenRouter account".
- Handle a 402 after PKCE with a specific screen: "key created but no credits → add credits, or Forget key".
- Treat the replay as the primary experience and "Try live" as secondary.
- The self-hosted wizard needs the same 402/401 branch and must not auto-bind a failing key.

**M11. The live benchmark budget blocks the launch claims.** `--max-cost-usd 60` in P6 blocks every "measured" claim.
- Fix: add `make bench-lite` (≤$5: RAGTruth-QA 200 × {gate, llm-same-checks} plus latency) as the launch requirement, with the full run later. `measured.json` records which tier produced it.

**M12. Jev is not easy to configure.** The canonical has about 45 Jev and decision env keys with no advanced marking, and the setup token is only discoverable in the logs.
- Fix:
  - Mark keys `advanced` in the registry. `.env.example` lists ≤15 keys (one key, backend, model pin, daily caps, mode seeds).
  - `/app/setup` shows the exact `docker compose logs proxy | grep SETUP` command. Add `make setup-url` and an optional `TRUTHTABLE_SETUP_TOKEN` seed.
  - The wizard offers "Explore with simulated Jev" (runtime `decision_model.backend=mock`, labelled) so a keyless `docker compose up` isn't a dead dashboard.
  - Add a per-route `decision.model` pin (calibration keyed by snapshot) to satisfy "very adjustable".

**M13. Items a senior reviewer expects are missing.**
- A "who it's for" section and an honest comparison table on the landing page (Guardrails/NeMo, LLM-as-judge in Langfuse/Arize, Datadog, Galileo Luna, Bedrock/Azure groundedness).
- A time-to-first-value acceptance test: fresh clone → `docker compose up` → wizard with mocked OpenRouter → first flagged trace in under 5 minutes (Playwright `first-run` in P5).
- The gateway's k6 overhead benchmark is absent from the canonical gates. Add it to P5 and publish p50/p95 overhead into `measured.json`.

**M14. Privacy disclosure and retention gaps.**
- The wizard must state that prompts, responses and evidence go to OpenRouter/TypeSafe, with the per-route opt-out.
- `labels.sample` keeps raw user inputs forever. Give it its own retention (`TRUTHTABLE_LABEL_RETENTION`, default 90d) and an admin delete.

## LOW

- **L1.** Compose service names aren't pinned (`audit-engine` today vs "engine" in the canonical). Pin them, since docs, doctor, Makefile and `GRPC_ADDRESS` all reference them.
- **L2.** `threshold_basis` for `groundedness` cites Arize's 0.80 cutoff, which was measured on a differently framed question (direction unknown). Say "not directly transferable".
- **L3.** Relabel the OpenRouter faithfulness result (95.5%, Brier 0.043) as "published by the model's distributor" (vendor-affiliated), not neutral third-party.
- **L4.** The P0 gate writes the `make test` result into `docs/PLAN.md` after the baseline commit, which contradicts "`git status` empty". Record it in the first feature commit.
- **L5.** Check the TypeSafe and OpenRouter terms for benchmark-publication or output-redistribution clauses before publishing recordings and `docs/benchmarks/`.
- **L6.** Frontend dependency sprawl on top of the jump from Vite 5 / Vitest 4 / TS 5.9 (verified). Drop visx (use recharts for reliability, confusion and waterfall), cmdk and TanStack Table v9 unless a page needs them.
- **L7.** The Recording label should also say "scripted scenarios" when `answer_source=scripted` dominates (ties to H5).
