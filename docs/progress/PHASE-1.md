# Phase 1 — CI/CD

**Status**: ✅ Complete (2026-06-10)
**Goal**: every push lint+test+scan-gated across all three languages.

## What was done

### CI workflow (`.github/workflows/ci.yml`)
Four parallel jobs on push to `main` and on PRs, with concurrency cancellation:
- **go**: protoc + plugin install, gRPC stub generation, gofmt check, `go vet`,
  `go test -race -coverprofile`, coverage summary.
- **python**: CPU-only torch install (avoids multi-GB CUDA wheels), `pip install .` +
  pinned dev tools (versions matched to the local environment: ruff 0.1.15, black 23.12.1,
  pytest 7.4.4, pytest-asyncio 0.23.8), stub generation, `ruff check`, `black --check`,
  `pytest tests/unit --cov`.
- **frontend**: `npm ci`, eslint, `tsc -b`, `vitest run`.
- **docker**: builds all three images (`backend-go`, `backend-python`, `frontend-react`)
  with GitHub Actions layer cache; stubs generated into the build contexts first.

Key constraint discovered: generated gRPC stubs are gitignored
(`backend-go/api/audit/`, `pb/*_pb2*.py`), so every CI job that builds or scans code
regenerates them from `proto/evaluator.proto` first — same as a developer running `make proto`.

### Security workflow (`.github/workflows/security.yml`)
gosec (Go SAST), bandit (Python SAST), `npm audit --audit-level=high`, Trivy filesystem
scan. Runs on push/PR/weekly schedule. **All jobs are `continue-on-error` for now** —
they flip to blocking at the end of Phase 3 (security hardening), per the plan.

### Frontend test toolchain
- Added vitest + jsdom + @testing-library/react; `npm test` now works
  (previously `make test` failed because the script didn't exist).
- `vite.config.ts` switched to `defineConfig` from `vitest/config` with jsdom environment.
- **15 new tests**:
  - `src/stores/auditStore.test.ts` (8): prepend ordering, 100-audit cap, selection,
    clear, stats math, localStorage persistence shape.
  - `src/hooks/useWebSocket.test.ts` (7): connect on mount, message parsing, malformed
    JSON resilience, auto-reconnect timing, disconnect status, send-only-when-open,
    close on unmount.

### Code fixes required to get CI green
- `useWebSocket.ts`: fixed a real react-hooks lint error (reconnect callback referenced
  `connect` before declaration) via a `connectRef` indirection, and removed a synchronous
  `setState` in the connect catch path (flagged by `react-hooks/set-state-in-effect`).
- Python: `black` reformatted 16 files; `ruff --fix` removed 7 unused imports / f-string
  issues; generated `grpc/pb` stubs excluded from both tools in `pyproject.toml`;
  `scripts/*` get an E402 per-file-ignore (they adjust `sys.path` before imports).
- Makefile `fmt` referenced a non-existent `npm run format`; now uses `eslint --fix`.

### Dependabot + badges
- `.github/dependabot.yml`: weekly grouped updates for gomod, pip, npm, GitHub Actions,
  and the three Dockerfiles.
- README: CI, Security, and version badges.

## Decisions / deviations
- **Coverage upload (Codecov) deferred** — needs an account/token; terminal coverage
  summaries suffice for now. Revisit in Phase 7.
- **golangci-lint deferred** — gofmt + go vet match the existing `make lint`; adding a
  stricter linter on day one risks a permanently red badge. Candidate for Phase 3.
- **mypy not in CI** — configured strict in pyproject but the codebase hasn't been
  verified against it; enabling it would block CI. Candidate for Phase 2/3 cleanup.
- Python CI installs with pip (CPU torch index) instead of Poetry — faster, smaller,
  and the dev-tool versions are pinned to match local.

## Verification evidence
- Local: `ruff check` clean, `black --check` clean, `pytest tests/unit` → **21 passed**;
  `gofmt -l` empty, `go vet` clean, `go test ./...` all ok; frontend `eslint` clean,
  `tsc -b` clean, `vitest run` → **15 passed**.
- Remote: CI run on GitHub Actions after push (see Actions tab for this commit).
