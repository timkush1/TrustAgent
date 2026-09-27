# TrustAgent v2 review: UX simplicity and modern design

I checked these points against the repo:
- `docker-compose.yml` builds `audit-engine`, `proxy` and `dashboard` from source (lines 132, 177, 217).
- No workflow publishes images: `grep ghcr|push` finds nothing in `.github/workflows`.
- The current README title carries an emoji ("🔍 TrustAgent").
- The dashboard is still the legacy set: `App.tsx`, the `audit/`, `dashboard/`, `history/`, `kb/` and `upload/` components, and a cyberpunk theme.

## HIGH

### H1. A new user can't get to a first gated request in under 5 minutes
Nothing in the design defines an end-to-end path, and five things block it:
1. **Everything builds from source.** All three app images build locally, so the first `make demo --build` compiles Go with proto generation, installs the Python venv and runs `npm ci` plus a Vite build. Estimate: 6–12 minutes on WSL.
2. **The setup token is buried.** Every write or spend needs admin (R29). The setup token only appears in the proxy logs (G2.3), so a user who ran plain `docker compose up` has to dig through logs.
3. **The wizard is long.** The canonical wizard (I.6) has 6 steps: claim, connect or paste, bindings, test decision, preset, first request.
4. **The first request shows nothing.** The default mode is `observe`, which returns only `Action: observe` and `Status: deferred`.
5. **Connect OpenRouter is dead inside the demo.** `make demo` forces mock (G2.2 step 1) and blanks every key, so connecting OpenRouter on the demo stack cannot take effect.

**Fix**
- **Publish images.** Add a CI job `release-images` that pushes `ghcr.io/timkush1/trustagent-{proxy,engine,dashboard}:{VERSION,edge}` for amd64 and arm64.
  - Each app service gets `image: ghcr.io/timkush1/trustagent-<svc>:${TRUSTAGENT_VERSION}` plus `build:` and `pull_policy: missing`.
  - `make up` and `make demo` pull; `BUILD=1` or `make dev-*` build.
  - Budget: `make demo` shows the first live row in ≤90 s warm and ≤3 min cold.
- **Surface the token automatically.**
  - `make up` and `make demo` wait for health, then print and open `http://localhost:3000/app/setup?token=…`.
  - `/app/setup?token=` claims the token automatically and redirects.
  - Without a token, `/app/setup` shows one copyable command, `docker compose logs proxy | grep -m1 SETUP`, and a paste field.
- **Cut the wizard to 3 steps.**
  1. "Secure this dashboard". This happens automatically from the token.
  2. "Connect OpenRouter".
     - One primary button, with a "Paste a key instead" link.
     - Bindings default to decision model plus judge, with no toggle screen. The upstream binding moves to Settings.
     - The probe runs by itself and shows "Ready · 231 ms · $0.00001". Never show the probe's noul value.
  3. "See it work".
     - A prefilled Playground run in `mode:"audit"` with `mode_override:"flag"`, using the refund example (answer says 60 days, evidence says 30).
     - It shows the outcome card and the highlighted claim, and needs no upstream key.
  - The preset stays at observe; the preset picker lives on Policies.
- **After the wizard,** Overview shows a "Connect your app" card.
  - Tabs: OpenAI Python, OpenAI TS, Anthropic, curl. Base URL and headers are prefilled from `/api/status`.
  - A "Waiting for your first request…" line flips green on WS `trace_started`.
- **On the demo stack,** when `SystemStatus.demo` is true, replace Connect with: "This is the demo stack: mock checks, $0. To use your own keys: `make demo-down && make up`."
- **Add an acceptance test,** `e2e/stack/golden-path.spec.ts`: fresh stack → token URL → Connect (mocked PKCE) → sample → outcome visible. Assert ≤8 clicks and no step needs log access.

### H2. There is no single "what happened" answer, and scores point in opposite directions
A user sees at least 10 overlapping axes:
- action
- would_action
- gate_status
- gate verdict (lowercase)
- evaluator level (`ok|warn|critical`)
- audit verdict (UPPERCASE)
- claim verdict
- audit status
- `trace.audit_status` (10 values)
- grade, trust_score, faithfulness and p_hallucinated

