# TrustAgent v2: status and handoff

Last updated: 24 October 2026. Read this first when you pick the project up in a new session,
for example a Claude cloud session at claude.ai/code. It says what is done, what runs next, and
what only the owner can do. The detail is in [`P6-PLAN.md`](P6-PLAN.md): the paid-step cards
(§8), the release steps (§9), the owner guides (§10) and the lead decisions (§1, D200 onward;
D220 to D233 record the move to the cloud and the review rounds of 23 October).

## Where things stand

- **`feat/v2-jev-gateway` holds everything.** All of v2 (phases P0 to P5), the CI fixes since,
  the dependency-security update (Go 1.26 toolchain, gRPC 1.84, LangGraph 1.x and the other Python
  updates, a narrow Trivy secret allow-list; PR #45, every check green) and the finished P6a
  preparation. **Build on this branch.**
- **`main`** holds v2 P0 to P5 and the CI fixes. It gets the rest when the owner merges pull
  request [#46](https://github.com/timkush1/TrustAgent/pull/46) on GitHub (A-8). Until then public `main` has no "How it was built" disclosure,
  README demo links that point at a Pages site that is not on yet, and a few local paths and a
  personal address in old design notes (D220, D223); the PR fixes all of that in the tree. It
  carries no 2.0.0: the version stays 1.0.0 until the release (D234).
- **P6a passed Gate A on 24 October 2026.** Every paid command was rehearsed against mocks: each
  is a dry run until `CONFIRM=1`, has a dollar cap, reads the key's remaining credit, and refuses to
  label mock or non-vendor output as measured or recorded. Four review rounds plus a final round:
  the independent verifier (`make check`, scanners, no key anywhere) and the task-coverage check
  passed; the launch-honesty review's last two findings are owner decisions, listed under "Before
  you merge" below. Not re-run: R-5 (the public demo under `docker stats`), planned for the cloud
  session before C14.
- **What is left is P6b and P6c:** measure with the owner's OpenRouter key (capped at $8),
  publish the measured numbers, release v2.0.0, and post on LinkedIn.

## Branches

| Branch | What it holds |
|---|---|
| `feat/v2-jev-gateway` | **Build on this.** Everything, including P6a. Claude pushes here. |
| `main` | The public default branch. It moves only when the owner merges a pull request on GitHub (D220). |
| `feat/v2-int7`, `feat/v2-w7-{meas,vend,ship,cloud}` | The P6a integration and stream branches, all merged. History only. |
| `fix/deps-security` | The dependency-security fix (PR #45), already inside `feat/v2-jev-gateway`. |
| `v1.0.0` (tag) | TrustAgent 1.0.0, the async auditor of June 2026. |

## Phase status

| Phase | Status |
|---|---|
| P0 to P3: contracts, walking skeleton, core services, the working gateway and dashboard | Done (1 Oct 2026) |
| P4: encrypted credentials, Connect OpenRouter, setup wizard, Settings, cost ledger and caps, Savings, evaluators, labels and calibration, Studio | Done (9 Oct 2026) |
| P5: the public static site, release and Pages workflows, fault and load tests, Tier-0 benchmarks, docs and launch drafts | Done (10 Oct 2026) |
| P6a: measurement, vendor-capture, recording, release and launch tooling, every paid command rehearsed against mocks, four review rounds | Done (Gate A, 24 Oct 2026) |
| **P6b: paid measurement with the owner's key** | **Next.** Waits on the owner's terms check (G0) |
| P6c: publish, release v2.0.0, post | After P6b |

## Moving to a cloud session (owner, once, about 10 minutes; D221)

A cloud session has Docker and about 16 GB of memory, so it runs the whole stack, the tests and
the paid steps. It pushes branches through the Claude GitHub app, so no token is needed. It cannot
push tags or merge into `main`; those stay on GitHub's web pages, which work from a phone.

1. Open https://claude.ai/code, choose the repository `timkush1/TrustAgent`, and install the
   Claude GitHub app if it asks.
2. In the environment's settings:
   - **Network access: Custom.** Keep the default list and add `openrouter.ai`. If a later
     download fails because a domain is blocked, the session names it; add it here.
   - **Setup script:** `sudo apt-get update && sudo apt-get install -y ffmpeg` (for the video).
   - **Environment variables:** none yet. The OpenRouter key goes here at G3 (below).
3. Start a session with: *"Read docs/v2/STATUS.md and continue TrustAgent P6 from where it says.
   Follow CLAUDE.md."*

## What only the owner can do, in order

### Before you merge the pull request (5 minutes)

1. **Your legal name for the licence: done** (24 October 2026). `LICENSE`,
   `frontend-react/package.json` and `backend-python/pyproject.toml` name **Tim Kushmaro**.
2. **Turn on private vulnerability reporting:** repository **Settings → Code security → Private
   vulnerability reporting → Enable**. The new `SECURITY.md`, `CONTRIBUTING.md` and issue forms
   send security reports there, and it is off today.
3. **Your interview talking points: private (done, 24 October 2026, H-7).** They left the
   repository and are kept by the owner outside it.

### Then: merge the pull request

Open https://github.com/timkush1/TrustAgent/pull/46 → **Merge pull request** → **Create a merge commit** → Confirm. That brings
everything above onto `main` (PR #45 then shows as merged too).

### S1: no money, about 45 minutes (P6-PLAN §10)

- **G0, the terms check.** A third-party write-up says TypeSafe's Master Customer Agreement
  (§2.3(f)) may forbid publishing benchmarks of Jev. Read the pages listed in §10 G0, **including
  that agreement**, and search them for the listed words. Then send one of the two emails (the
  drafts were handed to the owner separately; they are not in the repository):
  - nothing found, **and you read the agreement:** the courtesy note. This is **mode A**.
  - something found, unsure, or you could not find or open the agreement: the permission
    request. This is **mode W** (D231). In mode W the paid Jev steps wait (D222). If there is no
    answer in 7 days, choose to keep waiting or **mode R** (no Jev figures published; the site
    shows the simulated replay).
- **G1, answers** (skip what you already answered before the merge; the legal name is answered): any other
  `timkush1.github.io` sites; an "open to roles" line in the post, yes or no; launch day; audience (Israel, or US and EU); and the build line used in
  the post, README and release notes (D227, D230): *"Designed and directed by me, built with AI
  coding agents (Claude Code) working to the plans, tests and reviews I set."* Keep it or reword it.
- **G1, OpenRouter account** (§10 G1 step 4): sign in, **no credit yet**, check the privacy page.
- **The `main` ruleset** (§10 G9 step 4) as soon as you can, with the admin bypass **for pull
  requests only** (D220).

Say back: **"S1 done: mode A/W/R, sites none/…, roles yes/no, launch <day>, audience …, build
line ok/…"**.

### S2: the paid day, about 2.5 hours, mostly waiting (§8, §10)

- **G2, the localhost Connect check** needs a browser on the same machine as the stack, so it
  runs only on the laptop (5 minutes, no credit). It is optional (D221): the Pages check G11
  settles Connect anyway.
- **G3, credit and the key.** Add $10 of credit (Auto Top-Up off) and create the key
  `trustagent-p6` with a **credit limit of 8**. **Never paste it into the chat.** Put it in the
  cloud environment's settings as the variable `OPENROUTER_API_KEY`, then start a new session.
- **G4, approve each paid card** (B3 to B8) by saying "go B3", "go B4" and so on. Each card shows
  the command, the cap, the expected cost and "used so far $X of $8". Expected total $3.1 to $5.9
  [estimate].
- **G5,** the review of the 20 comparison cells, while the bench runs. **G6,** the Gate B review:
  "publish", or name what to re-check. **G7,** close the key on openrouter.ai and delete the
  variable from the cloud environment.

### S3: release (§9, §10 G9 to G13)

- G9: repository settings (merge commits, Pages source "GitHub Actions", the `github-pages`
  environment with `main` and a `v*` tag rule). The release PR goes from `feat/v2-jev-gateway`
  into `main`.
- G10: `PAGES_ENABLED=true`, merge, check `GHCR_PUBLIC=true` (the images are already public, D224).
- G11: the Pages Connect check (about $0.0002). C2 also points the README demo links back at the
  site (they say `make demo` until Pages is on, D220).
- **C5a, mode A only:** once the results are pushed, email TypeSafe the measured figures, the run
  link and the post's final text, as the courtesy note promised. **Post at least 3 days later**,
  tag TypeSafe only if they said yes, and put the Haqq link in the post's first comment.
- **The v2.0.0 tag** is created by the owner through a GitHub release (C10, D221): Releases →
  Draft a new release → Choose a tag → type `v2.0.0` → create it on publish, target `main`.
- G12, G13: About, topics, social preview, ruleset checks, code security, the stranger check, then
  the post.

## Numbers so far (and their labels)

- **Measured on the development laptop, vendors simulated:** gateway overhead in flag mode p50
  1 ms / p95 2 ms (`docs/benchmarks/load/`); Tier-0 instant checks about 0.27 ms on 8k-token
  evidence (Go benchmark, `docs/GATEWAY.md`).
- **Not yet measured:** accuracy on a benchmark, cost per 1,000 answers with real vendors,
  calibration error. These come only from P6b runs; `bench-publish` refuses mock output.
- Everything else on the site and in the README is labelled estimate, third-party, vendor or
  simulated, and `make check-claims` enforces it.

## Notes for the cloud session

- **Version 2.0.0 is not bumped yet** (D234), so `main` shows no release that has not happened.
  The bump runs on the P6b branch just before the recording (B8 needs it), with
  `RELEASE_DATE=<planned tag date>` so the CHANGELOG heading carries the real release date; it
  reaches `main` only with the release PR.
- **No Jev output on a pushed branch before G0 says mode A** (D222): every branch is public. Keep
  bench, calibration, capture and recording outputs in their gitignored paths until then.
- **Test database:** where the plan says `$ORCH/pg-url.sh`, start the preinstalled PostgreSQL 16
  (or `docker run postgres:16`) and point `TEST_DATABASE_URL` at it.
- **CI status and logs:** the public GitHub API or `gh`; there is no `$ORCH/ci_logs.py` (D220).
- **Long commands** (bench, record, CI waits) run in the background; a foreground command stops
  after 10 minutes.
- **Open P7 note** (SPEC H-20): `export-replay` takes the vendor hosts a caller passes for a
  recorded tape on trust beyond the mock cross-check.

## Rules for whoever continues

- Follow `CLAUDE.md` and P6-PLAN with its decisions. Contract-owned paths change only
  deliberately.
- **No money without the owner's "go" for that card.** Never print the key.
- Every user-facing number keeps its label; mock output is never shown as measured.
- Never force-push, rewrite history or delete a remote branch or tag.

## Quick start

```bash
make demo        # zero-key demo on http://localhost:3000 (simulated numbers)
make setup-url   # one-time sign-in link for the demo dashboard
make doctor ARGS="--expect demo"
make check       # the local gate (lint, unit tests, contracts, compose config)
```
