# CLAUDE.md

Guidance for AI-assisted development in this repository.

## What this project is

TrustAgent (internal package name: `truthtable`) is an AI Hallucination Control Plane: a Go reverse
proxy intercepts LLM API calls, returns responses to the client immediately, and asynchronously
audits them through a Python LangGraph pipeline (decompose claims → retrieve context from Qdrant →
verify via NLI → score faithfulness). Results stream to a React dashboard over WebSocket.

- **Naming**: user-facing surfaces (README, docs, dashboard UI) say **TrustAgent**; the Go module,
  Python package, and container prefix stay `truthtable` (renaming is high-churn, low-value).
- **Audit judge**: defaults to Anthropic Claude Haiku (`claude-haiku-4-5`); auto-falls-back to local
  Ollama (`llama3.2:1b`) when `ANTHROPIC_API_KEY` is unset. See `main.py::init_provider`.
- Master roadmap: [docs/PLAN.md](docs/PLAN.md) — check phase status before starting work.
- Docs index: [docs/README.md](docs/README.md); historical logs in [docs/archive/](docs/archive/).

## Layout

| Path | What |
|---|---|
| `backend-go/` | Go 1.25 proxy (gin): HTTP interception, worker pool, WebSocket hub, gRPC client |
| `backend-python/` | Python 3.11 audit engine (Poetry): LangGraph pipeline, providers, Qdrant store, gRPC server |
| `frontend-react/` | React 19 + TypeScript + Vite + Zustand + Tailwind dashboard |
| `proto/evaluator.proto` | Single source of truth for the Go↔Python gRPC contract |
| `docker-compose.yml` | Full stack: redis, postgres, qdrant, ollama, prometheus, grafana, engine, proxy, dashboard (no fixed container names: use `docker compose exec <service>`) |
| `Makefile`, `mk/*.mk` | Thin root Makefile (`help`, aggregate targets) that includes every `mk/*.mk`, one fragment per stream (`go`, `py`, `web`, `dx`, later `contracts`) |
| `.github/workflows/` | `ci-{go,python,web,contracts,docker,e2e}.yml` (job names are branch-protection checks: keep them stable), `security.yml`, `eval.yml` |
| `VERSION` | Single source of truth for the project version (mirror into pyproject.toml, package.json, `backend-go/internal/version/version.go`, `truthtable/__init__.py`) |

## Commands

```bash
make install        # deps for all three projects
make proto          # regenerate Go + Python gRPC stubs after editing proto/evaluator.proto
make help           # list every target (root Makefile + mk/*.mk)
make up             # start infrastructure containers (redis, postgres, qdrant, ollama, prometheus, grafana)
make dev-python     # run audit engine locally (gRPC :50051, metrics :8001)
make dev-go         # run proxy locally (HTTP+WS :8080, metrics :8002)
make dev-react      # run dashboard (Vite :5173)
make test           # all unit tests (Python, Go, React)
make test-e2e       # end-to-end tests (requires full stack running)
make lint           # ruff+black (py), gofmt check + go vet, eslint
make fmt            # auto-format everything
make check          # lint + unit tests (the local gate; later phases add more)
```

Per-project:
- Python: `cd backend-python && poetry run pytest` (asyncio_mode=auto). Tests live in `tests/unit`,
  `tests/integration`, and `tests/e2e`. The `integration` and `e2e` markers are **opt-in**: `addopts`
  in `pyproject.toml` deselects them from every plain run, and an explicit `-m` overrides it; run e2e
  via `make test-e2e` (needs the full stack up).
- Go: `cd backend-go && go test ./...` (Postgres store tests skip unless `TEST_DATABASE_URL` is set)
- React: `cd frontend-react && npm test` (vitest)

## Conventions

- **Proto changes**: edit `proto/evaluator.proto` only, then `make proto`. Never hand-edit
  generated code in `backend-go/api/audit/v1/` or `backend-python/src/truthtable/grpc/pb/`.
- **Python**: black (line-length 100), ruff, mypy strict. New LLM providers implement
  `providers/base.py::LLMProvider` and register in `providers/registry.py`.
- **Go**: standard gofmt; config via env vars parsed in `internal/config/config.go`
  (`TRUTHTABLE_*` prefix); new HTTP middleware lives in `internal/middleware/`.
- **Frontend**: state in Zustand stores (`src/stores/`), shared types in `src/types/audit.ts`;
  components grouped by domain (`audit/`, `dashboard/`, `layout/`, `upload/`).
- **Verdicts**: the pipeline emits SUPPORTED / CONTRADICTED / UNSUPPORTED / PARTIALLY_SUPPORTED
  (plus UNKNOWN on parse failure). CONTRADICTED (context disproves the claim) is distinct from
  UNSUPPORTED (nothing addresses it) end-to-end — keep both live across proto, Python, Go, and the UI.
- **Metrics**: both services use the unified `trustagent_*` Prometheus namespace.
- **Versioning**: bump `VERSION` and all four mirrors together.

## Gotchas

- Unit tests mock LLM calls; integration/E2E tests need the real stack (`make up` + `make ollama-pull`).
- The audit judge auto-falls-back to Ollama when no cloud key is set — a **weak local model produces
  poor verdicts** (expected; the eval suite quantifies it). Set `ANTHROPIC_API_KEY` for real quality.
- Qdrant is optional at startup: if `QDRANT_URL` is unset/unreachable the engine degrades to
  no-context verification (claims come back UNSUPPORTED).
- Generated gRPC stubs are gitignored; run `make proto-tools` once, then `make proto` on a fresh clone.
- Curated public docs live in `docs/`; historical build-journal material is under `docs/archive/`.
- Windows/WSL dev environment: prefer cross-platform commands in Makefile/scripts.