Risk is "high = bad", but trust_score, faithfulness and the grade are "high = good". Both appear in the same header (frontend §6.5: "score + grade" next to "Jev said flag 0.87").

**Fix**
- **Add `contracts/ui/outcomes.v1.json`** and derive from it in `src/domain/presentation.ts` (shared by the static and full builds). It defines one `Outcome` per trace: `passed | would_flag | flagged | blocked | replaced | not_checked | checking | error`, plus a `headline` sentence and a `next_step`.
- **Ban raw enums in the UI.** An ESLint rule forbids rendering GateStatus, AuditVerdict or EvaluatorLevel strings outside `presentation.ts`.
- **Level labels:** `ok|warn|critical` shows as "Low / Elevated / High risk". The risk number goes in a tooltip only.
- **Remove A–F grades and trust_score from lists and headers.** The trace detail shows "3 of 4 claims supported · 1 contradicted"; the numbers go in Details.
- **Reason copy map,** shared by UI, docs and headers:

| Reason | Copy |
|---|---|
| `groundedness:*` | "States facts your documents don't contain" |
| `contradiction:*` | "Contradicts your documents" |
| `det:numbers_not_in_evidence` | "Mentions a number not in your documents: {finding}" |
| `pii_secret_leak:*` | "May reveal personal data or a secret" |
| `commitments:*` | "Promises something your rules don't allow" |
| `uncalibrated_snapshot` | "Would have blocked — blocking stays off until this check is calibrated" |
| `no_evidence` | "No documents found in this request to check against" |
| `fail_closed` | "Blocked because the check didn't finish in time (this route fails closed)" |
| `disagreement` | "Two checks disagree — sent to deep audit" |

### H3. The trace detail screen puts diagnostics first and ignores rubric evaluators
- In frontend §6.5, the left pane is the pipeline waterfall.
- The header strip has about 13 items.
- The rail shows only deep-audit claims. Refusal, PII, tone and commitments results have nowhere to appear, even though they are the default question set.

**Fix.** Lay the page out top to bottom:
1. **Outcome card**
   - Outcome pill and headline.
   - "Checked against: refund-policy.md · get_order()".
   - "Was this right?" with **Correct** and **False alarm** buttons, which call `PUT /api/labels`.
   - "Open evaluator" and "Adjust threshold".
2. **Two columns**
   - **Left:** the answer text with inline marks. Detector findings (UTF-16 spans) appear immediately; claim marks arrive with the audit.
   - **Right:** a "Checks" list with one row per evaluator: name, level word, a risk bar with flag and block ticks, or "Not checked — no documents". Below it, a "Claims" section shows a skeleton ("Deep audit running… usually 5–20 s") until the audit arrives.
   - Disagreement is one line: "Fast check passed it; deep audit found 1 contradicted claim."
3. **Details** (collapsed): timings waterfall, IDs, costs, snapshot, response headers, raw `decision/v1` JSON.

### H4. "A newly attached evaluator starts in observe" can't be implemented
- Mode is per route. `RouteEvaluatorRef` is `{ref, params, thresholds, enabled, evidence_sources}`, with no stage field.
- So attaching from Studio to a `block` or `fallback` route enforces immediately. That breaks the safety promise in smart §3.1.

**Fix**
- Add `RouteEvaluatorRef.stage: "shadow"|"active"`. It defaults to `shadow` when attached through the API or Studio; shipped YAML uses `active`.
- In `decide()`, shadow evaluators are computed and reported (`EvaluatorResult.shadow: true`) but excluded from all of these:
  - `critical_ids`
  - corroboration
  - `block_set`
  - disagreement
  - `would`
  - verdict
  - `max_risk`
- Add three decide vectors:
  - a shadow critical evaluator doesn't flag;
  - a shadow evaluator doesn't corroborate;
  - a shadow evaluator still appears in `evaluators`.
- UI: "Watching (not affecting responses)", with a **Make active** button next to the dry-run numbers.

### H5. Block mode is effectively unreachable, and the downgrade is silent
Several things quietly downgrade a block:
- `require_calibration` defaults to true, and no real snapshot has a measured calibration until there are ≥200 labels with 30 of each class.
- Streaming passthrough becomes flag (`stream_not_buffered`).
- A snapshot change becomes flag.
- A budget cap or an open breaker makes the route fail open or skip.

