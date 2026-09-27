# TrustAgent v2 design review: correctness, failure modes, security, performance

**Checked against the repo source:**
- The gRPC `create_server` default is `max_workers=10` (`server.py:464`), and Go's `WORKER_COUNT` default is also 10 (`config.go:51`).
- The engine binds `grpc_host` to `0.0.0.0` by default (`config.py:50`). Compose does not publish port 50051, but it does publish 6379, 5432, 6333, 11434, 8080 and 3000 on 0.0.0.0.
- The rate limiter keys on `c.ClientIP()` (`ratelimit.go:104`), and `SetTrustedProxies` is never called.
- CORS allows only `GET, POST, OPTIONS` and a four-header list (`cors.go`).
- `normalizeStatus` already writes claim status in upper case (`pool.go:212`).
- The migration runner takes no lock and runs no transaction (`postgres.go:50-96`).

## HIGH

**H1. The LLM spend cap can be bypassed, and a Jev failure turns into unbounded LLM spend.**
Affects: §G.4.4 `deep_audit`, F.2 `SPEND_CAP_LLM`, C.14 `/api/audit`, `AuditOptions.llm_budget_exhausted`.

Problem:
- The spec says that past the LLM cap "only high-lane audits run". But `deep_audit=high` whenever `status∈{timeout,error,partial}`.
- During a Jev outage, a breaker trip or a timeout storm, every trace goes to the high lane. The engine then has `decision_model_disabled` set, so the cascade becomes LLM-only for 100% of traffic with no cap.
- `/api/audit` (ingest scope) always uses the high lane. At 120 requests/min/key that is about $6k/day, uncapped.
- `llm_budget_exhausted` skips only shadow, partial and explain escalations, not `jev_unavailable` escalations.
- `deep_audit=high` also fires for `would≠pass` on refusal and tone evaluators, which buys a pointless grounding audit.
- Caps are computed from `cost_events`, which lag behind in-flight audits.

Fix:
- (a) Make the LLM cap hard for every lane, including direct audits. Only admin-originated playground and Studio calls may exceed it, with a confirmation. `/api/audit` returns 503 `budget_exhausted`.
- (b) Gate `timeout|error|skipped` → `deep_audit=sample_candidate` with `error_sample_rate` (default 0.1), not high.
- (c) Set high only when a role-bearing evaluator, a detector or corroborated disagreement triggered it.
- (d) Admit high-lane audits through a token bucket `TRUTHTABLE_AUDIT_HIGH_PER_MIN` (default 30); overflow goes to the low lane.
- (e) In the engine, when `llm_budget_exhausted || decision_model_disabled`, claims that are `jev_unavailable` become UNKNOWN with `degraded_reasons+=escalation_budget`, not LLM calls.
- (f) Go keeps in-memory atomic spend counters, seeded from `cost_events` at startup. It reserves the estimated cost at submit and reconciles when the CostRecord arrives.

**H2. The `numeric_or_date` escalation fires whenever any number or date is present.**
Affects: §G.4.4 `r.escalate` uses `meta.has_numeric_or_date`. Groundedness, contradiction and commitments all set `numeric_or_date:true`.

Problem: every answer containing a price, date or order number escalates, so `deep_audit=high` covers most business traffic. This contradicts R12 ("escalate only").

Fix:
- Replace the meta field with `numeric_or_date_unverified = det.numbers_not_in_evidence || det.dates_not_in_evidence`, and escalate only on that.
- Rename the escalation `numeric_or_date_unverified`.
- Update the vectors.

**H3. The engine's Jev capacity is about 10× below deep-audit demand, and limiter waits become LLM spend.**
Affects: R22 `per_claim`, R32 `ENGINE_JEV_RPM=300`, `JEV_ASYNC_DEADLINE=10s`, `JEV_FALLBACK_TO_LLM=true`.

Problem:
- `per_claim` makes one call per claim. 300 RPM is about 37 audits/min at 8 claims.
- At about 13 rps gated, with 0.2 pass sampling, 0.05 uniform sampling plus flagged and escalated traffic, demand is about 3–4 audits/s, i.e. 25–30 Jev calls/s against a supply of 5/s.
- `await rate_limiter.acquire()` blocks until the deadline, raises DecisionTimeout, escalates as `jev_unavailable`, and sends most claims to the LLM.

