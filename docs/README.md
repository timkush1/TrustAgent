# TrustAgent Documentation

Start with the [project README](../README.md) for the overview and quick start. This folder holds the deeper material, organized below.

## Understand the system

| Doc | What's inside |
|---|---|
| [GATEWAY.md](GATEWAY.md) | The gateway reference: modes, response headers and stream trailers, streaming limits, fail semantics, block readiness and bodies, fallback, the decision long-poll, passthrough and inject credentials (and upstream keys saved in the dashboard), error shapes, test mode, capacity and budgets, the load test and its published run, metrics, and the Tier-0 instant checks with their budget |
| [EVALUATORS.md](EVALUATORS.md) | The built-in evaluators, attaching them to routes (active and shadow), the Evaluator Studio (and what it does on the static demo), labels and the label queue, the glass box, and the calibration workflow (adopt in the dashboard, or `make calibrate`) |
| [ARCHITECTURE.md](ARCHITECTURE.md) | The 2.0 system: the tiers, the control plane (settings, credentials, cost ledger), failure modes, ports, the static site, the release images and their flow to GHCR, key trade-offs |
| [WALKTHROUGH.md](WALKTHROUGH.md) | A guided tour of the whole project for an engineer: the life of a request through the tiers, every service, the contracts, the dashboard and the browser demo, DevOps and releases, security, tests, CI and the lessons learned |
| [KB-DESIGN.md](KB-DESIGN.md) | The dual-gate (VERITAS-lite) knowledge base: entailment gating, quarantine, contradiction detection, hybrid retrieval |
| [DECISIONS.md](DECISIONS.md) | Architecture Decision Records — why the notable choices were made: ADR-001 to ADR-004 (1.x) and ADR-005 to ADR-010 (2.0: Jev as tier 1, per-route policies, the frontend stack, the static demo, the credential store, compose profiles and GHCR images) |

## Run & operate it

| Doc | What's inside |
|---|---|
| [GETTING-STARTED.md](GETTING-STARTED.md) | Running TrustAgent locally (WSL, Linux or macOS; Docker 25+, Compose 2.24+): the zero-key `make demo`, real mode with your keys, the setup link, images built or pulled, memory, troubleshooting, and the maintainer targets (`pages-build`, `record-demo`, `first-run`) |
| [DEMO.md](DEMO.md) | The live demo in your browser (the replay, its tapes and labels, Try it live with your own key) and the zero-key `make demo`: starting it, the setup link, a five-minute tour, the 27 scenarios, checking it from the terminal, sharing it |
| [JEV-ACCESS.md](JEV-ACCESS.md) | Getting Jev access for the fast check: Connect OpenRouter, paste a key, `.env` or `make configure WITH_SECRETS=1`; which value wins, Jev from the browser (and the CORS result), simulated mode, credit limits, costs, settings, errors and fixes |
| [CONFIGURATION.md](CONFIGURATION.md) | Every configuration key with its default, who reads it, whether it is secret and whether it needs a restart (generated from the key registry; `proxy -check-config` checks a configuration against it) |
| [CI-CD.md](CI-CD.md) | The GitHub Actions pipelines (CI, Security, Eval, Pages, release images) and Dependabot: what each job does, how to read a failure, and the release runbook |

## Quality & safety

| Doc | What's inside |
|---|---|
| [EVALUATION.md](EVALUATION.md) | Evaluation methodology, metrics, the CI regression gate, and the v2 benchmark harness (`make bench-lite`, `bench-publish`, `eval-live`, `calibrate`) |
| [Honest numbers](../scripts/check-claims.mjs) | How every figure in the README and these docs stays tied to `contracts/claims/` (`<!-- claim:ID -->` markers, label chips, caveats, generated measured tables); `make check-claims` |
| [SECURITY.md](SECURITY.md) | Threat model; keys, scopes, sessions, CSRF and CORS; the encrypted credential store (master key and backups, origin binding, redaction, PKCE); budgets, the admin audit log, internal links; OWASP LLM Top 10 controls; the static demo (a visitor's key, the shared origin, CSP, caps); the supply chain; scanners and known gaps |

## Contracts & design (v2)

| Doc | What's inside |
|---|---|
| [v2/SPEC.md](v2/SPEC.md) | The TrustAgent 2.0 specification: contracts, algorithms, per-service changes, phases |
| [contracts/README.md](../contracts/README.md) | The cross-language contracts: schemas, key registry, templates, presets, prompts, API fixtures, validator checks |
| [contracts/vectors/README.md](../contracts/vectors/README.md) | Golden vectors: kinds, counts against the SPEC §7.6 minimums, how to write and freeze one |
| [frontend-react/DESIGN.md](../frontend-react/DESIGN.md) | Dashboard house rules: tokens, verdict and outcome encoding, the UI kit, chart rules, the landing page and static site (replay, live checks), accessibility |
| [v2/P3-PLAN.md](v2/P3-PLAN.md), [P4-PLAN.md](v2/P4-PLAN.md), [P5-PLAN.md](v2/P5-PLAN.md) | The binding plans of the integration, security and static-site phases: lead decisions, stream tasks, gates |

## Plan & history

| Doc | What's inside |
|---|---|
| [PLAN.md](PLAN.md) | The phased roadmap: 1.x phases 0–7 and the 2.0 phases 8–15, each with its gate and status — check it before starting work |
| [CHANGELOG.md](../CHANGELOG.md) | What changed in each release; for 2.0, every breaking change at a glance and the migration notes |
| [archive/](archive/) | Historical build logs (`progress/`) and early research (`research/`) — kept for provenance, not current guidance |

## Launch kit

| Doc | What's inside |
|---|---|
| [launch/launch-checklist.md](launch/launch-checklist.md) | What must be true before anything is posted, in order, with the command that proves each item |
| [launch/demo-script.md](launch/demo-script.md) | The 60-second demo, beat by beat |
| [launch/github-metadata.md](launch/github-metadata.md) | Repository description, topics and social preview |
| [launch/og-spec.md](launch/og-spec.md) | The 1280×640 social card: layout, content, how it is captured (`make og`; `make hero-gif` and `make screenshots` capture the README's hero and pictures) |
| [launch/linkedin-post.md](launch/linkedin-post.md) | The launch post draft; its figures stay placeholders until `measured.json` has them |
| [launch/interview-talking-points.md](launch/interview-talking-points.md) | Ten engineering stories: decision, alternatives, result, what went wrong |

## Component deep-dives

- [backend-go/README.md](../backend-go/README.md) — the Go proxy
- [backend-python/README.md](../backend-python/README.md) — the Python audit engine
- [frontend-react/README.md](../frontend-react/README.md) — the React dashboard
