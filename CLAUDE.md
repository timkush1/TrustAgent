# CLAUDE.md

Guidance for AI-assisted development in this repository.

## What this project is

TrustAgent (internal name: TruthTable) is an AI Hallucination Control Plane: a Go reverse proxy
intercepts LLM API calls, returns responses to the client immediately, and asynchronously audits
them through a Python LangGraph pipeline (decompose claims → retrieve context from Qdrant →
verify via NLI → score faithfulness). Results stream to a React dashboard over WebSocket.

- Master roadmap: [docs/PLAN.md](docs/PLAN.md) — check phase status before starting work.
- Per-phase progress logs: [docs/progress/](docs/progress/)

## Layout

| Path | What |
|---|---|
| `backend-go/` | Go 1.25 proxy (gin): HTTP interception, worker pool, WebSocket hub, gRPC client |
| `backend-python/` | Python 3.11 audit engine (Poetry): LangGraph pipeline, providers, Qdrant store, gRPC server |
| `frontend-react/` | React 19 + TypeScript + Vite + Zustand + Tailwind dashboard |
| `proto/evaluator.proto` | Single source of truth for the Go↔Python gRPC contract |
| `docker-compose.yml` | Full stack: redis, qdrant, ollama, prometheus, grafana, engine, proxy, dashboard |
| `VERSION` | Single source of truth for the project version (mirror into pyproject.toml, package.json, `backend-go/internal/version/version.go`, `truthtable/__init__.py`) |

## Commands

```bash
make install        # deps for all three projects
make proto          # regenerate Go + Python gRPC stubs after editing proto/evaluator.proto
make up             # start infrastructure containers (redis, qdrant, ollama, prometheus, grafana)
make dev-python     # run audit engine locally (gRPC :50051, metrics :8001)
make dev-go         # run proxy locally (HTTP+WS :8080, metrics :8002)
make dev-react      # run dashboard (Vite :5173)
make test           # all unit tests (Python, Go, React)
make test-e2e       # end-to-end tests (requires full stack running)
make lint           # ruff+black (py), go fmt+vet, eslint
make fmt            # auto-format everything
```

Per-project:
- Python: `cd backend-python && poetry run pytest -v` (asyncio_mode=auto; tests in `tests/unit`, `tests/integration`)
- Go: `cd backend-go && go test ./...`
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
- **Versioning**: bump `VERSION` and all four mirrors together.
- **Docs discipline**: every roadmap phase updates its `docs/progress/PHASE-N.md` with what was
  done, decisions/deviations, and verification evidence.

## Gotchas

- The pipeline needs Ollama running with the model pulled (`make ollama-pull`) — unit tests mock
  LLM calls, but integration/E2E tests need the real stack.
- Qdrant is optional at startup: if `QDRANT_URL` is unset/unreachable the engine degrades to
  no-context verification (claims will come back UNSUPPORTED).
- `docs-private/` is historical build-journal material; the curated public docs live in `docs/`.
- Windows dev environment: prefer cross-platform commands in Makefile/scripts.
