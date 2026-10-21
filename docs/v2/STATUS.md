# TrustAgent v2: status and handoff

Last updated: 21 October 2026. Read this first when you pick the project up in a new session
(for example a cloud session). It says what is done, which branch holds what, what runs next,
and what only the owner can do.

## Branches

| Branch | What it holds |
|---|---|
| `main` | v1.0.0, the public version from June 2026. It stays as it is until the v2.0.0 release PR (P6c). |
| `feat/v2-jev-gateway` | **v2 with P0 to P5 merged.** This is the branch to build on. |
| `feat/v2-int7` | P6 integration branch: P6 Step 0 is done (plan, contract PR, seams, free checks, P5 carry-overs owned by the lead). |
| `feat/v2-w7-{meas,vend,ship,cloud}` | P6a stream branches (measurement, vendor captures and recording, release and launch kit, cloud demo recipe). Work in progress; each is merged into `feat/v2-int7` after its review. |

## Phase status

| Phase | Status |
|---|---|
| P0 to P3: contracts, walking skeleton, core services, the working gateway and dashboard | Done (1 Oct 2026) |
| P4: encrypted credentials, Connect OpenRouter (PKCE), setup wizard, Settings, cost ledger and caps, Savings, evaluators, labels and calibration, Studio | Done (merged 9 Oct 2026 after five review rounds) |
| P5: the public static site (landing, replay, live-in-browser Jev with the visitor's key, static Studio), release and Pages workflows, fault and load tests, Tier-0 benchmarks, docs and launch drafts | Done (merged 10 Oct 2026 after three review rounds) |
| **P6: measure with the owner's key, launch, release v2.0.0** | **In progress: P6a (automated preparation, mocks only).** Plan: [`docs/v2/P6-PLAN.md`](P6-PLAN.md) on `feat/v2-int7`. |

Every phase plan is in this folder: [`P3-PLAN.md`](P3-PLAN.md), [`P4-PLAN.md`](P4-PLAN.md),
[`P5-PLAN.md`](P5-PLAN.md), and `P6-PLAN.md` on `feat/v2-int7`. [`SPEC.md`](SPEC.md) stays the
source of truth; its appendices record the errata and lead decisions of each phase.

## How P6 runs

1. **P6a, automated preparation (no money, no pushes).** Four streams (MEAS, VEND, SHIP, CLOUD),
   each followed by an adversarial review, then integration on `feat/v2-int7` with rehearsals of
   every paid command against mocks (P6-PLAN §7), then a verifier, a task-coverage check and a
   launch-honesty check. It ends at **Gate A**: a one-screen "ready to spend" report to the owner.
2. **P6b, owner-in-the-loop runs.** The real OpenRouter key goes only into the main checkout's
   `.env` (never into a chat or a commit). Each paid step is shown as a card with its command,
   cap and expected cost, and runs only after the owner says "go". The key has a credit limit of
   $8; the expected total is about $3 to $6 [estimate].
3. **P6c, finish and release.** Publish the measured results, fill the launch kit, version
   2.0.0, the PR to `main`, the tag, GHCR images, GitHub Pages and the social preview. Every push
   needs the owner's "go push".

## Waiting on the owner (P6-PLAN §10, sitting S1)

- **G0, the terms check.** A third-party article says TypeSafe's customer agreement may forbid
  publishing benchmarks of Jev. No paid Jev run and no publication of Jev measurements until the
  owner has checked the terms (TypeSafe Terms and AUP, OpenRouter Terms §5.1, any Master Customer
  Agreement) and, depending on the result, sent the courtesy note or the permission request.
  The outcome sets mode A (publish), W (wait for permission) or R (no Jev figures published).
- **G1, one-time setup:** the legal name for `LICENSE` (it currently reads differently in two
  files), other `timkush1.github.io` sites, choices for the post, an OpenRouter account (no
  credit yet), `ffmpeg`.
- **Comparison review:** the 20 cells in `contracts/claims/compare.v1.json` are
  `reviewed: false`; `make check-claims-launch` refuses them until the owner reviews each one.
- **Real Connect check (D88):** Connect OpenRouter has been proven against a simulated OpenRouter
  only; the owner checks the real localhost callback once (P6-PLAN §10, G2).
- **Before the tag deploy:** the `github-pages` environment needs a `v*` tag rule.

## Numbers so far (and their labels)

- **Measured on the development laptop, vendors simulated:** gateway overhead in flag mode p50
  1 ms / p95 2 ms (`docs/benchmarks/load/`); Tier-0 instant checks about 0.27 ms on 8k-token
  evidence (Go benchmark, published in `docs/GATEWAY.md`).
- **Not yet measured:** accuracy on a benchmark, cost per 1,000 answers with real vendors,
  calibration error. These come only from P6b runs; `bench-publish` refuses mock output.
- Everything else on the site and in the README is labelled estimate, third-party, vendor or
  simulated, and `make check-claims` enforces it.

## Rules for whoever continues

- Follow `CLAUDE.md` and the current phase plan. Contract-owned paths change only deliberately.
- **Mocks only** until Gate A: tests run without vendor keys (P6-PLAN §0.4).
- **Never push to `main`**, never force-push, never spend money or publish measured Jev numbers
  without the owner's explicit go.
- Every user-facing number keeps its label; mock output is never shown as measured.
- The development laptop has about 8 GB of RAM: run heavy commands one at a time and use
  `make check VITEST_FLAGS=--maxWorkers=2`.

## Quick start

```bash
make demo        # zero-key demo on http://localhost:3000 (simulated numbers)
make setup-url   # one-time sign-in link for the demo dashboard
make doctor ARGS="--expect demo"
make check       # the local gate (lint, unit tests, contracts, compose config)
```