Fix:
- The limiter waits at most `max_wait=250ms`, then returns `rate_limited`.
- Rate-limited claims escalate only while under the LLM budget and the escalation cap; otherwise they become UNKNOWN and degraded.
- Report `jev_claims_capacity_per_min` in EngineInfo. Go scales `pass_sample_rate` and `uniform_sample_rate` so that expected claims/min stays at or below 80% of that capacity.
- Run the R22 A/B test in P3, not P6. When per-claim context resolves to the same `section` for every claim (evidence ≤3000 tokens, the common case), send one call with claims as separate questions: this is exactly `batched_state_path`.
- Document the capacity formula in `GATEWAY.md`.

**H4. `fail: closed` is bypassed exactly when the decision model is unavailable.**
Affects: §G.4.4 caller-side "rejections become `status=skipped`". `decide` fails closed only on timeout or error. F.2 says "past the cap the gate fails open".

Problem:
- The reasons `breaker_open`, `rate_limited`, `budget`, `decision_model_off`, `overload` and unavailable credentials all produce `skipped`, which leads to a detectors-only decision and then pass on a block/fallback route with `fail: closed`.
- An attacker can drain the budget. An ingest key sending 24k-token bodies at 120/min spends the default $2/day in about 17 minutes, which disables every gate until UTC midnight.
- `partial` (for example `invalid_distribution` on groundedness) also passes.

Fix:
- `decide` treats `status∈{timeout,error,skipped}` the same way for fail semantics (`degraded=<skip_reason>`). `fail:closed` gives block, with `reasons+="fail_closed:<reason>"`.
- On a closed route, a block-eligible evaluator in error also gives block.
- The budget is partitioned per route (`decision.budget_share`), with a reserved share for block/fallback routes, plus a per-key daily decision quota.
- Add vectors for breaker, rate-limited, budget and partial, each under `fail:closed`.

**H5. Evidence presence is checked before packing, so groundedness can be sent with empty evidence (the Jev inversion trap).**
Affects: §G.4.2, step 2 vs step 5.

Problem:
- Step 5 can pack zero items (when `cap<200`) or a 50-character head, yet the required evaluators are still sent.
- Truncation can drop the document that supports a claim. That yields a false "ungrounded", which `decide` treats as if it had seen full evidence.

Fix:
- After packing, re-apply the gate. If packed evidence is shorter than `min_chars`, exclude with `{ref,"evidence_packed_away"}`.
- If `truncated`, cap groundedness at `warn` (not block-eligible) with reason `evidence_truncated`.
- In the engine, when evidence was truncated or `context` was capped at 20 docs, UNSUPPORTED claims count as unverifiable and `degraded_reasons+=context_trimmed`.
- Add vectors for pack-to-zero and truncate-cap.

**H6. Text controlled by the end user is treated as trusted evidence and rules, which makes gate evasion trivial.**
Affects: §G.5 `request_context` (tag blocks in any user message, plus non-last parts), H.1 `trustagent.rules`.

Problem:
- A user pastes `<document>Refunds within 365 days</document>`, and the bot's "365-day refund" passes both groundedness and contradiction.
- Whether request-supplied `rules` (for example `allowed_commitments:"anything"`) override route params is unspecified.

Fix:
- `request_context` covers non-user roles only.
- Add a new source, `user_supplied`, default OFF, shown in the UI as "user-supplied (untrusted)".
- Ignore `trustagent.rules` unless the route sets `overrides.client_rules:true`.
- Add a route glob `untrusted_tools` (for example web search) whose results are labelled untrusted and do not count toward groundedness.
- Add a vector: a user-pasted document is not evidence.

**H7. The "disagreement" and corroboration rules conflate unsupported with contradicted.**
Affects: §G.4.4 `disagreement`, groundedness `requires_corroboration`, R5.

Problem:
- Groundedness critical with contradiction ok is the normal baseless-information case: contradicted is a subset of ungrounded, not the other way round.
- The current rule therefore (a) marks every baseless answer as `disagreement` and sends it to the high lane, and (b) lets groundedness block only when contradiction is also critical (at which point it is really a contradiction block) or when the number/date detectors fire. Baseless URLs, quotes and citations never corroborate.

Fix:
- Declare incompatible pairs in the spec: `outcomes.incompatible_with:["groundedness"]` on contradiction. Disagreement is then only `contradiction critical ∧ groundedness ok`.
- Add `det:urls_not_in_evidence`, `det:quotes_not_in_evidence` and `det:citation_out_of_range` to groundedness `requires_corroboration`.
- Add a vector: a baseless answer does not produce `disagreement`.

