<!--
The body of the release pull request, feat/v2-jev-gateway → main (step C6). Copy it with
`make clip FILE=docs/launch/release-pr.md` and paste it into the "Open a pull request" form.
GitHub hides this comment. Links are absolute (a PR body has no relative links). Before copying: the TO FILL lines are filled at C4 (the measured run,
the tape, the terms mode), and the PR is merged with "Create a merge commit" only (D215).
-->

# TrustAgent 2.0.0 "Jev gateway"

TrustAgent 2.0 turns the 1.x async auditor into a **hallucination gateway**. Apps point their
OpenAI or Anthropic SDK at a Go proxy; every answer gets deterministic Tier-0 checks and one
**Fast check (Jev)** call that asks all of the route's checks at once, and the route's policy
observes, flags, blocks or falls back. Groundedness is checked against what the request already
carries (no RAG by default); an asynchronous deep audit (LangGraph) explains the risky answers
claim by claim; and a redesigned dashboard, a static demo site and Evaluator Studio sit on top.

The full list of changes is the `2.0.0 "Jev gateway"` section of [CHANGELOG.md](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/CHANGELOG.md);
the design is [docs/v2/SPEC.md](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/v2/SPEC.md).

## Phases

Each phase ran in parallel worktrees, was merged on an integration branch after its own gate, and
then into `feat/v2-jev-gateway`. The branch keeps that history, so this PR is merged with a merge
commit (no squash, no rebase).

| Phase | What it delivered | Plan |
|---|---|---|
| P0 / P0.5 | Baseline, CI split per stream, Makefile fragments, toolchain upgrade | [PLAN.md, phase 8](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/PLAN.md#phase-8--baseline-and-toolchain) |
| P1 / S0 / P1b | `contracts/`, proto v2, vendor spikes, the zero-key walking skeleton | [phase 9](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/PLAN.md#phase-9--contracts-and-walking-skeleton) |
| P2 | Gateway, engine, dashboard and DX cores | [phase 10](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/PLAN.md#phase-10--cores) |
| P3 | The enforcing pipeline, streaming, policies, the redesigned pages (M-Demo gate) | [P3-PLAN](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/v2/P3-PLAN.md) |
| P4 | Security, the credential store, Connect OpenRouter, Studio, calibration, Savings | [P4-PLAN](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/v2/P4-PLAN.md) |
| P5 | The static site with live checks, the landing page, release images and tooling | [P5-PLAN](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/v2/P5-PLAN.md) |
| P6 | Measured benchmarks, the recorded demo, the release tooling and launch kit | [P6-PLAN](https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/docs/v2/P6-PLAN.md) |

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

After the merge: the 2.0.0 tag, the GHCR images made public, Pages turned on, and the GitHub
release with its notes from `make release-notes TAG=v2.0.0`.