`EffectiveRoute` has no effective mode, so a user picks Block and nothing is ever blocked.

There is also a demo contradiction:
- The `calibrations.quality` CHECK allows only `measured` and `provisional`.
- So the mock file either fails `require_calibration`, and the demo's payments-faq fallback scenario never fires, or it must be labelled "measured", which is dishonest.

**Fix**
- Add `EffectiveRoute.effective_mode` and `downgrades: {code, message, fix:{kind:"link"|"action", target}}[]`.
- The route card reads "Block → currently acting as Flag", with one-click fixes:
  - "Buffer streams"
  - "Label 157 more responses"
  - "Allow uncalibrated blocking", behind a typed confirmation, which sets `require_calibration:false`.
- Add `mock` to the `calibrations.quality` CHECK and to `CalibrationState`. `calibrated()` is true for mock only when `backend=="mock"`. UI label: "Mock calibration (demo only)".
- Preset copy: "Block contradicted answers (starts as Flag until calibrated)".

### H6. Only the CLI can produce a measured calibration
- `make calibrate` and `POST /api/calibration` imports are the only paths to `measured`.
- `/api/studio/calibrate` has no side effects.
- Labels need admin rights and target strings.

A dashboard user can never unlock blocking.

**Fix**
- **Fit endpoint.** Add `POST /api/calibration/fit {target:"<id>@<v>", snapshot?, sources:["labels","deep_audit"], activate}` (admin). Go already has PAV and Platt through the `calibrate_fit` vectors. It returns a reliability preview, ECE and suggested thresholds, and writes a `calibrations` row with `origin='studio'`.
- **Progress meter** on each evaluator: "Labels 43/200 (31 correct, 12 false alarms) · deep-audit agreement 610 · [Label queue] [Calibrate now]". **Calibrate now** is enabled at the canonical thresholds.
- **Label queue page.** One response at a time, with two buttons plus Skip and keys C, F and S.
- **Studio wording:** quick calibration says "Suggested threshold from 12 labels" and never "calibrated".

### H7. The Overview hallucination rate is a biased number presented as a fact
- Audits are selected by outcome: flagged traces always go to the high lane, passes are sampled at 20%, and the uniform sample is 5%.
- Yet `totals.hallucination_rate`, `by_route` and `by_model` all divide hallucinated by audited.
- That overstates the rate by construction, which breaks "no fake metrics".

**Fix**
- Persist `audits.selection` (`high|pass_sample|uniform|direct|playground`) and `sample_weight = 1/p`.
- Compute a Horvitz–Thompson weighted rate, or use uniform-sample audits only.
- Return it as `LabelledValue {value, n, ci95, basis:"uniform sample"}`.
- Hide it when n < 30, with the copy "Needs 30 unbiased samples (have 12)".
- Exclude `playground`, `test` and `trafficgen` sources by default.

### H8. The advertised cost ($0.2 per 1k answers) is far below what the defaults will cost (about $6 per 1k)
All figures here are estimates from the contract assumptions.

| Component | Cost |
|---|---|
| Fast check (gate) | 5k tokens × $0.042/M ≈ **$0.21 per 1k** |
| Cascade deep audit | decompose ≈$0.003 + Jev ≈$0.0012 + about 31% of 8 claims × $0.004 ≈ **$0.014 per audit** |
| Defaults per 1k requests (10% flagged) | $0.21 + ~280 × $0.014 + 50 × $0.0355 (uniform LLM-only) ≈ **$6** |

That is about 28× the headline. The uniform ROI-measurement sampling alone (about $1.78 per 1k) costs 8× the gate it is meant to justify. The Savings panel will contradict the landing page.

**Fix**
- **Landing page "Why it's affordable"** shows three labelled lines:
  - "Fast check on every answer: ~$0.21 / 1,000 [estimate]"
  - "Plus deep audits on flagged answers and a small sample: ~$X / 1,000 [estimate, computed]"
  - "LLM judge on every answer: ~$35 / 1,000 [estimate]"
