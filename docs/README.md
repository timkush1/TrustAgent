# TrustAgent Documentation

Start with the [project README](../README.md) for the overview and quick start. This folder holds the deeper material, organized below.

## Understand the system

| Doc | What's inside |
|---|---|
| [GATEWAY.md](GATEWAY.md) | The gateway reference: modes, response headers and stream trailers, streaming limits, fail semantics, block readiness and bodies, fallback, the decision long-poll, passthrough and inject credentials, error shapes, test mode, capacity, metrics |
| [EVALUATORS.md](EVALUATORS.md) | The built-in evaluators, attaching them to routes (active and shadow), and the calibration workflow |
| [ARCHITECTURE.md](ARCHITECTURE.md) | System + sequence diagrams, component boundaries, key trade-offs (1.0; the 2.0 rewrite is planned for P5) |
| [WALKTHROUGH.md](WALKTHROUGH.md) | A guided tour of the whole project for an engineer: product logic, every service, DevOps, security, tests and CI (mostly 1.0; rewrite planned for P5) |
| [KB-DESIGN.md](KB-DESIGN.md) | The dual-gate (VERITAS-lite) knowledge base: entailment gating, quarantine, contradiction detection, hybrid retrieval |
| [DECISIONS.md](DECISIONS.md) | Architecture Decision Records (ADRs) — why the notable choices were made |

## Run & operate it

| Doc | What's inside |
|---|---|
| [GETTING-STARTED.md](GETTING-STARTED.md) | Running TrustAgent locally on WSL: the zero-key `make demo`, real mode with your keys, signing in with the one-time setup link, memory, troubleshooting |
| [DEMO.md](DEMO.md) | The zero-key demo: starting it, the setup link, a three-minute tour, the 26 scenarios, checking it from the terminal, sharing it (static replay site or a tunnel) |
| [JEV-ACCESS.md](JEV-ACCESS.md) | Getting Jev access for the v2 fast check: OpenRouter key and credits, costs, settings, errors and fixes |
| [CONFIGURATION.md](CONFIGURATION.md) | Every configuration key with its default, who reads it, whether it is secret and whether it needs a restart (generated from the key registry; `proxy -check-config` checks a configuration against it) |
| [CI-CD.md](CI-CD.md) | The GitHub Actions pipelines (CI, Security, Eval) and Dependabot — what each job does and how to read a failure |

## Quality & safety

| Doc | What's inside |
|---|---|
| [EVALUATION.md](EVALUATION.md) | Evaluation methodology, metrics, the CI regression gate, and the v2 benchmark harness (`make bench-lite`, `bench-publish`, `eval-live`, `calibrate`) |
| [SECURITY.md](SECURITY.md) | Threat model, controls mapped to the OWASP LLM Top 10, scanner policy |

## Contracts & design (v2)

| Doc | What's inside |
|---|---|
| [v2/SPEC.md](v2/SPEC.md) | The TrustAgent 2.0 specification: contracts, algorithms, per-service changes, phases |
| [contracts/README.md](../contracts/README.md) | The cross-language contracts: schemas, key registry, templates, presets, prompts, API fixtures, validator checks |
| [contracts/vectors/README.md](../contracts/vectors/README.md) | Golden vectors: kinds, counts against the SPEC §7.6 minimums, how to write and freeze one |
| [frontend-react/DESIGN.md](../frontend-react/DESIGN.md) | Dashboard house rules: tokens, verdict and outcome encoding, the UI kit, chart rules, accessibility |

## Plan & history

| Doc | What's inside |
|---|---|
| [PLAN.md](PLAN.md) | The phased roadmap — check phase status before starting work |
| [archive/](archive/) | Historical build logs (`progress/`) and early research (`research/`) — kept for provenance, not current guidance |

## Component deep-dives

- [backend-go/README.md](../backend-go/README.md) — the Go proxy
- [backend-python/README.md](../backend-python/README.md) — the Python audit engine
- [frontend-react/README.md](../frontend-react/README.md) — the React dashboard