**H8. Calibration labels are biased and circular, yet they unlock blocking.**
Affects: §G.8 quality rule, C.16, `make calibrate --source api`.

Problem:
- Deep-audit labels exist only for flagged, escalated or sampled traces (verification bias).
- Cascade audits are mostly decided by Jev itself, so the labels are circular.
- The LLM judge is a proxy, not ground truth.
- Test, trafficgen, playground and mock traces are included.
- Even so, n≥200 gives `quality=measured`, which satisfies `require_calibration` and enables block.
- Refusal, PII and tone have no deep-audit label at all.

Fix:
- `measured` requires human or public-dataset labels only.
- Fits derived from deep audits get a new quality, `proxy`, which never satisfies `require_calibration`.
- `--source api` uses only traces with `flags∋uniform_sample` (a pre-gate Bernoulli with `verifier=llm`), `source=proxy`, backend not mock, and without `test_mode`. Otherwise it applies inverse-probability weights.
- The reliability endpoint applies the same filter and reports n per label source.

**H9. The trace batch writer loses data.**
Affects: §B Writers; the FKs from `gate_decisions` and `trace_bodies` to `traces`; `traces.action` and `gate_status` are NOT NULL.

Problem:
- pgx `SendBatch` runs the whole batch as one implicit transaction. One FK violation aborts all 200 rows. That happens when a trace row was dropped from the full 10k channel, or when a trace is written after its dependents.
- Worker audit updates and late decisions UPDATE `traces` rows that may still be sitting in the 250 ms buffer. Zero rows update, and `audit_status` stays at "queued".
- `trace_started` has no action value yet, but the column is NOT NULL.

Fix:
- Route every trace-scoped write through one ordered writer as UPSERTs: `INSERT … ON CONFLICT (trace_id) DO UPDATE SET c=COALESCE(EXCLUDED.c,traces.c)`.
- Make `action` and `gate_status` nullable, or add a `pending` value.
- Drop the dependent FKs (retention deletes by `trace_id`), or make them `DEFERRABLE INITIALLY DEFERRED` in the same batch.
- On batch error, retry row by row.
- Drop per trace, taking all its dependents with it, and count the drops.
- Add `traces.updated_at`, which H14 also needs.

**H10. The rules for raw-content visibility are inconsistent.**
Affects: G2.5, C.13, WS `audit_completed` and `trace_started`, `store_bodies`, `labels.sample`.

Problem:
- Read scope is denied `body` but receives `user_query`, `llm_response`, `context_used`, `claims[].evidence` and `quote` through REST 13 and through every WS event.
- `audits` keeps full text for 30 days regardless of `store_bodies:false` or `BODY_RETENTION`.
- `labels.sample` freezes raw inputs forever.

Fix:
- Add a `content` scope (admin implies it). Without it, REST and WS strip those fields and the previews. The hub serializes a full and a redacted variant of each event, sharing one `seq`.
- Honour `store_bodies:false` in `audits` (keep hashes and claims only), and null out audit text after `BODY_RETENTION`.
- Add `TRUTHTABLE_LABEL_SAMPLE_RETENTION` (90 days).

**H11. A default install can become an open LLM relay on the owner's credential.**
Affects: G2.3 `auth_mode=open` gives {ingest, read}; inject mode via the `upstream_default` binding; C.1 readonly.

Problem:
- Once the wizard binds `upstream_default=openrouter`, anyone who can reach :8080 spends the owner's key.
- Completing setup creates an admin `api_keys` row. `auth_mode` then flips to `keys`, and all existing unauthenticated app traffic suddenly returns 401.
- Readonly mode does not cover `/v1/*` or `/api/audit`.
- All published ports bind to 0.0.0.0.

Fix:
- Bindings that enable inject mode require at least one ingest key; otherwise return 409 `ingest_key_required`, and the wizard mints one.
- `auth_mode` is derived from ingest and read keys only.
- Readonly mode blocks unauthenticated `/v1/*` and `/api/audit`.
- Compose uses `${BIND_ADDR:-127.0.0.1}:PORT:PORT` for every published port.

**H12. Credentials are not bound to hosts, so keys can reach the wrong or an attacker's host.**
Affects: G2.2, F.2 `JEV_BASE_URL`/`JEV_API_KEY`, G2.4, settings `upstream.*.url`.