- **The calculator reads the shipped policy defaults** from `contracts/`. Add a `cost` vector that pins `calculator(default policy)`, so the copy can't drift from the code.
- **Lower the defaults** to `pass_sample_rate: 0.05` and `uniform_sample_rate: 0.01`. Show the sampling in the UI as "Savings measurement: 1% of answers also get a full LLM audit (~$0.36 / 1,000)", with a toggle.
- **Savings headline:** "Your cost: $X per 1,000 answers (fast check $a + deep audit $b)".

### H9. The frontend page specs are stale against the canonical spec, and W-WEB will build the wrong UI
Examples of superseded content still in the frontend spec:

| Section | Stale content |
|---|---|
| §6.6 | Policies editor with `flag_risk`, `flag_contradiction` and `block_contradiction` sliders, `on_block`, and `/api/gate/policies` |
| §4.2 | `GateAction` includes `pending` and `not_assessed` |
| §6.10 | JevSetupGuide is ".env block + `docker compose up -d proxy audit-engine`", which contradicts requirement 6 |
| §7 | Landing limitations say "Without Qdrant, claims come back UNSUPPORTED", which contradicts requirement 2 |
| §2 | A "Qdrant unreachable" banner for everyone, although knowledge is off by default |
| §2 | No `/app/traces`, `/app/evaluators`, `/app/savings` or `/app/setup` routes |
| C9 | Bearer subprotocol (dropped by R27) |
| C7 | Code-point spans (superseded by R20) |

No page spec exists for Setup, Evaluators, Savings or the Label queue beyond the prose in smart §3.1.

**Fix**
- Add canonical §J, "UI page contract": the final route map, and for each page its purpose, the one question it answers, data calls, states (empty, loading, error, degraded, demo, readonly) and copy.
- Add an explicit "Superseded frontend-design items" list with the entries above.
- JevSetupGuide becomes a primary **Connect OpenRouter** button, with "Use environment variables instead" as a collapsed secondary option.

### H10. Navigation has about 12 destinations, three of which show the same data
- Destinations: Overview, Live, Traces/Audits, Gateway (Decisions, Policies, Calibration), Evaluators, Studio, Savings, Knowledge, Evals, Playground, Settings, Setup.
- Live, Traces and Gateway→Decisions are three lists of the same requests.

**Fix.** Six items:

| Nav item | Contents |
|---|---|
| **Overview** | Setup checklist, outcomes, savings strip; `/app/savings` stays as a tab and deep link |
| **Traffic** | One table with a "Live" toggle (streaming, pause pill) and outcome filters; the Decisions tab is removed |
| **Evaluators** | Library, Studio, Labels/Calibration, and a Benchmarks tab hidden until a measured run exists |
| **Policies** | Routes, preview, versions |
| **Playground** | — |
| **Settings** | Connections, Models, Budgets, Access, Add-ons |

Knowledge appears in the nav only when `knowledge.enabled`. Otherwise it is a card in Settings → Add-ons: "Knowledge base (optional): `COMPOSE_PROFILES=knowledge make up`".

### H11. Most real traffic will silently skip groundedness, the headline feature
- R9 removed the whole-system-prompt fallback.
- Extraction recognises only fixed tag names, markers and headings.
- Apps that interpolate documents without tags get `not_assessed` for groundedness on nearly every request.
- Users will conclude "it doesn't detect hallucinations."

**Fix**
- **Coverage metric on Overview:** "Groundedness checked on 12% of requests". Add a "Why not?" panel with a per-source breakdown.
- **Evidence-suggestion job** (admin; reads `trace_bodies`; free):
  - Scan recent system prompts for repeated tag or heading shapes that aren't in `doc-blocks`.
  - Offer "Treat `<policy>…</policy>` blocks as documents — 340 of your last 500 requests would gain evidence", with a one-click action that adds `evidence.tag_patterns`.
- A `trustagent.context` snippet sits on the same panel.
- RE2 patterns are shown only under Advanced.

## MEDIUM

### M1. Evaluator Studio exposes the raw spec (about 20 fields)
The form surfaces `noul`, `risk_weights`, `risk_levels`, `min_confidence`, `requires_corroboration` with `det:` ids, band, `on_error`, `state_fields`, `min_chars` and `invert`.

