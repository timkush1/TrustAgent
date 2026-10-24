# Contributing to TrustAgent

Thanks for taking the time. TrustAgent is a small project, so the most useful contributions are
bug reports with a way to reproduce them, fixes with a test, and corrections to the docs.

Please read the [code of conduct](CODE_OF_CONDUCT.md) first. Security problems do not go in a
public issue: use [private vulnerability reporting](https://github.com/timkush1/TrustAgent/security/advisories/new)
(see [docs/SECURITY.md](docs/SECURITY.md#reporting)).

## Before you start

- **A bug**: open a [bug report](https://github.com/timkush1/TrustAgent/issues/new?template=bug.yml)
  with the version (`curl -s localhost:8080/health`), how you run it (`make demo`, `make up` or the
  pulled images) and the output of `make doctor`.
- **A feature or a larger change**: open a [feature request](https://github.com/timkush1/TrustAgent/issues/new?template=feature.yml)
  first, so we can agree on the shape before you write it. The design lives in
  [docs/v2/SPEC.md](docs/v2/SPEC.md); changes to the shared contracts (below) need that agreement.

## Set up

You need Docker (with Compose v2), Go 1.25, Python 3.11 with Poetry, Node 22.12 or newer, and
`make`. On Windows, work inside WSL with Docker Desktop's WSL integration on.

```bash
make install        # dependencies for every project
make proto-tools    # once: the protoc Go plugins
make proto          # generate the gRPC stubs (they are not committed)
make demo           # the zero-key demo on mocks, at http://localhost:3000
make check          # the local gate: lint, unit tests, contracts, compose config
```

`make help` lists every target. The [getting-started guide](docs/GETTING-STARTED.md) covers the
real stack, and [docs/README.md](docs/README.md) indexes the rest of the docs.

## Making a change

- **Tests come with the change.** Go: `cd backend-go && go test ./...`. Python:
  `cd backend-python && poetry run pytest`. Dashboard: `cd frontend-react && npm test`.
  `make check` runs them all, plus the checks CI runs.
- **Never use a real key in a test or a fixture.** Tests use fake keys only (`sk-test-…`,
  `sk-or-v1-e2e-…`); gitleaks scans the whole history in CI. Keep your own keys in `.env`, which
  is ignored.
- **Contract-owned paths change on purpose, in step across languages**: `proto/`, `contracts/`,
  `backend-go/internal/store/migrations/`, `backend-go/internal/decision/`,
  `frontend-react/src/lib/decision/` and `frontend-react/src/types/api.ts`. Go is the reference
  for the decision logic; the TypeScript and Python sides pass the same golden vectors
  (`make vectors-ts`). Edit `proto/evaluator.proto` only, then run `make proto`.
- **Configuration keys** are registered in `contracts/config/keys.v1.json`; run
  `make config-docs` after adding one.
- **Honest numbers.** Every figure a reader sees carries its label (measured, estimate, vendor,
  third-party or simulated), and output from a mock is never called measured.
  `make check-claims` holds the README and the docs to `contracts/claims/`.
- **The dashboard** follows [frontend-react/DESIGN.md](frontend-react/DESIGN.md): colour means a
  verdict or an outcome, and every page has its empty, loading, error, degraded, demo and
  read-only states.
- **Style**: gofmt; black (line length 100), ruff and mypy for Python; ESLint for the
  dashboard. `make check` runs the linters.

## Commits and pull requests

- Use [Conventional Commits](https://www.conventionalcommits.org/) (`fix(gateway): …`,
  `feat(web): …`, `docs: …`), one logical change per commit.
- Open the pull request against `main` and fill in the template: what changed, why, and how you
  tested it. CI must be green; the job names are the required checks.
- Add a line to [CHANGELOG.md](CHANGELOG.md), under an `## Unreleased` heading at the top, for
  anything a user or an operator would notice.

By contributing you agree that your contribution is licensed under the [MIT License](LICENSE).
