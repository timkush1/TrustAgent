# Phase 0 — Hygiene & Doc Foundation

**Status**: ✅ Complete (2026-06-10)
**Goal**: eliminate the red flags a reviewer hits in the first 5 minutes; establish the
MD-driven planning system.

## What was done

### Version alignment → 0.4.0
The repo had four conflicting versions (0.0.0 / 0.1.0 / 0.2.1 / 0.3.0). Now:
- Root `VERSION` file added as the single source of truth.
- `backend-python/pyproject.toml`: 0.1.0 → 0.4.0
- `backend-python/src/truthtable/__init__.py`: new `__version__ = "0.4.0"`, consumed by the
  gRPC servicer (`grpc/server.py`) instead of a hardcoded "0.3.0".
- `backend-go/internal/version/version.go`: new package (ldflags-overridable), consumed by the
  `/health` handler in `cmd/proxy/main.go` instead of a hardcoded "0.1.0".
- `frontend-react/package.json`: 0.0.0 → 0.4.0
- `backend-go/README.md` example output updated.

### Documentation system
- `docs/PLAN.md` — master roadmap (8 phases to v1.0.0) with live checkboxes.
- `docs/progress/` — per-phase logs (this file is the first).
- `CLAUDE.md` — repo conventions, commands, and gotchas for AI-assisted development.
- `docs-private/GETTING-STARTED.md` → `docs/GETTING-STARTED.md` (promoted from the gitignored
  private folder so visitors can actually find it).
- VERITAS research artifact moved from repo root → `docs/research/VERITAS-claim-graph-research.md`.
- README: new Documentation section linking the above.

### Build/test wiring
- `make test-e2e` target added (runs `test_e2e.py` + `test_direct_audit.py`; requires full stack).

## Decisions / deviations
- `docs-private/` stays gitignored as a historical build journal; only curated content is
  promoted to `docs/`.
- Generated proto stubs are gitignored (`backend-go/api/audit/`, `pb/*_pb2*.py`) — CI (Phase 1)
  must regenerate them with protoc before building.

## Verification evidence
- `go build ./...` — success.
- `go test ./...` — `ok` for `internal/proxy`, `internal/websocket`, `internal/worker`
  (a Windows temp-file cleanup race produces a spurious non-zero exit after passing tests).
- `python -m pytest tests/unit -q` — **21 passed** in 32.7s.
- `frontend-react` has no `npm test` script yet — added in Phase 1 with the vitest toolchain.