Problem:
- (a) Changing `decision_model.base_url` or `upstream.default.url` while the binding stays `openrouter` sends the OpenRouter Bearer token to any host.
- (b) `JEV_API_KEY` "overrides vendor keys" while `auto` picks openrouter, so a Vercel or TypeSafe key goes to OpenRouter. `JEV_BASE_URL` with an openrouter backend sends the OpenRouter key to that base URL.
- (c) Engine gRPC has no authentication. `make dev-python` binds 0.0.0.0. `ApplyRuntimeConfig` can repoint `base_url` while the engine's env key "wins", so the env key is exfiltrated. A push with `version=2^62` blocks every later legitimate push. `Evaluate` and `DraftEvaluator` give anyone free LLM spend.

Fix:
- Each credential row gets `allowed_hosts`: openrouter.ai, api.typesafe.ai, api.anthropic.com, api.openai.com, or the host fixed when a compatible/generic credential is created. Go and Python both assert this on every outbound call. Changing the host requires re-entering the secret.
- `JEV_API_KEY` applies only to an explicit backend or `compatible`. `auto` checks `BASE_URL+JEV_API_KEY` first. `BASE_URL` is ignored for vendor backends unless the backend is explicit.
- Engine default `GRPC_HOST=127.0.0.1` (compose overrides it). Every RPC requires `x-trustagent-internal: $TRUTHTABLE_INTERNAL_TOKEN`, generated by `configure`.
- A pushed `base_url` is never combined with an env credential.

**H13. The engine's RPC concurrency cap equals Go's worker count, so control-plane RPCs fail under normal load.**
Affects: engine §7 `maximum_concurrent_rpcs=max_workers`; default 10 on both sides.

Problem: ten long-running `Evaluate` calls saturate the cap. `HealthCheck` (the M4 pinger), `RetrieveContext` (150 ms), `GetEngineInfo` and `ApplyRuntimeConfig` then return RESOURCE_EXHAUSTED. The engine flaps "down", knowledge shows `error`, and runtime config never applies.

Fix:
- `maximum_concurrent_rpcs = TRUTHTABLE_ENGINE_MAX_RPCS`, default `2*WORKER_COUNT+16`.
- Admit `Evaluate` through its own semaphore `ENGINE_MAX_AUDITS`.
- Go validates `WORKER_COUNT ≤ EngineInfo.max_audits`.

**H14. WebSocket resync misses updates, and the epoch is not sent on reconnect.**
Affects: §D client algorithm.

Problem:
- Resync filters `/api/traces?since=` by `created_at`. Audit completions and late decisions for traces created before the outage are never recovered, so those traces stay "auditing…".
- Reconnecting with `since_seq` but no epoch means that after a restart where the new head is ≥ N+1, the server replays events from a foreign epoch.

Fix:
- Add `updated_at` plus an index on `traces` and `gate_decisions`. Resync with `?updated_since=` (and `/api/audits?completed_since=`).
- Reconnect with `?since_seq=N&epoch=E`. Replay only if E matches; otherwise send `gap{reason:"epoch_changed"}`.

## MEDIUM

**M1. Late-decision continuation starves sync capacity.**
- The per-attempt timeout is `min(3s, remaining)`, so the attempt is cancelled at the deadline. "Continues detached" contradicts that.
- If it did continue for up to 10 s, a latency spike at 13 rps would hold about 130 calls in flight, above `MAX_INFLIGHT` 64, starting a fail-open storm.
- Fix:
  - Run the HTTP attempt on a detached context with `ATTEMPT_TIMEOUT`, and have only the waiter stop at the deadline.
  - Allow continuation only while inflight is below 50% of the cap, using the async class.
  - Cancel the hedge loser immediately.

**M2. The breaker counts our own deadline cancellations and hedge-loser cancellations.**
- A healthy vendor with P50 650 ms trips a breaker set to 600 ms.
- Fix: count only 5xx, 524, 529, network errors and attempts that exceed `ATTEMPT_TIMEOUT`.

**M3. Cold connections blow the sync deadline at low traffic.**
- A 90 s idle timeout means DNS, TCP and TLS (100–300 ms) on top of Jev's latency.
- Fix:
  - Use a dedicated HTTP/2 transport with `MaxIdleConnsPerHost ≥ MAX_INFLIGHT` and a 5-minute idle timeout.
  - Keep the connection warm with a free `GET /api/v1/key` every 60 s while any sync route exists.
  - Record `connect_ms`.

