# TrustAgent Documentation

Start with the [project README](../README.md) for the overview and quick start. This folder holds the deeper material, organized below.

## Understand the system

| Doc | What's inside |
|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | System + sequence diagrams, the zero-latency tee design, component boundaries, key trade-offs |
| [KB-DESIGN.md](KB-DESIGN.md) | The dual-gate (VERITAS-lite) knowledge base: entailment gating, quarantine, contradiction detection, hybrid retrieval |
| [DECISIONS.md](DECISIONS.md) | Architecture Decision Records (ADRs) — why the notable choices were made |

## Run & operate it

| Doc | What's inside |
|---|---|
| [GETTING-STARTED.md](GETTING-STARTED.md) | Zero-to-running walkthrough with troubleshooting |
| [DEMO.md](DEMO.md) | Live-demo runbook (incl. exposing the stack on a public URL) |
| [JEV-ACCESS.md](JEV-ACCESS.md) | Getting Jev access for the v2 fast check: OpenRouter key and credits, costs, settings, errors and fixes |
| [CONFIGURATION.md](CONFIGURATION.md) | Every configuration key with its default, who reads it, whether it is secret and whether it needs a restart (generated from the key registry) |
| [CI-CD.md](CI-CD.md) | The GitHub Actions pipelines (CI, Security, Eval) and Dependabot — what each job does and how to read a failure |

## Quality & safety

| Doc | What's inside |
|---|---|
| [EVALUATION.md](EVALUATION.md) | Two-tier evaluation methodology, metrics, and the CI regression gate |
| [SECURITY.md](SECURITY.md) | Threat model, controls mapped to the OWASP LLM Top 10, scanner policy |

## Plan & history

| Doc | What's inside |
|---|---|
| [PLAN.md](PLAN.md) | The phased roadmap — check phase status before starting work |
| [archive/](archive/) | Historical build logs (`progress/`) and early research (`research/`) — kept for provenance, not current guidance |

## Component deep-dives

- [backend-go/README.md](../backend-go/README.md) — the Go proxy
- [backend-python/README.md](../backend-python/README.md) — the Python audit engine
- [frontend-react/README.md](../frontend-react/README.md) — the React dashboard
