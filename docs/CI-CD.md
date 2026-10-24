# CI/CD: the GitHub Actions pipelines, explained

This doc explains, in plain English, everything that runs automatically on this repository:
what each workflow does, why it exists, how to read a failed run, the reliability decisions we
made, and how a release is cut ([Releasing](#releasing)). If you've never touched GitHub Actions,
start at the top.

## The 30-second mental model

**GitHub Actions** is GitHub's built-in automation. YAML files in `.github/workflows/` say
*"when X happens, run these steps on a fresh cloud machine."* Each file is a **workflow**; a
workflow contains **jobs** (each on its own machine, in parallel); a job is a list of **steps**
(shell commands or reusable "actions" like `actions/checkout`). If any step exits non-zero, its
job — and the workflow — goes red. On a pull request, red checks mean "don't merge this yet."

**Dependabot** is a GitHub bot (configured in `.github/dependabot.yml`) that opens PRs to update
dependencies on a weekly schedule. Every Dependabot PR triggers the same CI/Security checks as a
human PR — which is exactly the point: an update that breaks the build gets caught *before* merge.

CI is split into one workflow per area, plus security, the evaluations and the release workflows:

| Workflow | File | Runs on | Purpose |
|---|---|---|---|
| **CI — Go** | `ci-go.yml` | every push to `main` + every PR | gofmt, vet, `go test -race` against Postgres, govulncheck (informational) |
| **CI — Python** | `ci-python.yml` | every push to `main` + every PR | ruff, black, unit tests, golden eval gate |
| **CI — Web** | `ci-web.yml` | every push to `main` + every PR | generated files are current, eslint, `tsc -b`, vitest, production and static builds |
| **CI — Docker** | `ci-docker.yml` | every push to `main` + every PR | build all three images (not pushed), labelled with their version and git revision |
| **Contracts** | `ci-contracts.yml` | every push to `main` + every PR | validate `contracts/` (schemas, vectors, RE2/JS regex portability), the forbidden-identifier scan, every figure in the README and docs against `contracts/claims` (`check-claims.mjs`), and the version mirrors (`check_version.py`) |
| **E2E** | `ci-e2e.yml` | every push to `main` + every PR + manual button | the DX scripts job, then the demo, e2e and onboarding stacks with trafficgen, doctor and Playwright |
| **Security** | `security.yml` | every push/PR + weekly Mondays | security scanners (gosec, bandit, gitleaks, npm audit, Trivy) |
| **Eval** | `eval.yml` | manual button only | benchmark detector quality against HaluEval with a local model (free); its summary labels the figures as that run's own |
| **Eval (live)** | `eval-live.yml` | manual button only, in the protected `eval-live` environment | the paid benchmark (`make gate-eval`, then `make bench-lite`, hard cap $5) with the real fast check and judge; uploads the run, never publishes it |
| **Knowledge** | `ci-knowledge.yml` | manual, weekly, and changes to the knowledge path | the knowledge add-on end to end in Docker, on mock vendors; not a required check |
| **Pages** | `pages.yml` | push to `main`, `v*` tags, PRs (build only), manual | build and check the static site; deploy it only when the owner turned Pages on |
| **Release images** | `release-images.yml` | push to `main`, `v*.*.*` tags, PRs touching images (build only), manual | multi-arch images, provenance, SBOM, attestations, pushed to GHCR from `main` and tags only |

## What each workflow does

### CI (`.github/workflows/ci-*.yml`) — "does everything still work?"

One workflow per stream (the job ids `go`, `python`, `frontend`, `docker`, `contracts`, `scripts`, `e2e` stay unchanged so branch protection keeps working):

- **Go (proxy)** — spins up a real Postgres service container, regenerates the gRPC stubs from
  `proto/evaluator.proto` (they're gitignored, so every job regenerates them), then runs
  `gofmt` check → `go vet` → `go test -race` (the race detector catches concurrency bugs —
  important for a worker-pool + WebSocket-hub codebase).
- **Python (audit engine)** — installs CPU-only torch (the default wheels bundle CUDA and weigh
  gigabytes), installs the package, regenerates Python stubs, then `ruff` → `black --check` →
  `pytest tests/unit` → the **eval regression gate**: a 50-example golden set runs through the
  real pipeline with recorded model outputs and must match the committed baseline **bit-exactly**.
  Any change to parsing/scoring/thresholds that flips one prediction fails CI and forces a
  deliberate baseline update in the same PR.
- **Frontend (dashboard)** — `npm ci` (installs *exactly* what `package-lock.json` says) → the
  generated contract types and the CSP-safe policy and evaluator validators are current →
  `eslint` → `tsc -b` (typecheck) → `vitest` → the production and static builds → the landing
  page's size budget → `check_recording.py` on the demo recording.
- **Contracts** — the JSON Schemas, fixtures and golden vectors validate; no superseded
  identifier (SPEC Appendix A) appears anywhere; `docs/CONFIGURATION.md` and `.env.example` match
  the key registry; the api.v1 schema matches `src/types/api.ts`.
- **DX scripts** — `ruff`, `black` and the unit tests of `scripts/` (configure, doctor, setup
  link, config docs, compose files, Grafana, nginx, gitleaks config), then `docker compose config`
  on every profile and override, then `make onboarding-check-config`: the gateway's
  `-check-config` runs on the host with the environment compose gives the onboarding stack's
  proxy, so a fresh install that strict config would refuse (or one that carries a key by
  mistake) fails here with its warnings, before any container starts. Compose resolves the
  committed files alone (no `.env`, no shell variables), so `make check` runs the same proof on
  a laptop whose `.env` holds keys.
- **E2E (compose stack)** — the zero-key demo stack: every scenario with `trafficgen --expect`,
  `doctor --expect demo`, the Playwright stack specs (M-Demo, Studio and Savings, one worker) and
  the static site's specs; then the e2e stack (every vendor on mock-upstream) and its scenarios;
  then the onboarding stack (a fresh install: the setup link, Connect OpenRouter through
  mock-upstream, the wizard). Compose logs are uploaded when a step fails.
- **Docker builds** — builds all three production images (without pushing) to prove the
  Dockerfiles work. This is the job that catches base-image problems (see the python:3.14 story
  below).

### Security (`.github/workflows/security.yml`) — "did we introduce a vulnerability?"

Five jobs; the first four are **blocking**, Trivy is informational:

- **gosec** — Go static security analysis (SQL injection, weak crypto, unchecked conversions…),
  blocking at medium+ severity. Pinned to `v2.27.1`.
- **bandit** — the Python equivalent, pinned to `1.9.4`, over the engine (every level) and the
  setup scripts (medium and above). (It once caught a Trojan-Source pattern in this repo — these
  scanners earn their keep.)
- **gitleaks** — scans the **whole git history** for keys, with a pinned, checksum-verified
  binary and `--redact` (a finding never prints the secret). [`.gitleaks.toml`](../.gitleaks.toml)
  allows only fake keys in tests (`*_test.go`, `tests/unit/`, `e2e/`), the detector vectors, the
  project's fake-key conventions (`sk-test-…`, `sk-or-v1-e2e-…`) and SHA-256 calibration keys;
  `scripts/tests/test_gitleaks.py` keeps those allow-lists narrow.
- **npm audit** — checks frontend dependencies against GitHub's advisory database, **production
  dependencies only** (`--omit=dev`), blocking at high+. Why omit dev: the shipped bundle holds
  only the `dependencies` of `package.json` (React, TanStack, CodeMirror, Recharts and the like);
  Vite, esbuild and the test tools run at build time, so an advisory there reaches the local dev
  server at most. A real HIGH in anything we actually ship still fails.
- **Trivy** — broad filesystem/dependency scanner, `continue-on-error: true` (reports but never
  blocks) until its findings have been triaged. Its secret scan reads
  [`trivy-secret.yaml`](../trivy-secret.yaml), which allows only the detector vectors (synthetic
  keys the Tier-0 secret detector must flag), as `.gitleaks.toml` does;
  `scripts/tests/test_trivy_secret.py` keeps that allow-list narrow.

### Release and publishing workflows: "what do we ship?"

- **Pages** (`pages.yml`): the **build** job runs on every trigger and never deploys. It runs the
  same make targets as `make pages-build`, in order: `check_recording.py` on the tape the site will
  ship (`--require-recorded` on a tag), the static build under `/TrustAgent/` (the tape is `auto` on
  `main` and pull requests, `recorded` on a tag, or the simulated one on a tag when the
  repository variable `PAGES_TAG_DATASET` is `simulated`), the landing page's size budget, file checks (every
  page, the 404 fallback, the base path, the shipped tape, no source maps), **gitleaks over the
  built site**, then the Pages artifact. The **deploy** job runs only when the repository variable
  `PAGES_ENABLED` is `true`, never for a pull request, only from `main` or a `v*` tag (and, on a
  manual run, only with the `deploy` input); it alone holds `pages: write` and `id-token: write`.
- **Release images** (`release-images.yml`): a `plan` job reads `VERSION`, checks a release tag
  against it (`check_version.py --tag`) and decides whether the run pushes (only from
  `timkush1/TrustAgent`, only from `main` or a `v*.*.*` tag). One `images` job per service builds
  `linux/amd64` and `linux/arm64`, with BuildKit provenance (`mode=max`) and an SBOM, pushes the
  tags `edge` (main), `X.Y.Z`, `X.Y`, `latest` (a release) and `sha-<short>`, and attests each
  pushed digest. A tag build starts clean (no cache, base images pulled again). Then
  `release-notes` lists each digest with its verify command, `first-run` times a fresh stack on the
  pulled `sha-<short>` images, and `anonymous-pull` (once `GHCR_PUBLIC` is `true`) pulls every
  image without credentials.
- **Eval (live)** (`eval-live.yml`): runs only by hand, in the `eval-live` environment that holds
  `OPENROUTER_API_KEY` behind a required reviewer, so every paid run is approved first. Publishing a
  measured result stays a reviewed, local step (`make bench-publish`).

Every one of these workflows is hardened the same way: actions pinned to a commit SHA, no persisted
git credentials, no permissions at the top level and only what each job needs, no
`pull_request_target`, and no `${{ ... }}` expressions inside `run:` (values reach scripts through
`env:`). `make lint-workflows` runs actionlint and zizmor over every workflow.

### Eval (`.github/workflows/eval.yml`): "is the detector actually good?"

On demand only, via the *Run workflow* button (it no longer runs weekly): starts an Ollama service
container, pulls a small model, runs the HaluEval-QA benchmark through the real pipeline, and
uploads the metrics as an artifact. Its job summary labels the figures as measured in that run
with that local judge, not as published TrustAgent figures. It never blocks PRs; it is a quality
check you start by hand, not a gate.

### Dependabot (`.github/dependabot.yml`)

Weekly grouped update PRs for: Go modules, Python (pip), npm, GitHub Actions versions, and the
three Dockerfiles' base images. Two deliberate policies:

1. **npm majors are ignored** — major bumps of vite/typescript/eslint are migrations, not routine
   updates; they're done by hand.
2. **`python` base-image majors are ignored** — see failure #1 below for why.

## Past CI failures and how they were resolved

These failures were visible on the repo in mid-2026; each is understood and addressed:

### 1. "Bump python from 3.11-slim to 3.14-slim" (Dependabot docker PR #9)

Dependabot proposed bumping the audit-engine base image from Python 3.11 to 3.14. The Docker CI
job then tried to build that image, and the build has to `pip install torch` inside it. **torch
(and parts of the ML stack) don't publish pre-built wheels for a brand-new Python major for
months**, so pip fell back to building from source inside a slim container — which cannot succeed.
The failure is deterministic; re-running will never help.

**Resolution:** stay on 3.11 deliberately, and `dependabot.yml` now ignores `python` base-image
major bumps so this PR class stops appearing. Bumping Python is a manual decision made when the
ML wheels catch up.

### 2. "Bump the npm-deps group" (Dependabot npm PR #19) — CI *and* Security red

Two compounding causes:

- Dependabot's regenerated `package-lock.json` has previously been inconsistent with
  `package.json` in this repo — and `npm ci` **hard-fails on any mismatch** (that's its job:
  reproducible installs). There's direct precedent: commit `fa37cb1` exists precisely to fix an
  earlier bad Dependabot lockfile by regenerating it locally.
- The Security run on that PR also failed because of #3 below, independent of the bump itself.

**Resolution path:** regenerate the lockfile locally (`npm install`), verify
`npm ci && npm run lint && npx tsc -b && npm test`, and push that to the PR branch — or close the
PR and apply the same bumps locally.

### 3. "Security" workflow failing on `main` (and on every PR)

The blocking `npm audit --audit-level=high` job found **two HIGH advisories in `undici`** — a
transitive dev dependency — plus HIGH advisories in Vite itself (dev-server-only; fixed in v2 P0.5 by
upgrading to Vite 8). Because Security runs on `main`, every PR, *and* a weekly schedule,
one advisory turned the whole repo's checks red everywhere at once.

**Resolution (done):** `npm audit fix` cleared the undici advisories (non-breaking, verified:
lint/typecheck/tests all green after), and the audit step now scans **production deps only** —
`npm audit --omit=dev --audit-level=high` currently reports **0 vulnerabilities**. Real shipped
vulns still block.

### 4. "Bump actions/checkout from 6 to 7" (Dependabot PR #18)

The bump itself is harmless (it only changes which version of the checkout action runs). Its
checks were red because **every PR re-runs the full CI + Security suite** — so it inherited
failure #3. Once Security is green on `main`, re-running this PR's checks should turn it green;
it's then safe to merge.

### 5. The "30m 31s" CI run on the npm PR

A normal symptom, not a separate bug: the Docker job rebuilds images without a warm cache on
Dependabot PRs, and a failing job holds the workflow open until timeout/failure propagates.

## Reliability hardening we applied (so green stays green)

| Problem | Fix |
|---|---|
| `go install …@latest` for protoc plugins & gosec — any upstream release could break CI overnight, non-reproducibly | Pinned: `protoc-gen-go@v1.34.2`, `protoc-gen-go-grpc@v1.5.1`, `gosec@v2.27.1`, `bandit==1.9.4` (same pins used by `make proto-tools`) |
| npm audit red from dev-only toolchain advisories | Audit production deps only (`--omit=dev`); undici HIGHs actually fixed |
| Dependabot python-major docker PRs that can never build | `ignore: python semver-major` in `dependabot.yml` |
| CI tested Node 22 but the shipped image built on Node 26 | Frontend Dockerfile now builds on `node:22-alpine` (the CI-validated LTS) |
| `poetry.lock` stale (predated Poetry 2.x; `make install` failed) + an unresolvable numpy conflict between `qdrant-client` and never-imported `langchain`/`langchain-community` | Removed the two dead deps, regenerated the lock with Poetry 2.4; torch locked to the CPU wheel index so `poetry install` stays lean |
| `make proto` produced wrongly-nested Go stubs on a fresh clone | Now mirrors CI exactly (`--go_opt=module=…`); `make proto-tools` installs the pinned plugins |

## Releasing

A release is a version bump, a tag and two workflows. Nothing is published from a laptop.

**Once, before the first release (the owner, in the repository settings):**

1. **Settings → Pages → Source:** choose **GitHub Actions**.
2. **Settings → Environments → `github-pages` → Deployment branches and tags:** add the tag rule
   `v*` next to `main`. Without it, the deploy job of a `v2.0.0` tag is rejected by the
   environment, even though the build passed.
3. **Settings → Secrets and variables → Actions → Variables:** set `PAGES_ENABLED` to `true`.
   Only if the project may not publish a recorded tape (mode R of
   [docs/launch/terms-check.md](launch/terms-check.md)), also set `PAGES_TAG_DATASET` to
   `simulated`: a tag then ships the simulated tape. Unset, a tag ships the recorded one.
4. After the first `release-images` run, open each package (`truthtable-proxy`, `-engine`,
   `-dashboard`) → **Package settings** → visibility **Public**, then set the variable
   `GHCR_PUBLIC` to `true` so the `anonymous-pull` job checks it.

**Each release:**

1. On a clean release branch, set the version everywhere and rename the CHANGELOG heading
   (`## Unreleased — X.Y.Z ...` becomes `## vX.Y.Z — <date> ...`; the date is today unless
   `RELEASE_DATE=YYYY-MM-DD` names the planned tag day). The bump goes into the release pull
   request, never earlier, so `main` does not show a release before its tag:

   ```bash
   make version-bump V=2.0.0 DRY_RUN=1    # print the plan
   make version-bump V=2.0.0 RELEASE_DATE=YYYY-MM-DD   # VERSION, pyproject.toml, package.json + lock, version.go, __init__.py, compose default
   make check-version RELEASE_TAG=v2.0.0  # every mirror agrees, and the CHANGELOG has the heading
   make check && make check-claims-launch # the local gate, and the launch gate for public figures
   ```

2. Commit, open a pull request, merge it when every check is green.
3. Tag the merge commit and push the tag:

   ```bash
   git tag -a v2.0.0 -m "TrustAgent 2.0.0" && git push origin v2.0.0
   ```

4. Watch the two runs the tag starts: **Release images** (images, provenance, SBOM, attestations,
   release notes, first run) and **Pages** (which refuses a tag without a recorded tape).
5. Create the GitHub release from the tag, with the CHANGELOG section and the digests from the
   `release-notes` job, and attach the hero MP4 if there is one.
6. Check what was published:

   ```bash
   for svc in proxy engine dashboard; do
     gh attestation verify oci://ghcr.io/timkush1/truthtable-$svc:2.0.0 -R timkush1/TrustAgent
   done
   docker buildx imagetools inspect ghcr.io/timkush1/truthtable-proxy:2.0.0   # two platforms
   make doctor ARGS="--target pages --pages-url https://timkush1.github.io/TrustAgent/"
   ```

   On a fresh clone, `git checkout v2.0.0 && make demo` now pulls these images instead of building.

## How to read a failure (quick recipe)

1. GitHub → **Actions** tab → click the red run.
2. Click the red **job** in the left sidebar, then expand the red **step** — the real error is in
   the last ~30 lines of its log.
3. Ask: *does this failure mention the thing the PR changed?* If yes, the PR broke it. If no
   (e.g. a scanner advisory or an infra hiccup), check whether `main`'s latest run is also red —
   a red `main` means the failure is pre-existing and the PR merely inherited it.
4. `Re-run failed jobs` is only useful for flaky/infra failures; deterministic ones (compile
   errors, wheel availability, lockfile mismatch) will fail identically every time.

## Running the same checks locally

Everything CI does has a local equivalent — run these before pushing:

```bash
make check          # the local gate: lint, unit tests, contracts, compose files, generated files, both web builds
make eval-golden    # the eval regression gate              (≈ the CI blocker)
make pages-build    # the Pages build job, step for step
make lint-workflows # actionlint and zizmor over .github/workflows
cd frontend-react && npm audit --omit=dev --audit-level=high
gitleaks git --config .gitleaks.toml --redact .            # if gitleaks is installed
```

Scan git, as CI does, not the directory: `gitleaks dir .` also reads the gitignored `.env`, whose
generated `POSTGRES_PASSWORD` and `TRAFFICGEN_API_KEY` match the generic-api-key rule. Those are
local, never committed, and not a leak.