**Fix: a Simple form**
- Fields: Name; "What should it catch?" (instructions); Answer type "Yes/No" or "Pick one". Scale is JSON-only because it is poorly calibrated.
- Each option has a 3-state control "Problem / Borderline / Fine", which maps to weights 1, 0.5 and 0.
- "Needs the app's documents" sets `evidence.requires`.
- A "Sensitivity" slider sets `warn_at`.
- "Allow this check to block" sets `block_eligible` and autofills the recommended corroboration. It reads: "Only block when another signal agrees: Contradiction, Number check."
- Everything else goes under **Advanced**.

**Fix: test bench and flow**
- Each test-bench row shows "Fires" or "Doesn't fire", then "Matches your label" or "Doesn't match". Probabilities, latency and cost expand on click.
- Summary line: "5 of 6 match · $0.001".
- Merge Save and Attach into "Save and watch on route {X}" (shadow stage, H4).
- When no judge is configured, "Start from a template" is the primary button.

### M2. Two dry-runs with different semantics share one name
Policy dry-run is a free replay; Studio dry-run is a paid re-run.

**Fix**
- Label them "Preview impact (free — replays past answers)" and "Test on past traffic (~$0.02 — runs the check)".
- Explain skipped counts in plain words: "312 past requests never ran this evaluator — use Test on past traffic."

### M3. Degraded banners have no priority order
A fresh install can show "auth open", "Jev off", "no judge", "uncalibrated", "snapshot changed" and "budget" banners all at once.

**Fix**
- Add one top-bar **Health** pill with a dot and a count, opening a checklist popover.
- Allow at most one full-width banner, and only for states that change what users see right now: demo, readonly, budget exhausted, `unauthorized` or `no_credits`, and an open breaker on an enforcing route.
- "Jev off" and "No judge" on a fresh install are setup-checklist items, not warnings.
- Copy:

| State | Copy |
|---|---|
| `no_credits` | "OpenRouter has no credits left — fast checks are paused and answers pass through unchecked. Add credits at openrouter.ai/settings/credits." |
| `unauthorized` | "OpenRouter rejected the key. Reconnect in Settings → Connections." |
| `breaker_open` | "Fast checks are failing (5 errors in a row). Answers pass through unchecked; retrying in 30 s." |

### M4. Settings is a dump of about 120 registry keys, and the backend picker lists 7 ids
**Fix**
- **Connections:** cards for OpenRouter, TypeSafe, Anthropic and OpenAI, each with Connect/Paste, Test, Remove and "Used for" chips.
- **Models:** fast-check model and snapshot pin; deep-audit judge.
- **Budgets:** two inputs, each with today's spend bar.
- **Access:** keys and sessions.
- **Add-ons:** knowledge base, local LLM.
- The full `SettingsView` moves to "All configuration (read-only)".
- The backend picker has 3 cards: OpenRouter (recommended: one key, no waitlist), TypeSafe direct, Other gateway.
  - `openrouter-systemone`, model, base_url and path go under Advanced.
  - `mock` and `off` are not choices: "Disconnect" is the only off switch.

### M5. There are two key-entry wizards
- The `make configure` CLI pastes secrets into `.env`.
- Because a non-empty env var always wins, those keys then show as read-only "env managed" in the dashboard and can't be rotated there.

**Fix**
- `configure.py` handles non-secrets only (ports, profiles, preset) and ends with "Open http://localhost:3000/app/setup to connect your keys".
- Secret prompts sit behind `--with-secrets`, for headless or CI use.

### M6. The static Pages demo would show many explained-but-disabled controls
**Fix**
- **Static nav:** Overview (recorded), Traffic (replay), Trace, Playground, Studio, and a "Run it yourself" page.
  - Studio drafts and tests live when a key is present. Save becomes **Copy JSON** or **Download .json**.
  - Policies, Settings and Knowledge are hidden.
- **Playground** opens with 3 one-click scenarios, each with evidence prefilled so groundedness is actually assessed: grounded answer, "60 vs 30 days" refund, PII leak.
- Offer "Paste an existing OpenRouter key" next to PKCE.
- On a 402, show "Your OpenRouter account has no credits — add $1+ at openrouter.ai/settings/credits" and don't count the call against the cap.
- Launch checklist: don't post the launch while `manifest.kind == "mock"`.

### M7. Admin key lifecycle
- `created_admin_key` is shown once and the session TTL is 12 h.
- Losing the key means shell access to run CLI recovery.

