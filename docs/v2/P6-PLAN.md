# P6: plan to v2.0.0 (measure with the owner's key, launch, release)

P6 is the last v2 phase. It takes the P5 product to a public release:
- real measurements with the owner's OpenRouter key, each one capped;
- a recorded demo tape and a 60-second launch video made from it;
- launch material whose figures come only from measured runs, each with its label;
- version 2.0.0, the PR to `main`, the tags, the GitHub Release, GHCR, Pages and the social preview;
- optionally, an always-on cloud demo after the release.

I checked this plan read-only on 2026-10-10 (git show/log/grep only; nothing was built, run or pushed) against these heads:

| Branch | Head | State |
|---|---|---|
| `feat/v2-jev-gateway` | `a03ecb3` | P0–P4. **P6 is cut from here after P5 I-10 merges int6 into it.** |
| `feat/v2-int6` | `ac87b86` | P5 Step 0a and 0b are done. CP-REL (DR-1 to DR-3) is merged at `4828d4a`, and that merge records lead decisions **D142–D144**. The root `/tmp/` is ignored (P5 D143, `.gitignore:14`). |
| `feat/v2-w6-go` | `b170be0` | GO-1 to GO-3 are committed (trafficgen `--record` is in `696f26d` and `fc89612`). GO-10 (the load fold) has not started. |
| `feat/v2-w6-dxrel` | `548d515` | Merged into int6 up to DR-3. DR-4 (`release-images.yml`) and DR-6 (`record-demo`) have not started. `docker-compose.record.yml` is still a stub (`services: {}`). |
| `feat/v2-w6-dxdocs` | `ff0108f` | DD-1 (`check-claims`), DD-5 and DD-6 (the `docs/launch/*.md` drafts) are committed. |
| `feat/v2-w6-{wland,wlive,wrep,wsst}` | `e7edb0f` | No commits yet (still the int6 Step 0b head). |