**M4. Hedge losers and timed-out attempts are billed but not recorded.**
- Decision cost per 1k and the daily cap both undercount.
- Fix: write one `cost_event` per attempt, using `cost_source=computed` from the estimated tokens.

**M5. `on_no_evidence` never fires with the default evaluator set.**
- `all_na` requires every evaluator to be not assessed, but refusal and PII are assessed.
- With `block`, it blocks greetings and skips `require_calibration`.
- Fix:
  - Trigger when every `requires=required` evaluator is excluded as `no_evidence` AND the answer has an unverified number, date, URL or quote, or a new built-in `makes_factual_claims` noul (no evidence needed) is ≥0.5.
  - Subject that path to `require_calibration`.

**M6. The verdict reads `supported` when a role-bearing evaluator errored.**
- Fix: `supported` only when every attached role-bearer is assessed with level `ok`. If any errored, the verdict is `unknown`.

**M7. Errors are applied per request, not per question.**
- `meta.timed_out` and `error_kind` are global, but compile can produce K requests (split or `by_evidence`), so one failed request discards answered questions.
- Fix:
  - Carry `request_status[k]` and a qid→request map in `meta`.
  - Overall `timeout` or `error` only when all requests failed; otherwise `partial`.

**M8. `min_confidence` uses the vendor's concentration measure on an aggregated risk.**
- For `unsafe_policy`, harmful 0.5 plus rule-break 0.45 gives risk 0.95 but low concentration, so it never reaches critical.
- This also contradicts the "vendor confidence is never a threshold input" rule.
- Fix:
  - Set `conf_ok` from `max(risk,1−risk)` of the aggregated signal.
  - Store the vendor confidence for display only.

**M9. Thresholds silently change meaning when calibration activates.**
- `risk` switches from raw to calibrated, so flag and block rates change without a policy version.
- Fix:
  - Thresholds carry `space: raw|calibrated`, recorded in decision/v1.
  - Activating a calibration does not change live decisions until a policy version adopts the `recommended` thresholds (via dry-run).

**M10. Threshold overrides leave silent zones.**
- The band's upper edge is fixed at the spec's `warn_at`, so an override with `warn_at=0.9` leaves [0.80, 0.90) neither escalated nor warned.
- Fix:
  - The spec stores only `escalate_from`, and the band's upper edge is the *effective* `warn_at`.
  - Validate `escalate_from < warn ≤ critical` after merging overrides.

**M11. The calibration key omits things that change the score distribution.**
- The key is `question_sha` alone. It ignores `signal` (risk weights, invert, levels), route params text, and the state composition: the `single` partition gives rubric questions the evidence, rules and user request.
- Fix:
  - Key = sha256{question, signal, sorted state fields sent, partition, and the params hash if the evaluator has params}.
  - Default `partition: by_evidence`.
  - Evaluators with params calibrate per route.

**M12. The `critical_at` selection fits noise.**
- A point precision ≥0.97 with n_pos≥30 is not enough: the Wilson lower bound of 30/30 is 0.886.
- Fix:
  - Pick the smallest t such that for every t′≥t the Wilson 95% lower bound of precision is ≥0.95 and there are ≥50 predicted positives.
  - Studio quick-calibrate never emits a `critical_at`.