**Fix**
- When `PUBLIC_URL` is localhost, use a 30-day sliding session.
- The wizard requires Copy plus an "I saved it" checkbox.
- The sign-in page shows the recovery command.

### M8. It's unclear whether the file or the dashboard owns the policy
The newest version wins between the file and the dashboard, which will surprise people.

**Fix**
- Show a source badge.
- Show a banner: "policies.yaml changed at 14:02 and replaced your dashboard edit from 13:50 — [View diff] [Restore my edit]".

### M9. The Overview hierarchy leads with sparse data
The hero chart is claims-by-verdict, which is sparse because audits are sampled.

**Fix**
- Hero chart: "Requests by outcome" over time.
- Then, in order:
  - "What was caught" (top reasons in plain copy);
  - the cost/savings strip;
  - fast-check p50/p95;
  - evidence coverage (H11).
- Stage latencies, cascade share and backend mix move into a collapsed "Performance" section.

### M10. Jargon is unexplained, and each tier has several names
The same things are called gate / Tier-1 / Jev / decision model, and audit / Tier-2 / cascade / judge.

**Fix**
- Add `contracts/ui/glossary.v1.json` and a `<Term id>` hover component, used by both the landing page and the app.
- User-facing tier names: **Fast check (Jev)** and **Deep audit**.
- "noul", Tier-0/1/2, LangGraph, Qdrant and "cascade" never appear above the fold or in primary UI.
- Copy:

| Term | Definition |
|---|---|
| Jev | "A fast, cheap classifier from TypeSafe AI that answers yes/no and multiple-choice checks with probabilities — it doesn't write text." |
| Evidence | "The documents your app already gave the model: system-prompt documents, attached context, tool results." |
| Not checked | "We only judge facts against your documents. None were found, so we didn't guess." |
| Calibration | "Tuning thresholds on your own labelled traffic so 'risk 0.9' really means about 9 in 10." |
| Snapshot | "The exact Jev version that answered. A new version can shift scores, so blocking pauses until you re-check." |
| Second signal | "Blocking needs two checks to agree, so one noisy check can't block an answer." |

### M11. The landing page has two hero concepts and 8+ sections
The two heroes are frontend's GatewayTape and smart's highlighted answer.

**Fix**
- **One hero:** the answer card animating in 3 beats: answer arrives → "Flagged · 212 ms · $0.0002" → "60 days" gets a wavy underline, with the evidence snippet "…within 30 days of purchase" from refund-policy.md.
- **Copy:**
  - H1: "Catch hallucinations before your users do."
  - Subhead: "An open-source gateway that checks every LLM answer against the documents your app already sent — in about 0.2 s, for about $0.21 per 1,000 answers [estimate] — and sends only the risky ones to a full LLM audit."
- **At most 7 sections:** Hero · How it works (3 plain steps) · Why it's affordable (H8) · Write your own checks (Studio clip) · Try it (replay / Connect) · Honest limits (5 bullets plus a link) · Self-host.
- The tape moves into "Try it".

## LOW

- **L1.** Live feed pausing on hover is surprising. Pause only on scroll-away, Space or an explicit button, and show a "Paused · 12 new · Resume" pill.
- **L2.** There are 17 `X-TrustAgent-*` headers. Docs and the UI should lead with `Action`, `Reasons` and `Trace-Id`, and group the rest under "More headers".
- **L3.** Rewrite the PKCE consent copy as 3 bullets: where the key lives, what it costs, how to revoke it.
- **L4.** Copyable commands must come from one source, `contracts/ui/commands.v1.json`.
  - The engine service is `audit-engine` in the repo but `engine` in smart and the canonical spec. Pick one.
  - Add a CI check against `docker compose config --services`.
  - Settle `JEV.md` (dx) versus `JEV-ACCESS.md` (canonical) as `JEV-ACCESS.md`.
- **L5.**
  - Remove the emoji from the README title.
  - Record the hero GIF and OG image only from a `kind:"recorded"` manifest.
- **L6.** Savings copy before enough data: "So far: 212 answers checked for $0.04. An LLM-only judge would have cost ≈$7.50 (estimate from assumptions until 30 sampled audits exist)."
- **L7.** Wizard and Settings test results show latency, cost and snapshot only, never the probe's raw noul.