**Re-baseline, 2026-10-23 (fix round 1 of P6a).** Three things changed after this plan was written,
by the owner's decision; D220–D226 (§1, "After planning") record them and win where an older line
says otherwise:
- `main` has been **public with v2 P0–P5 since 2026-10-21** (merge `a582549`, pushed directly by
  the owner's decision), and Claude may push branches. `main` moves only through pull requests the
  owner merges on GitHub. The push guard, the G8 token sitting and the "go push" rule are dropped
  (D220).
- **P6b and P6c run from a Claude cloud session** (claude.ai/code), not the laptop: Docker there,
  the key as a cloud environment variable, no tag pushes (the owner creates `v2.0.0` through a
  GitHub release), no `$ORCH` paths (D221).
- The GHCR `:edge` images are already public (D224). D200 still binds for Jev measurements and the
  recorded tape (D222); the latency wording names where a run really ran (D225).

**Fix round 2 of P6a (2026-10-23)** added D229–D232: the held-out ECE cannot be left out of a
publish or a launch, and a measured cost names what it was measured on (D229); the post fits
LinkedIn and its first line says what was measured (D229); the build disclosure matches the record
(D230); an unread Master Customer Agreement is mode W (D231); the paid-target exceptions are
written down (D232).

**Fix round 3 of P6a (2026-10-23)** added D233: our RAGTruth-QA accuracy is not like for like with
Arize's accuracy at 0.5. The bench flags at each evaluator's own warn_at (groundedness 0.8, which
came from Arize's tuned cutoff on RAGTruth itself, and contradiction 0.7), so every surface names
those thresholds and shows Arize's figures at 0.5 and at its tuned 0.80, neither as like for like.

**Final round 2 of P6a (2026-10-24)** added D234: the 2.0.0 bump is not made at Gate A, so
`feat/v2-jev-gateway`, and the pull request into `main` right after Gate A (A-8), keep VERSION 1.0.0
and the CHANGELOG heading `Unreleased — 2.0.0 (in progress)` until the release PR. The bump is
made on the cloud session's P6b branch with the planned tag date: at C4, or right before B8 in
mode A, since the recorded tape stamps the version it was recorded with.

**How to read references**
- A bare `file:line` means int6 at `ac87b86`.
- A file that exists only on a P5 stream is written `<stream> path`, for example `dxdocs docs/launch/linkedin-post.md:40`.
- If a line has moved, find it by its symbol.

**Path conventions**
- `~` is the owner's home directory.
- `$ORCH` is the orchestration cache outside the repository, as in P5. The lead's notes hold its absolute path. It is not spelled out here, so tracked files stay free of local paths (D216).

**Binding decisions**
- P3-PLAN D1–D28.
- P4-PLAN D29–D90.
- SPEC Appendix F D91–D98 and D76b.
- P5-PLAN D99–D141.
- P5 integration decisions D142 onward. These are recorded in merge commits such as `4828d4a`.

**P6 numbering**
- **P6 decisions are D200–D234 so far**, inside the D200–D239 block that `scripts/check_decision_numbers.py` enforces (D200–D219 at planning, D220 onward after it). The next free number cannot be used: P5's integration merges have already used D142–D144 and will use more before I-10. Taking the next free number now would repeat P5 CO-1 (the same number defined twice). Step 0 checks this (§2.1).
- **SPEC errata.** P6 opens **SPEC Appendix H: P6 errata** at Step 0 (§2.5).
- **Wave name: "w7".**
  - The integration branch is `feat/v2-int7`.
  - Stream branches are `feat/v2-w7-<id>`.
  - Worktrees are `~/projects/TrustAgent-w7-<id>`.

---

## 0. How P6 runs

### 0.1 Three parts

| Part | Who | Money | Pushes | Ends with |
|---|---|---|---|---|
| **P6a: automated preparation** | lead + 4 agent streams | **$0**. Mocks only: httptest, mock-upstream, fake keys. | none | **Gate A**: the "ready to spend" report to the owner (§7) |
| **P6b: owner-in-the-loop runs** | owner + lead, from the cloud session (D221) | Real key, every step capped. Expected **$3.1–5.9** [estimate]. All tool caps together $9.61. The key limit is **$8**. | branches only; nothing Jev-derived before G0 says mode A (D222) | **Gate B**: the owner has reviewed the results (§8) |
| **P6c: finish and release** | lead + owner | ≤ $0.001 (the Pages PKCE check) | Claude pushes branches (D220); `main` only through the owner's merge of the PR; the `v2.0.0` tag through the owner's GitHub release (D221) | **Gate C**: the definition of done (§13). The LinkedIn post comes after it. |

- **Entry condition.** The P5 gate has passed (P5-PLAN §18), and P5 I-10 has merged int6 into `feat/v2-jev-gateway`.
- **Exception.** The owner's sitting S1 (§0.2) does not depend on code and starts **now**. The TypeSafe terms question may take days to answer, so it must start first.

### 0.2 Owner sittings (the guides are in §10)

| Sitting | When | Active time | Money | What |
|---|---|---|---|---|
| **S1** | now | about 45 min | $0 | • G0 terms check and the email<br>• G1 one-time setup: legal name, other github.io sites, choices (including the "built with AI agents" wording, D227), OpenRouter account and privacy setting, the cloud environment (D221) |
| **S2** | one day after Gate A (mode A, D222) | about 2.5 h, mostly waiting | about $3–6 | From the cloud session (D221):<br>• G2 localhost PKCE check (laptop, before any credit; optional)<br>• G3 credit and key (a cloud environment variable)<br>• G4 approve B3–B8<br>• G5 comparison review during the bench run<br>• G6 Gate B review<br>• G7 close the key |
| **S3a** | release day | about 30 min, then hours of CI | $0 | • mode A: the results to TypeSafe by email (C5a); the post waits at least 3 days from it<br>• G9 settings and the minimal ruleset (if not set already, D220)<br>• the PR (Claude pushes the branch; the owner opens or reviews the PR) |
| **S3b** | when CI is green | about 20 min, then a 90 min tag build | ≈ $0.0002 | • G10 Pages variable, merge, packages checked (public since 2026-10-21, D224)<br>• G11 Pages Connect check<br>• the GitHub release as a pre-release, which creates the `v2.0.0` tag (D221) |
| **S3c** | after the tag build | about 45 min | $0 | • G12 the release notes and videos, Latest, About, ruleset checks, presentation<br>• G13 stranger check, then **post** (mode A: at least 3 days after C5a), with the Haqq link as the first comment |

- **Ending a sitting.** Each sitting ends with an exact phrase the owner says back.
- **Owner time.** About 4.5 h over 4–6 days: in mode A the post comes at least 3 days after S3a's results email. A TypeSafe reply, if one is needed, can take up to 7 days, and that wait runs in parallel.

### 0.3 P6a streams

| id | Stream | Size | Heavy? | Starts |
|---|---|---|---|---|
| `meas` | **MEASURE**: <br>• dry-run default (`CONFIRM=1`);<br>• bench robustness and pinned inputs;<br>• key-limit awareness;<br>• warm, one-at-a-time gate latency;<br>• vendor-host stamp;<br>• publish thresholds;<br>• calibration set and `calib-pass`;<br>• held-out ECE;<br>• measured fold;<br>• `bench-smoke` | M–L, about 4.5 d | yes | after Step 0 |
| `vend` | **VENDOR**: <br>• `jevcheck --capture` and the label mask;<br>• `record-demo` completion: upstream, ledger, preflight, drift, featured trace, `ACCEPT`;<br>• host stamp in recordings;<br>• `featured` consumers;<br>• launch video tooling | M–L, about 4.5 d (+2 d if DR-6 slipped out of P5) | yes | after Step 0 |
| `ship` | **SHIP**: <br>• release notes;<br>• CHANGELOG;<br>• licence and metadata;<br>• launch kit ready to fill;<br>• compare and `reverify` re-checks;<br>• public-repo hygiene and community files;<br>• workflow readiness;<br>• `check-claims` additions;<br>• `make linkedin-post` and `make clip` | M, about 3.5 d | light (one `npm ci` under the lock) | after Step 0 |
| `cloud` | **CLOUD** (cut line, queued): <br>• public demo overlay and Caddyfile;<br>• make targets;<br>• `DEPLOY-DEMO.md`;<br>• doc fixes | S–M, about 1.5 d | light (`config -q` only) | when a light slot is free; never delays Gate A |

- **Dependencies.** Streams depend on nothing from each other, only on the Step 0 contract PR.
- **Merging.** The lead merges a stream into int7 as soon as its gate passes. There are no checkpoints.

### 0.4 Rules for every P6a stream

1. **Mocks only.**
   - Tests run with `env -u OPENROUTER_API_KEY -u ANTHROPIC_API_KEY -u OPENAI_API_KEY -u TYPESAFE_API_KEY TRUTHTABLE_ENV_FILE=`.
   - Use httptest, `cmd/mock-upstream` (including the GO-1 vendor fault channel), the vitest fetch guard and `blockUnmockedExternal`.
   - These free network reads are allowed:
     - GitHub raw and `git ls-remote` for RAGTruth;
     - the Hugging Face API for AggreFact;
     - `GET https://openrouter.ai/api/v1/models` (no key);
     - public web pages;
     - the unauthenticated GitHub REST API.
   - A stream that finds a real key in its environment or its worktree `.env` stops and tells the lead.
2. **No GitHub actions of any kind.** That means no push, no tag push, no `gh`, no settings change and no image push.
3. **Scratch only.** Rehearsal output goes to `$ORCH/p6-scratch/<stream>/` or to the gitignored `tmp/`. These paths are never written in P6a:
   - `docs/benchmarks/**`
   - `contracts/claims/measured.json`
   - `config/calibration/<real snapshot>.json`
   - `frontend-react/demo-data/recorded/**`
   - `contracts/fixtures/vendor/captured/**`
4. **Heavy commands run under `flock /tmp/trustagent-heavy.lock`.** That covers Go test packages, pytest suites, npm builds, Playwright and ffmpeg. Docker belongs to the lead only (§7), one stack at a time.
5. **Contract-owned paths change only in the lead's Step 0 contract PR.** These are `contracts/`, `internal/decision/`, migrations, `src/lib/decision/`, `src/types/api.ts` and `proto/`. A stream that needs a change asks the lead.
6. **Config keys** come only through `contracts/config/keys.v1.json` plus `make config-docs`. Make variables (`BENCH_ARGS`, `CALIB_N`, `RECORD_MAX_TOTAL_USD`, `CONFIRM`) are not env keys.
7. **Gates actually test.**
   - Go runs with `-v` and no `--- SKIP` in the targeted packages.
   - Playwright runs under `CI=1` with no skips.
   - Glob assertions must not match an empty set.
8. **Owner stacks stay untouched:**
   - projects `truthtable` and `truthtable-demo`;
   - ports 3000, 8080 and 5432;
   - tags `:1.0.0`, `:2.0.0` and `:edge`.

   Proofs use `TRUSTAGENT_VERSION=p6local` and projects `truthtable-p6*`.
9. **Labels.** Every new figure in docs or UI carries a label. Nothing produced by a mock or by mock-upstream may be labelled measured or recorded.
10. **Commits.** Commit often, with conventional commits and the attribution trailer. New identifiers must pass `scripts/check-forbidden.mjs`.
11. **No local paths, `$ORCH` paths or personal emails** in tracked files (the D216 hygiene grep).
12. **Worktree `.env` files** come only from `python3 scripts/configure.py --ensure`. Never copy the main checkout's `.env` (D202).
13. **Test database (substitution, recorded at Step 0).** Wherever this plan or a stream gate names a Docker test Postgres, P6 uses the local PostgreSQL 16 from `export TEST_DATABASE_URL=$($ORCH/pg-url.sh)` (pgserver, no Docker; `internal/storetest` gives each test its own schema). Go gates that must not touch it run with `env -u TEST_DATABASE_URL`.

### 0.5 Concurrency and memory

- **Heavy slots: 3.** Normally `meas`, `vend` and the lead.
- **Light slots.** `ship` and `cloud` run as light sessions when `free -m` shows ≥ 1.5 GB available. `cloud` runs only when nothing heavy is queued.
- **Docker.** The lead's Docker runs need ≥ 2.5 GB available and ≥ 1 GB of swap free. Otherwise the lead asks the owner to run `make demo-down` for the duration.
- **Readings.** The lead reads `free -m` before every start or restart.
- **Parking.** A waiting stream commits and ends its session.
- **Step 0 reading (2026-10-10, P5 agents gone, the owner's demo stack up):** `free -m` showed 3,416 MB available and 3,953 of 4,096 MB swap used (142 MB free). That allows the 3 heavy slots and the light slots (≥ 1.5 GB available). Swap is below the 1 GB the lead's Docker runs need, so before any Docker run the lead reads again and, if it is still short, asks the owner for `make demo-down` for the duration.
- **During P6b nothing else runs.** Every stream is parked. The owner's demo is down from B3 to B8, for memory and so the latency figures are honest (D205). In the cloud session (D221) no other stack runs there from B3 to B8, and `docker stats` is read before B5 and B7.

### 0.6 How a paid step runs (P6b)

1. **One open card at a time.** The lead posts a card from §8: the command, the cap, the expected cost, the stop rule and "used so far $X of $8" (read with the free `/api/v1/key` call). In the cloud session (D221) the key comes from the environment variable `OPENROUTER_API_KEY`; no card names a `$ORCH` path or a laptop path.
2. **The owner's go.** An affirmative reply that contains "go" approves **only the open card**. "go be five" counts as "go B5". Posting a new card cancels the old one. Anything ambiguous means the lead asks again. The owner may instead run the command in their own terminal.
3. **The key never goes into the chat.** No command prints the key or its OpenRouter label (D211).
4. **Dry run first.** Every paid target prints its plan and estimate and exits 0 unless `CONFIRM=1` is given (D204). The exceptions are written down in D232: `make jev-check` (one probe, about $0.00001) and `doctor --probe` (the same probe, asked for explicitly), and `make launch-video-stack`, which runs only on an attempt that `record-demo CONFIRM=1` created, inside its caps.
5. **One result line.** Every paid command ends with one line `TRUSTAGENT_P6_RESULT {json}` (not `make jev-check`, D232: its one probe of about $0.00001 is below the ledger's precision, so B3 sends back its snapshot and limit lines). A dry run prints none, with one exception: `make jev-capture`'s dry run prints its plan as a result line (`label` "plan", `dry_run` true, `cost_usd` 0), which `make p6-ledger` counts as $0. It holds:
   - `step`, `run_id`, `exit`;
   - `cost_usd`, `cap_usd`, `key_usage_usd`, `key_limit_remaining_usd`;
   - `label`, `snapshot`;
   - `hosts{decision, judge, upstream}`;
   - `counts{n, scored, failed}`.

   When the owner runs a step themselves, this line is what they paste back.
6. **Check against the card.** The lead compares the line with the card. Anything outside the expected range means stop and report.
7. **Attempts.** At most 2 attempts per step without a new go.

---

## 1. Lead decisions (binding; D200 onward)

### Terms, money and containment

| # | Question | Decision | Owner |
|---|---|---|---|
| D200 | When the publishing terms are checked, and the three modes | **Amended by D231 (fix round 2):** an unread Master Customer Agreement counts as unsure, so mode W. **Amended by D220 and D222 (2026-10-23):** `main` has been public since 2026-10-21, so "no push of anything" is now "nothing Jev-derived is committed to a pushed branch before mode A"; the rest stands.<br>**Trigger.** A third-party write-up ([wunderlandmedia, 2026-09-18](https://wunderlandmedia.com/typesafe-ai-jev-terms-of-service-gdpr)) says TypeSafe's Master Customer Agreement §2.3(f) bans publishing "benchmarks or performance information about the Services". It calls a breach material (§10.2(a)) with uncapped liability (§12.3(b)).<br>• I could not read the primary text.<br>• TypeSafe's public [Terms](https://typesafe.ai/legal/terms) (2026-09-19) and [AUP](https://typesafe.ai/legal/acceptable-use-policy) (2026-09-23), and [OpenRouter's Terms](https://openrouter.ai/terms) (2026-08-31), have no such clause.<br>• But OpenRouter §5.1 binds users to each model's terms.<br>• SPEC P6 step 6 also covers "redistributing outputs": the tape, the captured fixtures and the OG image.<br>**Rule.** SPEC P6 step 6 becomes step 0 (H-1). There is **no paid Jev run and no push of anything** until G0 returns. Pushing to the public repo is publishing, and the branch already carries third-party Jev figures.<br>**Record.** G0 goes into `docs/launch/terms-check.md`: date, URLs and the quoted clauses (or "none found in X, Y, Z"), plus the dataset licences (§2.2).<br>**Mode A** (no clause, or written permission): the full plan runs. The owner sends a **courtesy note**, not a permission request: an FYI, "may we tag TypeSafe in the post?", and the results shared 3 days before posting.<br>**Mode W** (a clause found, or unsure): the owner sends the **permission request**. P6a, Gate A, G2 and all non-Jev preparation continue. Paid Jev steps wait unless the owner explicitly chooses to measure privately; the outputs then stay gitignored. **P6c stops before C5: nothing is pushed.** After 7 days without a reply, the owner chooses R or keeps waiting.<br>**Mode R** (a clause and no permission):<br>• Never committed or pushed: the Jev-derived `measured.json` values (`fast_check_usd_per_1k`, `gate_latency_*`, `ragtruth_qa_*`, `same_checks_cost_ratio`, ECE), the real-snapshot calibration file, the recorded tape and captured Jev responses.<br>• Published: the load run (mock vendors), the Tier-0 benchmarks, `llm_same_checks_usd_per_1k` and `llm_full_audit_usd_per_1k` (Haiku judge), and the captured non-Jev shapes.<br>• Pages ships the simulated tape plus "Try it live with your key" (S-10).<br>• The owner decides in **H-R** whether third-party and vendor Jev figures that are already public elsewhere stay (with source links) or go.<br>• Launching needs H-R approved by the owner, because §8.2 forbids posting while the manifest is mock. The gate is `check-claims --launch --restricted`, and the post is the mode-R variant with no Jev figures.<br>**The email drafts live in `$ORCH`**, never in the repo. | owner, lead |
| D201 | Spending envelope | **Credit.** Add **$10**. The card is charged about **$10.80** [vendor: 5.5% fee, minimum $0.80]. Auto Top-Up stays **off**; unused credit may expire after a year [vendor]. The credit is bought in S2 **after** G2, so the Connect key G2 creates cannot spend anything.<br>**Key.** One measurement key, `trustagent-p6`, with a **limit of $8, set once and never reset**. There is no raise in the middle of a run.<br>**Tool caps (hard):**<br>• capture $0.01;<br>• smoke $0.30;<br>• `calib-pass` $0.30 (one re-run on a new card, $0.30 more);<br>• `bench-lite` $5 (ledger across sessions);<br>• `record-demo` **$4 in total across attempts**, covering the upstream answers and the video's stack beats.<br>The caps add up to **$9.61**.<br>**Key-limit awareness.** Each paid tool's preflight reads the free `GET /api/v1/key` and lowers its own cap to `limit_remaining − $0.10`. If that is below the step's minimum, it refuses. The $8 limit therefore ends a step cleanly, never as a 402 in the middle of a run.<br>**Expected:** capture < $0.005, smoke $0.05–0.15, calib $0.06–0.25, bench $2–3.5, record $1–2. Total **$3.1–5.9** [estimate].<br>**Connect keys** from G2 and G11 are deleted, or limited to $1, the same day.<br>**Minimum plan** (if the owner adds only $5; card about $5.80):<br>• key limit $4.70;<br>• `BENCH_MAX_COST_USD=2 BENCH_ARGS="--systems gate,llm_same_checks"` (the Savings full audit stays "published assumption");<br>• `RECORD_PASSES=1 RECORD_MAX_TOTAL_USD=1.5`;<br>• no video stack beats (H-6).<br>Caps $4.11.<br>**Close (G7):** after Gate B says "publish", the key is deleted or limited to usage + $1 and removed from `.env`. | owner, lead |
| D202 | Where P6b runs; key and push containment | **Superseded by D220 and D221 (2026-10-23):** P6b and P6c run from the cloud session with the key as a cloud environment variable, and the push guard is dropped. On the laptop the `.env` rule still holds: no worktree `.env` holds a key.<br>**One checkout.** At Gate A the lead merges int7 into `feat/v2-jev-gateway` **locally, with no push**. P6b and P6c run only in the main checkout `~/projects/TrustAgent`, and `RUN=` paths are absolute.<br>**One `.env`.** The real key lives only in the main checkout's `.env`. Two things make a copied `.env` dangerous:<br>• the Go config reads the `.env` next to `VERSION` (`internal/config/dotenv.go:14-17`);<br>• compose reads the project directory's `.env`.<br>A copied `.env` would spread the key into "mocks-only" worktrees. Before every card, `grep -l '^OPENROUTER_API_KEY=.\+' ~/projects/TrustAgent*/.env` must list only the main checkout.<br>**Push guard (S1, the owner runs it):**<br>• `git config remote.origin.pushurl DISABLED` makes `git push origin` fail;<br>• `git config credential.useHttpPath true` makes the old path-less `~/.git-credentials` entry (scope unknown) stop matching this repo.<br>Worktrees share this config. Until G8, no credential works for this repo.<br>**Fix-forward work** during P6b and P6c happens in worktrees without keys. The lead merges it into the main checkout. | lead, owner |

### Measurement

| # | Question | Decision | Owner |
|---|---|---|---|
| D203 | What makes a run "measured" or a tape "recorded" | **Problem.** Today the label is decided by the backend **name** alone (`evals/report.py:147,320,360`; `judges.py:696`; `internal/recording/recording.go:179-200`). A run pointed at mock-upstream through `TRUTHTABLE_OPENROUTER_BASE_URL` would be labelled measured.<br>**Fix: the vendor host is part of the evidence.**<br>• `measured.v1` gets `$defs/vendor_host`, an enum of `openrouter.ai`, `api.typesafe.ai` and `api.anthropic.com`.<br>• gate-eval lines carry `endpoint_host`, resolved from `TRUTHTABLE_OPENROUTER_BASE_URL` **or** `TRUTHTABLE_JEV_BASE_URL`; both are registered keys.<br>• Judge records carry the provider host.<br>• `benchmark-run.v1` gets `vendor{decision_host, judge_host}`.<br>• `recording.v1` gets `decision_model.host`, `judge.host` and `answer_sources.generated.host`. These are **bare hostnames, never URLs**, because `recording/sanitize.go:31` scrubs hosts inside URLs. A test checks that a sanitized manifest keeps them.<br>• A host outside the enum makes the run `simulated`, with the reason `non_vendor_host`. `publish`, `recording.go` and `check_recording.py --require-recorded` refuse it.<br>**Regression tests:**<br>• a bench run through mock-upstream is simulated, and publish refuses it;<br>• a tape recorded through mock-upstream is not `recorded`. | lead (contract), MEAS, VEND |
| D204 | Dry-run default, bench robustness, publish rules | **Amended by D229 and D232 (fix round 2):** publish also refuses a usable held-out check left out (`holdout_not_passed`) and a cost without its conditions (`cost_conditions`); the dry-run exceptions are written down in D232.<br>**Dry run by default.** Today `bench-lite` (`mk/py.mk:90`) and `eval-live` (`:101`, caps $40 + $20) spend as soon as they run. `bench-lite`, `bench-smoke`, `calib-pass` and `eval-live` now print their plan and **exit 0 unless `CONFIRM=1`** is given. The plan shows:<br>• n and the systems;<br>• the price-table estimate and the effective cap (D201);<br>• the resume key and the ledger spent so far;<br>• the resolved hosts and the snapshot (from the free `/key` and models calls; "no key: plan only" when there is none).<br>JEV-ACCESS and the README commands are updated.<br>**Inputs:**<br>• `BENCH_ARGS` is passed through;<br>• `RAGTRUTH_REVISION` defaults to the commit SHA pinned at Step 0 (today `main`, `py.mk:66`);<br>• `bench-publish` **requires `RUN=`**, because `--latest` picks by mtime (`bench.py:388-391`).<br>**Fixed resume keys:** `BENCH_RESUME_KEY ?= lite-p6` and `smoke-p6`. Date-based keys would start a new ledger and a new cap on another day. Cards print the literal key.<br>**gate-eval is built once, at B5.** Its sha256 goes into `run.json` `inputs`. A resume with a different binary is refused, because the cache key includes the binary's sha and the run would pay twice.<br>**Stop rule:** `--stop-after-failures 5`, counted per system on consecutive failures. It prints the failure kind (402, 404 `data_policy`, 401, parse error, timeout) with the JEV-ACCESS fix, and exits 3. Examples it never reached are not cached. The same command with `BENCH_ARGS="--retry-failed"` continues.<br>**Policy:** gate-eval runs with `--policy builtin`. `TRUTHTABLE_POLICY_FILE` is removed from the subprocess environment, and `inputs.policy_source` is recorded.<br>**Publish refuses:**<br>• `failure_rate > 0.05` or `scored < 0.95·n`;<br>• more than one gate snapshot (today `describe()` takes the most common one, `judges.py:744-748`);<br>• a dirty tree at run start (`git status --porcelain --untracked-files=no`);<br>• a non-vendor host;<br>• dataset text in `per_example` (ids, labels and scores only; the QA passages are MS MARCO, research-only);<br>• `cost_source: unknown`.<br>A measured cost needs `cost_source: reported`; `computed` is labelled "measured tokens × vendor price".<br>**Attempts are disclosed** in `measured.json` `sources.attempts{bench_runs, refused_run_ids}`, counted from the lite runs since Step 0.<br>There is no override flag. `bench publish --check-only` runs every refusal check into scratch for Gate B. | MEAS |
| D205 | Gate latency | **Wording amended by D225 (2026-10-23):** it names where the run really ran, from `inputs.host_env`, never an assumed laptop.<br>**Today:**<br>• each example is a fresh process, so TCP and TLS setup is inside `latency_ms` (`cmd/gate-eval/eval.go:64-65`);<br>• `BENCH_CONCURRENCY ?= 4` (`py.mk:63`; semaphore at `runner.py:127`) runs gate calls alongside judge calls.<br>**`gate-eval --warm`** (on by default in the bench and in `calib-pass`) first makes the free `GET /api/v1/key` on the same client. The line then carries `connection:"warm"`; other backends get `cold`.<br>**`--gate-concurrency 1`.** The gate system runs alone first, one example at a time: the same calls and cost, about 2–4 min. Then the LLM systems run at 4.<br>**Conditions are recorded:** `run.json` `inputs.host_env{mem_available_mb, loadavg, platform:"WSL2", region}`. The demo is down from B3 to B8.<br>**Wording:** "gate latency p50/p95: warm connection, one request at a time, run on {platform}, region {region}, snapshot {s}, n={n}" [measured] (D225: from the run's host_env, never an assumed laptop). | MEAS |
| D206 | Calibration: input, labels, honest "ECE after" | **Amended by D229 (fix round 2):** C1 passes `HOLDOUT=<the run's holdout-groundedness.json>`; publish refuses a usable held-out check beside the run that was not passed, and `check-claims --launch` refuses a measured file without the held-out ECE unless the tape is visibly uncalibrated.<br>**Today:**<br>• `make calibrate` needs `EVALUATOR`/`IN` (`mk/go.mk:70-75`), and nothing produces labelled input;<br>• the bench file is not valid gate-eval input (`example.go:52-56`);<br>• `ece_after` has no producer (`report.py:436-441`) and would be in-sample.<br>**Fit on the train split; measure on the test split.**<br>**`make calib-pass`:**<br>• takes a **natural (unstratified)** RAGTruth-QA **train** sample;<br>• `CALIB_N` is fixed at Step 0 from the free download, so that conflict positives are ≥ 40 and each `label_hallucinated` class is ≥ 60, with `CALIB_N ≤ 1000`;<br>• it **asserts that no `source_id` is shared** with the bench's test sample (RAGTruth has about 6 responses per source);<br>• labels: `groundedness@1` = `label_hallucinated`; `contradiction@1` = "has a conflict span". A baseless-only answer counts as 0, because it is unsupported, not contradicted;<br>• `label_source: public_dataset`;<br>• **one** warm gate-eval process with `--max-cost-usd 0.30`.<br>**Fit.** `make calibrate EVALUATOR=… IN=<gate.jsonl> LABEL_SOURCE=public_dataset DATASET=ragtruth:qa-train@<sha>`, dry run first, then `WRITE=1`. The second evaluator merges into the same `config/calibration/<snapshot>.json`.<br>**Held out.** `make calib-holdout RUN=<abs run.json>` (free) feeds `cmd/calibrate --holdout`, which gives ECE and Brier before and after. `bench publish --calibration-holdout` folds `groundedness_ece_before/after` with `method: holdout` and the calibration file's sha256.<br>**Quality:**<br>• `groundedness@1` must reach `measured` (n ≥ 200, ≥ 30 per class; `decision/calibrate_file.go:25-29`);<br>• `contradiction@1` is committed at whatever quality it reaches. If it is provisional, it cannot drive blocking or fallback (`policy/readiness.go:180`), so payments-faq falls back through groundedness only, and the tape shows that;<br>• if groundedness is not `measured`, the owner chooses: a larger `CALIB_N` re-run (≤ $0.30, new card), or `ALLOW_UNCALIBRATED=1` at B8.<br>**Caveat.** The contamination caveat from `value-claims.v1.json:158` also goes on our RAGTruth agreement and ECE claims. | MEAS, owner at B6 |
| D207 | One snapshot across calibration, bench and recording | • Each result line prints the snapshot.<br>• The `record-demo` preflight refuses a snapshot without `config/calibration/<snapshot>.json`, unless `ALLOW_UNCALIBRATED=1`.<br>• Publish refuses mixed snapshots.<br>• `measured.json` names the snapshot.<br>• `check-claims --launch` requires `measured.json` and the recorded manifest to share a snapshot.<br>**Rule:** B6, B7 and B8 run on the same day.<br>**If OpenRouter rolls the snapshot between B7 and B8,** the lead posts a choice:<br>• (a) re-run `calib-pass`, and the bench if it fits under the key's remaining limit, on new cards;<br>• (b) keep both, set `sources.snapshot_split: true`, and every surface names both snapshots.<br>The owner picks. | lead, owner |

### Recording, video and vendor captures

| # | Question | Decision | Owner |
|---|---|---|---|
| D208 | `make record-demo` in P6 (finishes P5 D123) | **Generated answers** (`generated-monthly-refund` and `generated-weekend-shipping`, model `gpt-4o-mini`):<br>• the record overlay sets `TRUTHTABLE_UPSTREAM_URL=https://openrouter.ai/api` and `TRUTHTABLE_UPSTREAM_API_KEY=${OPENROUTER_API_KEY}`;<br>• `trafficgen --record --model-prefix openai/` rewrites the model, with `max_tokens ≤ 300`, **only** for `answer_source: generated`;<br>• scripted scenarios make zero upstream calls (a test checks this);<br>• about 4 calls, about $0.001.<br>**Ledger.** The source of truth is the **change in `/api/v1/key` `usage`** (free) before and after each attempt, read after a 30 s settle. `/api/costs` gives the breakdown. `tmp/record-demo/ledger.jsonl` survives `down -v`.<br>• The remaining amount is `R = min(RECORD_MAX_TOTAL_USD − spent, limit_remaining − 0.10)`.<br>• Each attempt's caps are `decision = min(0.50, R)` and `llm = min(3, R − decision)`.<br>• It refuses when `R < 0.50`. `RECORD_PASSES` defaults to 2.<br>**The daily caps reset at 00:00 UTC (03:00 Israel).** An attempt in which any trace was skipped on budget cannot be accepted, and cards never cross 03:00.<br>**Isolation:**<br>• project `truthtable-record`;<br>• no host port for the proxy or the database;<br>• `TRUSTAGENT_VERSION=p6rec` image tags.<br>**Preflight:**<br>• `doctor --only decision_model,judge`;<br>• the snapshot and calibration file (D207);<br>• `VERSION` is 2.0.0 (D216).<br>`check_recording.py --require-recorded` checks `manifest.trustagent_version == VERSION`.<br>**Review before it counts:**<br>• `trafficgen --expect --report-only` writes `drift.json` and prints a table;<br>• the tape is exported to `tmp/record-demo/<attempt>/`;<br>• the stack stays up until `ACCEPT=1 ATTEMPT=<id>` or `DISCARD=1`.<br>`ACCEPT` copies the tape into `frontend-react/demo-data/recorded/`, runs `check_recording.py --require-recorded`, runs the video's stack beats if `VIDEO=1` (D210), then `down -v`.<br>**`ALLOW_UNCALIBRATED=1`** sets `require_calibration:false` for payments-faq in the record overlay. The manifest then has `calibration.quality:"none"`, so the UI chip says "uncalibrated". | VEND |
| D209 | The featured trace and live drift | Live outcomes may differ from the scripted ones. **The tape is never edited.**<br>`record_demo` picks `manifest.featured` by a rule:<br>1. the first trace with outcome flag, fallback or block that has ≥ 1 CONTRADICTED claim with a span;<br>2. it prefers `refund-contradiction-flag`, then any support-bot trace.<br>`/og`, `e2e/launch/hero.spec.ts`, the screenshots, the video, `demo-script.md` and the post's opening lines all read `featured`. None of them names a fixed scenario id.<br>**No trace meets the rule:** exit 4, "review needed". The owner picks another attempt within the total cap, or accepts the best flagged trace. The copy is reworded to match. | VEND, owner at B8 |
| D210 | The 60-second launch video | **Requirements.** SPEC §8.2's demo script needs real-stack beats (dxdocs `docs/launch/demo-script.md:10-14`: Studio Draft, "Test on past traffic", the Policies fallback replay), burned-in captions (`:20`) and an MP4 (`:40`).<br>**Two recorders:**<br>• **Stack beats:** `make launch-video-stack ATTEMPT=<id>` runs inside B8's `ACCEPT` (`VIDEO=1`) on the record stack, before `down -v`. It covers Studio draft, test on samples, test on past traffic, the Policies fallback replay and Savings. It uses Playwright `recordVideo`, and its cost (about $0.05) sits inside the record ledger.<br>• **Static beats:** `make launch-video` at C2 records the recorded Pages build (`vite preview`): hero, Try the demo, Traffic, then the featured trace.<br>**Assembly.** ffmpeg joins the clips and burns in captions from an `.srt` generated from the demo-script table. Outputs:<br>• `tmp/launch/video-4x5.mp4` (1080×1350, the LinkedIn feed);<br>• `tmp/launch/video-16x9.mp4` (1920×1080, the Release).<br>It refuses unless the manifest kind is `recorded`. Mock rehearsals write to scratch only, behind `VIDEO_ALLOW_MOCK=1`.<br>**What gets committed.** MP4s are **not** committed (repo size); they are Release assets and the post's own media. The hero GIF (≤ 8 MB) is committed for the README.<br>**Fallback (H-6).** A 45-second take from the Pages build only, for $0. It is used if V-7's stack part slips or the minimum plan applies.<br>**On LinkedIn**, the video is the post's own media (a comment cannot hold a native video upload), and the links go in the text. | VEND |
| D211 | `jevcheck --capture` (deferred by P5 D122) and the label mask | **Command:** `jevcheck --capture DIR [--max-cost-usd 0.01] [--dry-run]`. `make jev-capture` is a dry run unless `CONFIRM=1`.<br>**It captures:**<br>• a 200 from each decision path (`openrouter-decisions`, `openrouter-systemone`): body parity, key order, rounding, confidence and snapshot fields;<br>• `GET /api/v1/key`;<br>• a Haiku chat with `usage.include` (is `usage.cost` present?);<br>• a 30k-token probe (the 32k limit);<br>• a ZDR request;<br>• a bogus key (401, free).<br>**Budget.** It estimates tokens before each call, refuses when the estimate exceeds what is left, and sums `usage.cost`. Expected < $0.005.<br>**Sanitizing.** Keys, generation and request ids, key labels, and usage and limit amounts become placeholders. `source: "captured <date> via jevcheck --capture"`, `sanitized: true`. gitleaks runs over `DIR`.<br>402, 429 and 413 stay synthetic.<br>**Test.** `vendor_fixtures_test.go` decodes each captured 200 with the production decoders.<br>**Label mask.** `cmd/jevcheck/main.go:262-263` prints OpenRouter's key label today. It is masked (`sk-or-…xxxx`) in every output. | VEND |

### Claims and launch copy

| # | Question | Decision | Owner |
|---|---|---|---|
| D212 | Honesty rules for claims | **Amended by D233 (fix round 3):** the "like-for-like" premise below is wrong. The bench does not run at 0.5: it flags at each evaluator's warn_at (groundedness 0.8, from Arize's tuned cutoff; contradiction 0.7), either one, on questions worded differently from Arize's single one. Our agreement stands beside **both** of Arize's figures (0.76 at 0.5 and 0.87 at a tuned 0.80), neither called like for like, and its caveat names our thresholds; plain accuracy is published as the measure Arize reports, not as a like-for-like number.<br>**Pairs.** `value-claims.v1` gets an optional `measured_pair`:<br>• `same_checks_cost_multiple` (27×) ↔ `same_checks_ratio_measured`;<br>• `fast_check_cost_per_1k` ↔ `_measured`;<br>• the full-audit estimate ↔ `llm_full_audit_cost_per_1k_measured`.<br>`check-claims` warns when a pair differs by more than 2×. `--launch` errors until the lead updates the estimate's figure or copy, or removes it from the surfaces that show the measured value (a contract edit at C2).<br>**Like-for-like comparisons:**<br>• Our measured agreement uses default thresholds. It is compared with `arize_ragtruth_accuracy_at_default_threshold` = **0.76** (`value-claims.v1.json:223`), **not** with the 0.87 (`:158`), which needs a tuned 0.80 threshold and carries a contamination caveat. If 0.87 is shown at all, it is labelled "tuned threshold".<br>• Our headline agreement is balanced accuracy; Arize's figure is accuracy. So we also publish plain accuracy (`ragtruth_qa_accuracy`) as the like-for-like number.<br>• Each figure shows n per class and a 95% interval: Wilson for accuracy, a seeded bootstrap (2,000 resamples, seed 13) for balanced accuracy.<br>**`reverify`.** Every claim with a `reverify` field (`openrouter_faithfulness_accuracy`, `_median_ms`, `:236-261`) is re-checked in S-5. `--launch` fails while any `reverify` remains.<br>**Savings.** `measured.json` gets `llm_full_audit_usd_per_1k`. `cmd/proxy/roi_api.go:520-577` reads it, so the row says "measured · run X".<br>**Publish what comes out.** One run, named by `RUN=`, with no re-runs to fish for a better number; refused attempts are disclosed (D204). If agreement comes out below the LLM judge or below 0.76, it is published as it is, with the caveats, and the story stays the cost/latency Pareto. | lead, SHIP, MEAS |
| D213 | The LinkedIn post and the launch rule | **Amended by D229 (fix round 2):** the post stays within 2,800 characters (the command refuses more, exit 2); the first line says the fast check's cost on a public benchmark and names the category; the cost names what it was measured on, and the ratio names its judge. **Opening amended by D227 (fix round 1):** the example says where its answer came from (never "my support bot"), the latency is the benchmark median with its conditions, Jev is credited to TypeSafe AI, the build with AI coding agents is said, and the first line names TrustAgent within 150 characters; the text below is the original draft.<br>**Timing.** The post goes out only after Gate C (§13).<br>**`make linkedin-post`** prints **plain text** from `measured.json`, the recorded manifest's `featured` trace and the claims. Today the draft is a markdown blockquote with a paste-by-hand table (dxdocs `linkedin-post.md:14-66`). The command exits 1 on any missing value. `CLIP=1` copies it with `iconv -f UTF-8 -t UTF-16LE \| clip.exe`, because plain `clip.exe` garbles × and em dashes. Without `clip.exe` it prints to stdout.<br>**Opening lines.** They must survive the "…see more" cut and are built from the featured trace: "My support bot promised {claim}. The policy said {evidence}. TrustAgent caught it in {p50} for {cost}/1,000 answers, {ratio}× cheaper than an LLM judge doing the same checks [measured]."<br>**Contents:**<br>• `same_checks_ratio_measured`;<br>• the agreement **only** together with n, the interval, the thresholds the bench flags at and both Arize figures (0.76 at 0.5, 0.87 at a tuned 0.80), neither called like for like (D233); otherwise it moves to the README;<br>• one stack line for recruiters;<br>• an optional "open to roles" line (owner, S1);<br>• "no sign-up" (`:40`) becomes "live mode uses your own OpenRouter key".<br>Mode R has its own variant with no Jev figures.<br>**Phase grep** at Gate A, before the bump: `rg -n '\bP6\b\|in P6' frontend-react/src backend-go backend-python/src scripts contracts \| grep -v -F -f scripts/phase-refs.allow` prints nothing.<br>**Before posting:** LinkedIn Post Inspector (it caches link cards for about 7 days); the video as media; timing set by the owner. | owner, lead, SHIP |

### Release

| # | Question | Decision | Owner |
|---|---|---|---|
| D214 | Pushing and credentials | **Superseded by D220 and D221 (2026-10-23):** Claude pushes branches (the laptop's repo-scoped token since 2026-10-21, the Claude GitHub app in the cloud); `main` only through the owner's PR merge; no G8 sitting, no `pushurl DISABLED`, no `$ORCH/ci_logs.py`. Never a force push; never `main` by Claude. The text below is the history.<br>**Token.** A fine-grained PAT for `timkush1/TrustAgent` only, expiring in 30 days, created at S3a (not earlier). Permissions:<br>• **Contents** read and write;<br>• **Workflows** read and write;<br>• **Actions** read;<br>• Metadata read (automatic).<br>No Pull requests permission: the PR is opened in the web UI.<br>**Storage.** It is stored for this repo only: `useHttpPath` (S1) plus `path=timkush1/TrustAgent.git` in `git credential approve`. **There is no global `git credential reject`**, which would remove the owner's credential for their other repos.<br>**Dry run.** `git push --dry-run <url>` comes first. It checks authentication but **not** the Workflows permission. If the first real push is refused, the owner edits the token's permissions; no new token is needed.<br>**Push rules:**<br>• `pushurl` stays `DISABLED`; approved pushes use the explicit URL `https://github.com/timkush1/TrustAgent.git`;<br>• each push of a branch or tag needs "go push";<br>• during the PR, the owner may grant a window for `feat/v2-jev-gateway` only, for CI fixes;<br>• never `main`, never a force push, never deleting a remote ref;<br>• the `STATIC_CONNECT_PRIMARY` flip, if needed, goes through branch `fix/static-connect-paste` and a one-commit PR the owner merges in the UI.<br>**CI status.** Read through the public, unauthenticated API (60 calls an hour). Logs come from `$ORCH/ci_logs.py RUN_ID`, which gets the PAT through `git credential fill` inside a pipe, never prints it, and only GETs `/actions/` endpoints. It lives outside the repo.<br>**`gh` is optional** and not installed. | lead, owner |
| D215 | Merge, tags, Release, GHCR and Pages, in order | **Amended by D220, D221 and D224 (2026-10-23):** `main` already holds P0–P5 (merge `a582549`), so the PR brings P6 and 2.0.0; the merge-commit rule stands; `v2.0.0` is created by the owner's GitHub release (a pre-release first, Latest after the notes); `v1.0.0` is optional, from the laptop; the packages are already public. The order is §9 as amended.<br>**Merge.** The PR uses **"Create a merge commit"**. Squash and rebase are forbidden: 1,072 commits and 98 merges stay. "Require linear history" stays off.<br>**v1 tag.** `v1.0.0` at `d709df3` ("Phase 7: portfolio polish + v1.0.0"). There are no local tags; Step 0 confirms with `ls-remote` that none exists remotely. The v1 workflows trigger only on `branches:[main]`, so the tag runs nothing.<br>**Order:**<br>1. **S3a.** Allow merge commits; Pages Source = Actions; the `github-pages` environment gets the tag rule `v*`; a **minimal ruleset** on `main` (block force pushes and deletion, require a PR, no required checks yet). Then "go push": `v1.0.0` and the branch. Open the PR.<br>2. CI loop. `pages.yml` and `release-images.yml` run build-only.<br>3. **S3b.** The shared-origin check is clear (G1) and the recorded tape is on the branch (mode A), so set `PAGES_ENABLED=true`. Merge: `main` deploys `auto`, which means recorded. The first `release-images` run creates the 3 packages as **private**.<br>4. Packages public, plus `GHCR_PUBLIC=true`.<br>5. G11, the Pages PKCE check. If it is refused, the fix PR (D214) is merged **before** the tag.<br>6. `check_version.py --tag v2.0.0` on the merge commit, then "go push" for `v2.0.0`. That runs the Pages tag deploy (recorded) and the semver images (no cache, ≤ 90 min), plus `first-run` and `anonymous-pull`.<br>7. **S3c.** The Release in the UI, set as **Latest** (the D127 badge needs a Release, not just a tag). Then the ruleset's required checks, now that they have run on `main`.<br>**Optional early draft PR** (mode A or R only; **replaced by D220's pull request into `main` right after Gate A, A-8**, which the owner merges). At Gate A the owner may choose to push early and open a draft PR, so CI is shaken out during P6b. Fixes that touch measured code (`backend-go/internal`, `backend-go/cmd/gate-eval`, `backend-python/src`, `backend-python/evals`, `config/`) wait until after B8. The default is one push at S3a. | lead, owner |
| D216 | Version, CHANGELOG, licence, community files, public-repo hygiene | **Amended by D234 (final round 2 of P6a):** the bump is not made at Gate A; it is made on the P6b branch with the planned tag date, at C4, or right before B8 in mode A. **Amended by D223 (2026-10-23):** history is not rewritten; the hygiene grep holds for the current tree of every pushed branch and of `main` after the release merge.<br>**Bump before B8, never at Gate A (D234)**, so the tape stamps 2.0.0 (`frontend-react/scripts/build-demo-recording.ts:65`, `recording.go:54`). `make version-bump V=2.0.0 RELEASE_DATE=<the planned tag date>` is committed as `chore(release): 2.0.0` on the P6b branch.<br>• At C4 the bump is made if B8 did not make it (mode R), or the CHANGELOG heading date is set to the planned tag date if that date moved (a one-line edit; `bump_version.py` refuses the same version), then `make check-version RELEASE_TAG=v2.0.0` runs.<br>• After the bump, local builds are tagged `:2.0.0`. That is acceptable; `docker compose pull` replaces them after the release.<br>**CHANGELOG.** One `2.0.0 "Jev gateway"` section with Highlights, Upgrading from 1.x, Added, Changed, Fixed, Security and Known limits. The phase-labelled sections go under "Build log by phase". The second Unreleased heading (`CHANGELOG.md:510`) is retitled.<br>**Licence.** `LICENSE:3` says "Tim Kushnir" and `backend-python/pyproject.toml:5` says "Tim Kushmaro". The **owner gives the legal name at S1**. `package.json` gets `"license": "MIT"`, and the authors are made consistent.<br>**Community files.** `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md` (Contributor Covenant 2.1, contact through GitHub private vulnerability reporting, not an email), `.github/ISSUE_TEMPLATE/{bug,feature}.yml` and `.github/pull_request_template.md`. `docs/SECURITY.md` "Reporting" points at private reporting.<br>**Hygiene gate** (each pattern ends in a one-letter class, so this file does not match itself). `git grep -n -E '/home/timkushma[r]\|\.cache/trustagent-or[c]h\|timandga[l]\|mail\.tau\.ac\.i[l]' -- ':!docs/archive'` prints nothing. Today 9 tracked files have local paths and 5 have the university email.<br>• Commit author emails stay as they are: they are already public on `main`, and the 1,072 commits are not rewritten. The owner is told at S1.<br>• `interview-talking-points.md` stays public or moves private (into the interview kit artifact; erratum H-7), as the owner chooses at S1. | SHIP, lead, owner |
| D217 | Owner sittings and approvals | **Amended by D221 (2026-10-23):** in the cloud session files reach the owner as messages from the session (else from the laptop), and `make clip` prints to stdout (it also reads a pipe).<br>• Four sittings (§0.2), each ending with a say-back phrase.<br>• One open card at a time (§0.6).<br>• Waiting time is used: G5 (comparison review) runs while B7's bench runs.<br>• Files reach Windows through `explorer.exe <dir>` (drag and drop); every paste goes through `make clip FILE=…`.<br>• The owner approves the visuals (OG, GIF, video, post text) at C2, **before** any push. | lead |

### Cloud and deferrals

| # | Question | Decision | Owner |
|---|---|---|---|
| D218 | Always-on cloud demo | **Status.** Optional and **not part of v2.0.0's definition of done**. The recipe is built in P6a as a queued, cut-line light stream. The deploy happens only after v2.0.0, with the owner's go.<br>**Why it is still worth having.** Pages is already an always-on recorded demo for $0. The VM adds the live dashboard (Traffic, Studio, Savings) with simulated vendors, labelled "Simulated" everywhere.<br>**Target.** Hetzner CX23 (2 vCPU, 4 GB, x86) from €5.99 a month excl. VAT including IPv4, about €72 a year [vendor price page, read 2026-10-21; re-check at deploy]. Oracle Always Free is not recommended for always-on: it reclaims idle VMs, A1 capacity is scarce, and its iptables block 80/443.<br>**Hostname.** The owner's domain, or `<ip-with-dashes>.sslip.io` (free) for Caddy and Let's Encrypt.<br>**Images are pulled, not built:** `TRUSTAGENT_VERSION=2.0.0 … pull`, then `up --no-build`. `apt-get install git make python3`.<br>**Stack.** `docker-compose.yml` + `docker-compose.demo.yml` (mock Jev and judge, vendor keys `!reset null`) + the new `docker-compose.public.yml`:<br>• `TRUTHTABLE_READONLY=true`, `TRUTHTABLE_RETENTION_DAYS=7`;<br>• host ports 80 and 443 only;<br>• `BIND_ADDR` stays 127.0.0.1;<br>• engine memory limit 1g in the overlay (the base `4g` at `docker-compose.yml:345` is the whole VM), measured at R-5 with `docker stats`.<br>**Real client IPs.** Caddy sends `/api/*`, `/ws` and `/health*` **directly to `proxy:8080`**. Otherwise nginx overwrites XFF (`nginx/templates/snippets/proxy.conf.template:16`), and every visitor shares one IP: 10 WebSockets (`hub.go:440`) and 120 requests a minute (`ratelimit.go:157`) for everyone. `TRUTHTABLE_TRUSTED_PROXIES` defaults to `172.16.0.0/12` (`docker-compose.yml:264`). `/v1*` answers 404 at the edge.<br>**For interviews:** `make demo` plus a Cloudflare quick tunnel, for $0 (`docs/WALKTHROUGH.md:903-906`; one viewer, so the shared IP does not matter). | CLOUD, owner |
| D219 | Deferrals | **v2.0.x:**<br>• the knowledge Docker run (`ci-knowledge.yml` dispatch after the merge; if it is red, the docs say "CI proof pending");<br>• the `eval-live` environment and an AggreFact run;<br>• `doctor --expect public`;<br>• the cloud deploy, if not done at C14.<br>**P7:**<br>• nginx `set_real_ip_from` behind a new registered key;<br>• the P5 §19 P7 list (multi-replica PKCE store and shared caps, the keyed verify limiter, label re-scoring, the #66 bundle, cosign, a recorded Studio session, native arm runners, `SECRET_KEY_PREVIOUS`, engine-side Jev, `JudgeConfig.base_url`). | lead |

### After planning: owner decisions of 2026-10-21 to 2026-10-23, and the P6a fix and final rounds (binding)

| # | Question | Decision | Owner |
|---|---|---|---|
| D220 | `main` is public; who pushes | **Fact.** On 2026-10-21 the owner authorized Claude to push branches and to merge into `main`, and v2 P0–P5 went public on `main` that day (merge `a582549`, then the CI fixes up to `7e901cd`), pushed directly rather than through a PR. It was the owner's decision, not a violation: history is never rewritten and nothing is force-pushed.<br>**From now on.** Claude pushes branches (`feat/v2-*`, `fix/*`) without a per-push "go push". `main` moves only through pull requests the owner merges on GitHub with "Create a merge commit" (Claude's permission checks refuse merging into `main`).<br>**Dropped.** The push guard (`pushurl DISABLED`, G1 step 7), the G8 token sitting (the laptop has held a repo-scoped token since 2026-10-21; the cloud session pushes through the Claude GitHub app) and `$ORCH/ci_logs.py` (CI status from the public API; logs through the GitHub app in the cloud or the web UI).<br>**Ruleset.** The minimal `main` ruleset (G9 step 4) is set as soon as the owner can, with the admin bypass **for pull requests only**, so a direct push to `main` is refused for everyone.<br>**Today's public `main`** (`7e901cd`):<br>• it fails the D216 hygiene grep in its tree (the university email, a gmail address, local and `$ORCH` paths);<br>• its README "Live demo" badge and "Try the demo" link (`README.md:17`, `:22`) point at a Pages site that is not on yet (404);<br>• its measured block still reads "not yet measured [measured]";<br>• **it does not disclose the AI-agent build** (D227 d): the README has no "How it was built" section, the landing has no build line, and the talking points stop at story 10; the agents appear only deep inside `docs/v2/P5-PLAN.md` and `SPEC.md`. A visitor sees a large solo project with no disclosure.<br>**int7 fixes all four** (amended in P6a final round 1): the hygiene grep, the measured block, the disclosure (the README "How it was built" section, the landing footer's build line, talking-points story 11), and the demo links. The badge and the links were left pointing at the Pages URL by design until that round; they now point at `make demo` and say the hosted demo opens with the 2.0.0 release, under a `pages-pending` comment in `README.md` and `docs/DEMO.md` that C2 removes when it points them back at the site (`make check-claims-launch` refuses the comment, rule `launch-pages-pending`).<br>**How they reach `main`:** right after Gate A the lead opens **one pull request from `feat/v2-jev-gateway` (which then holds int7) into `main`** (A-8), and the owner merges it with "Create a merge commit". It carries the hygiene fixes, the build disclosure (the README "How it was built" section, the landing footer's build line, and talking-points story 11 if the talking points stay public: the owner's H-7 choice is applied before it opens), the reworded demo links and the corrected `measured.json` descriptions (D233), with nothing Jev-derived (D222); its CI runs every check on P6a's code. It carries **no version bump** (D234): the branch holds VERSION 1.0.0 and the CHANGELOG heading `## Unreleased — 2.0.0 "Jev gateway" (in progress)`, and `main` keeps them until the release PR. It replaces D215's optional early draft PR; the release PR (C6) then brings P6b, P6c and 2.0.0. | owner, lead |
| D221 | Where P6b and P6c run | The owner continues P6b and P6c from a **Claude cloud session** (claude.ai/code).<br>**It has:** Docker with about 16 GB of RAM; branch pushes through the Claude GitHub app; the OpenRouter key as the environment variable `OPENROUTER_API_KEY` in the cloud environment's settings (never the chat, never a file); network access that must allow `openrouter.ai` (and GitHub, `raw.githubusercontent.com` for RAGTruth, the package registries and `ghcr.io`); a setup script that installs `ffmpeg` (D226).<br>**It lacks:** tag pushes (the owner creates `v2.0.0` through a GitHub release in the web UI, C10); `$ORCH` paths (no card names one); `clip.exe` and `explorer.exe` (`make clip` prints to stdout; files reach the owner as messages from the session); a browser that reaches its localhost (G2 runs on the laptop, or is skipped: G11 on Pages settles Connect anyway).<br>**Replaces D202 there.** The key lives only in the cloud environment's variables; every paid tool reads it from the environment (`make jev-check`, `jev-capture`, `bench-*`, `calib-pass`, `record-demo` already do). On the laptop no worktree `.env` may hold a key.<br>**What stays on the laptop:** G2 (optional) and the owner's own demo. Everything on GitHub's web UI (G9–G12) works from any device.<br>**Handoff:** `docs/v2/STATUS.md`, rewritten by the lead at Gate A. | owner, lead |
| D222 | What may be published before G0 (keeps D200) | D200 still binds for what matters: **no measured Jev figure, no recorded Jev tape and no captured Jev response is committed to a pushed branch** (every branch is public, D220) before G0 says mode A or the owner has written permission. Vendor and third-party figures quoted from public pages, with their labels and links, are not measurements; those already public stay unless mode R and the owner's H-R decide otherwise. The cloud session has no private place for Jev outputs (its files go when it ends), so **in mode W the paid Jev steps (B3–B8) wait**; non-Jev work goes on. | owner |
| D223 | Personal data in git history | It stays: the university email in commit metadata, a gmail address and local paths in old revisions of design notes. No history rewrite. D216's hygiene grep applies to the current tree: of every branch Claude pushes, and of `main` after the release merge. | owner |
| D224 | GHCR | The three packages have been public since 2026-10-21 (an anonymous `docker pull ghcr.io/timkush1/truthtable-proxy:edge` works). G10 no longer makes them public: it checks they still are and name "Repository: TrustAgent", and sets the Actions variable `GHCR_PUBLIC=true` if it is missing. | owner |
| D225 | Where a latency was measured (amends D205) | The measured gate latency may come from a cloud VM. Every surface names where the run really ran, from its `inputs.host_env`, never an assumed laptop: bench publish writes measured.json `sources.gate_latency{connection, gate_concurrency, platform, region, n, conditions}` from the run (and refuses a published latency without it: rule `latency_conditions`), and the latency claims' caveat `{gate_conditions}` is filled from it in the README block, on the landing (hero and Cost), in the release notes and in the post. Example: "warm connection, one request at a time, run on Linux, region cloud-us, snapshot typesafe/jev-1.13-20260917, n=199". The card sets `BENCH_REGION` to a readable place: `israel` on the laptop, `cloud-<region>` in the cloud when the session can say where it runs, else `cloud`. | owner, lead |
| D226 | ffmpeg | No sudo on the laptop: a rehearsal uses a static ffmpeg (`pip install imageio-ffmpeg` in a scratch venv; its bundled binary first on `PATH` for that one command). In the cloud the environment's setup script installs it (`apt-get install -y ffmpeg`). G1 step 5 says so. | owner |
| D227 | Honest launch copy (fix round 1) | (a) The post's example says where its answer came from (`{featured.source}`): the recorded demo's answers are scripted (written for the demo) or generated (a named model wrote them), never "my support bot"; the filled post is refused if it says "my bot". (b) The latency in the post is the benchmark median with its conditions (`{gate.conditions}`), not the time of the one catch. (c) The post names Jev's maker: "Jev, TypeSafe AI's decision model (through OpenRouter)"; mode R "a third-party decision model"; the agreement paragraph says Arize's 76% is the same model on a different sample, so a difference is not an improvement made by TrustAgent. (d) Every public text says the project was built with AI coding agents (Claude Code), in the words D230 fixed: to plans the owner approved and decisions the owner made: both post variants, a README section, the release PR body and a talking-points story. **The owner settles that wording at S1** (it is in the say-back). (e) The post's first line names TrustAgent within 150 characters, so a phone shows it before "…see more". | lead, owner |
| D228 | R-3 cannot record a mock attempt (erratum) | `record_demo.py` cannot record an attempt on mocks, by design: `trafficgen --record` refuses mock-upstream, and `export-replay` refuses to call a stack recorded when its judge is the mock or its hosts are not vendor hosts (on the e2e stack it writes no manifest at all). **What replaces the end-to-end attempt in R-3** (run 2026-10-23, `$ORCH/p6-scratch/lead/fix1/`): (1) drift from `trafficgen --expect --report-only` on the stack; (2) a real stack export with the simulated label on the zero-key demo configuration: kind mock, `featured` computed by `recording.Build` (rule scripted, the refund trace), `check_recording` exit 0, `--require-recorded` exit 1; (3) the same export told the stand-in hosts are vendor hosts is refused (fixed in this round: a simulated tape never names a vendor host for the mock fast check or judge); (4) `ACCEPT` refused for attempts that exist: one still running, one under review whose tape is simulated; (5) the ffmpeg path with a static ffmpeg (D226): `VIDEO_ALLOW_MOCK=1 make launch-video PAGES_ONLY=1` wrote both MP4s (45 s, 16:9 and 4:5) in `tmp/launch/rehearsal/`, every frame stamped "Simulated rehearsal". A full attempt and the stack beats are proven at B8 with the owner's key; `scripts/tests/test_record_demo.py` covers the attempt logic on fakes. | lead |
| D229 | Figures that cannot lose their method (fix round 2) | **(a) The held-out ECE.** The C1 card passed `CALIBRATION_HOLDOUT=` with a calib-pass path, so make ignored it and `measured.json` would have gone out without the held-out ECE, with nothing to notice. C1 now passes `HOLDOUT=$PWD/backend-python/evals/results/<run_id>/holdout-groundedness.json` (what `make calib-holdout` writes beside the run), and B7's check-only publish passes it too. `bench publish` refuses a held-out check beside the run that it would accept but that was not passed (`holdout_not_passed`; a report it would refuse, such as a calibration that is not `measured`, does not stop a publish without it, D206's own path). `check-claims --launch` refuses a measured file without the ECE before and after and `sources.groundedness_ece` (`launch-ece`), unless the tape's `calibration.quality` says it was recorded without a measured calibration.<br>**(b) What a cost was measured on.** The fast check's measured cost is its own cost on bench-lite's RAGTruth-QA answers, and a cost per answer grows with the documents' length (the README's estimate assumes about 5,000 tokens). Every judge record carries the vendor's input-token count; a benchmark run's `cost` block publishes `input_tokens_per_answer` (the mean over the examples that made a call; null under 95% counted); `measured.json` `sources.<key>_usd_per_1k` carries `{dataset, n, input_tokens_per_answer, model, conditions}`, and publish refuses a cost without its conditions (`cost_conditions`, mirrored in `validate.mjs` with a fixture). The cost claims' caveats carry `{cost_conditions}` (the same-checks ratio takes the judge's, which names the judge), filled by check-claims, the landing, the release notes and the Savings row, and never shown bare.<br>**(c) The post.** `make linkedin-post` refuses a post over 2,800 characters (LinkedIn takes 3,000; UTF-16 units) with exit 2 and how much to cut; a test fills the real template with realistic values and keeps 100 characters of headroom. The first line names the category and says the fast check's cost on a public benchmark, never "TrustAgent checks each answer for $X"; the benchmark sentence says `{cost.conditions}` and that cost grows with document length; the ratio names `{same_checks.judge}` from the run; "risky answers plus a small sample" get the LLM audit (the default sampling). | lead |
| D230 | The build disclosure matches the record (amends D227 d) | The README said the developer "approved each merge", story 11 that pushing "stayed with the owner, step by step" and that the scanners were "clean at every merge"; D220 and the merge history say otherwise (P0–P5 reached `main` by a direct push; agents merged the integration branches; Trivy was red on `main` until the dependency fix). Every public text now says: built to plans the developer approved and decisions the developer made (agents drafted the plans, wrote the code and the tests, ran the reviews); the developer authorized the pushes and every paid run; agents merged after their gates and the review agents passed; `main` now changes only through pull requests the developer merges; the **gating** scanners (gosec, bandit, npm audit, gitleaks) were clean at every merge. Trivy going red after the push is a "what went wrong" item in story 11. The post's default AI line is "Designed and directed by me; built with AI coding agents (Claude Code) to plans I approved and decisions I made." The owner may still give their own words at S1. | lead, owner |
| D231 | An unread agreement is not "nothing found" (amends D200) | The Master Customer Agreement is the document whose reported §2.3(f) started G0, and D200 already records that the public Terms, the AUP and OpenRouter's Terms have no such clause. So a search that cannot open the agreement would come back "nothing found" and land on mode A, and in mode A Jev figures, the tape and the captures go to public branches before TypeSafe sees anything. **Rule:** if the owner could not find or open the Master Customer Agreement, or cannot tell which terms OpenRouter's Jev page binds its users to, that counts as unsure: **mode W**. The permission request then asks whether the agreement applies to OpenRouter users and whether the figures and the recorded demo may be published; a written yes is permission (mode A). G0 step 4 and `docs/launch/terms-check.md` say so; the drafts outside the repo were updated. | lead, owner |
| D232 | Paid-target exceptions, written down (amends D204, §0.6) | **No `CONFIRM=1`:** `make jev-check` (one probe call billed about $0.00001, the check an owner runs first) and `doctor --probe` (free unless `--probe` asks for the same single probe). Neither prints a `TRUSTAGENT_P6_RESULT` line: the amount is below the ledger's precision, so B3 sends back the snapshot and limit lines, and `make p6-ledger` does not count it. **Gated by an attempt:** `make launch-video-stack` runs only on an attempt under review (which exists only after `record-demo CONFIRM=1`), and its calls count against that attempt's record caps; its help now names its cost, so `scripts/tests/test_paid_targets.py` scans it (`ON_A_CONFIRMED_ATTEMPT`). **Also:** `make calibrate` writes only with `WRITE=1` (`WRITE=0` no longer writes), and a bench plan or run with the gate warns when `BENCH_REGION` is left at `local`, before anything is spent (D225: a published latency would say "region local"). | lead |
| D233 | The agreement comparison is not like for like (amends D212 and H-9; fix round 3) | **Finding.** The post, the release notes and the generated README block said our RAGTruth-QA accuracy was "at the same threshold" and "like for like (default thresholds)" with Arize's 76% at 0.5, and the post added "same model ... any difference is not TrustAgent's doing". The bench runs gate-eval with `groundedness@1,contradiction@1` at spec thresholds and flags an answer when either reaches its warn_at: groundedness **0.8** (its `threshold_basis` started from Arize's tuned 0.80 on RAGTruth) or contradiction **0.7**, on questions worded differently from Arize's single one. So neither Arize figure is like for like, Arize's snapshot is not stated ("same model" cannot be backed), and a cutoff taken from a third party's tuning on the same public dataset may favour our figure: a selection effect the reader is told about.<br>**Rule.**<br>• Every surface names our thresholds and where the groundedness one came from: the caveats of `ragtruth_qa_agreement_measured` and `ragtruth_qa_accuracy_measured` (README block, site, release notes) and the post's paragraph (read from `contracts/evaluators`, never typed).<br>• Ours stands beside **both** of Arize's figures, 76% at 0.5 and 87% at a tuned 0.80 [third-party], one question worded differently, with the words "not like for like". `check-claims` `[agreement-alone]` needs both in the section; the release notes refuse without them.<br>• `contracts/validate.mjs` checks that each agreement caveat names `groundedness <warn_at>` and `contradiction <warn_at>` as the evaluator files hold them; a Python test ties them to `evals/judges.py` `DEFAULT_EVALUATORS`.<br>• `measured.json` descriptions of the two values: bench publish writes them (`evals/report.py` `VALUE_DESCRIPTIONS`), so the Step 0 template's "like-for-like" description would have been replaced only at C1; **final round 1 of P6a** wrote the same two descriptions into the committed template (descriptions only, no value), because the pull request after Gate A (A-8) makes the file public on `main`, and `tests/unit/test_eval_report.py` keeps the template, the bench fixtures and `VALUE_DESCRIPTIONS` equal. The claim caveats say "may favour TrustAgent's figure", not "may favour it".<br>• The claim text says "balanced accuracy", not "agreement"; a measured multiple is said one way on every surface, whole from 10 up (27×, not 26.84×).<br>**Not done:** a groundedness-only accuracy at 0.5, computable offline from the run's per-example scores ($0). It would be a new measured key across the contracts in the last P6a round; the lead may add it at C2 as the closest comparison, labelled, and still not like for like. | lead |
| D234 | The 2.0.0 bump is not made at Gate A (amends D216 and H-8; final round 2 of P6a) | **Finding.** A-3 ran `make version-bump V=2.0.0` at Gate A. `scripts/bump_version.py` sets VERSION and every mirror to 2.0.0 and renames the CHANGELOG heading `## Unreleased — 2.0.0 "Jev gateway" (in progress)` to `## v2.0.0 — <the day of the bump> "Jev gateway"`. A-8 opens its pull request from `feat/v2-jev-gateway`, which would then hold that commit, so once the owner merged it public `main` (VERSION 1.0.0 today) would show a dated 2.0.0 release with no tag, no GitHub Release and no hosted demo, its `edge` images reporting 2.0.0, for weeks in mode W, while the README says the hosted demo "opens with the 2.0.0 release".<br>**Rule.**<br>• **No bump at Gate A.** After Gate A, `feat/v2-jev-gateway` and the A-8 pull request from it hold VERSION 1.0.0 and `## Unreleased — 2.0.0 "Jev gateway" (in progress)`; `main` keeps them until the release PR (C6). A-3 checks this instead of bumping.<br>• **The bump belongs to P6c (C4)**, on the cloud session's P6b branch: `make version-bump V=2.0.0 RELEASE_DATE=<the planned tag date>` (the make target passes `--date`), committed as `chore(release): 2.0.0`, so the heading carries the planned tag date rather than the day of the bump.<br>• **Mode A moves it forward to just before B8**, on the same branch, because the recorded tape stamps the version it was recorded with (`export-replay` builds it in from `version.go`): `record-demo` refuses VERSION ≠ 2.0.0 (D208), and `check_recording --require-recorded` (C1, and `pages.yml` on the tag) compares the tape's `trustagent_version` with VERSION; the tape is never edited (D209). C4 then only checks it, and re-dates the heading if the planned tag date moved (a one-line edit).<br>• **The A-8 pull request follows its branch.** The P6b branch reaches `feat/v2-jev-gateway` only at C5; if A-8 is still open then, the owner merges or closes it first, or C5's push would add P6b and 2.0.0 to it.<br>So 2.0.0 first reaches `feat/v2-jev-gateway` at C5 and `main` only through the release PR. | lead |

---

## 2. Step 0 (lead, about 1 d)

### 2.1 Branches, numbering, re-baseline

1. **Branches.**
   - `git -C ~/projects/TrustAgent worktree add ~/projects/TrustAgent-int7 -b feat/v2-int7 feat/v2-jev-gateway`.
   - After the contract PR, create `~/projects/TrustAgent-w7-{meas,vend,ship,cloud}` on `feat/v2-w7-<id>` from int7.
   - Each worktree's `.env` comes from `python3 scripts/configure.py --ensure`.
2. **Decision numbers.** A script lists every `D<n>` defined in the P3, P4 and P5 plans, SPEC Appendices F, G and H, and the int6 merge-commit bodies (for example `4828d4a`: D142–D144).
   - It asserts that no number is defined twice and that every P5 number is below D200.
   - If P5 I-10 did not copy its integration decisions into P5-PLAN, this plan's §1 cites them by commit.
3. **P5 slips.** P5 has no cut line, but slips happen. Each item is re-checked on the merged head:

| P5 item | If it landed | If it slipped or is partial |
|---|---|---|
| GO-10 load fold in `derive_measured` | MEAS only adds keys | MEAS M-8 implements it |
| DR-4 `release-images.yml` | SHIP S-7 checks it | **Blocks P6c.** The lead writes it per P5 D125 before Gate A. |
| DR-6 `record-demo` (overlay, `scripts/record_demo.py`, `mk/release.mk`) | VEND finishes it (D208) | VEND V-0 builds it per D123, then D208 |
| D115 capture (`npm run og`, `scripts/make_hero_gif.py`, `e2e/launch/**`) | VEND V-6 switches it to `featured` | V-6 builds the minimum: OG and the hero GIF |
| LD-3 compare cells filled from fetched sources | SHIP S-5 re-verifies them | S-5 fills cells from fetched sources only, or drops them |
| P5 I-8 published load run (`docs/benchmarks/load/*.json`) | Folded by `bench-publish` | The lead runs `make load-test-gate LOAD_PUBLISH=1` before Gate A |

**Step 0 result (2026-10-10, on the merged head `b39e61c`):**

| P5 item | State | What P6 does |
|---|---|---|
| GO-10 load fold | **Landed.** `evals/report.py` `derive_measured(..., load_runs=...)` takes `proxy_overhead_ms_p50/p95` from the newest clean load run (`load_load_runs`, `is_clean_load_run`). | MEAS only adds keys (M-8 drops its GO-10 part). |
| DR-4 `release-images.yml` | **Landed** (`.github/workflows/release-images.yml`). | SHIP S-7 checks it. P6c is not blocked. |
| DR-6 `record-demo` | **Landed as D123**: `docker-compose.record.yml` (68 lines, project `truthtable-record`), `scripts/record_demo.py` (657 lines: dry run unless `CONFIRM=1`, preflight, traffic rounds, export, check, swap-in), `make record-demo` and `make record-check-config`. The D208–D209 parts (generated-answer upstream, ledger across attempts, `ACCEPT`/`DISCARD`, drift review, `featured`) are not there. | VEND skips V-0 (the +2 d is not needed) and finishes it per D208. |
| D115 capture | **Landed**: `npm run og`, `e2e/launch/{capture.ts,hero.spec.ts,og.spec.ts,screenshots.spec.ts}`, `scripts/make_hero_gif.py`, `make og`, `make hero-gif`, `make screenshots`. | VEND V-6 switches them to `featured`. |
| LD-3 compare cells | **Landed, unreviewed**: all 20 cells carry a `source_url`; all 20 are `reviewed:false` (P5C-28). P5C-16 (vendor names in cell text versus D169) and P5C-17 (a stale fetch-log row) are open. | SHIP S-5 re-verifies every cell and fixes P5C-16/17; the owner reviews at G5. |
| I-8 published load run | **Landed**: `docs/benchmarks/load/20261010T131516Z-f60a8a4.json`. Not folded yet (`measured.json` is still `not_measured`). P5C-21: it predates the fix-round gateway changes and ran beside the owner's demo. | `bench-publish` folds it. Because P6a never writes `docs/benchmarks/**`, the quiet re-run is a P6b step: at the release commit, while the owner's demo is down (B3–B8) and before `bench-publish`, the lead runs `make load-test-gate LOAD_PUBLISH=1` ($0, mock vendors) with nothing else running. `load-run.v1` has no field for the image revision or concurrent load (P5C-21); the lead's contract PR adds them (§2.3 g). |

### 2.2 Free checks (no key)

1. **Repository state.**

   ```bash
   curl -s -o /dev/null -w '%{http_code}\n' https://api.github.com/repos/timkush1/TrustAgent   # 200 = public
   git -C ~/projects/TrustAgent ls-remote --heads --tags origin                                  # expect no v* tags
   git -C ~/projects/TrustAgent fetch origin
   git -C ~/projects/TrustAgent rev-list --count feat/v2-jev-gateway..origin/main              # must be 0
   curl -s -o /dev/null -w '%{http_code}\n' https://timkush1.github.io/                         # 404 = no user site
   ```

   If `origin/main` has moved (the local refs date from 2026-06-12), merge it into int7 first.
2. **RAGTruth revision.** The data comes from GitHub raw, not Hugging Face (`evals/datasets/ragtruth.py:38-40`). Run `git ls-remote https://github.com/ParticleMedia/RAGTruth refs/heads/main` and pin the SHA as the default of `RAGTRUTH_REVISION`.
3. **Calibration sample.** Download the data (free) and count `label_hallucinated` and conflict-span positives in the QA **train** split. Set `CALIB_N` per D206 and check that train and test share no `source_id`. Record the counts in the Step 0 commit. If the loader lacks `--split train`, M-6 adds it.
4. **Licences.**
   - RAGTruth's repository licence (`ragtruth.py:43`, marked "[A9] check at P6").
   - The MS MARCO terms for the QA passages (research-only, so `per_example` keeps ids only).
   - AggreFact's sources (`aggrefact.py:23`).

   The text goes into the `terms-check.md` template.
5. **Prices, ids and the privacy toggle.** Read `GET https://openrouter.ai/api/v1/models` (public) to confirm:
   - the `anthropic/claude-haiku-4.5` price against `contracts/pricing/prices.v1.json` (Appendix D "$1/$5");
   - that `openai/gpt-4o-mini` and `typesafe/jev-1.13` exist.

   Then read the model page `https://openrouter.ai/typesafe/jev-1.13` and write the **exact provider name and privacy-toggle wording** into G1. JEV-ACCESS `:178` names no provider. A price difference becomes a dated pricing contract edit.
6. **G0 links.** Check that `https://openrouter.ai/typesafe/jev-1.13`, `https://typesafe.ai/legal/terms`, `https://typesafe.ai/legal/acceptable-use-policy` and `https://openrouter.ai/terms` return pages.
7. **Gitignore.** `/tmp/` is already ignored (int6 `ac87b86`). Add `calibration/` to `backend-python/evals/.gitignore`. A test (`scripts/tests/test_gitignore_p6.py`) runs `git check-ignore` on every P6 scratch path:
   - `tmp/record-demo/ledger.jsonl`
   - `tmp/launch/video-4x5.mp4`
   - `backend-python/evals/calibration/x/gate.jsonl`
   - `backend-python/evals/results/x/run.json`

**Step 0 results (2026-10-10, free reads only, no key):**

| Check | Result |
|---|---|
| Repository | `api.github.com/repos/timkush1/TrustAgent` 200 (public). `ls-remote`: `main`, a `copilot/*` branch and dependabot branches; **no `v*` tags**. After `git fetch origin`, `feat/v2-jev-gateway..origin/main` counts **0** (`origin/main` is still `7ddc270`), so nothing is merged into int7. `https://timkush1.github.io/` answers 404 (no user site). |
| RAGTruth | `refs/heads/main` = `c103204b9ce28d6bbad859304bf30de72b8ed8fe`, now the `RAGTRUTH_REVISION` default (`mk/py.mk`). Licence MIT (GitHub licence API). `response.jsonl` sha256 `e4c2e4ac…18073`, `source_info.jsonl` `0dffc26e…d578b`. |
| Calibration sample (D206) | The loader's own conversion (`convert`, good quality only): QA **train** 4,892 answers, 1,546 hallucinated, 3,346 clean, **342 with a conflict span**, 1,204 baseless-only, 839 sources. QA **test** 875 answers, 160 hallucinated, 26 conflict, 150 sources. **Train and test share no `source_id`** (0 of 839 and 150). A natural sample of 800 expects 55.9 conflicts (sd 6.6, so about 42.7 at −2 sd), 253 hallucinated and 547 clean. **`CALIB_N ?= 800`** (`mk/py.mk`). Mean example size 515 tokens (p95 772), so about $0.03 at Jev's price, inside the $0.30 cap. `--split train` exists. M-6 must add `source_id` to the prepared examples: `meta` has only `model`, `spans` and `task_type` today. |
| Licences | RAGTruth: MIT. MS MARCO (the QA passages), from microsoft.github.io/msmarco: "The MS MARCO datasets are intended for non-commercial research purposes only to promote advancement in the field of artificial intelligence and related areas, and is made available free of charge without extending any license or other intellectual property rights." So `per_example` keeps ids, labels and scores only. LLM-AggreFact (`lytang/LLM-AggreFact`): CC-BY-ND-4.0, gated (auto), revision `981dfd0`; its sub-datasets keep their own licences. All of this goes into the `terms-check.md` template (S-4). |
| Prices and ids | `/api/v1/models`: `anthropic/claude-haiku-4.5` $1 / $5 per M tokens, the same as `prices.v1.json`, so no pricing edit. `openai/gpt-4o-mini` exists ($0.15 / $0.60). **`typesafe/jev-1.13` is missing from the default list**, which returns only text-output models. It is listed with `?output_modalities=all` or `?output_modalities=decisions`, next to `~typesafe/jev-latest`, at $0.042 per M input and $0 output (matches). Its endpoint: provider "TypeSafe", 64k context, 32k max prompt, snapshot `jev-1.13-20260917`. **Any tool that checks the id against `/api/v1/models` must pass `output_modalities=all`.** A new `typesafe/jev-router` (a chat-model router with price −1) is not ours. |
| G0 links | `openrouter.ai/typesafe/jev-1.13`, `typesafe.ai/legal/terms`, `typesafe.ai/legal/acceptable-use-policy` and `openrouter.ai/terms` all answer 200. The model page's data policy for TypeSafe: `training:false`, `retainsPrompts:false`, `canPublish:false` (OpenRouter's flag for publishing prompts; it says nothing about benchmarks). |
| Gitignore | `backend-python/evals/.gitignore` adds `calibration/`; `scripts/tests/test_gitignore_p6.py` checks the scratch paths are ignored and the published ones are not. |

### 2.3 Contract PR (additive; `make contracts-check contracts-test gen-types`)

- **a. `benchmark-run.v1`:**
  - `vendor{decision_host, judge_host}`;
  - `inputs{dataset_revision, policy_source, gate_eval_sha256, host_env{mem_available_mb, loadavg, platform, region}}`;
  - `quality{n, scored, failed, excluded, failure_rate}`;
  - `dirty`;
  - `latency{connection: warm|cold, gate_concurrency}`.
- **b. `measured.v1`:**
  - `$defs/vendor_host`;
  - `values.llm_full_audit_usd_per_1k` and `values.ragtruth_qa_accuracy`;
  - `intervals.<key>{lo, hi, method}`;
  - `sources.<key>.cost_source` (`reported|computed`; `unknown` is refused);
  - `sources.groundedness_ece{method: holdout, calibration_file, calibration_sha256, dataset}`;
  - `sources.attempts{bench_runs, refused_run_ids}`;
  - `sources.snapshot_split`;
  - `snapshot`.

  `mock: const false` stays.
- **c. `recording.v1`:**
  - `decision_model.host`, `judge.host`, `answer_sources.generated.host` (bare hostnames);
  - `featured{trace_id, rule}`;
  - `calibration{snapshot, quality: measured|provisional|none}`.
- **d. `value-claims.v1`:**
  - `measured_pair` (optional);
  - the new claims `llm_full_audit_cost_per_1k_measured`, `groundedness_ece_after_measured` (caveat: "held out on RAGTruth-QA test; fitted on train") and `ragtruth_qa_accuracy_measured`;
  - the contamination caveat on `ragtruth_qa_agreement_measured`.
- **e. `contracts/validate.mjs`:**
  - every `measured_pair` resolves;
  - `reverify` is an allowed field;
  - mock-run fixtures for the D203 and D204 refusals.
- **f. `contracts/README.md`.**

### 2.4 Seams and ownership

- **Make stubs.** Each prints `not implemented (P6 <task>)` and exits 2:
  - `mk/py.mk`: `BENCH_ARGS ?=`, `BENCH_RESUME_KEY ?= lite-p6`, the pinned `RAGTRUTH_REVISION`, the `CONFIRM` gate, `bench-smoke`, `calib-pass`, `calib-holdout`, `p6-ledger`;
  - `mk/go.mk`: `jev-capture`;
  - `mk/release.mk`: `release-notes`, the `record-demo` additions (`ACCEPT`, `DISCARD`, `VIDEO`);
  - `mk/launch.mk`: `linkedin-post`, `clip`, `launch-video`, `launch-video-stack`;
  - `mk/dx.mk`: `public-demo-{up,down,setup-url}`.
- **Ownership:**

  | Stream | Owns |
  |---|---|
  | MEAS | `backend-python/evals/**`, `cmd/gate-eval/**`, `cmd/calibrate/**`, `cmd/proxy/roi_api.go` (measured lines only) |
  | VEND | `cmd/jevcheck/**`, `cmd/trafficgen/**`, `internal/recording/**`, `scripts/record_demo.py`, `docker-compose.record.yml`, `scripts/check_recording.py`, `e2e/launch/**`, the `/og` route files, `scripts/make_hero_gif.py`, `scripts/launch_video.py` |
  | SHIP | `scripts/{release_notes.py,check-claims.mjs,linkedin_post.py}`, `CHANGELOG.md`, `LICENSE`, `package.json` (licence and author only), `docs/launch/**`, the community files, `.github/workflows/eval.yml`, `.github/workflows/pages.yml` (S-10 only), `docs/SECURITY.md` "Reporting", the hygiene edits in `docs/v2/**` and `docs/design*` |
  | CLOUD | `docker-compose.public.yml`, `deploy/public-demo/**`, `docs/DEPLOY-DEMO.md`, `docs/{DEMO,WALKTHROUGH}.md` (the named lines) |

### 2.5 SPEC Appendix H (P6 errata), opened at Step 0

| # | Erratum |
|---|---|
| H-1 | §4.2 P6 step 6 (terms) becomes step 0 (D200). |
| H-2 | The P6 gate path `frontend-react/public/demo/recorded` (`SPEC.md:2479`) becomes `frontend-react/demo-data/recorded` (P5 D110), unless Appendix G already says so. |
| H-3 | P6 step 2 "`make calibrate WRITE=1`" is replaced by the D206 sequence (fit on train, held-out ECE). |
| H-4 | §8.2 item 3 `e2e/hero.spec.ts` becomes `e2e/launch/hero.spec.ts`, unless Appendix G already says so. |
| H-5 | The owner's credit is $10 and the key limit $8 (D201). The bench-lite cap stays $5. |
| H-6 | The §8.2 demo video comes from `make launch-video` (D210). It is the post's own media. Its fallback is a 45-second Pages-only take. |
| H-7 | (only if the owner chooses) `interview-talking-points.md` leaves the public repo (D216). |
| H-8 | P6 step 7 order: the bump happens before the recording (B8) on the P6b branch, never at Gate A, with the planned tag date; without a recording (mode R) at C4 (D216, D234). |
| H-9 | The measured agreement is compared with the default-threshold third-party figure (0.76), not the tuned 0.87 (D212). |
| H-R | Reserved for mode R (D200). |

Appendix D rows get dated results during P6b and P6c.

### 2.6 Step-0 gate

```bash
L="flock /tmp/trustagent-heavy.lock"
$L make check && make contracts-check && make contracts-test && make gen-types-check && make config-docs-check
node scripts/check-forbidden.mjs
python3 -m unittest scripts/tests/test_gitignore_p6.py -v
```

---

## 3. MEAS: bench, calibration, publish

- **Branch:** `feat/v2-w7-meas`.
- **Size:** M–L, about 4.5 d.
- **Read first:**
  - D201 and D203–D207, D212;
  - SPEC §2.13 and §4.2 P6;
  - `evals/{bench,judges,report,budget,runner}.py`, `evals/datasets/ragtruth.py`;
  - `cmd/{gate-eval,calibrate}/`;
  - `decision/calibrate_file.go:25-29`, `policy/readiness.go:180`.

### Tasks

1. **M-1 (S–M): make seams and the dry-run default** (D204).
   - `CONFIRM=1` gate on `bench-lite`, `bench-smoke`, `calib-pass` and `eval-live`.
   - `BENCH_ARGS` is appended last in `BENCH_RUN`.
   - `bench-publish` without `RUN=` prints usage and exits 2.
   - `bench-smoke`: `BENCH_N=20`, full-audit 3, `--max-cost-usd 0.30`, resume key `smoke-p6`, `--out-dir evals/results/smoke`.
   - `--policy builtin`, with `TRUTHTABLE_POLICY_FILE` removed from the subprocess environment.
   - `gate_eval_sha256` recorded; a resume with another sha is refused.
2. **M-2 (M): stop and resume** (D204).
   - `--stop-after-failures` (default 5) per system.
   - Examples it never reached stay uncached.
   - The failure kind and its fix are printed; exit 3.
3. **M-3 (M): vendor host** (D203).
   - `endpoint_host` comes from either base-URL key.
   - Judge records get the provider host.
   - `report.py` labels the run `non_vendor_host`.
   - `run.json` gets the `vendor` and `inputs` blocks.
4. **M-4 (S–M): `--warm`, `--gate-concurrency 1`, `host_env`** (D205).
   - `latency_ms` leaves out the warm-up.
5. **M-5 (M): publish thresholds** (D204).
   - Each refusal has its own message, and all of them are listed together.
   - The dirty flag is taken at run start.
   - No dataset text in `per_example`.
   - The `cost_source` rule.
   - `sources.attempts`.
   - `--check-only` writes nothing outside scratch.
6. **M-6 (M): calibration set** (D206).
   - `python -m evals.calibset prepare --split train --n $(CALIB_N) --revision … --exclude-sources <bench sample> --out …`.
   - `make calib-pass` runs prepare, then one warm gate-eval process (`--max-cost-usd $(CALIB_MAX_COST_USD)`, default 0.30) into `backend-python/evals/calibration/<run_id>/gate.jsonl`. It prints the result line with positives per evaluator.
7. **M-7 (M): held out** (D206).
   - The bench keeps `risk_raw{groundedness@1, contradiction@1}` and the snapshot per gate record.
   - `python -m evals.bench export-labels`.
   - `cmd/calibrate --holdout FILE --calibration FILE` reuses the decision apply code without changing it.
   - `make calib-holdout RUN=…`.
   - `bench publish --calibration-holdout …`.
8. **M-8 (S–M): measured fold.**
   - `llm_full_audit_usd_per_1k`.
   - `ragtruth_qa_accuracy` and the intervals (Wilson; seeded bootstrap).
   - GO-10 if it slipped (`proxy_overhead_ms_p50/p95` from the newest clean load run).
   - `roi_api.go` reads the full-audit value.
9. **M-9 (S): key helper `evals/vendor_key.py`.**
   - Reads the free `GET /api/v1/key`: usage, limit and `limit_remaining`.
   - Never prints the key or its label.
   - Bench and `calib-pass` lower their caps per D201.
   - `record_demo.py` (VEND) imports it.
10. **M-10 (S, cut line): `make p6-ledger`.** A read-only sum of the bench ledgers, the `calib-pass` costs, the record ledger and the capture logs, reconciled against the `/key` usage and the D201 caps.

### Tests

- **`test_eval_bench_cli.py`** (with the `bench_fakes.py` fake gate-eval):
  - a dry run without `CONFIRM` spends nothing and prints the plan;
  - the stop rule, then a resume after a 402 with `--retry-failed`, and the ledger counts both sessions;
  - `BENCH_ARGS`;
  - `RUN=` required;
  - `--policy builtin`;
  - a sha mismatch is refused;
  - warm/cold and concurrency are carried through;
  - the cap is lowered from a fake `/key`.
- **`test_eval_report.py`:**
  - each refusal (failure rate, scored, mixed snapshot, dirty, host, text, `cost_source`);
  - the measured path on vendor hosts;
  - the holdout fold;
  - `llm_full_audit`;
  - accuracy and intervals (fixed vectors);
  - `attempts`.
- **`test_calibset.py`:**
  - the label mapping (conflict → 1, baseless-only → 0, clean → 0);
  - the output validates as gate-eval input;
  - source overlap is refused.
- **Go:**
  - gate-eval `endpoint_host` (both keys), `--warm` (an httptest TLS server counts **1** connection), `--gate-concurrency`, `--policy builtin`;
  - calibrate `--holdout` and the evaluator merge;
  - `roi_api` measured full audit.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
(cd backend-python && $L env -u OPENROUTER_API_KEY -u ANTHROPIC_API_KEY TRUTHTABLE_ENV_FILE= poetry run pytest \
   tests/unit/test_eval_bench_cli.py tests/unit/test_eval_report.py tests/unit/test_eval_judges.py \
   tests/unit/test_eval_runner.py tests/unit/test_eval_loaders.py tests/unit/test_calibset.py -q)
$L go -C backend-go test -p 1 -v ./cmd/gate-eval/... ./cmd/calibrate/... ./cmd/proxy -run 'GateEval|Calibrate|ROI' -count=1 2>&1 | tee /tmp/w7-meas.out
! grep -q -- '--- SKIP' /tmp/w7-meas.out
env -u OPENROUTER_API_KEY make bench-lite | grep -q 'CONFIRM=1'        # dry run by default, spends nothing
make bench-publish 2>&1 | grep -q 'RUN=' && make contracts-check
```

---

## 4. VEND: vendor captures, the recording and the video

- **Branch:** `feat/v2-w7-vend`.
- **Size:** M–L, about 4.5 d (+2 d if V-0 is needed).
- **Read first:**
  - D203 and D207–D211;
  - P5 D115 and D121–D123;
  - `cmd/jevcheck/main.go`, `cmd/trafficgen/record.go`;
  - `internal/recording/{recording,sanitize}.go`, `internal/proxy/testfallback.go:12-38`;
  - `scripts/check_recording.py`, `config/policies.yaml:108-136`;
  - dxdocs `docs/launch/demo-script.md`.

### Tasks

0. **V-0 (only if DR-6 slipped):** the D123 overlay, `scripts/record_demo.py` and the `mk/release.mk` target, as P5 specified.
1. **V-1 (M): `jevcheck --capture` and the label mask** (D211).
   - `make jev-capture [CONFIRM=1] [CAPTURE_DIR=contracts/fixtures/vendor/captured]`.
   - gitleaks runs over the output.
2. **V-2 (S–M): upstream for generated answers** (D208).
   - The overlay gets the upstream URL and key.
   - `trafficgen --model-prefix` with `max_tokens`.
   - httptest asserts the model id and bearer header, and zero upstream calls for scripted scenarios.
   - No host ports in the overlay.
3. **V-3 (M): ledger across attempts** (D208).
   - The `/key` usage delta is the source of truth.
   - `RECORD_MAX_TOTAL_USD` (default 4) and `RECORD_PASSES` (default 2).
   - Caps come from what is left; it refuses below $0.50.
   - The budget-skip refusal.
   - The ledger survives `down -v`.
4. **V-4 (M): preflight, drift, featured, accept** (D207–D209).
   - Snapshot, calibration and `VERSION` checks.
   - `ALLOW_UNCALIBRATED` → the overlay policy plus `calibration.quality:"none"`.
   - `--expect --report-only` → `drift.json` and a table.
   - The featured rule (exit 4).
   - Export to `tmp/record-demo/<attempt>/`.
   - The stack stays up until `ACCEPT=1 ATTEMPT=<id>` or `DISCARD=1`.
   - `TRUSTAGENT_VERSION=p6rec`.
   - The result line.
5. **V-5 (S): host stamp in the recording** (D203).
   - Bare hostnames; `recording.go` refuses `recorded` without vendor hosts.
   - `check_recording.py` validates the hosts and `trustagent_version == VERSION`.
6. **V-6 (S–M): consumers of `featured`.**
   - `/og`, `e2e/launch/hero.spec.ts` and the screenshots spec read `manifest.featured`. The scripted id is the fallback only for the simulated tape.
   - Missing D115 pieces get the minimum:
     - `npm run og` writes `frontend-react/public/og.png` and `docs/launch/og.png` (1280×640, < 1 MB);
     - `make hero-gif` writes `docs/images/v2/hero.gif` (≤ 8 MB, 1200 px, 12 fps) and `tmp/launch/hero.mp4`.
   - Both refuse mock unless `*_ALLOW_MOCK=1`.
7. **V-7 (M, cut line → H-6): launch video** (D210).
   - `scripts/launch_video.py`: the beat specs, `.srt` generation from the demo-script table, ffmpeg concatenation and burned-in captions, the 4:5 and 16:9 outputs to `tmp/launch/`.
   - `make launch-video-stack ATTEMPT=<id>` is the stack-beats recorder, called by `ACCEPT` with `VIDEO=1`.
   - `make launch-video` is the static beats plus assembly.
   - Both refuse unless the manifest is `recorded`.

### Tests

- **`cmd/jevcheck`** (httptest only):
  - the dry-run plan;
  - the budget refusal;
  - the sanitizer (no `sk-or-`, no `gen-` id, amounts replaced, label masked);
  - the 401 capture;
  - captured shapes decode;
  - the normal output never contains the label.
- **`cmd/trafficgen`:** the prefix and `max_tokens` apply to generated scenarios only.
- **`internal/recording`:**
  - a mock-upstream host → not recorded;
  - the vendor-host fixture → recorded;
  - a sanitized manifest keeps its hosts;
  - `featured` and `calibration` are written.
- **`scripts/tests/test_record_demo.py`:**
  - cap arithmetic across attempts, including the key's `limit_remaining`;
  - the dry-run plan;
  - `ACCEPT` without a reviewed attempt is refused;
  - a budget-skipped attempt is refused;
  - an uncalibrated snapshot is refused;
  - `ALLOW_UNCALIBRATED` writes the overlay;
  - a version mismatch is refused.
- **`test_check_recording.py`:** the host and version rules.
- **`scripts/tests/test_launch_video.py`:**
  - the `.srt` timings from the table;
  - refusal for a non-recorded manifest;
  - ffmpeg arguments for both sizes (with a canned ffmpeg).

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
$L go -C backend-go test -p 1 -v ./cmd/jevcheck/... ./cmd/trafficgen/... ./internal/recording/... ./cmd/export-replay/... -count=1 2>&1 | tee /tmp/w7-vend.out
! grep -q -- '--- SKIP' /tmp/w7-vend.out
python3 -m unittest scripts/tests/test_record_demo.py scripts/tests/test_check_recording.py scripts/tests/test_launch_video.py -v
python3 scripts/check_recording.py frontend-react/demo-data/simulated
docker compose -p truthtable-record -f docker-compose.yml -f docker-compose.record.yml config -q
env -u OPENROUTER_API_KEY make jev-capture      # dry run: prints the plan, spends nothing
env -u OPENROUTER_API_KEY make record-demo      # dry run: prints the plan, spends nothing
```

---

## 5. SHIP: release tooling, launch kit, hygiene

- **Branch:** `feat/v2-w7-ship`.
- **Size:** M, about 3.5 d. Light: the only heavy command is one `npm ci`, under the lock.
- **Read first:**
  - D200 and D212–D216;
  - SPEC §8.2 and §8.3, Appendix D;
  - `dxdocs docs/launch/*`, `scripts/check-claims.mjs`;
  - `scripts/{bump_version,check_version}.py`, `.gitleaks.toml`.

### Tasks

1. **S-1 (S): `scripts/release_notes.py` and `make release-notes TAG=v2.0.0`.**
   - It builds the CHANGELOG section, the per-image digests (`docker buildx imagetools inspect`, anonymous), the verify commands and the labels of the measured figures.
   - It refuses when a digest is missing.
   - Tests use canned inspect output.
2. **S-2 (M): CHANGELOG consolidation** (D216).
   - `bump_version.py --dry-run 2.0.0` and `check_version.py` still pass.
   - A test shows that the bump never touches `demo-data/recorded/**`.
3. **S-3 (S): licence and metadata.**
   - `package.json` `license`; authors made consistent.
   - The LICENSE holder is `{{OWNER_LEGAL_NAME}}` until S1 answers.
4. **S-4 (S–M): launch kit ready to fill.**
   - `linkedin-post.md` is rewritten per D213: the opening lines, the stack line, the optional roles line, the agreement rule, "no sign-up" removed, the video as media (`:85` "first comment" removed), and a mode-R variant.
   - `demo-script.md` uses the featured trace by rule.
   - `github-metadata.md`: description, the 16 topics, website.
   - New files:
     - `docs/launch/release-pr.md`: the PR body (summary, phases, how to verify, breaking changes, checks);
     - `docs/launch/terms-check.md` (template);
     - `docs/launch/ruleset.md`: the exact required check names, one per line;
     - `docs/launch/launch-checklist.md`, reordered by sitting.
   - The courtesy and permission email drafts go to `$ORCH/p6/` (not the repo).
5. **S-5 (S): re-verification** (P5 D113, D212).
   - Re-fetch every compare `source_url`; update `as_of` and `compare-fetch.tsv`; drop cells without a source.
   - Re-check every claim that has `reverify`.
   - Re-read each §8.3 [T] market signal in `interview-talking-points.md`, then date it or delete it.
   - Print the G5 table: each cell with the **quoted supporting sentence** from its source.
6. **S-6 (S): hygiene** (D216).
   - Replace local paths and emails in tracked docs (not `docs/archive`).
   - The community files.
   - Full-history gitleaks: `make gitleaks-install`, then the pinned binary as `git --config .gitleaks.toml --redact .`.
7. **S-7 (S): workflow readiness.**
   - `make lint-workflows`.
   - `test_workflows.py`: job names unchanged.
   - `npm audit --omit=dev --audit-level=high`.
   - **`eval.yml`** (weekly cron, unpinned `actions/checkout@v6` at `:38`, unlabelled HaluEval numbers) becomes `workflow_dispatch` only, with SHA pins, the job name unchanged, a CHANGELOG line, and its README badge removed.
   - `release-images.yml` path filters match this PR.
8. **S-8 (S–M): `check-claims` additions** (D207, D212).
   - The `measured_pair` divergence: warn at > 2×, error in `--launch`.
   - `--launch` also requires:
     - `quality.failure_rate ≤ 0.05`;
     - one snapshot, or `snapshot_split` with both snapshots named;
     - the recorded `trustagent_version == VERSION`;
     - no remaining `reverify`;
     - the agreement shown beside the 0.76 figure, never alone beside 0.87.
   - `--restricted` is built only if G0 gives mode R.
9. **S-9 (S): `make linkedin-post [CLIP=1]` and `make clip FILE=…`** (D213), each with tests: a missing value exits 1; the UTF-16LE pipe is used when `clip.exe` exists; stdout otherwise.
10. **S-10 (S, mode R only):** a `PAGES_TAG_DATASET` repo variable (default `recorded`) read at `.github/workflows/pages.yml:56`, plus a `test_workflows.py` case for the default.

### Gate

```bash
L="flock /tmp/trustagent-heavy.lock"
python3 -m unittest scripts/tests/test_release_notes.py scripts/tests/test_bump_version.py scripts/tests/test_check_version.py \
  scripts/tests/test_workflows.py scripts/tests/test_linkedin_post.py -v
node scripts/check-claims.mjs --self-test && node scripts/check-claims.mjs
node scripts/check-claims.mjs --launch; test $? -eq 1     # expected now: recorded, measured, licence (3 files), compare review, og (2 files)
python3 scripts/bump_version.py --dry-run 2.0.0 && python3 scripts/check_version.py
make lint-workflows && (cd frontend-react && $L npm ci && npm audit --omit=dev --audit-level=high)
test -z "$(git grep -n -E '/home/timkushma[r]|\.cache/trustagent-or[c]h|timandga[l]|mail\.tau\.ac\.i[l]' -- ':!docs/archive')"
```

---

## 6. CLOUD: the always-on demo recipe (cut line, queued; D218)

- **Branch:** `feat/v2-w7-cloud`.
- **Size:** S–M, about 1.5 d. Light.

### Tasks

1. **CL-1: `docker-compose.public.yml`.**
   - `proxy` and `dashboard` get `ports: !reset []`.
   - `READONLY` and `RETENTION_DAYS=7` on proxy and engine.
   - Engine `memory: 1g`.
   - A `caddy` service: `caddy:2-alpine@sha256:…`, ports 80, 443 and 443/udp, the `caddy-data` and `caddy-config` volumes, `mem_limit: 128m`, `restart: unless-stopped`, `depends_on` a healthy dashboard, and `TRUTHTABLE_PUBLIC_URL` (an existing key; the registry notes the new reader).
2. **CL-2: `deploy/public-demo/Caddyfile` and make targets.**
   - The Caddyfile sends `/api/*`, `/ws` and `/health*` to `proxy:8080`, answers 404 to `/v1*`, and sends the rest to `dashboard:3000`.
   - `make public-demo-{up,down,setup-url}` use `-p truthtable-demo -f docker-compose.yml -f docker-compose.demo.yml -f docker-compose.public.yml --profile demo`. `up` runs `pull` then `up --no-build`.
   - `compose-check` and `test_compose.py` entries.
3. **CL-3: `docs/DEPLOY-DEMO.md`** (G14).
   - The sslip.io option, the firewall, apt packages, a 2 GB swap file.
   - Cost labels; why not Oracle.
   - Linked from `docs/README.md` and DEMO.md §5.
4. **CL-4: doc fixes.**
   - `WALKTHROUGH.md:903-912`: "under 1 GB, 4 GB VM" [estimate]; Oracle "2 OCPU / 12 GB" [vendor].
   - `DEMO.md:164-183`: the shared-IP caps and the env workaround for tunnels.
5. **CL-5 (cut): `doctor --expect public`.**

### Gate

```bash
docker compose -p truthtable-demo -f docker-compose.yml -f docker-compose.demo.yml -f docker-compose.public.yml --profile demo config -q
python3 -m unittest scripts/tests/test_compose.py -v && make compose-check
```

---

## 7. Integration, dress rehearsal and Gate A (lead, on `feat/v2-int7`)

- **I-1. Merges.** MEAS, then VEND, then SHIP, then CLOUD if it is done. The Step-0 gate runs after each one.
- **Scratch.** `S=$ORCH/p6-scratch/lead`. Everything runs under the lock.

### R-1. Bench rehearsal

```bash
make gate-eval
(cd backend-python && TRUTHTABLE_ENV_FILE= poetry run python -m evals.datasets.ragtruth --subset qa --split test \
   --n 20 --seed 13 --revision $RAGTRUTH_SHA --out $S/rt20.jsonl)
# a) mock backend → SIMULATED, publish refused (exit 1)
(cd backend-python && TRUTHTABLE_ENV_FILE= env -u OPENROUTER_API_KEY poetry run python -m evals.bench run --dataset $S/rt20.jsonl \
   --tier lite --systems gate --max-cost-usd 1 --gate-eval-bin ../backend-go/bin/gate-eval --gate-eval-arg=--backend=mock \
   --resume-key r1a --cache-dir $S/cache --out-dir $S/r1a)
# b) mock-upstream posing as OpenRouter → SIMULATED (non_vendor_host): the D203 hole is closed
go -C backend-go run ./cmd/mock-upstream -addr 127.0.0.1:18090 &
(cd backend-python && TRUTHTABLE_ENV_FILE= OPENROUTER_API_KEY=sk-or-v1-fake TRUTHTABLE_OPENROUTER_BASE_URL=http://127.0.0.1:18090 \
   TRUTHTABLE_JEV_BACKEND=openrouter-decisions poetry run python -m evals.bench run --dataset $S/rt20.jsonl --tier lite \
   --systems gate --max-cost-usd 1 --gate-eval-bin ../backend-go/bin/gate-eval --resume-key r1b --cache-dir $S/cache --out-dir $S/r1b)
cp contracts/claims/measured.json $S/measured.json
for r in r1a r1b; do (cd backend-python && poetry run python -m evals.bench publish --run $S/$r/*/run.json \
   --results-dir $S/pub --index $S/index.json --measured $S/measured.json); echo "$r exit=$? (want 1)"; done
# c) stop rule: the GO-1 fault channel returns 402 on every call → stops after 5, exit 3; --retry-failed resumes
# d) env -u OPENROUTER_API_KEY make bench-lite → prints the plan with "no key: plan only", exit 0, nothing spent
```

### R-2 to R-5

- **R-2. Calibration rehearsal.**
  1. `calib-pass` against mock-upstream with a fake key and `CALIB_N=60`, into `$S`.
  2. `cmd/calibrate --out $S/calib`, without and then with `--write` (into `$S` only).
  3. `calib-holdout` on the R-1b run.

  Expected: the quality is not `measured`, and the hosts are refused.
- **R-3. Vendor rehearsal.**
  1. `make jev-capture` (dry run).
  2. `jevcheck --capture $S/cap` against mock-upstream with a fake key. The output is sanitized, and gitleaks finds nothing.
  3. **One** record rehearsal through mock-upstream: project `truthtable-p6rec`, `TRUSTAGENT_VERSION=p6local`, memory precondition from §0.5, output to `$S` only. Expected:
     - the manifest is not `recorded` (host rule);
     - drift and featured are computed;
     - `ACCEPT` is refused;
     - `make launch-video` refuses;
     - `VIDEO_ALLOW_MOCK=1` writes a scratch video, which proves the ffmpeg path.

     **Erratum D228:** an attempt cannot be recorded on mocks by design, so R-3 proves these through a simulated-label export on the zero-key demo configuration, `ACCEPT` on attempts that exist (one running, one under review with a simulated tape), and a static ffmpeg (D226). Done 2026-10-23.
  4. `down -v`, and remove the `p6local` images.
- **R-4. Release rehearsal.**

  ```bash
  make version-bump V=2.0.0 DRY_RUN=1 && make check-version
  make pages-build PAGES_DATASET=mock && make lint-workflows
  make release-notes TAG=v0.0.0-rehearsal RELEASE_NOTES_INSPECT=scripts/tests/fixtures/inspect.json
  make check-claims; make check-claims-launch; echo "want the script's exit 1 (make prints Error 1 and exits 2) with: recorded, measured, licence (3 files), compare review, og (2 files), pages-pending (2 files)"
  make linkedin-post; echo "want the script's exit 1 (make prints Error 1 and exits 2): values missing"
  (cd frontend-react && npm audit --omit=dev --audit-level=high)
  ```

- **R-5 (optional). Cloud.** `make public-demo-up` with `TRUTHTABLE_PUBLIC_URL=http://localhost:28443`, project `truthtable-p6pub`. Not run in P6a; the cloud session (16 GB, D221) is the better place for it, before C14.
  - A request through Caddy reaches the proxy with the client's IP.
  - Record `docker stats --no-stream` [measured], labelled with where it ran (the cloud session or the laptop, D225); the engine stays under its 1g limit.
  - Then bring it down.

### Release candidate on the main checkout

- **A-1.** The phase grep (D213) prints nothing.
- **A-2.** Merge `feat/v2-jev-gateway` into int7 first if it moved, then int7 into `feat/v2-jev-gateway`, and push both (D220). The owner's demo keeps running from its images.
- **A-3.** **No version bump** (D234): `make check-version` passes with VERSION 1.0.0, and `CHANGELOG.md` still opens with `## Unreleased — 2.0.0 "Jev gateway" (in progress)`. The bump is made on the P6b branch with the planned tag date (right before B8 in mode A, else at C4), so neither `feat/v2-jev-gateway` nor the A-8 pull request carries a release that has not happened.
- **A-4.** `make check` on the main checkout.
- **A-5.** With the owner's OK: `make demo-down && make demo`. This rebuilds the owner's images on the new head, so G2's `make up` reuses them.
- **A-6.** Containment (D220, D221):
  - on the laptop, `grep -l '^OPENROUTER_API_KEY=.\+' ~/projects/TrustAgent*/.env` lists nothing (the key goes into the cloud environment, never a worktree);
  - the pushed branches pass the hygiene grep and gitleaks;
  - CI status reads through the public API (no token).
- **A-7.** `docs/v2/STATUS.md` is rewritten for the cloud session (D221): what is done, the cards, the owner steps, nothing local.
- **A-8 (right after Gate A, D220, D234).** The lead opens one pull request from `feat/v2-jev-gateway` (which then holds int7) into `main` and sends the owner its link; the owner merges it with "Create a merge commit".
  - **No release in it** (D234): the branch holds VERSION 1.0.0 and `## Unreleased — 2.0.0 "Jev gateway" (in progress)`, and `main` keeps them until the release PR (C6).
  - **Its body** (the lead drafts it; a public text, so it carries the build disclosure, D227 d) names what reaches `main` with it: the hygiene fixes; the build disclosure (the README "How it was built" section, the landing footer's build line, and talking-points story 11 if the talking points stay public); the demo links pointed at `make demo` until Pages is on (`pages-pending`); the `measured.json` descriptions that no longer call our accuracy like for like (D233); P6a's tooling and launch kit, rehearsed on mocks. Nothing Jev-derived is on the branch (D222).
  - **The talking points first (H-7).** The owner's S1 choice is applied before the pull request opens: on int7 before A-2 when S1 has answered, else on `feat/v2-jev-gateway` before A-8 with the default (private). Private moves `docs/launch/interview-talking-points.md` into the interview kit artifact and out of the tree (with the links to it), so the pull request never brings story 11, or any note the owner chose to keep private, to `main`. Stories 1–10 stay in `main`'s history and story 11 in the pushed int7 branch's (D223: no rewrite).
  - **It follows its branch.** The P6b branch is cut from `feat/v2-jev-gateway` as before, whether or not the owner has merged yet; if this pull request is still open at C5, the owner merges or closes it before C5 pushes P6b into `feat/v2-jev-gateway`, or the push would add P6b and 2.0.0 to it.

### Gate A

**What passes:**
- `make check`;
- R-1 to R-4 as expected (R-3 as amended by D228);
- A-1 to A-7;
- `git status` clean.

**The report to the owner (one screen):**
- what is ready;
- the G0 mode, if S1 has not answered;
- the legal name, if missing;
- the P6b card list with caps (§8);
- owner time (S2 about 2.5 h);
- what public `main` shows today (D220): the hygiene grep fails, the README badge and "Try the demo" link are 404s, the measured block reads "not yet measured [measured]", and **nothing discloses the AI-agent build** (no README "How it was built", no landing build line, no talking-points story 11; the agents appear only deep inside `docs/v2/P5-PLAN.md` and `SPEC.md`);
- **the pull request into `main` (A-8):** the lead opens it right after Gate A from `feat/v2-jev-gateway` (which then holds int7), with its link, and the owner merges it with "Create a merge commit" (any device). It brings the build disclosure (the README "How it was built" section, the landing build line, and talking-points story 11 if the talking points stay public, the H-7 choice applied before it opens), the hygiene fixes, the demo links reworded to `make demo` until Pages is on (C2 points them back) and the corrected `measured.json` descriptions; it brings **no version bump** (D234): `main` keeps VERSION 1.0.0 and `Unreleased — 2.0.0 (in progress)` until the release PR. Its CI runs during P6b, so the release PR's CI loop is short. Recommend merging it before S2, so public `main` stops hiding how the project was built, and before C5 in any case (a pull request follows its branch);
- the wording of the "built with AI coding agents" line (D227);
- what was not re-run in P6a's later rounds, said plainly: R-5 (the public demo under `docker stats`) was never run in P6a: it is optional and planned for the cloud session before C14 (D221); R-3's Docker part (the stack export, the `ACCEPT` refusals and the ffmpeg path) was last run in the lead's D228 run of 2026-10-23 (fix round 1) and is covered by it, while its unit tests (`scripts/tests/test_record_demo.py`, `test_launch_video.py`) run in every `make check`;
- anything that slipped.

---

## 8. P6b: owner-in-the-loop runs (sitting S2, one day)

**Before starting:**
- **Mode A only** (D222): B3–B8 commit Jev-derived outputs to a public branch, so they wait for G0 = mode A or written permission. In mode W they wait; in mode R only the non-Jev parts run.
- **Where (D221).** B3–B8 run in the Claude cloud session, on a branch cut from `feat/v2-jev-gateway` after Gate A. Its environment has `OPENROUTER_API_KEY` set as a cloud environment variable (G3), network access to `openrouter.ai`, GitHub, `raw.githubusercontent.com`, the package registries and `ghcr.io`, and `ffmpeg` from the setup script. One-time setup there: `python3 scripts/configure.py --ensure`, `make install`, `make proto-tools proto`, then `make check` once. No card names a `$ORCH` path.
- Every P6a session is parked. In the cloud nothing else runs; on the laptop, `make down; make demo-down` if a step runs there. Nothing else runs until B8 ends.
- Cards B6–B8 run on the same day and never cross 03:00 Israel time (D207, D208).
- `BENCH_REGION` names where the session runs (D225): `cloud-<region>` when the session can say it, else `cloud`; `israel` on the laptop.

| Step | Who | Exact command | Cap | Expected | Stop if | Send back |
|---|---|---|---|---|---|---|
| **B1** | owner (G2), on the laptop | localhost PKCE check, **before any credit**; optional (D221: G11 on Pages settles Connect) | — | $0 (no credit) | — | "accepted", "refused" or "skipped" |
| **B2** | owner (G3) | $10 credit (Auto Top-Up off), key `trustagent-p6` with **limit 8**; the key goes into the cloud environment's variables as `OPENROUTER_API_KEY` (D221), never the chat | — | card ≈ $10.80 [vendor] | — | "key added" |
| **B3** | Claude on `go B3` | `make jev-check` with no stack running (otherwise it runs inside that stack's proxy container, `mk/go.mk`): one probe, ≈ $0.00001 | — | ≈ $0.00001 | not ready, `data_policy`, no snapshot | the snapshot and limit lines (snapshot, "limit 8"); jev-check prints no result line (D232) |
| **B4** | Claude on `go B4` | `make jev-capture`, then `make jev-capture CONFIRM=1`; then `go -C backend-go test ./cmd/jevcheck -run TestCommittedCapturesDecode -v -args -require-captures` (fails on an empty capture directory) | $0.01 | < $0.005 | any non-200 apart from the planned 401 | result line; the decode test passes |
| **B5** | Claude on `go B5` | `make gate-eval` (built once; sha printed); `make bench-smoke BENCH_REGION=<where>`, then the same with `CONFIRM=1` (resume key `smoke-p6`, a ledger name, not an OpenRouter key; `<where>` per D225, e.g. `cloud-us`; the default `local` says nothing) | $0.30 | $0.05–0.15 | any failed record; label ≠ measured; host ≠ openrouter.ai | result line |
| **B6** | Claude on `go B6` | `make calib-pass`, then `make calib-pass CONFIRM=1`. Then free: `make calibrate EVALUATOR=groundedness@1 IN=<printed gate.jsonl> LABEL_SOURCE=public_dataset DATASET=ragtruth:qa-train@<sha>`, then the same with `WRITE=1`, and the same for `contradiction@1` | $0.30 | $0.06–0.25 | groundedness quality ≠ measured, so the owner picks (D206) | result line; both quality lines |
| **B7** | Claude on `go B7` | `make bench-lite BENCH_REGION=<where>`, then the same with `CONFIRM=1` (resume key `lite-p6`; the same `<where>` as B5). The dry run's own estimate, from the judge prompt sizes, is about $1.16 [estimate]; the $2–3.5 planning range stays the budget figure and the $5 cap the guard. Then free: `make calib-holdout RUN=$PWD/backend-python/evals/results/<run_id>/run.json`, and `make bench-publish RUN=<the same run.json> HOLDOUT=$PWD/backend-python/evals/results/<run_id>/holdout-groundedness.json CHECK_ONLY=1` (every refusal check, into `tmp/` only). **The owner does G5 while it runs.** | $5 (ledger) | $2–3.5, 10–20 min | exit 3 (stop rule); failure rate > 5%; mixed snapshot; snapshot ≠ B6 | result line; ECE before/after; check-only refusals (expect none) |
| **B8** | Claude on `go B8` | **First, $0 (D234):** `make version-bump V=2.0.0 RELEASE_DATE=<the planned tag date>` on the P6b branch, `git diff` reviewed, committed as `chore(release): 2.0.0` (record-demo refuses VERSION 1.0.0; the tape stamps the version). Then `make record-demo`, then `make record-demo CONFIRM=1`. Review the drift table and the featured trace. Then `make record-demo ACCEPT=1 ATTEMPT=<id> VIDEO=1` (ffmpeg from the setup script, D226). **Calibration first:** payments-fallback falls back only on a calibrated route (D207); without the B6 file written (`make calibrate ... WRITE=1`) or `ALLOW_UNCALIBRATED=1`, the gate flags it instead and it shows as drift. | $4 for all attempts together; each attempt's caps ($0.50 decision, $3 LLM at most) come out of what is left (`record_demo.plan_caps`) | $1–2, 25–40 min | snapshot ≠ B6 without a calibration file; exit 4 (no featured trace); a budget-skipped trace | result line; drift table; accept / retry / accept the best flagged trace |

- **Total.** Expected $3.1–5.9 [estimate]. Tool caps $9.61. The $8 key limit stops anything beyond that cleanly (D201).
- **Mode W, or mode R when the owner chooses private measurement.** B3–B8 run, nothing Jev-derived is committed, and the outputs stay in gitignored paths (§2.2 item 7). In the cloud session those paths go when the session ends (D222), so private measurement there means the owner keeps the result lines only; the default in mode W is to wait.
- **What leaves the cloud session.** Committed outputs are pushed on the P6b branch (mode A). Files that are never committed (the MP4s, `tmp/launch/*`) reach the owner as messages from the session; if that fails, the laptop makes the H-6 take from the pushed tape (`make launch-video PAGES_ONLY=1`, $0).
- **Gate B (G6, about 20 min).** The lead posts:
  - a table of every figure with its label, n, interval, snapshot, scored and failed;
  - the calibration quality and the held-out ECE before and after;
  - the drift count and the **featured trace** (the claim, the evidence and the outcome);
  - the **post's opening lines filled** with these numbers. `make linkedin-post` reads the repository's `measured.json`, which is not published until C1, so the lead fills the post from a scratch copy that holds B7's check-only `measured.json` and B8's accepted tape (`--root` is not shown by `--help`; add `--roles` if the owner chose the roles line):

    ```bash
    G=tmp/gate-b; rm -rf $G; mkdir -p $G
    cp -r --parents docs/launch/linkedin-post.md contracts/claims/value-claims.v1.json \
      contracts/schema/common.v1.schema.json contracts/evaluators frontend-react/demo-data/recorded $G/
    cp tmp/bench-check/<run_id>/measured.json $G/contracts/claims/measured.json
    python3 scripts/linkedin_post.py --root $G
    ```
  - the headline changes (for example "landing: 27× (estimate) → N× (measured)");
  - the total spent against the `/key` usage.

  The owner answers "publish" or names what to re-check. There are no re-runs to improve a number (D212).
- **G7 (close the key)** follows "publish".

---

## 9. P6c: finish and release

| Step | Who | What (exact) | Gate |
|---|---|---|---|
| **C1** | lead | **Publish** (the cloud session's P6b branch, D221; mode A, D222):<br>`make bench-publish RUN=$PWD/backend-python/evals/results/<run_id>/run.json HOLDOUT=$PWD/backend-python/evals/results/<run_id>/holdout-groundedness.json` (the file `calib-holdout` wrote at B7; publish refuses to leave it out while it is usable, D229)<br>Commit:<br>• `docs/benchmarks/results/<run_id>.json` and `docs/benchmarks/index.json`;<br>• `contracts/claims/measured.json`;<br>• `config/calibration/<snapshot>.json`;<br>• `contracts/fixtures/vendor/captured/*`;<br>• `frontend-react/demo-data/recorded/**`.<br>Mode R: only the non-Jev keys, the load run and the non-Jev captures (D200). | `python3 scripts/check_recording.py frontend-react/demo-data/recorded --require-recorded`; `make contracts-check` |
| **C2** | lead, then owner | **Fill and look:**<br>• `make claims-write`;<br>• `make og` → `docs/launch/og.png` and `frontend-react/public/og.png`;<br>• `make hero-gif` → `docs/images/v2/hero.gif` and `tmp/launch/hero.mp4`;<br>• `make screenshots`, then the README prose that describes the pictures (mode A): the Screenshots line "Every picture below is **Simulated**", the `<sub>` under the top picture and the `alt` texts say **Recorded** (with the tape's date and snapshot), not Simulated, once the pictures come from the recorded tape;<br>• the demo links back on the hosted site (both modes; Pages turns on at C8): in `README.md` and `docs/DEMO.md`, every link the `pages-pending` comment lists points at `https://timkush1.github.io/TrustAgent/` again and the comment is deleted (D220; `make check-claims-launch` refuses one left, `launch-pages-pending`);<br>• `make launch-video` → `tmp/launch/video-{4x5,16x9}.mp4` (or H-6);<br>• `make linkedin-post`;<br>• fix any D212 divergence (a contract edit).<br>• the README Roadmap says what the release delivered (the measured benchmark, the recorded tape, 2.0.0) and what is next.<br>Then the session sends the OG image, the GIF, the videos and the post text to the owner (D221; on the laptop `explorer.exe docs/launch` and `explorer.exe tmp/launch`): the owner opens them and says "looks good" or names a change. | `make check-claims-launch` passes; the owner has said "looks good" |
| **C3** | lead | **Records:**<br>• dated Appendix D rows (PKCE localhost, `/key` shape, `usage.cost`, decision-path parity, 32k, ZDR, the Haiku price, licences, market signals);<br>• Appendix H;<br>• JEV-ACCESS `:84-88` PKCE status;<br>• `docs/launch/terms-check.md`;<br>• the LICENSE name. | `make check` |
| **C4** | lead | **Release candidate checks:**<br>• the version bump (D234), if B8 did not make it (mode R): `make version-bump V=2.0.0 RELEASE_DATE=<the planned tag date>`, committed as `chore(release): 2.0.0` on the P6b branch; after B8's bump, the CHANGELOG heading date set to the planned tag date if that date moved;<br>• `make check-version RELEASE_TAG=v2.0.0`;<br>• the phase grep;<br>• `make check`;<br>• `make pages-build PAGES_DATASET=recorded`;<br>• full-history gitleaks;<br>• the hygiene grep;<br>• `npm audit --omit=dev --audit-level=high`;<br>• the README Roadmap and Known limits match the release. | clean tree; every command green |
| **C5** | Claude (D220) | Push the P6b branch and merge it into `feat/v2-jev-gateway`, push that (no "go push" needed; never `main`). **If the A-8 pull request is still open**, the owner merges or closes it first: a pull request follows its branch, so this push would add P6b and 2.0.0 to it (D234). **Optional, on the laptop:** the `v1.0.0` tag at `d709df3` (`git tag -a v1.0.0 d709df3 -m "TrustAgent 1.0.0 (async auditor)" && git push origin v1.0.0`), with the owner's OK; the cloud session cannot push tags (D221). | the branch is on GitHub |
| **C5a** | owner (mode A), by email | **The results to TypeSafe**, as the G0 courtesy note promised (mode A, also when TypeSafe's written yes made it mode A). Once C5 has pushed, the session sends the owner: the measured figures with their labels, the link to the published run (`docs/benchmarks/results/<run_id>.json` on `feat/v2-jev-gateway`) and the post's final text (C2). The owner emails them to TypeSafe, asking again whether TypeSafe may be tagged if they have not answered, and tells the lead the date; the lead writes it in `docs/launch/terms-check.md` (the correspondence table) and pushes that one-line commit. **The post (C12) waits at least 3 days from that date**; the PR, the CI loop, S3b and the tag build go on meanwhile. Mode R skips it: nothing of Jev's is published. | the date is in terms-check.md |
| **C6** | owner (G9) | Settings and the minimal ruleset if not set yet (D220); the PR `feat/v2-jev-gateway` → `main` with the body of `docs/launch/release-pr.md` (Claude may open it through the GitHub app; else the owner opens it and pastes the body, `make clip FILE=docs/launch/release-pr.md` on the laptop) | — |
| **C7** | lead | **CI loop:**<br>• status from the public API, logs through the GitHub app (cloud) or the web UI;<br>• fix forward on a branch and push it (D220);<br>• likely failures: npm advisories of the day, `TestTier0Budget` under `CI=true`, the arm64 engine timeout, runner disk, gitleaks on the v1 history. | every PR check is green, `pages.yml` and `release-images.yml` included (build-only) |
| **C8** | owner (G10) | `PAGES_ENABLED=true` (mode A; the shared-origin check is clear), then merge with **Create a merge commit**. `main` runs `ci-*`, deploys Pages (recorded) and pushes `edge`. | the main runs are green |
| **C9** | owner (G10, G11) | The packages are already public (D224): check each still is, and set `GHCR_PUBLIC=true` if missing; the Pages PKCE check. If PKCE is refused, the `fix/static-connect-paste` PR (`frontend-react/src/domain/liveScenarios.ts:37` and its test) is merged in the UI. | `docker buildx imagetools inspect ghcr.io/timkush1/truthtable-proxy:edge` lists amd64 and arm64 |
| **C10** | lead, then owner | `python3 scripts/check_version.py --tag v2.0.0` on the merge commit (`git fetch origin && git checkout --detach origin/main`). Then the owner creates the release in the web UI as a **pre-release** (G12 step 1): it creates the `v2.0.0` tag on `main` (D221). If the tag's workflows have not started within 5 minutes, run `pages.yml` (deploy) and `release-images.yml` (push) by "Run workflow" on the tag. | tag runs green: Pages deploy (recorded), semver images, `first-run`, `anonymous-pull` |
| **C11** | lead + owner (G12) | `make release-notes TAG=v2.0.0 > tmp/release-notes.md` once the tag's images exist (it reads their digests); the session sends it (laptop: `make clip FILE=tmp/release-notes.md`). The owner edits the pre-release: the notes, `video-16x9.mp4` and `hero.mp4`, then unticks pre-release and sets it as Latest. Then About, topics, social preview; the ruleset's required checks (`docs/launch/ruleset.md`); code security; close Dependabot PRs #21, #23, #24, #28 and #30; delete the stale remote branches in the UI (Branches page): `copilot/check-insecurities`, every `dependabot/*` branch, and `fix/deps-security` once PR #45 is merged or closed; pin the repo; LinkedIn Featured. | the release badge shows v2.0.0 |
| **C12** | owner (G13) | The stranger check and Post Inspector. **Mode A:** at least 3 days have passed since C5a's email (its date in terms-check.md), and the post tags TypeSafe only if they said yes. Then **the owner posts** with `video-4x5.mp4` and, right after, adds the Haqq link (https://www.haqq.ai/blog/jev-legal-ai-hallucination-detection) as the post's first comment, which the post's text promises (the mode-R post has no such link). | §13 all true |
| **C13** | lead | Afterwards:<br>• update `TrustAgent-CV-context.md` and the interview kit artifact (same URL);<br>• memory notes;<br>• remind the owner: every P6 key deleted or ≤ $1 and removed from the cloud environment's variables; the laptop's push token (stored 2026-10-21) kept or deleted, as the owner chooses. | — |
| **C14 (optional)** | owner + lead | `ci-knowledge.yml` dispatch; the cloud demo (G14); the `eval-live` environment. | — |

**Mode notes**
- **Mode W** stops before C5.
- **Mode R:**
  - C1 publishes the subset only;
  - C5a is skipped, and so is C12's 3-day wait;
  - C2 uses the mode-R post;
  - C8 also sets `PAGES_TAG_DATASET=simulated` (S-10);
  - the C10 tag run deploys the simulated tape;
  - H-R must be approved before C12.

---

## 10. Owner guides (plain words, by sitting)

**Where each step runs (D221).**

| Where | Steps |
|---|---|
| **Your laptop** (browser and terminal) | G2, the localhost Connect check (optional); the optional `v1.0.0` tag; your own `make demo` |
| **The Claude cloud session** (claude.ai/code) | B3–B8 (the paid cards), C1–C5, C7, the release notes; Claude pushes its branches there |
| **GitHub's web UI, any device** | G9–G12: settings, the ruleset, the PR merge, Pages, packages, the release and its tag, About |
| **OpenRouter's site, any device** | G3 (credit and key) and G7 (closing the key) |
| **Anywhere** | G0, G1, G5, G6, the results email to TypeSafe (C5a, mode A), G13 and the post |

### Sitting S1 (now, about 45 min, $0)

**G0. May you publish Jev numbers?**
1. Open each of these:
   - https://openrouter.ai/typesafe/jev-1.13 (look for any "Terms" or "Model terms" link);
   - https://typesafe.ai/legal/terms;
   - https://typesafe.ai/legal/acceptable-use-policy;
   - TypeSafe's **Master Customer Agreement** (look for a link on typesafe.ai/legal, the model page or the Terms). It is the document whose reported clause started this check, so it matters most;
   - https://openrouter.ai/terms (section 5.1).
2. In each one, press Ctrl+F and search for: benchmark, performance, publish, compare, output, redistribut, display, competitive, evaluat, disclos, reverse.
3. Tell Claude what you found. Paste the sentence and the link, or say "nothing found in X, Y, Z".
4. Then:
   - **Nothing found, and you read the Master Customer Agreement:** send the short courtesy note Claude drafted. It asks whether you may tag TypeSafe, and says you will send the results at least 3 days before posting (C5a in S3a sends them; G13 waits the 3 days). This is **mode A**.
   - **If you could not find or open the Master Customer Agreement, or cannot tell which terms OpenRouter's Jev page binds you to, that counts as unsure: mode W** (D231). A search that found nothing in the other pages does not make it mode A.
   - **Something found, or unsure:** send the permission request Claude drafted. It asks plainly whether the Master Customer Agreement applies to OpenRouter users and whether you may publish benchmark figures and the recorded demo; a written yes is permission, and the mode becomes A. This is **mode W**. Claude carries on with everything that does not need permission. If there is no reply in 7 days, you choose: keep waiting, or **mode R**. Mode R means the site shows a simulated replay instead of a recorded one, and the post has no Jev numbers.

**G1. One-time setup.**
1. **Your legal name** for the licence file. `LICENSE`, `frontend-react/package.json` and `backend-python/pyproject.toml` hold the placeholder `{{OWNER_LEGAL_NAME}}` until you give it (the launch gate refuses the placeholder).
2. **Your other sites.** Do you have any site at `https://timkush1.github.io/<name>`, including one from a private repository (repo Settings → Pages)? If one of them loads third-party scripts (analytics, widgets), say so. The demo then needs its own domain.
3. **Choices:**
   - an "open to roles" line in the post, yes or no;
   - interview talking points public in the repo, or private (the default is private). Stories 1–10 are on public `main` already, and story 11 on the pushed int7 branch; private takes the file out of the tree before the pull request after Gate A (A-8), so story 11 and anything new in it never reach `main`;
   - your preferred launch day;
   - your audience: Israel, or US and EU.
4. **OpenRouter account.** Sign in or sign up at https://openrouter.ai. **Do not add credit yet.** Open https://openrouter.ai/settings/privacy and check that Jev's provider can be reached:
   - **The provider is "TypeSafe".** OpenRouter lists exactly one endpoint for `typesafe/jev-1.13`, named "TypeSafe | typesafe/jev-1.13-20260917" (the snapshot of 2026-09-17), served from `api.typesafe.ai`. Its published data policy: it does not train on prompts, does not retain prompts, and OpenRouter does not train on them either (Step 0, 2026-10-10, from the public model page and `/api/v1/models/typesafe/jev-1.13/endpoints`).
   - OpenRouter's privacy guide says: "On your account settings page, you can set whether you would like to allow routing to providers that may train on your data (according to their own policies). There are separate settings for paid and free models." TypeSafe does not train, so you can leave both of those settings **off**.
   - Make sure "TypeSafe" is not on any list of ignored or blocked providers on that page.
   - If the page has a setting that allows only Zero Data Retention (ZDR) endpoints, leave it **off**: TypeSafe's ZDR status is not published, and with it on every Jev call can fail with a 404 `data_policy` error.
   - The settings page needs a signed-in account, so Step 0 could not read its exact labels. Read them out to Claude if anything looks different from this.
5. **The cloud environment** (D221). In claude.ai/code, open the environment settings of the session that continues P6:
   - **Network access:** keep the default trusted list and add `openrouter.ai`. If you use a custom list instead, it needs `openrouter.ai`; GitHub (`github.com`, `api.github.com`, `codeload.github.com`, `raw.githubusercontent.com` for RAGTruth); the package registries (`pypi.org`, `files.pythonhosted.org`, `registry.npmjs.org`, `proxy.golang.org`, `sum.golang.org`); `ghcr.io` (and `pkg-containers.githubusercontent.com`); Docker Hub for the base images B8's builds and the stack pull (`registry-1.docker.io`, `auth.docker.io`, `production.cloudflare.docker.com`: node, python, golang, alpine, nginx-unprivileged, postgres, caddy); and the OS package mirrors those builds use (`deb.debian.org`, `dl-cdn.alpinelinux.org`).
   - **Setup script:** add `sudo apt-get install -y ffmpeg` (or `apt-get install -y ffmpeg` if it runs as root). ffmpeg makes the launch video (D226).
   - **Environment variables:** nothing yet. The OpenRouter key goes in at G3, as `OPENROUTER_API_KEY`, never in the chat.
6. **Power.** Only if a step runs on your laptop: Windows Settings → System → Power & battery → Screen and sleep → "When plugged in, put my device to sleep after": **Never**, for the P6 days.
7. **How it was built.** The post, the README and the release say TrustAgent was built with AI coding agents (D227, D230). The default line is: "Designed and directed by me; built with AI coding agents (Claude Code) to plans I approved and decisions I made." It is exact: the agents drafted the plans, wrote the code and the tests and ran the reviews; you approved the plans, made the product decisions and authorized the pushes and the spending. Keep it, or give your own words.
8. Notes, nothing to do unless you want to:
   - your university email is in the commit history; it is already public on `main` and stays as it is (D223). New commits still carry it as the author email; if you would rather the P6b and P6c branches did not, set your GitHub noreply address (GitHub → Settings → Emails → "Keep my email addresses private" shows it) as the author email before S2, and tell Claude, who then commits with it;
   - pushes no longer need your "go push" (D220): Claude pushes branches, and `main` changes only when you merge a pull request on GitHub.

Say back: **"S1 done: mode A/W/R, name …, sites none/…, roles yes/no, talking points public/private, launch <day>, audience Israel or US/EU, OpenRouter account ready, cloud environment set (network and ffmpeg), AI line default/<words>"**.

### Sitting S2 (after Gate A, one day, about 2.5 h, mostly waiting)

**G2. Localhost Connect check (5 min, $0; on your laptop; optional, D221).** The cloud session has no browser of yours, so this one runs on the laptop. Skip it if you like: G11 checks the same Connect flow on the public site.
1. Run `make demo-down`. Your demo is kept; this only frees port 3000.
2. Run `make up`. The first start can take 10–25 minutes. Open the setup link it prints, or get it with `make setup-url`. On a first claim, save the admin key in a password manager.
3. Click **Connect OpenRouter** (setup step 2, or Settings → Connections), sign in, and click **Authorize**.
4. Tell Claude:
   - **"accepted"** if you came back to `localhost:3000/connect/callback` and a masked `sk-or-…` key is shown. A "no credits" message is expected and fine;
   - **"refused"** if openrouter.ai showed an error.
5. In Settings → Connections, **disconnect** the key. At https://openrouter.ai/settings/keys, **delete** the new key.
6. Run `make down`.

**G3. Credit and the measurement key (10 min).**
1. Go to https://openrouter.ai/settings/credits → **Add credits** → **10** → pay. The card shows about $10.80 because of the fee. Check that **Auto Top-Up is off**.
2. Go to https://openrouter.ai/settings/keys → **Create key**:
   - name `trustagent-p6`;
   - **credit limit 8**, never resets;
   - Create. The key is shown only once.
3. **Do not paste the key into the chat.** Put it where the cloud session reads it (D221): in claude.ai/code, the environment settings of the P6 session → **Environment variables** → add `OPENROUTER_API_KEY` = the key → save. Start (or restart) the session so it sees the variable.
   - Only if a paid step runs on your laptop instead: `cd ~/projects/TrustAgent && make configure WITH_SECRETS=1`, paste the key at the first prompt (nothing appears while you paste; that is normal), then press Enter three times.
4. Say **"key added"**.

**G4. Approving a paid step.**
- Claude shows one card at a time: the command, the cap, the expected cost, "used so far $X of $8", and when it will stop.
- Say **"go"** (for example "go B5"). That approves only the card on screen.
- If a step stops early, nothing more is spent; Claude explains and you decide.
- You can also run the command in your own terminal and paste only the last line, the one starting with `TRUSTAGENT_P6_RESULT`. It never contains the key.

**G5. Comparison review (about 20 min, while the bench runs).**
1. Claude shows each of the 20 cells: the vendor, the feature, yes/no/partial, the quoted sentence from the source, the link and the date.
2. Open the links you want to check.
3. Answer **"accept all except …"**, giving "change to …" or "drop" for each exception.

**G6. Gate B review (about 20 min).** Read Claude's table, the featured example and the post's filled-in opening lines. Answer **"publish"**, or name what to re-check. Numbers are not re-run to look better.

**G7. Close the key (5 min, after "publish").**
1. At https://openrouter.ai/settings/keys, delete `trustagent-p6`, or set its limit to the shown usage + 1.
2. In claude.ai/code, remove `OPENROUTER_API_KEY` from the environment's variables. (If the key was on your laptop too: `make configure WITH_SECRETS=1`, type `-` at the OpenRouter prompt, then press Enter three times.) `make demo` is unaffected.
3. Optional: if G1 step 4 made you change a privacy setting (TypeSafe taken off an ignored list, or a ZDR-only setting turned off), set it back as you prefer. G1 asked for no change otherwise, so there is nothing to undo.

### Sitting S3a (release day, about 30 min, then hours of CI)

**Before Claude pushes the release branch (C5; 1 min, GitHub web UI).** If the pull request Claude opened right after Gate A (A-8) is still open, merge it with **Create a merge commit**, or close it. A pull request follows its branch, so C5's push would otherwise add the measured run and 2.0.0 to it (D234). Say **"A-8 merged"** or **"A-8 closed"**.

**The results to TypeSafe (mode A only, 10 min; your email, any device; C5a).** Claude says the release branch is pushed (C5) and sends you three things: the measured figures with their labels, the link to the published run, and the post's final text.
1. Email them to the TypeSafe address you wrote to at G0. The courtesy note promised them at least 3 days before the post. If TypeSafe has not answered whether you may tag them, ask again in the same email.
2. Tell Claude the date you sent it (**"results sent <date>"**). Claude writes it in `docs/launch/terms-check.md`.
3. Count 3 full days from that date: the post (G13) waits until then, whatever else is ready. The PR, the CI loop, S3b and the tag build go on meanwhile.

**G8. GitHub token: dropped (D220).** Claude pushes branches already (the laptop has your repo-scoped token since 2026-10-21; the cloud session uses the Claude GitHub app). Nothing to do. Never paste a token into the chat.

**G9. Settings, the minimal ruleset and the PR (15 min; GitHub web UI, any device).** Steps 1–4 can be done any time before; they are listed here so nothing is missed.
1. Repo **Settings → General → Pull Requests**: tick **Allow merge commits**.
2. **Settings → Pages → Source: GitHub Actions**.
3. **Settings → Environments → github-pages.** If it is not listed, choose **New environment** and name it exactly `github-pages`. Then go to **Deployment branches and tags** → Selected → add `main`, and add a **Tag** rule `v*`.
4. **Settings → Rules → Rulesets → New branch ruleset:**
   - name `main`, Active, target the default branch;
   - tick **Restrict deletions**, **Block force pushes** and **Require a pull request** (0 approvals);
   - bypass: Repository admin, with the bypass mode **For pull requests only**, so nobody (Claude included) can push to `main` directly;
   - Save.
5. Claude says the release branch is pushed. Open https://github.com/timkush1/TrustAgent/compare/main...feat/v2-jev-gateway → **Create pull request** (or open the PR Claude made). The body is `docs/launch/release-pr.md`: Claude sends it as a message; paste it in.

### Sitting S3b (when Claude says CI is green, about 20 min)

**G10. Pages, merge and packages.**
1. **Settings → Secrets and variables → Actions → Variables** → New: `PAGES_ENABLED` = `true`. In mode R, also add `PAGES_TAG_DATASET` = `simulated`.
2. On the PR: **Merge pull request** → choose **Create a merge commit** → Confirm.
3. The packages have been public since 2026-10-21 (D224). Check that github.com/timkush1?tab=packages shows `truthtable-proxy`, `truthtable-engine` and `truthtable-dashboard` as **Public**, each with "Repository: TrustAgent" (if not: Package settings → Manage Actions access → add the repo).
4. If the Actions variable `GHCR_PUBLIC` is missing, add it with the value `true`.
5. Say **"packages checked"**.

**G11. Pages Connect check (5 min, ≈ $0.0002).**
1. Open https://timkush1.github.io/TrustAgent/app/playground/#live → accept → **Connect OpenRouter**. On OpenRouter's authorize page, if it offers a credit limit for the new key, set a small one (for example $0.10): the page's link asks for no limit, so otherwise the key could spend whatever credit is left after P6b. Then **Authorize**.
2. You should land back on the playground with "Live · your key". Run **one** example. If you like, open the browser's developer tools first (F12 → Network): the requests go only to `timkush1.github.io` and `openrouter.ai`. Then click **Forget key**.
3. Right away, at https://openrouter.ai/settings/keys, delete the new key.
4. Say **"accepted"** or **"refused"**.

**The tag (5 min; GitHub web UI).** The cloud session cannot push tags (D221), so the release makes the tag. When Claude says the merge commit passed `check_version.py --tag v2.0.0`:
1. **Releases → Draft a new release** → **Choose a tag** → type `v2.0.0` → **Create new tag: v2.0.0 on publish**; target `main`.
2. Title "TrustAgent 2.0.0 — Jev gateway"; body: "Release notes follow once the images are built."
3. Tick **Set as a pre-release** (not Latest yet) → **Publish release**. This creates the tag, and the tag build starts (up to 90 minutes). If nothing starts within 5 minutes, tell Claude: the tag's workflows can be started by hand.

### Sitting S3c (after the tag build, about 45 min)

**G12. Release and presentation.**
1. **Releases → the v2.0.0 pre-release → Edit:**
   - replace the body with the notes Claude sends (from `make release-notes TAG=v2.0.0`, which reads the images' digests);
   - drag in `video-16x9.mp4` and `hero.mp4` (Claude sends them; or make the H-6 take on the laptop);
   - untick **Set as a pre-release**, tick **Set as the latest release** → **Update release**.
2. **The About gear** on the repo page:
   - paste the description;
   - Website `https://timkush1.github.io/TrustAgent/`;
   - the 16 topics from `docs/launch/github-metadata.md`.
3. **Settings → General → Social preview** → upload `docs/launch/og.png`.
4. **Settings → Rules → the `main` ruleset** → **Require status checks** → type each name from `docs/launch/ruleset.md`, one by one → Save.
5. **Settings → Code security:** turn on Private vulnerability reporting and Dependabot alerts.
6. Close PRs #21, #23, #24, #28 and #30 with the comment "superseded by 2.0". On the Branches page delete `copilot/check-insecurities`, every `dependabot/...` branch, and `fix/deps-security` once PR #45 is merged or closed.
7. On your GitHub profile, **pin** TrustAgent. On LinkedIn, add it under **Featured** and **Projects**.

**G13. The stranger check, then post.**
1. Open the site in a private window and on your phone, in light and dark mode:
   - the banner says **Recorded** (mode A);
   - the first-visit guide reaches the caught answer;
   - every README link and badge opens.
2. Paste the site link into https://www.linkedin.com/post-inspector/ and check that the card shows the OG image.
3. Claude sends the post text: `make linkedin-post`, with `ROLES=1` if you said yes to the roles line at S1, and `RESTRICTED=1` in mode R (on the laptop, `CLIP=1` copies it). It is at most 2,800 characters, so LinkedIn takes it whole (D229). Read it: every number has a label, the cost and the latency say what they were measured on, and the "built with AI coding agents" line is the one you chose.
4. **Mode A: check the date.** At least 3 full days have passed since you emailed TypeSafe the results (C5a; the date is in `docs/launch/terms-check.md`). If not, wait: that was the promise. Tag TypeSafe in the post only if they said yes to being tagged; otherwise leave the tag out (the post still names "TypeSafe AI's decision model").
5. Post, with `video-4x5.mp4` as the post's video.
6. Right away, add the post's **first comment**: the Haqq link the post promises ("link in the first comment"), https://www.haqq.ai/blog/jev-legal-ai-hallucination-detection (mode A; the mode-R post has no such link).
7. Afterwards, keep or delete the laptop's push token as you prefer (Settings → Personal access tokens).

### Optional

**G14. Always-on demo server (about 30 min, from about €6 a month excl. VAT).** Follow `docs/DEPLOY-DEMO.md`. In short:
1. Create an SSH key.
2. Create a Hetzner CX23 server running Ubuntu 24.04, with firewall rules 22 (your IP only), 80 and 443.
3. Use your own domain, or `<ip-with-dashes>.sslip.io`.
4. `apt-get install git make python3`, install Docker, and add a 2 GB swap file.
5. `git clone`, `git checkout v2.0.0`, then `python3 scripts/configure.py --ensure`.
6. Add the public lines to `.env` and run `make public-demo-up` (it pulls the images; it does not build them).
7. Claim the setup link right away.
8. Check that "Read-only" and "Simulated" are visible.

To remove it: `make public-demo-clean` (it also deletes the Caddy volumes), then delete the server (billing is hourly).

---

## 11. Schedule and critical path

- **P6a: about 5–6 working days.**
  - Step 0 takes 1 d.
  - The streams run in parallel: MEAS 4.5 d; VEND 4.5 d (+2 d with V-0); SHIP 3.5 d; CLOUD 1.5 d, queued.
  - Integration and rehearsal take about 1 d.
- **P6b: one day (S2).** Mode W adds the TypeSafe wait, up to 7 days, which runs in parallel with P6a.
- **P6c: about 4 days, set by a 3-day minimum.** In mode A the post waits **at least 3 days** after C5a sends TypeSafe the results (the G0 courtesy note's promise).
  - C1–C5 take half a day; C5a goes out as soon as C5 has pushed.
  - Inside the 3 days: S3a and the PR, the CI loop (hours to a day), S3b, the tag build (≤ 90 min) and G12.
  - G13 and the post come on day 3 at the earliest. Mode R has no wait.
- **Critical path:** Step 0 → VEND (`record-demo`, featured trace, video) → I-1 → R-3 → Gate A → S2 → C1–C5 → C5a (results to TypeSafe) → **at least 3 days** (S3a, the CI loop, S3b, the tag and G12 run inside them) → G13 and the post.
- **Shortening it:** the pull request into `main` right after Gate A (A-8, D220) runs CI on P6a's code during P6b, so the release PR's CI loop is short. The 3-day wait after C5a is not shortened: it is a promise to TypeSafe.

---

## 12. Risks

| Risk | Effect | Mitigation |
|---|---|---|
| TypeSafe's terms forbid publishing performance numbers (third-party report; primary text unread) | A breach claim; launch blocked or reworked | D200: G0 first; modes A, W and R; nothing Jev-derived pushed before G0 (D222; `main` itself is public since 2026-10-21, D220); the email; the 7-day rule |
| P5 integration keeps adding D numbers | Numbers defined twice (CO-1 again) | P6 uses D200+; the Step 0 script checks |
| "Measured" decided by the backend name | Mock output published as measured | D203 host stamp and allow-list; R-1b and R-3 regressions; the sanitizer keeps the hosts |
| A paid target spends on a plain `make` | Unplanned spend | D204 `CONFIRM=1` default for every paid target |
| Credit runs out or the key limit is reached mid-run | A 402, a partial run, a broken recording | $10 credit; an $8 limit set once; caps lowered from `limit_remaining`; the stop rule; resume with fixed keys; the record ledger from `/key` usage |
| A resume pays twice | Double spend | Fixed resume keys; gate-eval built once, with a sha check |
| A systematic vendor failure (402, an alpha body change, `data_policy`, the judge refusing `response_format`) | Ledger spent on failures | B3 and B5 first; the stop rule after 5; the privacy setting fixed at S1 |
| Snapshot rolls during P6b | Calibration not applied; mixed figures | D207: one day, preflight, refusals, and the owner's (a)/(b) choice |
| Contradiction calibration stays provisional | It cannot drive fallback | D206: a train sample sized for ≥ 40 positives; groundedness drives fallback; an honest chip |
| Live outcomes differ from the scripted demo | OG, hero, video and post broken | D209: featured by rule; drift reviewed; `ACCEPT`; the tape is never edited |
| Latency measured cold or under load | Published p50/p95 misleading | D205: warm, one at a time, demo down, `host_env` recorded, wording names the conditions |
| Like-for-unlike comparison (balanced accuracy vs a tuned accuracy) | Misleading copy | D212: compared with 0.76; plain accuracy and intervals published; caveats carried |
| An unflattering number | Pressure to re-run or hide it | D212: one named run; attempts disclosed |
| Estimate and measured claims disagree | Contradictory figures | `measured_pair` plus the `--launch` divergence error |
| The tape shows version 1.0.0 | An inconsistent site | D234: the bump on the P6b branch right before B8, never at Gate A; the version check in `check_recording` and `check-claims` |
| The real key spreads to worktrees | A key in "mocks-only" sessions | D202: one checkout; `.env` grep before every card; `configure --ensure` only |
| An agent pushes something it should not | Unapproved publishing (every branch is public, D220) | D222: nothing Jev-derived before mode A; the hygiene grep and gitleaks before every push; `main` only through the owner's PR merge, with the ruleset's admin bypass for pull requests only |
| Paid outputs not ignored | Dataset text or Jev outputs committed | §2.2 item 7: gitignore plus the `check-ignore` test |
| Shared github.io origin | A visitor's `sessionStorage` key exposed to another site | G1 item 2 before `PAGES_ENABLED`; custom domain if needed; DoD item 2 |
| First GitHub run of every v2 workflow | A red PR | S-7 local checks; pushes allowed (D220); logs through the GitHub app or the web UI; the pull request into `main` right after Gate A (A-8) runs them on P6a's code; Trivy and path-filtered jobs not required |
| The cloud session cannot push tags or reach the owner's browser | No tag; no localhost Connect check | D221: the owner's GitHub release makes the tag; G2 runs on the laptop or is skipped (G11 decides) |
| GHCR packages private at the tag | `anonymous-pull` skipped | D215 order: public before the tag; `GHCR_PUBLIC` |
| Pages tag deploy blocked | Tag deploy fails | The `v*` environment rule in G9 |
| Pages ships simulated after the merge | A simulated site at launch | `PAGES_ENABLED` set only once the recorded tape is committed (mode A) |
| Mode R tag build fails (`pages.yml:56` forces recorded) | No Pages | S-10 `PAGES_TAG_DATASET` |
| Connect keys have no spending limit | Unbounded spend | G2 with zero credit; G11 deletes its key the same day |
| The 8 GB laptop | OOM in rehearsal, recording or video | 3 heavy slots; Docker only with ≥ 2.5 GB free; everything parked and the demo down in S2; P6b and P6c run in the cloud session (about 16 GB, D221) |
| The laptop sleeps | A stalled run | The S1 power setting, for steps that still run on the laptop |
| ffmpeg or Playwright missing | No GIF, OG or video | ffmpeg at S1; V-6 and V-7 tool checks; H-6 fallback |
| A personal path or email in tracked docs | Embarrassment in a public repo | The D216 hygiene grep is a gate (C4) on the current tree; history stays (D223) |
| Public `main` hides the AI-agent build until the release | A visitor sees a large solo project with no disclosure (D227 d) | A-8: the pull request into `main` right after Gate A brings the README "How it was built" section, the landing's build line and, if the talking points stay public, talking-points story 11 |
| The release posted before TypeSafe has had the results 3 days (mode A) | The courtesy note's written promise broken, with the vendor whose terms are the launch's legal risk | C5a sends them once C5 has pushed and dates it in terms-check.md; C12, G13 step 4, §11 and DoD 15 wait at least 3 days; TypeSafe tagged only on a yes |
| Licence name mismatch | Legal ambiguity | The owner gives the name at S1 |
| The public cloud demo shares one IP | The 10-viewer cap | D218: Caddy straight to the proxy; the nginx fix in P7 |

---

## 13. Definition of done for v2.0.0 (all true before the LinkedIn post)

1. **Terms.** The G0 result is in `docs/launch/terms-check.md`. The mode is A, or R with H-R approved by the owner. Mode W cannot launch.
2. **Shared origin.** No other `timkush1.github.io` site loads third-party scripts, or the demo is on its own domain.
3. **Measured.**
   - `contracts/claims/measured.json` has `status: measured`, `mock: false` and a named snapshot.
   - Its run in `docs/benchmarks/results/` was made on vendor hosts (D203), with one snapshot (or `snapshot_split` named), failure rate ≤ 5%, a clean tree, a pinned dataset SHA, `cost_source` stated and attempts disclosed.
   - A load run is published (proxy overhead, simulated vendors, labelled).
   - Mode R: the non-Jev keys only.
4. **Calibration.**
   - `config/calibration/<snapshot>.json` is committed.
   - `groundedness@1` is `measured`, or the tape is visibly uncalibrated.
   - The held-out ECE before and after is published with its method (`make check-claims-launch` refuses it missing, D229).
5. **Recording.**
   - `check_recording.py frontend-react/demo-data/recorded --require-recorded` passes.
   - The manifest has vendor hosts, `trustagent_version` 2.0.0, and a `featured` trace with a contradicted claim, or the alternative the owner accepted.
   - Drift was reviewed.
6. **Claims.**
   - `make check-claims-launch` passes: compare cells reviewed, `og.png` present, no divergence, no `reverify` left.
   - The README measured block comes from `claims-write`.
   - The agreement is shown with n, an interval and the thresholds the bench flags at (groundedness 0.8, contradiction 0.7), beside Arize's 0.76 at 0.5 and 0.87 at a tuned 0.80, neither called like for like (D233).
   - Every measured cost says what it was measured on (dataset, n, input tokens per answer) and every measured latency how it was taken (D205, D229).
7. **Launch assets.**
   - The GIF is ≤ 8 MB, and the video (4:5 and 16:9) comes from the recorded tape (or H-6).
   - The post text comes from `make linkedin-post` and is at most 2,800 characters (D229).
   - The owner approved the visuals at C2.
8. **Docs.** Appendix D rows are dated (PKCE localhost and Pages, vendor shapes, the Haiku price, licences, market signals); Appendix H is written; the JEV-ACCESS PKCE status is updated.
9. **Version.**
   - `VERSION` is 2.0.0, and all mirrors agree.
   - The CHANGELOG has `## v2.0.0 — <tag date> "Jev gateway"`.
   - `check_version.py --tag v2.0.0` passes on `main`.
10. **Repository.**
    - The PR was merged by the owner as a merge commit (D220).
    - Every required check is green on `main`.
    - The ruleset is active with its required checks, the admin bypass for pull requests only.
    - The tag `v2.0.0` exists (made by the release, D221); `v1.0.0` is optional.
    - The Release v2.0.0 is Latest, with notes, digests and the MP4s.
    - The release badge shows v2.0.0.
11. **Images.**
    - The three GHCR packages are public, each with amd64 and arm64.
    - The tag's `anonymous-pull` and `first-run` jobs are green.
    - Attestations appear on each digest.
12. **Pages.**
    - https://timkush1.github.io/TrustAgent/ is served from the tag, with the manifest `recorded` (mode A).
    - Network requests go only to the site itself and openrouter.ai.
    - The Pages PKCE result is recorded, and Paste is primary if PKCE was refused.
13. **Presentation.**
    - About, website, the 16 topics and the social preview are set.
    - The community files and private vulnerability reporting are on.
    - The repo is pinned on the profile.
14. **Hygiene and secrets.**
    - The hygiene grep prints nothing on `main`'s tree after the merge (history stays as it is, D223).
    - Full-history gitleaks is green on the PR.
    - Every P6 OpenRouter key is deleted or limited to ≤ $1, and gone from the cloud environment's variables and from any `.env`.
    - The laptop's push token is kept or deleted, as the owner chose.
15. **The stranger check (G13)** passes, Post Inspector shows the card, and the owner has approved the post.
    - Mode A: at least 3 days have passed since the results went to TypeSafe (C5a, dated in `docs/launch/terms-check.md`), and the post tags TypeSafe only if they said yes.
    - Mode A: the Haqq link is ready to go in as the post's first comment right after posting, as the post's text promises.

The cloud demo, the knowledge CI run and `eval-live` are **not** required (D219).

---

## 14. Deferred

- **v2.0.x:**
  - the knowledge Docker run (`ci-knowledge.yml`);
  - the `eval-live` environment and an AggreFact run;
  - `doctor --expect public`;
  - the cloud deploy, if it was not done at C14.
- **P7 (v2.1):**
  - nginx real-IP support behind a registered key;
  - a multi-replica PKCE nonce store, shared caps and the keyed verify limiter;
  - label re-scoring on a new snapshot (paid);
  - the #66 zip bundle;
  - cosign signing;
  - a recorded Studio session;
  - native arm runners, if QEMU is too slow;
  - `SECRET_KEY_PREVIOUS`, engine-side Jev, `JudgeConfig.base_url`;
  - the P5 §23 candidates (the label-queue latest-version filter, the `would_gain` time budget).

---

## 15. Critique disposition

### Applied

| Source | Item | Where |
|---|---|---|
| risk 1 | Shared-origin check restored | G1 item 2, D215 step 3, DoD 2 |
| risk 2 | Repo visibility and remote state checked | §2.2 item 1 |
| risk 3 | Agents can push today | D202 push guard, D214 per-repo PAT; "Pull requests" permission dropped; dry-run caveat |
| risk 4 | Dry run by default for every paid target | D204, M-1 |
| risk 5 | Paid outputs not ignored | §2.2 item 7 (`/tmp/` was already ignored on int6) |
| risk 6 | Where P6b runs; key spread | D202, A-2, A-6 |
| risk 7 | Mode R/W contradictions | D200, S-10, §9 mode notes |
| risk 8 | Wrong third-party comparison | D212, H-9 |
| risk 9 | "Single stream" wording false | D205 `--gate-concurrency 1`, `host_env` |
| risk 10 | Record ledger misses spend | D208 (`/key` delta, budget-skip refusal, no host ports) |
| risk 11, owner-ux 12 | Date-based resume keys | D204 fixed keys |
| risk 12 | Tape shows the wrong version | D216 and D234 (the bump right before B8, on the P6b branch), H-8, the version checks |
| risk 13 | Sanitizer erases the host stamp | D203 bare hostnames, both base-URL keys |
| risk 14 | Dataset source and leakage | §2.2 items 2–4, D206 source disjointness |
| risk 15 | G2 order lets a key spend | G2 before any credit; disconnect step |
| risk 16 | Minimum plan relies on the key limit | D201 minimum plan |
| risk 17 | G0 too narrow; email in the repo | G0 word list; drafts in `$ORCH` |
| risk 18 | `reverify` claims uncovered | S-5, S-8 |
| risk 19 | No `cost_source` in measured | §2.3b, D204 |
| risk 20, owner-ux 6 | Commit to `main` contradicts D214 | fix-branch PR; minimal ruleset at S3a |
| risk 21 | Re-runs undisclosed | `sources.attempts` |
| risk 22 | Public repo content | D216 grep gate, Covenant contact, talking-points choice |
| risk 23 | Agreement without n | D213 |
| risk 24 | Key label printed; `jev-check` hits the wrong stack | D211 mask; B3 card |
| risk 25 | `eval.yml`; CHANGELOG date; Close steps | S-7, D216, G7 |
| owner-ux 1 | No video plan | D210, V-7, H-6 |
| owner-ux 2 | Post filled by hand | D213, S-4, S-9 |
| owner-ux 3, 14 | Scattered steps; voice approvals | Sittings (§0.2, D217); one open card |
| owner-ux 4 | Mid-run limit raise | One $8 limit, plus caps lowered from `limit_remaining` |
| owner-ux 5 | G0 not doable; no end to mode W | Exact URLs checked at Step 0; 7-day rule; plain-words mode R |
| owner-ux 7 | Log pasting | Actions read; `ci_logs.py`; dry-run caveat |
| owner-ux 8 | WSL-to-Windows files | `explorer.exe`, `make clip`, named paths |
| owner-ux 9 | Privacy toggle unnamed | §2.2 item 5, G1 item 4 |
| owner-ux 10 | Hidden build; wrong stack for B3 | A-5 rebuild with OK; B3 card |
| owner-ux 11 | Redundant curl; prompt details | G3, G7 |
| owner-ux 13 | No visuals before release | Gate B post preview; C2 visual approval before push |
| owner-ux 15 | Slow comparison review | G5 quoted sentences; "accept all except" |
| owner-ux 16 (fixes) | Cloud recipe gaps | D218 (sslip.io, pull not build, apt, engine 1g, cost label, Oracle rejected) |
| owner-ux 17 | Small owner steps | G9, G10, G12, G13 |

### Rejected or changed, with reasons

| Source | Suggestion | Decision and reason |
|---|---|---|
| draft | "No P5 branch defines a D number above D141; P6 adds D142–D158" | **Rejected.** int6 merge `4828d4a` already defines D142–D144, and P5 integration will add more. P6 uses D200+ (D219 is the last), with a Step 0 check. |
| owner-ux 16 | Cut the CLOUD stream from P6a | **Changed.** The recipe stays, because the P6 scope includes a cloud deploy recipe, it costs $0, and it runs as a queued light cut line that cannot delay Gate A. The deploy stays optional and after the release. All the practical fixes are adopted. |
| owner-ux 5 | Send the permission email whatever G0 finds | **Changed.** Asking permission and then publishing without waiting for the answer is worse than not asking. Mode A sends a courtesy note (FYI, tagging, a preview); only a found or unclear clause triggers the permission request (mode W). |
| owner-ux 3 | Add the $10 credit in S1 | **Changed.** The credit moves to S2 right after G2, so G2's unlimited Connect key cannot spend (risk 15). Nothing needs credit before B3, and credit may expire after a year. |
| risk 3 | Unset pushurl at each push window and set it back; warn that `reject` affects other repos | **Changed.** pushurl stays `DISABLED`, and approved pushes use the explicit URL. That is simpler and never leaves the guard off. There is no global `reject` at all: `useHttpPath` plus a path-scoped approve leaves the owner's other credentials alone. |
| risk 9 | Alternatively, word the latency "concurrency 4" | **Not chosen.** `--gate-concurrency 1` costs the same and gives the figure a reader expects. |
| risk 12 | Alternatively, record the version separately | **Not chosen.** It would mean editing the tape (forbidden by D209) or showing two versions. The bump moves before the recording instead (at Gate A in the plan; right before B8 on the P6b branch since D234, so the pull request after Gate A carries no release). |
| risk 10 | Gateway-level allowlist for the upstream model | **Changed.** No registered key exists for it, and adding one is a contract change for about 4 calls. trafficgen enforces the model and `max_tokens` (tested), and the `/key` usage delta counts the spend. |
| risk 21 | List refused run ids in `index.json` | **Changed.** They go in `measured.json` `sources.attempts`. Refused runs never reach `docs/benchmarks/`, and `index.json` lists published results only. |
| risk 7 | `PAGES_TAG_DATASET` in all modes | **Changed.** It is built only if G0 gives mode R (S-10), so mode A keeps P5 D124's rule that a tag ships a recording or fails. |
| risk 22 | Move the interview talking points out of the repo | **Changed.** The owner chooses at S1 (default private, H-7). SPEC §8.2 lists the file. |
| owner-ux 7 | `scripts/ci_logs.sh` in the repo | **Changed.** It lives in `$ORCH`. A script that reads the owner's credential store does not belong in a public repo. |
| owner-ux 1 | Record every video beat inside B8 | **Changed.** The stack beats are recorded in B8; the hero, replay and trace beats come from the recorded Pages build at C2, because that site exists only after C1. MP4s are not committed (repo size). |
| owner-ux 2 | Agreement beside the Arize 0.87 | **Rejected, then changed by D233.** That figure is for a tuned threshold with a contamination caveat, but 0.76 is no like-for-like figure either: our bench flags at groundedness 0.8 (Arize's tuned cutoff) or contradiction 0.7. Both Arize figures now stand beside ours, labelled, neither as like for like (risk 8). |
| owner-ux 10 | The lead rebuilds the owner's demo at Gate A | **Changed.** Only with the owner's OK (rule 8: owner stacks are untouched). |
| owner-ux 14 | Any reply containing "go" approves | **Changed.** Kept, but ambiguous replies are asked again, and only the open card is approved. |
| measure | `BENCH_N ≈ 240–260` so calibration keeps n ≥ 200 | **Rejected.** Calibration fits on a separate train sample (D206), so the bench stays at 200 and its test split stays held out. |
| measure | Add `connect_ms` to `studio.Usage` and subtract it | **Rejected.** `--warm` measures what a warm gateway sees without changing the shared `internal/studio` runner. |
| owner-cloud | $5 credit, a $2 key limit raised to $4.50 before the bench | **Rejected.** A mid-run manual raise is error-prone, and the total is too tight. Replaced by D201. |
| launch | Install `gh` and use `gh auth login` and `gh release create` | **Not default.** `gh` is optional. The web UI and a per-repo PAT suffice, and attestations show in the repo's Attestations view or through `docker buildx imagetools inspect`. |
| owner-cloud / owner-ux | Hetzner price labelled [third-party] or [vendor] | **Resolved.** [vendor price page; re-check at deploy]. |

---

**Key paths** (owner home shown as `~`)
- `~/projects/TrustAgent-int6/docs/v2/P5-PLAN.md`: §19 at :1425, §21 at :1517, D99–D141; merge `4828d4a` holds D142–D144
- `~/projects/TrustAgent/docs/v2/SPEC.md`: P6 at :2459-2480; §8.2 at :2877-2904; Appendix D at :3037; Appendix G at :3179
- `~/projects/TrustAgent-int6/mk/{py.mk:57-111,go.mk:66-100,release.mk,launch.mk}`
- `~/projects/TrustAgent-int6/backend-python/evals/{bench.py,judges.py,report.py,budget.py,runner.py,datasets/ragtruth.py,.gitignore}`
- `~/projects/TrustAgent-int6/backend-go/cmd/{gate-eval,calibrate,jevcheck/main.go:262-263,trafficgen,export-replay}`
- `~/projects/TrustAgent-int6/backend-go/internal/{recording/recording.go,recording/sanitize.go,policy/readiness.go,decision/calibrate_file.go,config/dotenv.go}`
- `~/projects/TrustAgent-int6/contracts/claims/{value-claims.v1.json,measured.json,compare.v1.json}`
- `~/projects/TrustAgent-int6/.github/workflows/{pages.yml:56,eval.yml:38}`, `docker-compose.record.yml`, `docker-compose.yml:264,345`
- `~/projects/TrustAgent-int6/frontend-react/src/domain/liveScenarios.ts:37`, `frontend-react/scripts/build-demo-recording.ts:65`
- `~/projects/TrustAgent-w6-dxdocs/{scripts/check-claims.mjs,docs/launch/linkedin-post.md,docs/launch/demo-script.md}`
- `~/projects/TrustAgent-w6-go/backend-go/cmd/trafficgen/record.go`
---

## 16. Carry-overs from P5 (binding addendum, added 2026-10-10 after P5 merged at b39e61c)

P5 passed its three review rounds; these non-blocking items from the final round go to P6a. Each must be fixed (or rejected with a reason recorded in SPEC Appendix H) before Gate A. Launch-honesty items belong to SHIP; UI items to the stream that owns the file (SHIP takes landing/README/launch copy; VEND takes replay/recording consumers; otherwise the lead).

| id | from | item | owner | Step 0 status |
|---|---|---|---|---|
| P5C-1 | design r3 | Latency headline is lower than its own caveat (Hero.tsx latency strip, README 'Fast enough to sit in the request path' bullet; claims jev_p50_latency_openrouter and fast_check_latency_approx). It shows 'P50 0.23 s on OpenRouter' / 'about 0.2 s' [Third-party], then says independent reports range from about 0.3 s to 0.9 s median, so the headline is below every independent figure it cites. Also, OpenRouter's latency is labelled Third-party while OpenRouter's faithfulness test is labelled Vendor (distributor). LEAD REQUEST (contract-owned): lead with the range, e.g. 'about 0.2–0.9 s median [third-party]', or give the label distinction a one-line reason. | lead + SHIP | **Contract part done at Step 0**: the unsourced 0.3–0.9 s range is dropped from both claims' caveats (with P5C-23), so the headline no longer sits below its own caveat; `jev_p50_latency_openrouter`'s basis gives the label reason (OpenRouter measures latency on the traffic it routes: third-party; its faithfulness test evaluates a model it distributes: vendor). README, JEV-ACCESS and ARCHITECTURE caveats follow. SHIP checks the Hero latency strip still reads well (S-4). |
| P5C-2 | design r3 | Hero card's '212 ms' (D168, Simulated) contradicts the replay record it links to. The same request (trace 01a0e73e) shows total 264 ms in Traffic and '4 ms' in the glass box, and the Sources entry says 'Computed in this project', though the number is illustrative. LEAD REQUEST: take the time from the tape (refundTrace generation, timings.total_ms, still Simulated) or drop the time from beat 2. Also have the Sources footer say 'Illustrative, from the demo' for simulated claims. | SHIP | Landing hero (`HeroCard.tsx`, `Sources.tsx`). If the time is taken from the tape, the `hero_card_latency_ms` edit is a LEAD REQUEST; dropping the time needs only the claim's removal (also a LEAD REQUEST). |
| P5C-3 | design r3 | 'Measured by us: not yet measured' sits in the hero's first viewport (Hero.tsx). The Cost section's 'Our measurements' block already says this. Until bench-publish runs, keep it there only, so the first thing a LinkedIn visitor reads is not 'not yet measured'. | SHIP | Landing hero. |
| P5C-4 | design r3 | The README, linkedin-post.md and interview-talking-points.md measured blocks (generated by scripts/check-claims.mjs) have 7 rows of 'not yet measured [measured]'. Tagging an absent value [measured] reads as a contradiction. The overhead rows also carry internal jargon ('not adopted yet: the latest laptop load run is in GATEWAY.md (Load test)'). Until a run is published: show one sentence instead of two empty tables, drop the [measured] tag on empty values, and rewrite the note in plain words. | SHIP | `scripts/check-claims.mjs` `renderMeasuredBlock` (S-8); then `make claims-write`. The Step 0 contract added three measured rows, so the block is longer until SHIP folds it. |
| P5C-5 | design r3 | SelfHost.tsx 'Without make' command: at 1440 px it is cut off mid-word ('… --ensure && docker comp') with no scroll hint, so it looks broken. Do the same for the SDK line on phones. Either add a right-edge fade or scroll shadow to CodeScroll, or split it into two Command rows (configure.py --ensure / docker compose up -d --wait). | SHIP | Landing `SelfHost.tsx` / `CodeScroll`. |
| P5C-6 | design r3 | On a phone, 'Try the demo' lands on Traffic (out/app_traffic_400 and the replay mobile shots). The tour card is good, but the table shows only Outcome, Time and Reason, with reasons cut to about 9 characters ('States fac…') and route, checked-against and audit off-screen. Below 640 px, either render Traffic rows as cards (outcome chip, reason, checked against) or point phones' Try the demo at the featured trace, which reads well on a phone (replay-trace-mobile-light). | VEND | A replay consumer: point phones' Try the demo at `manifest.featured` (V-6), or Traffic rows as cards below 640 px. |
| P5C-7 | design r3 | The 'Where every number on this page comes from' disclosure shows raw ids in basis text: '(fast_check_cost_per_1k)' and 'pricing/assumptions.v1.json figure studio_draft_per_1k'. Its header says 'Claims as of 2026-09-27', but entries are dated 2026-10-09. LEAD REQUEST (value-claims basis text) to use plain words. Sources.tsx should give the latest as-of date or say 'as of each entry's date'. | lead + SHIP | **Contract part done at Step 0**: plain-word basis text for `fast_check_cost_per_check`, `studio_draft_cost_estimate`, `fast_check_latency_approx` and `same_checks_ratio_measured`. SHIP: `Sources.tsx` gives the latest as-of date or "as of each entry's date". |
| P5C-8 | design r3 | Mobile header focus order (Landing.tsx Header/SectionNav with max-lg:order-last): at 400 px, Tab reaches the section links (visual row 2) before theme, GitHub and Try the demo (visual row 1). Put the nav after the actions in the DOM below lg, or move the actions into the DOM before the nav. Also, the 'Show more section links' chevron still takes focus after the last link has scrolled into view. | SHIP | Landing header focus order. |
| P5C-9 | design r3 | Live Playground deep-audit step: 'Run it yourself to see it: make demo' renders make demo as plain text, while every other surface uses code style, and make demo shows a mock judge there. The static Studio's 'On your own stack … Run it yourself: make demo' mixes 'own stack' (the landing says make up) with the zero-key demo. Suggested wording: 'Try it on the zero-key demo: `make demo`; on your own stack: `make up`'. | lead | **Done at Step 0**: the three own-stack sentences say "Try it on the zero-key demo: `make demo`; on your own stack: `make up`" (contract copy), and the live Playground renders them through `CommandText` (code style); tests assert both commands are code. |
| P5C-10 | design r3 | Label chips have two visual styles. The landing's Estimate, Vendor and Third-party chips are text-only, while the app's carry icons (calculator, store, flask). Pick one, or note the difference in DESIGN.md §12b, so readers see one label system across site and app. | SHIP | Landing chips: unify, or note the difference in DESIGN.md §12b. |
| P5C-11 | design r3 | Static Studio results line: '8 samples · ≈ $0.00011 Estimate · OpenRouter  8 of 100 calls to OpenRouter (checks and AI drafts)'. The bare 'OpenRouter' token plus double space reads like a stray word. Fold it into the counter. | lead | **Done at Step 0**: in the browser Studio the call counter takes the backend name's place in the results line (`TestBench.tsx`); a test fails on a bare "· OpenRouter". |
| P5C-12 | design r3 | Studio Review step (README studio image): the 'Yes means' and 'No means' fields are single-line inputs that cut long text mid-sentence ('… or give'). Use auto-growing textareas like the question field. | lead | **Done at Step 0**: "Yes" and "No" means are growing text areas (`SimpleTab.tsx`, field-sizing), with a test. |
| P5C-13 | design r3 | How-it-works on a phone: the dashed Deep audit box sits under 'Your LLM provider', which suggests it follows the provider. Place it under the gateway box, as on desktop, or add a 'from the gateway, after the reply' arrow. | SHIP | Landing How-it-works on a phone. |
| P5C-14 | design r3 | Replay Overview 'Requests by outcome' draws 4 identical thin bars at 09:00, 09:30, 10:15 and 11:00 on a 2-hour axis; the last bar has no label. Clamp the domain to the tape's span with a little padding, or switch a replay to hourly bins, so it reads as a chart rather than 4 slivers. | VEND | Replay Overview chart (a recording consumer). |
| P5C-15 | verify r3 | frontend-react/src/components/playground/playgroundCurl.ts:7 has a stray '^' at the start of a JSDoc line ('^ *   and an app key...'), added by 7b56907. It is inside the comment, so the build is unaffected; delete the character. | lead | **Done at Step 0** (`playgroundCurl.ts`). |
| P5C-16 | verify r3 | docs/v2/SPEC.md Appendix G D169 and contracts/schema/compare.v1.schema.json (Cell.text) say a cell's text carries no vendor name and that validate.mjs enforces it. It does not: contracts/validate.mjs:724 only rejects 'Not documented (as of' or a leading 'Not documented/As of'. Three cells name their own vendor: observability_judge.inline ('the Langfuse worker'), cloud_groundedness.self_hosted ('Amazon Bedrock is a fully managed...') and small_model_evaluators.cost_class ('Galileo says...'). Either reword D169 and the schema description, or enforce the rule and rewrite those three texts. The fix-round-2 report claimed the rule was enforced. | lead (contract) + SHIP | **Done at Step 0**: `validate.mjs` now enforces D169 (a cell's text names no vendor of its row), with a test; the three texts are reworded without changing what they say. SHIP's S-5 re-verification re-reads them against their sources. |
| P5C-17 | verify r3 | p5-evidence/compare-fetch.tsv has two rows for cloud_groundedness.self_hosted: a stale one with the old guardrails-contextual-grounding-check.html URL and the new what-is-bedrock.html row. Drop the stale row so the log maps one row to each cell's current source. | SHIP | S-5 (`$ORCH/p5-evidence/compare-fetch.tsv` is outside the repository). |
| P5C-18 | verify r3 | Landing comparison at 1440 px: a923112 keeps the vendor name on one line, but the ' · ' separator is outside it. In narrow columns 'Varies ·' ends one line before 'NeMo Guardrails', or '· Galileo' starts a line. Put the separator inside the nowrap span (frontend-react/src/landing/Compare.tsx, VendorCell). | SHIP | Landing `Compare.tsx` `VendorCell`. |
| P5C-19 | verify r3 | frontend-react/src/landing/costModel.ts auditShareWords: the capped wording 'every answer (the parts below add up to more)' is reachable with calculator inputs, but the parts follow in the same sentence after the colon, not below. Say 'the parts add up to more'. | SHIP | Landing `costModel.ts`. |
| P5C-20 | verify r3 | The app entry is 247.8 of 249.9 kB (it was 247.4 at I-5); 2.1 kB of headroom is left before the D112 budget. | lead | Budget watch: every stream that touches the app entry reports its size; the lead checks `npm run size` at each merge and at Gate A. The Step 0 gate's build is the baseline: **app entry 248.1 of 249.9 kB** (1.8 kB left; the contract's three new claims and the copy edits took 0.3 kB). |
| P5C-21 | verify r3 | The published load run (20261010T131516Z-f60a8a4) predates the fix-round gateway changes (Tier-0 refscan, the breaker's key cause) and ran beside the owner's demo stack. The launch checklist already calls for a quiet re-run at the release commit before bench-publish (P6). load-run.v1 still has no field for image revision or concurrent load. | lead | **Contract part done at Step 0**: `load-run.v1` `environment.image_revision` and `concurrent_load`, written by `scripts/load/report.py` and named in the GATEWAY.md block. The quiet re-run is P6b (§2.1). |
| P5C-22 | verify r3 | Docs still say 'ingest key' (docs/GETTING-STARTED.md:191, 210, 219; docs/GATEWAY.md:394) while the UI and landing now say 'app key'. The WALKTHROUGH glossary maps the two, but the wording could match. | lead | **Done at Step 0**: GETTING-STARTED and GATEWAY say "app key". |
| P5C-23 | verify r3 | The jev_p50_latency claims' caveat (contracts/claims/value-claims.v1.json:141, 154, shown in README:38 and JEV-ACCESS) gives 'independent reports range from about 0.3 s to 0.9 s median' with no source. Cite the reports, or drop the range. | lead | **Done at Step 0** with P5C-1: the range had no source, so it is dropped (Appendix H). |
| P5C-24 | verify r3 | Owner edit: the CLAUDE.md Commands block should list make first-run-proof, golden-path, first-run-onboarding (D165), bench-tier0-publish and bench-tier0-report (D167), and docs-links (CO-21). | lead | **Done at Step 0**: CLAUDE.md lists `first-run-proof`, `bench-tier0-publish` / `bench-tier0-report` and `docs-links` (with P5C-25). |
| P5C-25 | coverage r3 | DD-5 'CLAUDE.md new commands' is only partly current. CLAUDE.md (a DX-DOCS file per §15) lists the commands from DD-5 time, but not the targets added later: make first-run-proof, make golden-path and make first-run-onboarding (D165), make bench-tier0-publish and make bench-tier0-report (D167), and make docs-links (CO-21). Round 2 reported that it could not edit CLAUDE.md, so the owner or the lead adds these lines to the Commands block. | lead | **Done at Step 0** (see P5C-24). |
| P5C-26 | coverage r3 | SPEC Appendix G's opening sentence (docs/v2/SPEC.md about line 3181) still says 'D142-D168 below', but the appendix now also records D169-D172 under 'Decisions made in the second fix round'. The intro could say D142-D172. | lead | **Done at Step 0**: Appendix G names D142-D172. |
| P5C-27 | coverage r3 | CO-20's '/api/policy version and hash unchanged' check applies only if the I-6 stack screenshots (p5-shots) are ever committed. The committed captures come from the static build, which has no /api/policy, and the capture spec only checks for repeated questions. If stack captures are committed later, add the before/after policy-hash check. | VEND | If stack captures are ever committed (V-6), add the before/after `/api/policy` hash check. |
| P5C-28 | coverage r3 | contracts/claims/compare.v1.json: all 20 vendor cells are still reviewed:false. This is the owner's review (§21 #7, D113/D169) and blocks check-claims --launch until done. It is not a P5 coverage gap. The round-2 report said 'five cells' became Not documented; the data has 4 not_documented cells plus Bedrock self-host as 'no', which matches the 7a25f6b diff. | SHIP + owner | S-5 prints the G5 table; the owner reviews every cell at G5 (S2). |
| P5C-29 | coverage r3 | Commit a923112 (Compare.tsx: product name kept on one line) landed after round 2's make check, which ran at 19:38. I re-ran vitest on src/landing (57 passed); the next full make check covers the rest. Round 2 also did not re-run make e2e-faults after 80c2954, which changed only the test helper; watch the I-4 fault line in CI. | lead | The Step 0 gate's full `make check` covers a923112; the I-4 fault line is watched in CI at S3a. |

Also open from P5 (owner or P6): the 20 comparison cells need the owner's review (`reviewed:false`); the OG card and README hero need a recorded tape; the `github-pages` environment needs a `v*` tag rule before the tag deploy; a sourced Sonnet price pair (D149); the live Playground's text moves into `copy.playground_live.ui.*` (D163).

**Owners of the other open items (Step 0):**
- the 20 comparison cells: SHIP S-5, then the owner at G5 (same as P5C-28);
- the OG card and README hero from a recorded tape: VEND V-6 (consumers of `featured`), captured after B8;
- the `github-pages` environment's `v*` tag rule: the owner at S3a (D215 step 1);
- a sourced Sonnet price pair (D149): lead, before Gate A. Step 0's free read lists Sonnet entries on `/api/v1/models`; adding one is a dated pricing contract edit that also changes the landing calculator's judge list, so it lands with SHIP's landing check. **Done in fix round 1:** Claude Sonnet 5.5 at $2 / $10 per million tokens, Anthropic and OpenRouter, read 2026-10-23 (`prices.v1.json` version 2026-10-23);
- the live Playground's text into `copy.playground_live.ui.*` (D163): lead, before Gate A (contract copy plus `src/components/replay/live/liveText.ts`). **Deferred to v2.0.x in fix round 1** (SPEC Appendix H): the copy contract is bundled whole into the app entry and the landing's first load, so the move would add about 1.4 kB gzip to both budgets (the app entry had 1.8 kB left) for no visible change.