**M13. Deterministic blocks have a high false-positive rate and are exempt from the calibration cap.**
- The known sets exclude `system_instructions`, which hold support URLs, phone numbers and prices.
- `citation_out_of_range` counts items after dedup and packing, so "[5]" can go out of range.
- `payments-faq` blocks on both.
- Fix:
  - Known set = full pre-packing evidence ∪ `user_request` ∪ `system_instructions`.
  - Citation range = max(the request's document marker numbers, the pre-dedup item count).
  - Add vectors.

**M14. The gate treats knowledge-only evidence as app grounding.**
- The engine treats it as unverifiable; the gate does not.
- Fix: when `knowledge` is the only source, groundedness caps at `warn` and is not block-eligible (reason `knowledge_only`).

**M15. Streaming gaps.**
- (a) A client that disconnects before the held `[DONE]` skips the audit even though every content chunk was delivered. Fix: if a finish or stop reason was seen, gate (unenforced) and audit with flag `client_disconnected`.
- (b) In buffer mode the first keepalive commits the headers. Fix: state that decision headers then go only to the SSE comment and trailers.
- (c) Fix: exclude `delta.reasoning`, `reasoning_content`, `thinking_delta` and tool arguments from the gated text, and document that tool calls are ungated.
- (d) Fix: extend the write deadline through `ResponseController` before the fallback upstream call.

**M16. Evidence extraction misses mainstream request shapes.**
- It misses the OpenAI `developer` role and Anthropic's top-level `system` (a string or blocks).
- `/v1/responses` returns 404.
- System prompts that use a plain "Context:" line header produce zero evidence and a silent `not_assessed`.
- Fix:
  - Treat `developer` like `system`, and parse Anthropic `system`.
  - Pass `/v1/responses` through with `X-TrustAgent-Status: skipped:unsupported_api`.
  - Add a line-header rule `^(context|sources|documents|references?|knowledge)\s*:\s*$` (mi).
  - Raise a status warning when more than 50% of a route's traffic is `no_evidence`.

**M17. WebSocket memory and overflow.**
- A ring of 2000 full AuditDetails can reach hundreds of MB.
- Overflow on the producer channel happens before `seq` is assigned, so no client ever sees a gap.
- Fix:
  - The event carries a summary plus claims, without `context_used`.
  - Ring bounded by `WS_REPLAY_MAX_BYTES` (64 MiB).
  - Producer overflow is counted, and `Run` emits `gap{reason:"hub_overflow"}`.

**M18. The runtime-config version can go backwards.**
- It resets when Go runs without a database, after a DB restore, or across replicas, and the engine then ignores every push.
- Fix: push `(epoch, version)`. The engine accepts any push with a new epoch.

**M19. "Newest wins" lets a routine upgrade overwrite dashboard edits.**
- Shipping a changed default `policies.yaml` changes the file's sha, and it silently overrides the dashboard edits.
- Multiple replicas each insert their own file version.
- Fix:
  - If a dashboard version is newer than the last file version, a file change gives `file_status=diverged` and needs an explicit admin "Adopt file".
  - Insert file versions under an advisory lock, deduplicated by sha.

**M20. Sessions survive key revocation.**
- Revoking an API key or removing an env admin key does not end its 12-hour sessions.
- Fix:
  - Resolve `sub` on each request: the `api_keys` row must not be revoked (cached ≤30 s), and an env label must still exist.
  - Intersect the session's scopes with what that resolves to.

**M21. Client identity behind nginx.**
- With `TRUSTED_PROXIES=""`, every dashboard user shares one IP: `WS_MAX_PER_IP=10` caps the whole dashboard and there is one rate bucket.
- Today, gin trusts every proxy, so client IPs are spoofable.
- Fix:
  - Compose sets the trusted-proxy CIDR, and nginx sets `X-Forwarded-For`.
  - Key cookie-authenticated rate and WS caps by session id.

**M22. Cross-origin CORS for `DASHBOARD_API_BASE`.**
- A cross-origin dashboard needs PUT, PATCH and DELETE; the request headers `If-Match`, `X-TrustAgent-Key`, `-CSRF`, `-Route` and `-Mode`; and exposed `ETag`, `Retry-After`, `X-Request-ID` and `X-TrustAgent-*`.
- `SameSite=Strict` without Allow-Credentials means cookie auth cannot work cross-site.
- Fix: specify that cross-origin mode uses the key header only, plus the full allow and expose lists and `Max-Age 600`.

**M23. `trace_bodies` grows unbounded.**
- Up to 1 MiB of pre-packing evidence per trace, for 7 days, on all traffic.
- Fix:
  - Add `TRUTHTABLE_BODY_MAX_BYTES` (256 KiB, sets a truncated flag).
  - Store bodies for all non-pass traces plus a `body_sample_rate` of 0.2 for passes.

**M24. `RetrieveContext` times out during knowledge-base ingest.**
- It shares a single-worker embedding executor with ingest.
- Fix: give it a separate executor or priority, and add a query-embedding LRU cache.

**M25. The PII template flags data that came from tools.**
- A support bot repeating the customer's record from a tool result gets `exposes_personal_data`, which escalates to high on the default evaluator set.
- Fix:
  - Add `evidence` (optional) to `state_fields`.
  - Add an option `repeats_data_from_evidence` with weight 0.

**M26. Rule-based UNSUPPORTED claims inflate hallucination counts.**
- A per-claim BM25 miss becomes "UNSUPPORTED by rule", which counts as a hallucination.
- Fix:
  - Fall back to all documents.
  - If there are still none, the claim is excluded from scoring as "not checked".

**M27. The uniform sample is not actually uniform.**
- It is listed alongside `pass_sample`, which implies it is drawn after the decision, and it sits in the low lane, which is dropped first.
- Fix:
  - Draw it as a pre-gate Bernoulli over all gated traces, flagged at creation.
  - Give it reserved lane capacity so it is never silently dropped.

**M28. Migration and rollback.**
- (a) Backfill `traces` from legacy audits in 0002 with `ON CONFLICT DO NOTHING` (`skip_reason='legacy'`). Otherwise the audits→traces redirect returns 404 for all pre-2.0 history.
- (b) Document that downgrading after 0002 is impossible, because v1 cannot scan the NULL scores.
- (c) Take the advisory lock *before* `CREATE TABLE IF NOT EXISTS schema_migrations`.
- (d) At startup, mark stale `dryrun_jobs` as failed.

## LOW

**L1. Type and contract mismatches.**
- `confidence_source` is missing `choice_only`.
- `trace_started` needs a wire value `pending`, or nullable `action` and `gate_status`.
- Document the key mapping `not_addressed`→`unsupported` between `jev_probabilities` and `verdict_probabilities`.

**L2. Canonical JSON.**
- Require ASCII-only keys inside `content` objects, because Go, Python and JS sort non-ASCII keys differently.
- Never compare floats bytewise; vectors should compare parsed values.
- Fix the summation order of risk to sorted option keys.

**L3. Token estimation.**
- Go's `f` multiplier and Python's `cpt` EWMA are different parameters; unify them as `cpt_effective`.
- Count codepoints ≥U+3000 as one token each, to avoid a CJK underestimate that leads to 413 errors and missed deadlines.

**L4. Detector and regex cost.**
- `system_prompt_leak` is O(|S|/50·|R|); use a rolling-hash set of 200-grams.
- The static playground should cap input at 200 KB before running the doc-block regexes, to limit backtracking.

**L5. Setup token handling.**
- Put the token in the URL fragment, not the query string.
- Use a 1-hour TTL.
- Claim it atomically with `UPDATE … RETURNING`.
- Set `Referrer-Policy: no-referrer` on the self-hosted dashboard.

**L6. Audit-log hygiene.** Reject or scrub URLs with userinfo in `admin_audit_log.diff` and in settings.

**L7. Evaluate fallback caching.** Cache the `Unimplemented` fallback per connection or engine version, rather than deciding per call.

**L8. Multiple choices.** When `n>1`, gate only choice 0 and annotate the rest `skipped:multi_choice`.

**L9. Score leakage to end users.** Add a route option `expose_scores:false` that sends only Action. Document that `Risk`, `Reasons` and SSE annotations act as an oracle once passed through to end users.

**L10. Static site.**
- Count Studio drafts against `VITE_PLAYGROUND_MAX_CALLS`.
- A meta CSP cannot set `frame-ancestors`, so add JS frame-busting for the consent dialog.

**L11. Retention sweeper.** Use adaptive batch halving after a timeout, and expose a lag metric.

**L12. Trace search.** `/api/traces?q=`: bound it to the time window, or add a `pg_trgm` index.

**L13. Jev response validation.** Accept `application/json;charset=…` as a prefix match, not only the bare type.

**L14. Superseded mechanisms in the subsidiary designs.** Add a per-design "superseded" checklist to the contract. Mechanisms still present include:
- Frontend: the bearer subprotocol, the `?api_key=` fallback, the `X-API-Key` dashboard key, code-point spans, `/api/system/status`, underscore backend ids, C8 409.
- DX: `X-TrustAgent-Gate` in `traffic.py --expect`, the open-admin dev mode, monthly `jev_spend`.
- Gateway: the enforcement ladder, `rag_blocks`, `custom`.

**L15. Vectors need an executable reference before P1 merges.** The P1 vectors are hand-authored and nothing runs them before merge. Add a reference implementation under `contracts/tools/` that must pass every vector as part of the P1 gate.

**L16. Mock calibration files.** The calibration loader must refuse `mock/*` snapshot files unless `DEMO_MODE` is set.