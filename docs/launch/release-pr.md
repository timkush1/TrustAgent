<!--
The body of the release pull request, feat/v2-jev-gateway → main (step C6). Copy it with
`make clip FILE=docs/launch/release-pr.md` and paste it into the "Open a pull request" form.
GitHub hides this comment. Links are absolute (a PR body has no relative links). Before copying: the TO FILL lines are filled at C4 (the measured run,
the tape, the terms mode, what P6 brings in that mode), and the owner merges it on GitHub with "Create a merge commit" only (D215, D220).
main already holds v2 phases P0 to P5: the owner made it public on 2026-10-21 (merge a582549, D220),
and P6a too once the owner has merged the pull request the lead opens right after Gate A (D220),
so this PR brings P6b and P6c (and P6a, if that one was not merged), the fixes made since, and the 2.0.0 version.
-->

# TrustAgent 2.0.0 "Jev gateway"

TrustAgent 2.0 turns the 1.x async auditor into a **hallucination gateway**. Apps point their
OpenAI or Anthropic SDK at a Go proxy; every answer gets deterministic Tier-0 checks and one
**Fast check (Jev)** call (Jev is TypeSafe AI's decision model, reached through OpenRouter) that
asks all of the route's checks at once, and the route's policy observes, flags, blocks or falls
back. Groundedness is checked against what the request already carries (no RAG by default); an
asynchronous deep audit (LangGraph) explains the risky answers claim by claim; and a redesigned
dashboard, a static demo site and Evaluator Studio sit on top.

`main` has carried phases P0 to P5 since 2026-10-21. This PR adds P6: TO FILL at C4 by the terms
mode (mode A: the measured benchmark and the recorded demo; mode R: the measured figures that are
not Jev's and the simulated demo), with the release tooling and launch kit if `main` does not hold
them yet, and makes it version 2.0.0. The full list of
changes is the `2.0.0 "Jev gateway"` section of [CHANGELOG.md](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/CHANGELOG.md);
the design is [docs/v2/SPEC.md](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/v2/SPEC.md).

## How it was built

Designed and directed by one developer, built with AI coding agents (Claude Code) to the plans
below, which the developer approved, and the decisions the developer made; the agents drafted the
plans, wrote the code and the tests, and ran the reviews. Each phase ran in parallel worktrees,
merged on an integration branch only after its own gate, and was checked by separate review agents
before it reached `feat/v2-jev-gateway`. The developer authorized the pushes and every paid run and
merges this PR. The branch keeps that history, so this PR is merged with a merge commit (no
squash, no rebase).

| Phase | What it delivered | Plan |
|---|---|---|
| P0 / P0.5 | Baseline, CI split per stream, Makefile fragments, toolchain upgrade (on `main` since 2026-10-21) | [PLAN.md, phase 8](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/PLAN.md#phase-8--baseline-and-toolchain) |
| P1 / S0 / P1b | `contracts/`, proto v2, vendor spikes, the zero-key walking skeleton (on `main`) | [phase 9](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/PLAN.md#phase-9--contracts-and-walking-skeleton) |
| P2 | Gateway, engine, dashboard and DX cores (on `main`) | [phase 10](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/PLAN.md#phase-10--cores) |
| P3 | The enforcing pipeline, streaming, policies, the redesigned pages (on `main`) | [P3-PLAN](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/v2/P3-PLAN.md) |
| P4 | Security, the credential store, Connect OpenRouter, Studio, calibration, Savings (on `main`) | [P4-PLAN](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/v2/P4-PLAN.md) |
| P5 | The static site with live checks, the landing page, release images and tooling (on `main`) | [P5-PLAN](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/v2/P5-PLAN.md) |
| P6a | The release tooling, the launch kit and the build disclosure, rehearsed on mocks (TO FILL: "on `main` since <date>" if the pull request after Gate A was merged, else "this PR") | [P6-PLAN](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/v2/P6-PLAN.md) |
| **P6b and P6c (this PR)** | TO FILL by the terms mode: mode A, the measured benchmark and the recorded demo; mode R, the measured figures that are not Jev's and the simulated demo. Version 2.0.0 either way | [P6-PLAN](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/v2/P6-PLAN.md) |

## Breaking changes

Upgrading from 1.x: the metric names (`trustagent_engine_*` and `trustagent_gateway_*`),
WebSocket v2 only, the gateway's key in `X-TrustAgent-Key`, test mode off by default, no
`VITE_TRUTHTABLE_API_KEY`, `/health` as liveness only, app keys required on `/v1`, the compose
service `engine`, ports bound to `127.0.0.1`, images from GHCR, and no downgrade after migration
0002. Each one, with what to do, is under *Upgrading from 1.x* in the CHANGELOG.

## How to verify

```bash
git fetch origin && git switch --detach origin/feat/v2-jev-gateway
make check                       # lint, unit tests, contracts, compose config (the local CI gate)
make demo                        # zero keys: mock checks and judge, every figure labelled Simulated
make check-version RELEASE_TAG=v2.0.0
make check-claims-launch         # recorded tape, measured run, reviewed comparison, social card
python3 scripts/check_recording.py frontend-react/demo-data/recorded --require-recorded
```

The measured figures come from one published run, on vendor hosts, with its run file in
`docs/benchmarks/results/`: TO FILL (run id, snapshot, date). The demo tape is TO FILL (recorded
on, snapshot, TrustAgent version). The terms check is in
[docs/launch/terms-check.md](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/launch/terms-check.md): mode TO FILL.

## Checks

- [ ] Every required check is green (the list is [docs/launch/ruleset.md](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/launch/ruleset.md)).
- [ ] `pages.yml` and `release-images.yml` passed as build-only runs on this PR.
- [ ] Full-history gitleaks is clean, and the hygiene grep (`make hygiene-check`) prints nothing.
- [ ] `npm audit --omit=dev --audit-level=high` is clean in `frontend-react`.
- [ ] No figure in the README, the docs or the site is shown without its label, and nothing from
      a mock is labelled measured (`make check-claims-launch`).
- [ ] The owner reviewed the comparison table, the social card, the GIF, the video and the post.

After the merge: Pages deploys the demo (the recorded tape in mode A, the simulated one in mode R), the GitHub release creates the 2.0.0 tag (the
owner makes it in the web UI) with its notes from `make release-notes TAG=v2.0.0`, and the tag's
build publishes the semver images (the `edge` images on GHCR are public already).
